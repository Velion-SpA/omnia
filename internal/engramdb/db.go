// Package engramdb provides a read-only structural reader for the Engram SQLite
// database. It uses direct SQL queries for exact enumeration and filtering,
// superseding the FTS text-search workaround for structural operations.
//
// The SQLite driver is modernc.org/sqlite (pure-Go, no CGO), identical to the
// one Engram uses. WAL mode is left to the existing DB; opening read-only while
// Engram writes concurrently is safe per SQLite WAL semantics.
package engramdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/datadir"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// Observation mirrors the columns from Engram's observations table that the
// dashboard needs. Timestamps are stored as plain text in SQLite
// (format: "2006-01-02 15:04:05" in UTC). Fields not needed by the dashboard
// (session_id, tool_name, revision_count) are omitted. SyncID is populated only
// by queries that select it (ListForEmbedding, ListByIDs); the hot-path List
// leaves it empty.
type Observation struct {
	ID        int
	SyncID    string
	Type      string
	Title     string
	Content   string
	Project   string
	Scope     string
	TopicKey  string
	CreatedAt string
	UpdatedAt string
}

// ProjectCount pairs a project name with its live (non-deleted) observation count.
type ProjectCount struct {
	Name  string
	Count int
}

// TypeCount pairs an observation type name with its live count.
type TypeCount struct {
	Name  string
	Count int
}

// Filter specifies optional constraints for List and Count queries.
// All fields are optional; zero values mean "no constraint on this field".
// Limit defaults to 200 when 0; Offset defaults to 0 when negative.
type Filter struct {
	Project string
	// CanonicalProject, when non-empty, matches rows WHERE LOWER(TRIM(project)) = LOWER(TRIM(?)).
	// Handles case-only variants (homelab, Homelab, HOMELAB → same result set).
	// Takes priority over Project when both are set.
	CanonicalProject string
	// RawProjects, when non-empty, matches rows WHERE project IN (?, ...).
	// Used for alias expansion when multiple distinct raw names map to one canonical.
	// Ignored when CanonicalProject is set.
	RawProjects []string
	Type        string
	Scope       string
	Limit       int
	Offset      int
}

// DB is a read-only connection to an Engram SQLite database.
type DB struct {
	db *sql.DB
}

// Open resolves the database path and opens a read-only connection.
//
// Resolution order for dataDir:
//  1. dataDir parameter (if non-empty)
//  2. $OMNIA_DATA_DIR (or legacy $ENGRAM_DATA_DIR)
//  3. ~/.omnia (migrating a legacy ~/.engram once, if present)
//
// The database file is omnia.db, falling back to a legacy engram.db when only
// the old filename is present. The DSN uses mode=ro and a 5-second busy-timeout,
// safe for concurrent reads while Omnia writes in WAL mode. Returns an error if
// the file does not exist or cannot be pinged.
func Open(dataDir string) (*DB, error) {
	dir := resolveDataDir(dataDir)
	path := datadir.DBPath(dir)

	// Read-only SQLite URI. Path values from resolveDataDir are system paths
	// (no user input), so direct concatenation is safe here.
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("engramdb: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("engramdb: ping %s: %w", path, err)
	}
	return &DB{db: db}, nil
}

func resolveDataDir(dataDir string) string {
	return datadir.Resolve(dataDir)
}

// ResolveDataDir exposes the same data-directory resolution Open uses, so callers
// that need sibling files in the Omnia data dir (e.g. cloud.json) resolve the
// exact same directory the database was opened from.
func ResolveDataDir(dataDir string) string {
	return resolveDataDir(dataDir)
}

// CloudTargetKeys returns the set of cloud sync target keys that show real
// replication activity in the local store. A target key qualifies when the
// project has recorded synced chunks (sync_chunks) or has a healthy/advanced
// sync_state row — pure idle/blocked placeholder rows are excluded so a failed
// or never-completed attempt does not look like a successful push. The local
// chunk-tracking key ("local") is excluded.
//
// The query is strictly read-only and tolerant: on an older database that
// predates the sync tables (or any per-source query error) it skips that source
// and returns whatever it could read, never failing the overview.
func (d *DB) CloudTargetKeys(ctx context.Context) (map[string]struct{}, error) {
	out := map[string]struct{}{}

	collect := func(query string) {
		rows, err := d.db.QueryContext(ctx, query)
		if err != nil {
			return // table absent or unreadable — degrade silently
		}
		defer rows.Close()
		for rows.Next() {
			var tk string
			if err := rows.Scan(&tk); err != nil {
				return
			}
			if tk = strings.TrimSpace(tk); tk != "" && tk != "local" {
				out[tk] = struct{}{}
			}
		}
	}

	// Strongest signal: a chunk was actually exported/imported for this target.
	collect(`SELECT DISTINCT target_key FROM sync_chunks WHERE target_key <> 'local'`)
	// Secondary: a per-target sync_state that reached healthy or advanced a cursor.
	collect(`SELECT target_key FROM sync_state
		WHERE target_key <> 'local'
		  AND (lifecycle = 'healthy' OR last_acked_seq > 0 OR last_pulled_seq > 0)`)

	return out, nil
}

// Close releases the database connection pool.
func (d *DB) Close() error {
	return d.db.Close()
}

// Projects returns every project that has live observations, with counts.
// Rows where project IS NULL or empty string are excluded.
// Ordered by count descending, then project name ascending.
func (d *DB) Projects(ctx context.Context) ([]ProjectCount, error) {
	const q = `
		SELECT project, COUNT(*) AS cnt
		FROM observations
		WHERE deleted_at IS NULL
		  AND project IS NOT NULL
		  AND project != ''
		GROUP BY project
		ORDER BY cnt DESC, project ASC`

	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("engramdb: Projects: %w", err)
	}
	defer rows.Close()

	var out []ProjectCount
	for rows.Next() {
		var pc ProjectCount
		if err := rows.Scan(&pc.Name, &pc.Count); err != nil {
			return nil, fmt.Errorf("engramdb: Projects scan: %w", err)
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// TypesByProject returns distinct observation types and their live counts for
// the given project. Ordered by count descending, then type name ascending.
func (d *DB) TypesByProject(ctx context.Context, project string) ([]TypeCount, error) {
	const q = `
		SELECT type, COUNT(*) AS cnt
		FROM observations
		WHERE deleted_at IS NULL
		  AND project = ?
		  AND type IS NOT NULL
		  AND type != ''
		GROUP BY type
		ORDER BY cnt DESC, type ASC`

	rows, err := d.db.QueryContext(ctx, q, project)
	if err != nil {
		return nil, fmt.Errorf("engramdb: TypesByProject: %w", err)
	}
	defer rows.Close()

	var out []TypeCount
	for rows.Next() {
		var tc TypeCount
		if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
			return nil, fmt.Errorf("engramdb: TypesByProject scan: %w", err)
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// Types returns distinct observation types and their live counts across all
// projects. Ordered by count descending, then type name ascending.
func (d *DB) Types(ctx context.Context) ([]TypeCount, error) {
	const q = `
		SELECT type, COUNT(*) AS cnt
		FROM observations
		WHERE deleted_at IS NULL
		  AND type IS NOT NULL
		  AND type != ''
		GROUP BY type
		ORDER BY cnt DESC, type ASC`

	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("engramdb: Types: %w", err)
	}
	defer rows.Close()

	var out []TypeCount
	for rows.Next() {
		var tc TypeCount
		if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
			return nil, fmt.Errorf("engramdb: Types scan: %w", err)
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// List returns observations matching the filter, ordered by updated_at DESC.
// Soft-deleted rows (deleted_at IS NOT NULL) are always excluded.
// User-supplied filter values are always bound as SQL parameters and never
// string-interpolated, preventing SQL injection.
func (d *DB) List(ctx context.Context, f Filter) ([]Observation, error) {
	where, args := buildWhere(f)
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	q := `SELECT id,
		         COALESCE(type,''),
		         COALESCE(title,''),
		         COALESCE(content,''),
		         COALESCE(project,''),
		         COALESCE(scope,''),
		         COALESCE(topic_key,''),
		         COALESCE(created_at,''),
		         COALESCE(updated_at,'')
		  FROM observations
		  WHERE ` + where + `
		  ORDER BY updated_at DESC
		  LIMIT ? OFFSET ?`

	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("engramdb: List: %w", err)
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(
			&o.ID, &o.Type, &o.Title, &o.Content,
			&o.Project, &o.Scope, &o.TopicKey,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("engramdb: List scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Count returns the number of live (non-deleted) observations matching f.
func (d *DB) Count(ctx context.Context, f Filter) (int, error) {
	where, args := buildWhere(f)
	q := `SELECT COUNT(*) FROM observations WHERE ` + where

	var n int
	if err := d.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("engramdb: Count: %w", err)
	}
	return n, nil
}

// ListForEmbedding returns every live (non-deleted) observation with the columns
// the embeddings reconciler needs: ID, SyncID, Type, Title, Content, Project,
// TopicKey, UpdatedAt. SyncID is the stable cross-instance key; ID is the local
// (autoincrement) row id used for the dashboard detail link. The existing List
// SELECT is intentionally left untouched so the dashboard hot path is unaffected.
func (d *DB) ListForEmbedding(ctx context.Context) ([]Observation, error) {
	const q = `
		SELECT id,
		       COALESCE(sync_id,''),
		       COALESCE(type,''),
		       COALESCE(title,''),
		       COALESCE(content,''),
		       COALESCE(project,''),
		       COALESCE(topic_key,''),
		       COALESCE(updated_at,'')
		FROM observations
		WHERE deleted_at IS NULL
		ORDER BY updated_at DESC`

	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("engramdb: ListForEmbedding: %w", err)
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(
			&o.ID, &o.SyncID, &o.Type, &o.Title, &o.Content,
			&o.Project, &o.TopicKey, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("engramdb: ListForEmbedding scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListByIDs returns live observations whose id is in ids, with full columns
// (including SyncID). Used by semantic search to re-fetch ranked rows for
// rendering. The returned order is unspecified; callers that need ranking order
// should reorder by id. Returns an empty slice when ids is empty.
func (d *DB) ListByIDs(ctx context.Context, ids []int) ([]Observation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT id,
		         COALESCE(sync_id,''),
		         COALESCE(type,''),
		         COALESCE(title,''),
		         COALESCE(content,''),
		         COALESCE(project,''),
		         COALESCE(scope,''),
		         COALESCE(topic_key,''),
		         COALESCE(created_at,''),
		         COALESCE(updated_at,'')
		  FROM observations
		  WHERE deleted_at IS NULL
		    AND id IN (` + strings.Join(placeholders, ", ") + `)`

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("engramdb: ListByIDs: %w", err)
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(
			&o.ID, &o.SyncID, &o.Type, &o.Title, &o.Content,
			&o.Project, &o.Scope, &o.TopicKey,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("engramdb: ListByIDs scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// buildWhere constructs a parameterized WHERE clause from a Filter.
// The returned clause always includes "deleted_at IS NULL".
// User values are appended to args as parameters — never interpolated.
// Priority for project matching: CanonicalProject > RawProjects > Project.
func buildWhere(f Filter) (string, []any) {
	clauses := []string{"deleted_at IS NULL"}
	var args []any

	switch {
	case f.CanonicalProject != "":
		clauses = append(clauses, "LOWER(TRIM(project)) = LOWER(TRIM(?))")
		args = append(args, f.CanonicalProject)
	case len(f.RawProjects) > 0:
		placeholders := make([]string, len(f.RawProjects))
		for i := range f.RawProjects {
			placeholders[i] = "?"
		}
		clauses = append(clauses, "project IN ("+strings.Join(placeholders, ", ")+")")
		for _, p := range f.RawProjects {
			args = append(args, p)
		}
	case f.Project != "":
		clauses = append(clauses, "project = ?")
		args = append(args, f.Project)
	}

	if f.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, f.Type)
	}
	if f.Scope != "" {
		clauses = append(clauses, "scope = ?")
		args = append(args, f.Scope)
	}

	return strings.Join(clauses, " AND "), args
}

// ProjectsCanonical returns the same projects as Projects but with each raw
// project name run through the provided canonicalize function. Counts for
// names that collapse to the same canonical key are summed.
// Ordered by count descending, then canonical name ascending.
func (d *DB) ProjectsCanonical(ctx context.Context, canonicalize func(string) string) ([]ProjectCount, error) {
	raw, err := d.Projects(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, pc := range raw {
		key := canonicalize(pc.Name)
		counts[key] += pc.Count
	}
	out := make([]ProjectCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, ProjectCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
