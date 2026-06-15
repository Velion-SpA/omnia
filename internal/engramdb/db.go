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
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// Observation mirrors the columns from Engram's observations table that the
// dashboard needs. Timestamps are stored as plain text in SQLite
// (format: "2006-01-02 15:04:05" in UTC). Fields not needed by the dashboard
// (sync_id, session_id, tool_name, revision_count) are omitted.
type Observation struct {
	ID        int
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
//  2. $ENGRAM_DATA_DIR environment variable
//  3. ~/.engram
//
// The DSN uses mode=ro and a 5-second busy-timeout, safe for concurrent reads
// while Engram writes in WAL mode. Returns an error if the file does not exist
// or cannot be pinged.
func Open(dataDir string) (*DB, error) {
	dir := resolveDataDir(dataDir)
	path := filepath.Join(dir, "engram.db")

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
	if dataDir != "" {
		return dataDir
	}
	if env := os.Getenv("ENGRAM_DATA_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".engram"
	}
	return filepath.Join(home, ".engram")
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
