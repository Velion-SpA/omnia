package main

import (
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// ─── #234 [RED]: the eval harness must open the store with encryption ───
//
// defaultRunEvalHarness built its store.Config from store.DefaultConfig()
// alone and never threaded Encryption, so against a store written by
// `omnia security encrypt` it died on the first pragma:
//
//	engram: pragma "PRAGMA journal_mode = WAL": file is not a database (26)
//
// That takes `rank-train`'s promotion gate down with it — the learned ranker
// can train a model and never promote one, reporting "evaluation regressed or
// failed" when the harness simply could not open the database.

func TestEvalStoreConfig_ThreadsEncryptionFromAppConfig(t *testing.T) {
	base := store.Config{DataDir: t.TempDir()}
	app := &config.Config{}
	app.Encryption.Enabled = true
	app.Encryption.KeychainService = "omnia-test"
	app.Encryption.AllowPlaintextFallback = true

	got := evalStoreConfig(base, app)

	if !got.EncryptionEnabled {
		t.Fatal("eval must open the store with encryption enabled when config.yaml says so")
	}
	if got.EncryptionKeychainService != "omnia-test" {
		t.Fatalf("keychain service must be threaded: got %q", got.EncryptionKeychainService)
	}
	if !got.EncryptionAllowPlaintextFallback {
		t.Fatal("allow_plaintext_fallback must be threaded")
	}
	if got.DataDir != base.DataDir {
		t.Fatalf("the base config must be preserved: got DataDir %q", got.DataDir)
	}
}

// A missing/unreadable config.yaml is the fresh-install case: encryption is
// off by default, and eval must still run exactly as before this fix.
func TestEvalStoreConfig_NilAppConfigLeavesBaseUnchanged(t *testing.T) {
	base := store.Config{DataDir: t.TempDir()}

	got := evalStoreConfig(base, nil)

	if got.EncryptionEnabled {
		t.Fatal("no config.yaml must not turn encryption on")
	}
	if got != base {
		t.Fatalf("a nil app config must leave the store config byte-for-byte unchanged: %+v vs %+v", got, base)
	}
}

// Encryption explicitly disabled must also be a no-op, not an accidental enable.
func TestEvalStoreConfig_DisabledEncryptionStaysDisabled(t *testing.T) {
	base := store.Config{DataDir: t.TempDir()}
	app := &config.Config{}
	app.Encryption.Enabled = false

	got := evalStoreConfig(base, app)

	if got.EncryptionEnabled {
		t.Fatal("encryption.enabled=false must stay disabled")
	}
}
