package engramdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/engramdb"
)

// createFullSyncStateDB builds a throwaway DB with the full sync_state schema
// (including the reason_code/reason_message/last_error columns added via the
// guarded ALTER in internal/store), seeded with one row per lifecycle so
// SyncStates' tolerant-but-complete read path is exercised end to end.
func createFullSyncStateDB(t *testing.T) string {
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
		`CREATE TABLE sync_state (
			target_key           TEXT PRIMARY KEY,
			lifecycle            TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
			last_acked_seq       INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			backoff_until        TEXT,
			lease_owner          TEXT,
			lease_until          TEXT,
			last_error           TEXT,
			reason_code          TEXT,
			reason_message       TEXT,
			updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`INSERT INTO sync_state (target_key, lifecycle, last_acked_seq) VALUES ('cloud:omnia', 'healthy', 42)`,
		// Degraded with ZERO chunks/cursor advance — CloudTargetKeys excludes this
		// entirely; SyncStates must still return it (OBL-12 gap).
		`INSERT INTO sync_state (target_key, lifecycle, reason_code, reason_message, last_error) VALUES ('work:velion', 'degraded', 'auth_required', 'token expired', 'HTTP 401')`,
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('personal:notes', 'pending')`,
		// Local chunk tracking → must be excluded.
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('local', 'healthy')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v\nstmt: %s", err, s)
		}
	}
	return dir
}

func TestSyncStates(t *testing.T) {
	dir := createFullSyncStateDB(t)
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	got, err := db.SyncStates(context.Background())
	if err != nil {
		t.Fatalf("SyncStates: %v", err)
	}

	byKey := map[string]engramdb.SyncTargetState{}
	for _, s := range got {
		byKey[s.TargetKey] = s
	}

	if _, ok := byKey["local"]; ok {
		t.Errorf("local target key should be excluded; got %+v", got)
	}
	if len(byKey) != 3 {
		t.Fatalf("got %d states %+v, want 3", len(byKey), got)
	}

	healthy, ok := byKey["cloud:omnia"]
	if !ok || healthy.Lifecycle != "healthy" || healthy.LastAckedSeq != 42 {
		t.Errorf("cloud:omnia = %+v, want healthy with last_acked_seq=42", healthy)
	}

	degraded, ok := byKey["work:velion"]
	if !ok || degraded.Lifecycle != "degraded" || degraded.ReasonCode != "auth_required" ||
		degraded.ReasonMessage != "token expired" || degraded.LastError != "HTTP 401" {
		t.Errorf("work:velion = %+v, want degraded/auth_required with message+last_error", degraded)
	}

	pending, ok := byKey["personal:notes"]
	if !ok || pending.Lifecycle != "pending" || pending.ReasonCode != "" {
		t.Errorf("personal:notes = %+v, want pending with no reason", pending)
	}
}

// TestSyncStates_NoSyncTable verifies graceful degradation on an older DB that
// predates the sync_state table entirely: no error, empty result.
func TestSyncStates_NoSyncTable(t *testing.T) {
	dir := createTestDB(t) // observations only, no sync tables
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	got, err := db.SyncStates(context.Background())
	if err != nil {
		t.Fatalf("SyncStates on table-less DB must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
}

// TestSyncStates_MissingReasonColumns verifies graceful degradation on a DB
// whose sync_state predates the reason_code/reason_message ALTER (see
// internal/store's addColumnIfNotExists guarded migration): no error, empty
// result rather than a query failure.
func TestSyncStates_MissingReasonColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engram.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	stmts := []string{
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, deleted_at TEXT)`,
		`CREATE TABLE sync_state (
			target_key           TEXT PRIMARY KEY,
			lifecycle            TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
			last_acked_seq       INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:omnia', 'healthy')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			t.Fatalf("seed: %v\nstmt: %s", err, s)
		}
	}
	db.Close()

	eng, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer eng.Close()

	got, err := eng.SyncStates(context.Background())
	if err != nil {
		t.Fatalf("SyncStates on reason-column-less DB must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result (missing columns), got %+v", got)
	}
}
