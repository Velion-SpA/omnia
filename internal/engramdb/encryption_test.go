package engramdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/velion/omnia/internal/keychain"
)

// fakeReaderKeychain scripts GetOrCreateHexKey responses without shelling out
// to a real keychain — mirrors internal/store's fakeKeychain and
// internal/embed's fakeEmbedKeychain (all three share keychain.Resolver).
type fakeReaderKeychain struct {
	hexKey string
	err    error
}

func (f *fakeReaderKeychain) GetOrCreateHexKey(ctx context.Context, service, account string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.hexKey, false, nil
}

func withFakeReaderKeychain(t *testing.T, f *fakeReaderKeychain) {
	t.Helper()
	orig := newKeychainClient
	newKeychainClient = func() keychain.Resolver { return f }
	t.Cleanup(func() { newKeychainClient = orig })
}

const testHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// observationsDDL mirrors the subset of Omnia's observations schema this
// package reads (same columns db_test.go's createTestDB uses).
const observationsDDL = `CREATE TABLE observations (
	id             INTEGER PRIMARY KEY,
	sync_id        TEXT,
	session_id     TEXT,
	type           TEXT,
	title          TEXT NOT NULL DEFAULT '',
	content        TEXT,
	tool_name      TEXT,
	project        TEXT,
	scope          TEXT,
	topic_key      TEXT,
	revision_count INTEGER DEFAULT 0,
	created_at     TEXT,
	updated_at     TEXT,
	deleted_at     TEXT
)`

// createEncryptedTestDB writes an adiantum-encrypted omnia.db carrying one
// live observation, exactly as `omnia security encrypt` leaves the real store.
// Returns the data directory (not the file path), matching Open's argument.
func createEncryptedTestDB(t *testing.T, hexKey string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "omnia.db")

	db, err := ncrucesdriver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + hexKey + "'")
	})
	if err != nil {
		t.Fatalf("createEncryptedTestDB: open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(observationsDDL); err != nil {
		t.Fatalf("createEncryptedTestDB: create table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO observations
		(id, sync_id, type, title, content, project, scope, created_at, updated_at)
		VALUES (1, 'sync-1', 'decision', 'Encrypted title', 'encrypted-content', 'omnia', 'project',
		        '2024-01-01 00:00:00', '2024-01-02 00:00:00')`)
	if err != nil {
		t.Fatalf("createEncryptedTestDB: insert: %v", err)
	}
	return dir
}

// createPlaintextTestDB writes an unencrypted omnia.db with the same schema,
// standing in for a store whose operator flipped encryption.enabled=true
// without running the migration first.
func createPlaintextTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "omnia.db")

	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("createPlaintextTestDB: open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(observationsDDL); err != nil {
		t.Fatalf("createPlaintextTestDB: create table: %v", err)
	}
	return dir
}

// ─── #228 [RED]: the read-only reader must honor at-rest encryption ───

// The regression itself: `omnia embed` (and the dashboard's structural reader)
// open the memory database through this package, which had no encrypted path
// at all. Against an encrypted store every caller died with an opaque
// "file is not a database (26)".
func TestOpen_EncryptionEnabled_ReadsEncryptedDatabase(t *testing.T) {
	withFakeReaderKeychain(t, &fakeReaderKeychain{hexKey: testHexKey})
	dir := createEncryptedTestDB(t, testHexKey)

	db, err := Open(dir, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("Open (encrypted): %v", err)
	}
	defer db.Close()

	obs, err := db.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("want 1 observation from the encrypted store, got %d", len(obs))
	}
	if obs[0].Title != "Encrypted title" {
		t.Fatalf("encrypted-path read must round-trip the row: got title %q", obs[0].Title)
	}
}

// Reading must stay read-only: the encrypted path may not create, migrate or
// otherwise mutate the file it opens.
func TestOpen_EncryptionEnabled_DoesNotWriteToDatabase(t *testing.T) {
	withFakeReaderKeychain(t, &fakeReaderKeychain{hexKey: testHexKey})
	dir := createEncryptedTestDB(t, testHexKey)
	path := filepath.Join(dir, "omnia.db")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile (before): %v", err)
	}

	db, err := Open(dir, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("Open (encrypted): %v", err)
	}
	if _, err := db.List(context.Background(), Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	db.Close()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile (after): %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("read-only Open must not modify the database file")
	}
}

// Encryption disabled (the zero value) must be byte-for-byte the pre-fix
// modernc read-only path — no keychain consultation of any kind.
func TestOpen_EncryptionDisabled_UnchangedPlaintextPath(t *testing.T) {
	withFakeReaderKeychain(t, &fakeReaderKeychain{err: keychain.ErrKeyUnavailable})
	dir := createPlaintextTestDB(t)

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (plaintext, encryption off): %v", err)
	}
	defer db.Close()
	if _, err := db.List(context.Background(), Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
}

// An existing plaintext file with encryption.enabled=true is the operator
// flipping the flag without migrating. Without this guard the adiantum VFS
// reports the same opaque "file is not a database" the fix exists to kill.
func TestOpen_EncryptionEnabled_PlaintextFileGivesActionableError(t *testing.T) {
	withFakeReaderKeychain(t, &fakeReaderKeychain{hexKey: testHexKey})
	dir := createPlaintextTestDB(t)

	db, err := Open(dir, WithEncryption(true, "omnia", false))
	if err == nil {
		db.Close()
		t.Fatal("Open must refuse a plaintext database when encryption.enabled=true")
	}
	if !strings.Contains(err.Error(), "omnia security encrypt") {
		t.Fatalf("error must point at the migration command, got: %v", err)
	}
}

// Fail-closed (spec REQ-434): an unreachable keychain with the default
// allow_plaintext_fallback=false must refuse to open, never silently read as
// plaintext.
func TestOpen_EncryptionEnabled_KeychainUnavailableFailsClosed(t *testing.T) {
	withFakeReaderKeychain(t, &fakeReaderKeychain{err: keychain.ErrKeyUnavailable})
	dir := createEncryptedTestDB(t, testHexKey)

	db, err := Open(dir, WithEncryption(true, "omnia", false))
	if err == nil {
		db.Close()
		t.Fatal("Open must fail closed when the keychain is unavailable")
	}
}
