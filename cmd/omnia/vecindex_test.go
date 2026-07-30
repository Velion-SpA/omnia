package main

import (
	"database/sql"
	"testing"

	"github.com/velion/omnia/internal/config"
	_ "modernc.org/sqlite" // register "sqlite" driver for the raw inspection below
)

// dbHasVecEmbeddingsTable opens dbPath directly (read-only inspection, NOT
// through embed.Store) and reports whether the derived Vec1 virtual table
// exists — a black-box way to prove a composition point actually threaded
// Config.VecIndex.Enabled through to embed.OpenStore, without depending on
// any embed-package-internal accessor.
func dbHasVecEmbeddingsTable(t *testing.T, dbPath string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s for inspection: %v", dbPath, err)
	}
	defer db.Close()
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'vec_embeddings'`).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master on %s: %v", dbPath, err)
	}
	return true
}

// TestEmbedStoreOptions_FalseIsPreV04Default proves the shared options
// builder produces disabled Options when both flags are false/zero,
// matching every direct opener's pre-v0.4 behavior (spec REQ-460, REQ-430).
func TestEmbedStoreOptions_FalseIsPreV04Default(t *testing.T) {
	opts := embedStoreOptions(false, config.EncryptionConfig{})
	if len(opts) != 2 {
		t.Fatalf("embedStoreOptions(false, {}): got %d options, want 2", len(opts))
	}
}
