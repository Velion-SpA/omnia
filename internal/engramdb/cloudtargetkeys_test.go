package engramdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Velion-SpA/omnia/internal/engramdb"
)

// createSyncTablesDB builds a throwaway DB with the sync tables CloudTargetKeys
// reads, plus a minimal observations table so engramdb.Open's ping succeeds.
// Returns the data dir.
func createSyncTablesDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engram.db")

	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, deleted_at TEXT)`,
		`CREATE TABLE sync_chunks (
			target_key  TEXT NOT NULL DEFAULT 'local',
			chunk_id    TEXT NOT NULL,
			imported_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (target_key, chunk_id)
		)`,
		`CREATE TABLE sync_state (
			target_key           TEXT PRIMARY KEY,
			lifecycle            TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
			last_acked_seq       INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		// Pushed via chunks → should appear.
		`INSERT INTO sync_chunks (target_key, chunk_id) VALUES ('cloud:omnia', 'aaa')`,
		`INSERT INTO sync_chunks (target_key, chunk_id) VALUES ('work:omnia', 'bbb')`,
		// Local chunk tracking → must be excluded.
		`INSERT INTO sync_chunks (target_key, chunk_id) VALUES ('local', 'ccc')`,
		// Healthy per-project state → should appear.
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:velion', 'healthy')`,
		// Advanced cursor but not healthy → should appear.
		`INSERT INTO sync_state (target_key, last_pulled_seq) VALUES ('personal:notes', 5)`,
		// Blocked / idle placeholders → must NOT appear (failed/never-completed).
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:blockedproj', 'blocked')`,
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:idleproj', 'idle')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v\nstmt: %s", err, s)
		}
	}
	return dir
}

func TestCloudTargetKeys(t *testing.T) {
	dir := createSyncTablesDB(t)
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	got, err := db.CloudTargetKeys(context.Background())
	if err != nil {
		t.Fatalf("CloudTargetKeys: %v", err)
	}

	want := map[string]struct{}{
		"cloud:omnia":    {},
		"work:omnia":     {},
		"cloud:velion":   {},
		"personal:notes": {},
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing expected target key %q; got %v", k, keys(got))
		}
	}
	for _, unexpected := range []string{"local", "cloud:blockedproj", "cloud:idleproj"} {
		if _, ok := got[unexpected]; ok {
			t.Errorf("target key %q should be excluded; got %v", unexpected, keys(got))
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys %v, want %d", len(got), keys(got), len(want))
	}
}

// TestCloudTargetKeys_NoSyncTables verifies graceful degradation on an older DB
// that predates the sync tables: no error, empty set.
func TestCloudTargetKeys_NoSyncTables(t *testing.T) {
	dir := createTestDB(t) // observations only, no sync tables
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	got, err := db.CloudTargetKeys(context.Background())
	if err != nil {
		t.Fatalf("CloudTargetKeys on table-less DB must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty set, got %v", keys(got))
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
