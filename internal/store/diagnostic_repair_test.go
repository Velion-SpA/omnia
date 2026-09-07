package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEstimateSessionProjectReclassificationDoesNotMutate(t *testing.T) {
	s := newTestStore(t)
	seedRepairRows(t, s, "repair-s1", "sias-app")

	counts, err := s.EstimateSessionProjectReclassification([]SessionProjectReclassification{{SessionID: "repair-s1", FromProject: "sias-app", ToProject: "engram"}})
	if err != nil {
		t.Fatalf("EstimateSessionProjectReclassification: %v", err)
	}
	if counts.Sessions != 1 || counts.Observations != 1 || counts.Prompts != 1 {
		t.Fatalf("counts=%+v", counts)
	}
	assertRepairProjects(t, s, "repair-s1", "sias-app", "sias-app", "sias-app")
}

func TestApplySessionProjectReclassificationBacksUpAndUpdatesAllowedTables(t *testing.T) {
	s := newTestStore(t)
	seedRepairRows(t, s, "repair-s1", "sias-app")
	beforeSyncState := scalarString(t, s, `SELECT COALESCE(group_concat(target_key || ':' || last_acked_seq || ':' || last_pulled_seq, ','), '') FROM sync_state`)
	beforeMutations := scalarString(t, s, `SELECT COALESCE(group_concat(seq || ':' || entity || ':' || entity_key || ':' || project, ','), '') FROM sync_mutations`)
	beforeSessionCount := scalarInt(t, s, `SELECT count(*) FROM sessions`)
	beforeObservationCount := scalarInt(t, s, `SELECT count(*) FROM observations`)
	beforePromptCount := scalarInt(t, s, `SELECT count(*) FROM user_prompts`)

	result, err := s.ApplySessionProjectReclassification([]SessionProjectReclassification{{SessionID: "repair-s1", FromProject: "sias-app", ToProject: "engram"}})
	if err != nil {
		t.Fatalf("ApplySessionProjectReclassification: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if filepath.Dir(result.BackupPath) != filepath.Join(s.cfg.DataDir, "backups") {
		t.Fatalf("backup path outside backups dir: %s", result.BackupPath)
	}
	if result.Counts.Sessions != 1 || result.Counts.Observations != 1 || result.Counts.Prompts != 1 {
		t.Fatalf("counts=%+v", result.Counts)
	}
	assertRepairProjects(t, s, "repair-s1", "engram", "engram", "engram")
	if got := scalarString(t, s, `SELECT COALESCE(group_concat(target_key || ':' || last_acked_seq || ':' || last_pulled_seq, ','), '') FROM sync_state`); got != beforeSyncState {
		t.Fatalf("sync_state changed: before=%q after=%q", beforeSyncState, got)
	}
	if got := scalarString(t, s, `SELECT COALESCE(group_concat(seq || ':' || entity || ':' || entity_key || ':' || project, ','), '') FROM sync_mutations`); got != beforeMutations {
		t.Fatalf("sync_mutations changed: before=%q after=%q", beforeMutations, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sessions`); got != beforeSessionCount {
		t.Fatalf("session count changed: before=%d after=%d", beforeSessionCount, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM observations`); got != beforeObservationCount {
		t.Fatalf("observation count changed: before=%d after=%d", beforeObservationCount, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM user_prompts`); got != beforePromptCount {
		t.Fatalf("prompt count changed: before=%d after=%d", beforePromptCount, got)
	}
}

// TestBackupSQLiteLocksDownPermissions (operational item #3,
// docs/conversational-retrieval-plan.md): a VACUUM INTO snapshot is a full
// plaintext copy of the store, so the backups directory and the backup file
// itself must be owner-only — the same posture New() already applies to the
// primary data dir (0700) and database file (0600). Previously the directory
// was created at 0755 and the file inherited whatever the process umask gave
// it (typically 0644 on a default 022 umask), which internal/diagnostic's
// StoreExposureCheck never inspected.
func TestBackupSQLiteLocksDownPermissions(t *testing.T) {
	s := newTestStore(t)

	backupPath, err := s.BackupSQLite()
	if err != nil {
		t.Fatalf("BackupSQLite: %v", err)
	}

	dirInfo, err := os.Stat(s.BackupDir())
	if err != nil {
		t.Fatalf("stat backup dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode = %s, want 0700", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode = %s, want 0600", fileInfo.Mode().Perm())
	}
}

// newEncryptedTestStore opens a *Store with encryption.enabled=true against
// a fake keychain (never a real OS keychain — same isolation reasoning as
// every other encrypted-store test in this package, see encryption_test.go's
// fakeKeychain). Returns the store and the hex key so a test can also open
// the resulting backup directly to verify its content.
func newEncryptedTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	withFakeKeychain(t, &fakeKeychain{hexKey: hexKey})

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.EncryptionEnabled = true
	cfg.EncryptionKeychainService = "omnia"

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New (encrypted): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, hexKey
}

// TestBackupSQLiteEncryptedStoreProducesEncryptedBackup is the regression
// test for the live failure: `omnia doctor repair ... --apply` against a
// real encrypted store failed with `backup sqlite database: sqlite3: unable
// to open database file`, because the original BackupSQLite ran a bare
// `VACUUM INTO ?` on a connection whose current VFS is adiantum — the exact
// failure MigrateToPlaintext's own vacuumSQL comment already documents.
//
// The fix must do more than stop erroring: it must produce a backup that is
// ITSELF encrypted, matching the source. BackupSQLite's own prior doc
// comment already asserted "a VACUUM INTO snapshot is a full plaintext copy
// of the store" — an assumption this test is the first to actually exercise
// against an encrypted source, since every other BackupSQLite test in this
// file/internal/diagnostic/provenance_test.go uses a plaintext store.
func TestBackupSQLiteEncryptedStoreProducesEncryptedBackup(t *testing.T) {
	s, hexKey := newEncryptedTestStore(t)
	seedRepairRows(t, s, "repair-enc-1", "sias-app")

	backupPath, err := s.BackupSQLite()
	if err != nil {
		t.Fatalf("BackupSQLite (encrypted source): %v", err)
	}

	// The backup file itself must NOT be readable as plain SQLite — it must
	// carry the SAME encryption the source did, not the source's plaintext
	// content copied out unencrypted.
	data := mustReadFile(t, backupPath)
	if hasSQLiteHeader(data) {
		t.Fatal("backup of an encrypted store must not be plaintext SQLite — found the 'SQLite format 3' magic header")
	}

	// Permissions still hold on the encrypted path too.
	if info, err := os.Stat(backupPath); err != nil {
		t.Fatalf("stat backup file: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode = %s, want 0600", info.Mode().Perm())
	}

	// The backup must actually be a valid, restorable encrypted database
	// under the SAME key — not just "not plaintext". Open it directly via
	// the same adiantum path New() uses and confirm the seeded row is there.
	backupDB, err := openAdiantumDB(backupPath, hexKey)
	if err != nil {
		t.Fatalf("open backup with source key: %v", err)
	}
	defer backupDB.Close()
	var project string
	if err := backupDB.QueryRow(`SELECT project FROM sessions WHERE id = ?`, "repair-enc-1").Scan(&project); err != nil {
		t.Fatalf("query backup content: %v", err)
	}
	if project != "sias-app" {
		t.Fatalf("backup content mismatch: project = %q, want %q", project, "sias-app")
	}

	// No plaintext intermediate may survive this call.
	if _, err := os.Stat(backupPath + ".decrypting.tmp"); !os.IsNotExist(err) {
		t.Fatalf("plaintext intermediate must not survive a successful backup, stat err = %v", err)
	}
}

// TestBackupSQLiteEncryptedStoreSucceedsWithConcurrentConnection is the
// direct regression test for the live failure hit while re-running --apply:
// the first attempt aborted with `wal_checkpoint(TRUNCATE) reported busy=1`
// because migrate_encryption.go's decryptToPlainFile — reused verbatim in an
// earlier version of this fix — requires an EXCLUSIVE checkpoint, and an
// unrelated MCP connection (mem_search/mem_save, active in the very session
// that built this fix) held the same encrypted database open at the time.
//
// BackupSQLite must succeed even while another connection holds an open read
// transaction against the same file — this is normal, expected, effectively
// always-on usage (an `omnia serve` daemon or MCP server routinely has the
// store open), not an edge case.
func TestBackupSQLiteEncryptedStoreSucceedsWithConcurrentConnection(t *testing.T) {
	s, hexKey := newEncryptedTestStore(t)
	seedRepairRows(t, s, "repair-enc-4", "sias-app")

	concurrent, err := openAdiantumDB(s.DBPath(), hexKey)
	if err != nil {
		t.Fatalf("open concurrent connection: %v", err)
	}
	defer concurrent.Close()
	tx, err := concurrent.Begin()
	if err != nil {
		t.Fatalf("begin concurrent transaction: %v", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("hold a concurrent read snapshot open: %v", err)
	}

	// The concurrent transaction above is still open at this point — a
	// wal_checkpoint(TRUNCATE) attempted right now would report busy.
	backupPath, err := s.BackupSQLite()
	if err != nil {
		t.Fatalf("BackupSQLite must succeed with a concurrent connection open, got: %v", err)
	}
	if backupPath == "" {
		t.Fatal("expected a backup path")
	}
}

// TestBackupSQLiteEncryptedStoreFailsClosedWhenKeyUnavailable: a backup call
// must NEVER produce a result the caller cannot tell apart from a real
// encrypted snapshot. If the key cannot be resolved, BackupSQLite must fail
// loudly and leave no partial backup or plaintext intermediate behind — the
// same fail-closed contract ErrEncryptionKeyUnavailable already documents
// for opening the store itself, extended here to backing it up.
//
// allow_plaintext_fallback=true on the live store's own Config would let a
// fresh Open degrade to plaintext; BackupSQLite must NOT inherit that
// tolerance — there is no key to encrypt a backup with, so degrading here
// would mean silently writing nothing (an error) or something worse.
func TestBackupSQLiteEncryptedStoreFailsClosedWhenKeyUnavailable(t *testing.T) {
	s, _ := newEncryptedTestStore(t)
	seedRepairRows(t, s, "repair-enc-2", "sias-app")

	// Swap the keychain out from under the already-open store — simulates
	// the key becoming unavailable between Open time and this repair call.
	s.cfg.EncryptionAllowPlaintextFallback = true
	withFakeKeychain(t, &fakeKeychain{err: errors.New("keychain: locked")})

	if _, err := s.BackupSQLite(); err == nil {
		t.Fatal("expected BackupSQLite to fail when the encryption key cannot be resolved")
	} else if !errors.Is(err, ErrEncryptionKeyUnavailable) {
		t.Fatalf("expected an ErrEncryptionKeyUnavailable-wrapped error, got: %v", err)
	}

	entries, err := os.ReadDir(s.BackupDir())
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed backup must leave nothing behind (no partial backup, no plaintext intermediate), got %v", entries)
	}
}

// TestApplySessionProjectReclassificationBacksUpEncryptedStore is the
// end-to-end regression test through the actual repair caller (not just
// BackupSQLite in isolation): `omnia doctor repair ... --apply` for
// session_project_directory_mismatch — the check whose repair path was
// already supported before this fix, and was STILL completely unusable on
// an encrypted store, since ApplySessionProjectReclassification's first
// step is BackupSQLite.
func TestApplySessionProjectReclassificationBacksUpEncryptedStore(t *testing.T) {
	s, hexKey := newEncryptedTestStore(t)
	seedRepairRows(t, s, "repair-enc-3", "sias-app")

	result, err := s.ApplySessionProjectReclassification([]SessionProjectReclassification{
		{SessionID: "repair-enc-3", FromProject: "sias-app", ToProject: "engram"},
	})
	if err != nil {
		t.Fatalf("ApplySessionProjectReclassification (encrypted store): %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a backup path")
	}
	if result.Counts.Sessions != 1 || result.Counts.Observations != 1 || result.Counts.Prompts != 1 {
		t.Fatalf("counts=%+v", result.Counts)
	}

	data := mustReadFile(t, result.BackupPath)
	if hasSQLiteHeader(data) {
		t.Fatal("backup of an encrypted store must not be plaintext SQLite")
	}
	backupDB, err := openAdiantumDB(result.BackupPath, hexKey)
	if err != nil {
		t.Fatalf("open backup with source key: %v", err)
	}
	defer backupDB.Close()
	var count int
	if err := backupDB.QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, "repair-enc-3").Scan(&count); err != nil {
		t.Fatalf("query backup content: %v", err)
	}
	if count != 1 {
		t.Fatalf("backup must have captured the pre-reclassification state, got count=%d", count)
	}
}

// TestDeletePendingSyncMutationsBacksUpEncryptedStore is the direct
// regression test for the failure the coordinator hit live: `omnia doctor
// repair --check sync_mutation_required_fields --apply` against the real
// (encrypted) store failed on the backup step before ever reaching the
// delete. Mirrors the real shape — a fan-out pair, same entity_key, two
// target_keys — and confirms delete-only still holds against an encrypted
// store: the rows are gone, and the pre-delete state survives in an
// encrypted backup.
func TestDeletePendingSyncMutationsBacksUpEncryptedStore(t *testing.T) {
	s, hexKey := newEncryptedTestStore(t)

	for _, targetKey := range []string{"cloud", "work:umbral"} {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`, targetKey, SyncLifecycleIdle); err != nil {
			t.Fatalf("seed sync_state %q: %v", targetKey, err)
		}
		if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			targetKey, SyncEntityPrompt, "prompt-enc-fanout-1", SyncOpUpsert, `{"session_id":"s1"}`, SyncSourceLocal, "umbral"); err != nil {
			t.Fatalf("seed sync_mutations %q: %v", targetKey, err)
		}
	}
	mutations, err := s.ListPendingProjectMutations("umbral")
	if err != nil {
		t.Fatalf("ListPendingProjectMutations: %v", err)
	}
	if len(mutations) != 2 {
		t.Fatalf("setup: expected 2 pending mutations, got %d", len(mutations))
	}
	seqs := []int64{mutations[0].Seq, mutations[1].Seq}

	result, err := s.DeletePendingSyncMutations(seqs)
	if err != nil {
		t.Fatalf("DeletePendingSyncMutations (encrypted store): %v", err)
	}
	if result.Deleted != 2 {
		t.Fatalf("Deleted = %d, want 2", result.Deleted)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a backup path")
	}

	data := mustReadFile(t, result.BackupPath)
	if hasSQLiteHeader(data) {
		t.Fatal("backup of an encrypted store must not be plaintext SQLite")
	}
	backupDB, err := openAdiantumDB(result.BackupPath, hexKey)
	if err != nil {
		t.Fatalf("open backup with source key: %v", err)
	}
	defer backupDB.Close()
	var count int
	if err := backupDB.QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity_key = ?`, "prompt-enc-fanout-1").Scan(&count); err != nil {
		t.Fatalf("query backup content: %v", err)
	}
	if count != 2 {
		t.Fatalf("backup must have captured both pending rows BEFORE deletion, got count=%d", count)
	}

	remaining, err := s.ListPendingProjectMutations("umbral")
	if err != nil {
		t.Fatalf("ListPendingProjectMutations after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected both fan-out rows deleted from the live store, %d remain", len(remaining))
	}
}

func seedRepairRows(t *testing.T, s *Store, sessionID, project string) {
	t.Helper()
	if err := s.CreateSession(sessionID, project, "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: sessionID, Type: "bugfix", Title: "repair", Content: "content", Project: project, Scope: "project"}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: sessionID, Content: "prompt", Project: project}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
}

func assertRepairProjects(t *testing.T, s *Store, sessionID, sessionProject, observationProject, promptProject string) {
	t.Helper()
	if got := scalarString(t, s, `SELECT project FROM sessions WHERE id = ?`, sessionID); got != sessionProject {
		t.Fatalf("session project=%q want %q", got, sessionProject)
	}
	if got := scalarString(t, s, `SELECT project FROM observations WHERE session_id = ?`, sessionID); got != observationProject {
		t.Fatalf("observation project=%q want %q", got, observationProject)
	}
	if got := scalarString(t, s, `SELECT project FROM user_prompts WHERE session_id = ?`, sessionID); got != promptProject {
		t.Fatalf("prompt project=%q want %q", got, promptProject)
	}
}

func scalarString(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	var got string
	if err := s.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

func scalarInt(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var got int
	if err := s.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}
