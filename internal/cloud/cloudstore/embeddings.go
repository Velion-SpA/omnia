package cloudstore

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// EmbeddingRow is a stored cloud embedding record, scoped by account + project
// — the cloud multi-tenant boundary (mirrors cloud_memberships/cloud_devices).
// It carries the same vector BLOB/model/dim/content_hash/updated_at shape as
// the LOCAL embeddings schema (internal/embed.Row), keyed by
// (account_id, project, sync_id) instead of a single-tenant sync_id.
type EmbeddingRow struct {
	AccountID   string
	Project     string
	SyncID      string
	Type        string
	Vector      []float32
	Model       string
	Dim         int
	ContentHash string
	UpdatedAt   time.Time // zero value defaults to time.Now().UTC() on upsert
}

// EmbeddingHit is a scoped semantic search result: a stored row's sync
// identity and its cosine similarity score against the query vector.
type EmbeddingHit struct {
	SyncID string
	Score  float32
}

// embeddingCandidate is a decoded (sync_id, vector) pair ready for scoring.
// Kept separate from EmbeddingRow so rankEmbeddingHits stays pure and
// DB-independent.
type embeddingCandidate struct {
	SyncID string
	Vector []float32
}

// UpsertEmbedding inserts or replaces a cloud embedding row, keyed by
// (account_id, project, sync_id). Mirrors embed.Store.Upsert's ON CONFLICT
// semantics, scoped by account+project — cloudstore's multi-tenant boundary —
// instead of a single-tenant sync_id.
func (cs *CloudStore) UpsertEmbedding(ctx context.Context, row EmbeddingRow) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	accountID := strings.TrimSpace(row.AccountID)
	project := strings.TrimSpace(row.Project)
	syncID := strings.TrimSpace(row.SyncID)
	if accountID == "" {
		return fmt.Errorf("cloudstore: account_id is required")
	}
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	if syncID == "" {
		return fmt.Errorf("cloudstore: sync_id is required")
	}
	blob, err := encodeEmbeddingVector(row.Vector)
	if err != nil {
		return err
	}
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	const q = `
		INSERT INTO cloud_embeddings (account_id, project, sync_id, type, vector, model, dim, content_hash, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (account_id, project, sync_id) DO UPDATE SET
			type = EXCLUDED.type,
			vector = EXCLUDED.vector,
			model = EXCLUDED.model,
			dim = EXCLUDED.dim,
			content_hash = EXCLUDED.content_hash,
			updated_at = EXCLUDED.updated_at`
	_, err = cs.db.ExecContext(ctx, q, accountID, project, syncID, row.Type, blob, row.Model, row.Dim, row.ContentHash, updatedAt)
	if err != nil {
		return fmt.Errorf("cloudstore: upsert embedding %s: %w", syncID, err)
	}
	return nil
}

// SearchEmbeddings returns the top-k cloud_embeddings rows by cosine
// similarity to query, STRICTLY scoped to (accountID, project).
//
// This scoping is the hard requirement: a cross-account or cross-project leak
// here is the multi-tenant equivalent of the PR3 local cross-project recall
// leak (fixed via recallScopeFilter in internal/mcp) — and worse, since this
// is shared cloud infra, not a single local process. The WHERE clause below
// is the only thing standing between two tenants' vectors; only rows it
// returns are ever loaded, decoded, or scored — there is no broader scan to
// filter after the fact.
func (cs *CloudStore) SearchEmbeddings(ctx context.Context, accountID, project string, query []float32, k int) ([]EmbeddingHit, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	project = strings.TrimSpace(project)
	if accountID == "" {
		return nil, fmt.Errorf("cloudstore: account_id is required")
	}
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}

	const q = `SELECT sync_id, vector FROM cloud_embeddings WHERE account_id = $1 AND project = $2`
	rows, err := cs.db.QueryContext(ctx, q, accountID, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: search embeddings: %w", err)
	}
	defer rows.Close()

	var candidates []embeddingCandidate
	for rows.Next() {
		var syncID string
		var blob []byte // never sql.RawBytes — the driver may reuse the buffer per Next()
		if err := rows.Scan(&syncID, &blob); err != nil {
			return nil, fmt.Errorf("cloudstore: search embeddings scan: %w", err)
		}
		vec, err := decodeEmbeddingVector(blob)
		if err != nil {
			continue // skip undecodable rows defensively, mirrors embed.Store.Search
		}
		candidates = append(candidates, embeddingCandidate{SyncID: syncID, Vector: vec})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return rankEmbeddingHits(query, candidates, k), nil
}

// rankEmbeddingHits scores candidates against query by cosine similarity (dot
// product — both sides assumed unit-normalized, the SAME convention as
// internal/embed.Store.Search) and returns the top-k, descending by score.
// Candidates whose vector dimension differs from the query's are skipped
// defensively (e.g. a half-migrated store after a model change). Pure — no
// DB, no I/O — so this is unit-testable without Postgres. k <= 0 returns all
// ranked hits.
func rankEmbeddingHits(query []float32, candidates []embeddingCandidate, k int) []EmbeddingHit {
	var hits []EmbeddingHit
	for _, c := range candidates {
		if len(c.Vector) != len(query) {
			continue
		}
		hits = append(hits, EmbeddingHit{SyncID: c.SyncID, Score: embeddingDot(query, c.Vector)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// encodeEmbeddingVector serializes v as little-endian float32 (dim*4 bytes) —
// the SAME encoding as internal/embed's encodeVector, so a vector computed
// locally and synced verbatim decodes identically on the cloud side.
func encodeEmbeddingVector(v []float32) ([]byte, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("cloudstore: refuse to store empty vector")
	}
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf, nil
}

// decodeEmbeddingVector reverses encodeEmbeddingVector.
func decodeEmbeddingVector(b []byte) ([]float32, error) {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil, fmt.Errorf("cloudstore: bad vector blob length %d", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// embeddingDot computes the dot product of two equal-length vectors — the
// SAME cosine math as internal/embed's dot (both vectors are assumed
// unit-normalized upstream, so dot product IS cosine similarity).
func embeddingDot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
