package embed

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// Row is a stored embedding record.
type Row struct {
	SyncID      string
	ObsID       int
	Project     string
	Type        string
	TopicKey    string
	Title       string
	UpdatedAt   string
	ContentHash string
	Model       string
	Dim         int
	Vector      []float32
	EmbeddedAt  string
}

// Meta is the per-row metadata the reconciler uses to decide whether to re-embed.
type Meta struct {
	ContentHash string
	Model       string
	Dim         int
}

// Hit is a search result: a stored row's identity and its similarity score.
type Hit struct {
	SyncID string
	ObsID  int
	Score  float32
}

// Store is Omnia's own writable embeddings database.
type Store struct {
	db *sql.DB
	// vec is nil unless WithVecIndex(true) was passed to OpenStore AND the
	// bundled Vec1 connector registered successfully on a supported host
	// (v0.4 sqlite-vec-index, design ADR-4). Every method that consults vec
	// is nil-receiver-safe: a nil vec always means "brute force only,"
	// preserving byte-for-byte pre-v0.4 behavior (spec REQ-460).
	vec *vecIndex
}

const createTable = `
CREATE TABLE IF NOT EXISTS embeddings (
    sync_id      TEXT PRIMARY KEY,
    obs_id       INTEGER NOT NULL,
    project      TEXT,
    type         TEXT,
    topic_key    TEXT,
    title        TEXT,
    updated_at   TEXT,
    content_hash TEXT NOT NULL,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    vector       BLOB NOT NULL,
    embedded_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_emb_obs ON embeddings(obs_id);
CREATE INDEX IF NOT EXISTS idx_emb_project ON embeddings(project);`

// OpenStore opens (creating if needed) the embeddings DB at path. It mirrors the
// engramdb/state pattern: mkdir the parent, open a pure-Go SQLite file in WAL
// mode. MaxOpenConns is 1 — writes are serialized within the embed run and the
// brute-force search loads all rows on a single connection anyway.
//
// opts is variadic so every pre-v0.4 call site (`OpenStore(path)`) is
// unaffected: with no options, this is byte-for-byte the pre-v0.4 modernc
// path. Passing WithVecIndex(true) additionally opts this Store into the
// v0.4 sqlite-vec-index capability (design ADR-4) — when the flag is off,
// absent, or the host/connector can't support it, OpenStore silently keeps
// the modernc/brute-force path; Vec1 never causes OpenStore itself to fail.
func OpenStore(path string, opts ...Option) (*Store, error) {
	var cfg storeOptions
	for _, o := range opts {
		o(&cfg)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("embed: create store dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

	// v0.4 memory-at-rest-security (design ADR-1/ADR-2/ADR-3): resolve the
	// encryption decision FIRST — a fail-closed refusal (keychain
	// unavailable, allow_plaintext_fallback=false) must stop OpenStore
	// before any file is touched, and an allowed-fallback degradation must
	// be decided before the vec1-only branch below runs (a degraded
	// encryption request still wants Vec1 if vector_index.enabled).
	hexKey, encryptionActive, err := resolveEmbedEncryption(cfg, path)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if encryptionActive {
		// A brand-new/nonexistent path is fine (created encrypted from
		// scratch below). An EXISTING, detectably plaintext file means the
		// operator flipped encryption.enabled=true on an already-populated
		// plaintext store without migrating it first — without this check
		// ncruces' adiantum VFS fails with a generic "file is not a
		// database"-style error, not an actionable hint (reuses
		// migrate_encryption.go's own isPlaintextSQLiteFile helper,
		// previously unused here).
		if existed, plaintext, perr := isPlaintextSQLiteFile(path); perr != nil {
			return nil, fmt.Errorf("embed: inspect %s: %w", path, perr)
		} else if existed && plaintext {
			return nil, fmt.Errorf("embed: %s is a plaintext SQLite database but encryption.enabled=true — run `omnia security encrypt` first to migrate it", path)
		}
	}
	vecWanted := cfg.vecIndexEnabled && hostIsLittleEndian()

	var db *sql.DB
	usingVec1Connector := false
	switch {
	case encryptionActive && vecWanted:
		// Both capabilities compose in ONE connector (design: "encryption
		// may compose its VFS initialization and vec1.Register in the same
		// internal/embed connection initializer").
		edb, everr := openEncryptedEmbedDB(path, hexKey, true)
		if everr != nil {
			return nil, fmt.Errorf("embed: %w", everr)
		}
		db, usingVec1Connector = edb, true
	case encryptionActive:
		edb, everr := openEncryptedEmbedDB(path, hexKey, false)
		if everr != nil {
			return nil, fmt.Errorf("embed: %w", everr)
		}
		db = edb
	case vecWanted:
		// UNCHANGED pre-v0.4-encryption vec1-only path (sqlite-vec-index,
		// already shipped): byte-for-byte identical to before this
		// capability existed whenever encryption is not active.
		if vdb, verr := openVec1DB(dsn); verr == nil {
			db, usingVec1Connector = vdb, true
		} else {
			log.Printf("[embed/vec1] connector unavailable (%v); vector_index falls back to brute force", verr)
		}
	case cfg.vecIndexEnabled:
		log.Printf("[embed/vec1] vector_index.enabled but this host is not little-endian; unsupported, using brute force")
	}
	if db == nil {
		var err error
		db, err = sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("embed: open %s: %w", path, err)
		}
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), createTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("embed: create schema: %w", err)
	}

	var vec *vecIndex
	if usingVec1Connector {
		vec = setupVecIndex(db)
	}
	return &Store{db: db, vec: vec}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts or replaces a row keyed by sync_id.
func (s *Store) Upsert(ctx context.Context, r Row) error {
	blob, err := encodeVector(r.Vector)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO embeddings (sync_id, obs_id, project, type, topic_key, title, updated_at, content_hash, model, dim, vector, embedded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sync_id) DO UPDATE SET
    obs_id=excluded.obs_id,
    project=excluded.project,
    type=excluded.type,
    topic_key=excluded.topic_key,
    title=excluded.title,
    updated_at=excluded.updated_at,
    content_hash=excluded.content_hash,
    model=excluded.model,
    dim=excluded.dim,
    vector=excluded.vector,
    embedded_at=excluded.embedded_at`

	if !s.vec.healthyOK() {
		_, err = s.db.ExecContext(ctx, q,
			r.SyncID, r.ObsID, r.Project, r.Type, r.TopicKey, r.Title,
			r.UpdatedAt, r.ContentHash, r.Model, r.Dim, blob, r.EmbeddedAt)
		if err != nil {
			return fmt.Errorf("embed: upsert %s: %w", r.SyncID, err)
		}
		return nil
	}

	// Vec1 additive dual-write (design capability 7 "Write, backfill, and
	// recovery"): attempt source and derived changes in ONE transaction. Any
	// derived-write failure rolls back and retries the source mutation alone
	// in a fresh transaction, so an index-only failure never surfaces to the
	// caller (spec REQ-465).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("embed: upsert %s: begin tx: %w", r.SyncID, err)
	}
	if _, err := tx.ExecContext(ctx, q,
		r.SyncID, r.ObsID, r.Project, r.Type, r.TopicKey, r.Title,
		r.UpdatedAt, r.ContentHash, r.Model, r.Dim, blob, r.EmbeddedAt); err != nil {
		tx.Rollback()
		return fmt.Errorf("embed: upsert %s: %w", r.SyncID, err)
	}
	var rowid int64
	if err := tx.QueryRowContext(ctx, `SELECT rowid FROM embeddings WHERE sync_id = ?`, r.SyncID).Scan(&rowid); err != nil {
		tx.Rollback()
		return s.upsertSourceOnly(ctx, q, r, blob, fmt.Errorf("locate rowid for derived write: %w", err))
	}
	if _, derr := s.vec.upsertRow(ctx, tx, rowid, r.Vector, r.Project); derr != nil {
		tx.Rollback()
		return s.upsertSourceOnly(ctx, q, r, blob, derr)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("embed: upsert %s: commit: %w", r.SyncID, err)
	}
	return nil
}

// upsertSourceOnly re-applies ONLY the source-table mutation in a fresh
// transaction after a derived Vec1 write failed and the combined transaction
// was rolled back (design: "roll back that transaction, commit the
// source-table mutation in a source-only transaction, mark the derived
// index unhealthy/stale ... never surface an index-only write failure").
func (s *Store) upsertSourceOnly(ctx context.Context, q string, r Row, blob []byte, cause error) error {
	s.vec.markUnhealthy(cause)
	_, err := s.db.ExecContext(ctx, q,
		r.SyncID, r.ObsID, r.Project, r.Type, r.TopicKey, r.Title,
		r.UpdatedAt, r.ContentHash, r.Model, r.Dim, blob, r.EmbeddedAt)
	if err != nil {
		return fmt.Errorf("embed: upsert %s: source-only retry after derived failure: %w", r.SyncID, err)
	}
	return nil
}

// Stored returns sync_id → Meta for every stored row (for the reconcile diff).
func (s *Store) Stored(ctx context.Context) (map[string]Meta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sync_id, content_hash, model, dim FROM embeddings`)
	if err != nil {
		return nil, fmt.Errorf("embed: Stored: %w", err)
	}
	defer rows.Close()
	out := make(map[string]Meta)
	for rows.Next() {
		var id string
		var m Meta
		if err := rows.Scan(&id, &m.ContentHash, &m.Model, &m.Dim); err != nil {
			return nil, fmt.Errorf("embed: Stored scan: %w", err)
		}
		out[id] = m
	}
	return out, rows.Err()
}

// Count returns the number of stored embeddings.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("embed: Count: %w", err)
	}
	return n, nil
}

// Search returns the top-k stored rows by cosine similarity. Since every stored
// vector and the query are unit-normalized, cosine == dot product. Rows whose
// stored dimension differs from the query's are skipped defensively (e.g. a
// half-migrated store after a model change). k <= 0 returns all ranked hits.
//
// This is a brute-force scan over EVERY project's vectors — callers that need
// the top-k WITHIN a single project (recall's semantic leg; see
// recall.Service.semanticHits) must use SearchScoped instead, otherwise a
// project that is a small fraction of the store can be crowded out of this
// global top-k by other, larger projects before any caller-side filter runs
// (engram obs #1436's root-cause analysis).
func (s *Store) Search(ctx context.Context, query []float32, k int) ([]Hit, error) {
	return s.search(ctx, query, k, "")
}

// SearchScoped is Search restricted to project via a SQL WHERE clause,
// mirroring cloudstore.SearchEmbeddings' account+project scoping
// (internal/cloud/cloudstore/embeddings.go). project == "" behaves exactly
// like Search — no WHERE clause is added, so unscoped callers (the dashboard
// browse semantic search, memory-conflict-semantic's future FindCandidates)
// are byte-for-byte unaffected by this addition.
//
// Scoping at the SQL level (rather than filtering Search's global top-k
// after the fact) is the fix for the crowding-out bug: computing the top-k
// WITHIN project means a project's valid matches can never be pushed out of
// the result set by other projects' higher-scoring vectors.
func (s *Store) SearchScoped(ctx context.Context, query []float32, k int, project string) ([]Hit, error) {
	return s.search(ctx, query, k, project)
}

// search is the shared brute-force cosine scan behind Search and
// SearchScoped. project == "" scans every row (Search's behavior); a
// non-empty project restricts the scan to that project's rows via WHERE.
func (s *Store) search(ctx context.Context, query []float32, k int, project string) ([]Hit, error) {
	if hits, ok := s.tryVecSearch(ctx, query, k, project); ok {
		return hits, nil
	}

	q := `SELECT sync_id, obs_id, vector FROM embeddings`
	var args []any
	if project != "" {
		q += ` WHERE project = ?`
		args = append(args, project)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("embed: Search: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var syncID string
		var obsID int
		var blob []byte // never sql.RawBytes — that buffer is reused per Next()
		if err := rows.Scan(&syncID, &obsID, &blob); err != nil {
			return nil, fmt.Errorf("embed: Search scan: %w", err)
		}
		vec, err := decodeVector(blob)
		if err != nil || len(vec) != len(query) {
			continue
		}
		hits = append(hits, Hit{SyncID: syncID, ObsID: obsID, Score: dot(query, vec)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// GraphNode is one memory in the semantic-similarity graph. Degree is the number
// of distinct undirected edges incident to it within the built graph.
type GraphNode struct {
	ObsID   int
	SyncID  string
	Project string
	Type    string
	Title   string
	Degree  int
}

// GraphEdge is an undirected, weighted similarity edge between two memories,
// identified by their observation IDs. Weight is the cosine similarity (== dot,
// since stored vectors are unit-normalized) and is symmetric.
type GraphEdge struct {
	Source int     // ObsID
	Target int     // ObsID
	Weight float32 // cosine similarity in [-1, 1]
}

// Graph builds the k-nearest-neighbor semantic-similarity graph over every stored
// vector. For each node it keeps up to k neighbors whose cosine similarity is
// >= minScore. Edges are UNDIRECTED and DEDUPED: a pair (a, b) is emitted once and
// exists when EITHER endpoint ranks the other among its top-k. Because the stored
// vectors are unit-normalized, cosine == dot product, so an edge's weight is the
// same regardless of direction.
//
// The scan is O(N^2) over the stored set — the upper triangle is computed once and
// mirrored into both nodes' candidate lists — which is sub-second for ~1k vectors
// (the existing Search proves brute force over this store is fast). Vectors that
// fail to decode, or whose dimension differs from a candidate's, are skipped
// defensively, mirroring Search. k <= 0 disables the per-node cap (every neighbor
// at or above minScore is kept).
//
// Every stored node is returned, including those with degree 0 (no neighbor met
// the threshold), so callers can report total vs. connected counts and decide
// whether to render isolated nodes.
//
// Graph is a thin wrapper over GraphScoped with no project filter, preserved
// for existing whole-store callers.
func (s *Store) Graph(k int, minScore float32) ([]GraphNode, []GraphEdge, error) {
	return s.GraphScoped(nil, k, minScore)
}

// GraphScoped is Graph restricted to a SET of projects via a SQL WHERE...IN
// clause, mirroring SearchScoped's project-scoping fix (engram obs #1436) but
// generalized to a project SET so a group-parent graph view (parent +
// children) can be scoped in ONE query instead of scanning the whole store.
//
// The O(N^2) pairwise scan below only ever runs over the rows this WHERE
// clause returns: for a project that is a small fraction of a large shared
// store, this is the difference between a sub-second query and a full-store
// scan on every /graph page load regardless of the requested project (audit
// finding H3 — engram obs #1484/#1488).
//
// projects == nil or empty behaves exactly like Graph — no WHERE clause is
// added, so the whole-store view is byte-for-byte unaffected by this
// addition.
func (s *Store) GraphScoped(projects []string, k int, minScore float32) ([]GraphNode, []GraphEdge, error) {
	if nodes, edges, ok := s.tryVecGraph(projects, k, minScore); ok {
		return nodes, edges, nil
	}

	q := `SELECT sync_id, obs_id, COALESCE(project,''), COALESCE(type,''), COALESCE(title,''), vector FROM embeddings`
	var args []any
	if len(projects) > 0 {
		placeholders := make([]string, len(projects))
		for i, p := range projects {
			placeholders[i] = "?"
			args = append(args, p)
		}
		q += ` WHERE project IN (` + strings.Join(placeholders, ", ") + `)`
	}
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("embed: GraphScoped: %w", err)
	}
	defer rows.Close()

	type record struct {
		node GraphNode
		vec  []float32
	}
	var recs []record
	for rows.Next() {
		var n GraphNode
		var blob []byte // never sql.RawBytes — that buffer is reused per Next()
		if err := rows.Scan(&n.SyncID, &n.ObsID, &n.Project, &n.Type, &n.Title, &blob); err != nil {
			return nil, nil, fmt.Errorf("embed: GraphScoped scan: %w", err)
		}
		vec, derr := decodeVector(blob)
		if derr != nil {
			continue // skip undecodable vectors defensively
		}
		recs = append(recs, record{node: n, vec: vec})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	n := len(recs)

	// Candidate neighbor lists, in BOTH directions, computed from the upper
	// triangle so each dot product is evaluated once.
	type neighbor struct {
		idx   int
		score float32
	}
	nbrs := make([][]neighbor, n)
	for i := 0; i < n; i++ {
		vi := recs[i].vec
		for j := i + 1; j < n; j++ {
			vj := recs[j].vec
			if len(vi) != len(vj) {
				continue // dim mismatch (e.g. half-migrated store) — skip
			}
			sc := dot(vi, vj)
			if sc >= minScore {
				nbrs[i] = append(nbrs[i], neighbor{idx: j, score: sc})
				nbrs[j] = append(nbrs[j], neighbor{idx: i, score: sc})
			}
		}
	}

	// Keep each node's top-k candidates and union the directed picks into a single
	// undirected, deduped edge set keyed by the ordered index pair. dot is
	// symmetric, so the score is identical from either endpoint.
	type pair struct{ a, b int }
	edgeScore := make(map[pair]float32)
	for i := range nbrs {
		list := nbrs[i]
		sort.Slice(list, func(x, y int) bool { return list[x].score > list[y].score })
		if k > 0 && len(list) > k {
			list = list[:k]
		}
		for _, nb := range list {
			a, b := i, nb.idx
			if a > b {
				a, b = b, a
			}
			edgeScore[pair{a, b}] = nb.score
		}
	}

	nodes := make([]GraphNode, n)
	for i := range recs {
		nodes[i] = recs[i].node
	}
	edges := make([]GraphEdge, 0, len(edgeScore))
	for p, sc := range edgeScore {
		nodes[p.a].Degree++
		nodes[p.b].Degree++
		edges = append(edges, GraphEdge{
			Source: recs[p.a].node.ObsID,
			Target: recs[p.b].node.ObsID,
			Weight: sc,
		})
	}
	// Deterministic edge order for stable output and tests.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})
	return nodes, edges, nil
}

// DeleteBySyncID physically removes a single row by sync_id (memory-provenance
// foundation, omnia-provenance-foundation): the fan-out target for a hard
// delete. A real DELETE — not a soft/mark — because a soft-deleted vector is
// still reconstructible (design obs #1601's "Ghost Vectors" concern) and
// hard-delete's whole point is a provable physical purge. Deleting a sync_id
// that was never embedded (embeddings disabled, or not yet reconciled) is a
// no-op, not an error: 0 rows removed, nil error.
func (s *Store) DeleteBySyncID(ctx context.Context, syncID string) (int, error) {
	if !s.vec.healthyOK() {
		res, err := s.db.ExecContext(ctx, `DELETE FROM embeddings WHERE sync_id = ?`, syncID)
		if err != nil {
			return 0, fmt.Errorf("embed: DeleteBySyncID %s: %w", syncID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("embed: DeleteBySyncID %s rows affected: %w", syncID, err)
		}
		return int(n), nil
	}

	// Vec1 additive dual-write: mirror the delete into the derived table
	// inside the same transaction as the source delete (design capability 7).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("embed: DeleteBySyncID %s: begin tx: %w", syncID, err)
	}
	var rowid sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT rowid FROM embeddings WHERE sync_id = ?`, syncID).Scan(&rowid); err != nil && err != sql.ErrNoRows {
		tx.Rollback()
		return 0, fmt.Errorf("embed: DeleteBySyncID %s: locate rowid: %w", syncID, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE sync_id = ?`, syncID)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("embed: DeleteBySyncID %s: %w", syncID, err)
	}
	if rowid.Valid {
		if _, derr := tx.ExecContext(ctx, `DELETE FROM vec_embeddings WHERE rowid = ?`, rowid.Int64); derr != nil {
			tx.Rollback()
			s.vec.markUnhealthy(derr)
			res2, err2 := s.db.ExecContext(ctx, `DELETE FROM embeddings WHERE sync_id = ?`, syncID)
			if err2 != nil {
				return 0, fmt.Errorf("embed: DeleteBySyncID %s: source-only retry after derived failure: %w", syncID, err2)
			}
			n2, err2 := res2.RowsAffected()
			if err2 != nil {
				return 0, fmt.Errorf("embed: DeleteBySyncID %s rows affected: %w", syncID, err2)
			}
			return int(n2), nil
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("embed: DeleteBySyncID %s: commit: %w", syncID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("embed: DeleteBySyncID %s rows affected: %w", syncID, err)
	}
	return int(n), nil
}

// Prune deletes rows whose sync_id is not in liveSyncIDs. Returns rows removed.
func (s *Store) Prune(ctx context.Context, liveSyncIDs []string) (int, error) {
	live := make(map[string]struct{}, len(liveSyncIDs))
	for _, id := range liveSyncIDs {
		live[id] = struct{}{}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT sync_id FROM embeddings`)
	if err != nil {
		return 0, fmt.Errorf("embed: Prune list: %w", err)
	}
	var toDelete []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("embed: Prune scan: %w", err)
		}
		if _, ok := live[id]; !ok {
			toDelete = append(toDelete, id)
		}
	}
	rows.Close() // close before issuing DELETEs on the single connection
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range toDelete {
		if !s.vec.healthyOK() {
			// Disabled path: the ORIGINAL pre-v0.4 statement, untouched — no
			// derived write, no RowsAffected() call, no double-wrapped error
			// text (review remediation SHOULD-FIX #2: routing this through
			// DeleteBySyncID unconditionally changed disabled-path behavior).
			if _, err := s.db.ExecContext(ctx, `DELETE FROM embeddings WHERE sync_id = ?`, id); err != nil {
				return 0, fmt.Errorf("embed: Prune delete %s: %w", id, err)
			}
			continue
		}
		// Vec1 enabled: reuse DeleteBySyncID so Prune gets the exact same
		// derived dual-write/fallback contract for free (design capability
		// 7), rather than duplicating it here. This is a NEW branch that
		// only ever runs when Vec1 is healthy, so it never changes the
		// disabled-path behavior above.
		if _, err := s.DeleteBySyncID(ctx, id); err != nil {
			return 0, fmt.Errorf("embed: Prune delete %s: %w", id, err)
		}
	}
	return len(toDelete), nil
}

// encodeVector serializes v as little-endian float32 (dim*4 bytes).
func encodeVector(v []float32) ([]byte, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("embed: refuse to store empty vector")
	}
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf, nil
}

// decodeVector reverses encodeVector.
func decodeVector(b []byte) ([]float32, error) {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil, fmt.Errorf("embed: bad vector blob length %d", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// dot computes the dot product of two equal-length vectors.
func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
