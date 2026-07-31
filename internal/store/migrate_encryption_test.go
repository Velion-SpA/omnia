package store

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── 5.1/5.2 [RED→GREEN]: migrate-on-first-enable ───

// seedPlaintextObservations creates a plaintext omnia.db at dbPath (via a
// normal disabled-encryption New) and writes n observations, returning the
// Store closed and ready for migration.
func seedPlaintextObservations(t *testing.T, dbPath string, n int) {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = filepath.Dir(dbPath)
	cfg.DedupeWindow = time.Hour
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("seed New: %v", err)
	}
	if err := s.CreateSession("seed-sess", "engram", "/tmp/seed"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "seed-sess", Type: "manual", Title: fmt.Sprintf("seed %d", i),
			Content: fmt.Sprintf("seed content %d", i), Project: "engram", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation %d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMigrateToEncrypted_SeededPlaintextStore_EncryptsInPlaceKeepingBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 25)

	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	result, err := MigrateToEncrypted(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}
	if result.RowsBefore != result.RowsAfter {
		t.Fatalf("row count mismatch: before=%d after=%d", result.RowsBefore, result.RowsAfter)
	}
	if result.RowsBefore != 25 {
		t.Errorf("RowsBefore = %d, want 25", result.RowsBefore)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a non-empty BackupPath")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup file must survive: %v", err)
	}
	backupRaw, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.HasPrefix(string(backupRaw), "SQLite format 3") {
		t.Fatal("backup file must be the ORIGINAL plaintext file")
	}

	// The now-encrypted dbPath must be openable via the encrypted path and
	// must contain the SAME data.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	cfg.DedupeWindow = 0
	cfg.EncryptionEnabled = true
	withFakeKeychain(t, &fakeKeychain{hexKey: hexKey})
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen encrypted store: %v", err)
	}
	defer s.Close()
	obs, err := s.AllObservations("engram", "project", 100)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(obs) != 25 {
		t.Fatalf("len(obs) = %d, want 25", len(obs))
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile dbPath: %v", err)
	}
	if containsString(raw, "seed content 0") {
		t.Fatal("migrated dbPath still contains plaintext content — migration did not encrypt")
	}
}

func TestMigrateToEncrypted_AlreadyEncrypted_IsNoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 3)

	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	if _, err := MigrateToEncrypted(context.Background(), dbPath, hexKey); err != nil {
		t.Fatalf("first MigrateToEncrypted: %v", err)
	}

	result, err := MigrateToEncrypted(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("second MigrateToEncrypted: %v", err)
	}
	if !result.AlreadyEncrypted {
		t.Error("expected AlreadyEncrypted=true on a second migration attempt")
	}
}

func TestMigrateToEncrypted_MissingFile_IsNoop(t *testing.T) {
	dir := t.TempDir()
	result, err := MigrateToEncrypted(context.Background(), filepath.Join(dir, "does-not-exist.db"), "irrelevant")
	if err != nil {
		t.Fatalf("MigrateToEncrypted on a missing file must be a no-op, got: %v", err)
	}
	if result.RowsBefore != 0 || result.RowsAfter != 0 {
		t.Errorf("expected zero-value result for a missing file, got %+v", result)
	}
}

// ─── 5.3/5.4 [RED→GREEN]: reversible decrypt ───

func TestMigrateToPlaintext_EncryptedStore_DecryptsRestoringRowCount(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 10)

	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	if _, err := MigrateToEncrypted(context.Background(), dbPath, hexKey); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}

	result, err := MigrateToPlaintext(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("MigrateToPlaintext: %v", err)
	}
	if result.RowsBefore != 10 || result.RowsAfter != 10 {
		t.Fatalf("row count mismatch: %+v", result)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("decrypted dbPath must start with the plain SQLite header")
	}

	// Readable by the plain default (disabled-encryption) path — never
	// locks the user out (REQ-435).
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	cfg.DedupeWindow = 0
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen decrypted store via default path: %v", err)
	}
	defer s.Close()
	obs, err := s.AllObservations("engram", "project", 100)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(obs) != 10 {
		t.Fatalf("len(obs) = %d, want 10", len(obs))
	}
}

func TestMigrateToPlaintext_AlreadyPlaintext_IsNoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 2)

	result, err := MigrateToPlaintext(context.Background(), dbPath, "irrelevant")
	if err != nil {
		t.Fatalf("MigrateToPlaintext: %v", err)
	}
	if !result.AlreadyPlaintext {
		t.Error("expected AlreadyPlaintext=true when the file is already plaintext")
	}
}

func TestMigrateToPlaintext_WrongKey_FailsCleanlyWithoutDataLoss(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 5)

	rightKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	wrongKey := "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"[:64]
	if _, err := MigrateToEncrypted(context.Background(), dbPath, rightKey); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}

	if _, err := MigrateToPlaintext(context.Background(), dbPath, wrongKey); err == nil {
		t.Fatal("expected MigrateToPlaintext with the wrong key to fail")
	}

	// The encrypted file must be untouched — still openable with the RIGHT key.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("a failed decrypt attempt must never leave the file plaintext/corrupted")
	}
}

// ─── rotate-key: direct old-key-source -> new-key-target re-encryption ───

func TestRotateKey_EncryptedStore_ReencryptsWithNewKeyPreservingData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 7)

	oldKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	newKey := "f0e0d0c0b0a090807060504030201000f0e0d0c0b0a090807060504030201000"[:64]
	if _, err := MigrateToEncrypted(context.Background(), dbPath, oldKey); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}

	result, err := RotateKey(context.Background(), dbPath, oldKey, newKey)
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if result.RowsBefore != 7 || result.RowsAfter != 7 {
		t.Fatalf("row count mismatch: %+v", result)
	}

	// Old key must no longer work.
	if _, err := countRowsInEncryptedFile(context.Background(), dbPath, oldKey, observationsRowCountQuery); err == nil {
		t.Fatal("expected the OLD key to fail against the rotated file")
	}
	// New key must work and see all rows.
	n, err := countRowsInEncryptedFile(context.Background(), dbPath, newKey, observationsRowCountQuery)
	if err != nil {
		t.Fatalf("countRowsInEncryptedFile(newKey): %v", err)
	}
	if n != 7 {
		t.Fatalf("rotated row count = %d, want 7", n)
	}
}

func TestRotateKey_AlreadyPlaintext_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 1)

	if _, err := RotateKey(context.Background(), dbPath, "irrelevant", "irrelevant2"); err == nil {
		t.Fatal("expected RotateKey to error on a plaintext (not-yet-encrypted) file")
	}
}

func TestRotateKey_MissingFile_IsNoop(t *testing.T) {
	dir := t.TempDir()
	result, err := RotateKey(context.Background(), filepath.Join(dir, "does-not-exist.db"), "irrelevant", "irrelevant2")
	if err != nil {
		t.Fatalf("RotateKey on a missing file must be a no-op, got: %v", err)
	}
	if result.RowsBefore != 0 || result.RowsAfter != 0 {
		t.Errorf("expected zero-value result for a missing file, got %+v", result)
	}
}

// ─── security regression: never embed the key in VACUUM INTO URI text ───

// TestMigrateEncryption_SourceNeverEmbedsHexKeyInVacuumIntoURI is a static
// regression guard for a real security bug: `VACUUM INTO
// 'file:...?vfs=adiantum&hexkey=...'` embeds the raw encryption key
// directly in SQL/URI text. The adiantum package's own docs
// (vfs/adiantum/api.go) warn this "makes your key easily accessible to
// other parts of your application (e.g. through vfs.Filename.URIParameters)"
// and recommend invoking `PRAGMA hexkey=...` immediately after opening a
// connection instead — exactly what openAdiantumDB already does for reads.
// This scans the package's own source rather than instrumenting a runtime
// SQL spy, since the fix removes the vulnerable string construction
// entirely (via Conn.Backup/Conn.Restore) rather than gating it behind a
// flag.
func TestMigrateEncryption_SourceNeverEmbedsHexKeyInVacuumIntoURI(t *testing.T) {
	src, err := os.ReadFile("migrate_encryption.go")
	if err != nil {
		t.Fatalf("read migrate_encryption.go: %v", err)
	}
	if strings.Contains(string(src), "vfs=adiantum&hexkey=") {
		t.Fatal("migrate_encryption.go must never embed the encryption key in a VACUUM INTO URI's hexkey= parameter — use PRAGMA hexkey on a directly-controlled connection instead")
	}
}

// ─── 5.8 Verification: migration against a 10k-row fixture ───

func TestMigrateToEncrypted_TenThousandRowFixture_PreservesAllRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 10k-row migration fixture in -short mode")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")
	seedPlaintextObservations(t, dbPath, 10000)

	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	result, err := MigrateToEncrypted(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}
	if result.RowsBefore != 10000 || result.RowsAfter != 10000 {
		t.Fatalf("row count mismatch: %+v", result)
	}

	decryptResult, err := MigrateToPlaintext(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("MigrateToPlaintext: %v", err)
	}
	if decryptResult.RowsBefore != 10000 || decryptResult.RowsAfter != 10000 {
		t.Fatalf("row count mismatch after decrypt: %+v", decryptResult)
	}
}

// ─── WAL-sidecar regression: stale -wal/-shm after a hard-stopped writer ───
//
// Real production incident (2026-07): running `omnia security encrypt`
// against the user's real omnia.db reported SUCCESS (row counts matched)
// but left the resulting encrypted file permanently unreadable by any
// subsequent fresh process. Confirmed root cause: MigrateToEncrypted (and
// MigrateToPlaintext/RotateKey) never checkpointed or cleaned up the
// SOURCE database's `-wal`/`-shm` sidecar files before renaming a new file
// into place at the original path. A source with ANY live,
// un-checkpointed WAL at migration time (completely normal — e.g. a
// daemon stopped via `launchctl bootout`/SIGKILL without a final
// checkpoint, exactly what happened in the real incident) leaves stale
// `dbPath-wal`/`dbPath-shm` files sitting next to the NEW file that takes
// over dbPath's name after rename. The next fresh WAL-mode open (any
// subsequent store.New) tries to reconcile that stale, structurally
// incompatible WAL against the new file and fails with the exact
// misleading error seen in production: `sqlite3: file is not a database`.

// TestHelperProcess is not a real test: it is the subprocess body invoked
// by spawnWALWriterAndHardKill below via the standard Go "re-exec the test
// binary as a helper process" idiom (the same pattern os/exec's own tests
// use). It no-ops unless GO_WANT_HELPER_PROCESS=1 is set, so a normal
// `go test` run treats it as an ordinary (instantly-passing) test.
//
// It opens dbPath directly via the plain modernc driver (NOT the encrypted
// path — the source is still plaintext at this point), turns on WAL mode,
// commits a batch of writes WITHOUT ever checkpointing, then signals
// readiness and blocks forever so the parent test can SIGKILL it — a real
// hard stop, never a graceful db.Close() (which risks SQLite's own
// checkpoint-on-close cleaning up the WAL for us and masking the bug).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	dataDir := os.Getenv("HELPER_DATA_DIR")

	// Reuse the package's own New/AddObservation path (not raw SQL) so the
	// resulting WAL matches the real daemon's actual write shape exactly —
	// including FTS5 shadow-table writes and revision bookkeeping — rather
	// than a single plain table's pages.
	cfg, err := DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: DefaultConfig:", err)
		os.Exit(1)
	}
	cfg.DataDir = dataDir
	cfg.DedupeWindow = 0
	s, err := New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: New:", err)
		os.Exit(1)
	}
	if err := s.CreateSession("helper-sess", "engram", "/tmp/helper"); err != nil {
		fmt.Fprintln(os.Stderr, "helper: CreateSession:", err)
		os.Exit(1)
	}
	for i := 0; i < 500; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "helper-sess", Type: "manual", Title: fmt.Sprintf("wal probe %d", i),
			Content: fmt.Sprintf("wal probe content %d", i), Project: "engram", Scope: "project",
		}); err != nil {
			fmt.Fprintln(os.Stderr, "helper: AddObservation:", i, err)
			os.Exit(1)
		}
	}
	fmt.Println("ready")
	select {} // block until SIGKILLed by the parent test — never a graceful close
}

// spawnWALWriterAndHardKill starts the real OS subprocess described above
// against a store rooted at dataDir, waits for it to signal readiness (so
// we know the writes have landed), then SIGKILLs it — leaving a genuine,
// live, non-empty `dbPath-wal` sidecar on disk with no process holding it
// open anymore, exactly mirroring the real incident's trigger (`omnia
// serve` stopped via SIGKILL/`launchctl bootout` mid-session).
func spawnWALWriterAndHardKill(t *testing.T, dataDir, dbPath string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_DATA_DIR="+dataDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper writer process: %v", err)
	}

	ready := make(chan error, 1)
	go func() {
		line, rerr := bufio.NewReader(stdout).ReadString('\n')
		if rerr != nil {
			ready <- rerr
			return
		}
		if strings.TrimSpace(line) != "ready" {
			ready <- fmt.Errorf("unexpected helper output: %q", line)
			return
		}
		ready <- nil
	}()

	select {
	case rerr := <-ready:
		if rerr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper writer process did not become ready: %v\nstderr: %s", rerr, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("timed out waiting for helper writer process to become ready\nstderr: %s", stderr.String())
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL helper writer process: %v", err)
	}
	_ = cmd.Wait() // expected to report a kill signal — not a real failure

	walPath := dbPath + "-wal"
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("expected a live WAL sidecar after hard-killing the writer, got: %v\nstderr: %s", err, stderr.String())
	}
	if info.Size() == 0 {
		t.Fatalf("expected a non-empty WAL sidecar after hard-killing the writer, got zero bytes\nstderr: %s", stderr.String())
	}
}

// TestMigrateToEncrypted_SourceWithUncheckpointedWAL_FreshReopenSucceeds is
// the deterministic reproduction of the real incident described above: it
// must fail against the unfixed migration (a fresh reopen errors with a
// "file is not a database"-style message) and pass once the source WAL is
// checkpointed/truncated before the row count is read, and any stale
// dbPath-wal/dbPath-shm is removed before the final rename.
func TestMigrateToEncrypted_SourceWithUncheckpointedWAL_FreshReopenSucceeds(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnia.db")

	spawnWALWriterAndHardKill(t, dir, dbPath)

	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	result, err := MigrateToEncrypted(context.Background(), dbPath, hexKey)
	if err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}
	if result.RowsBefore != result.RowsAfter {
		t.Fatalf("row count mismatch: before=%d after=%d", result.RowsBefore, result.RowsAfter)
	}
	if result.RowsBefore != 500 {
		t.Errorf("RowsBefore = %d, want 500", result.RowsBefore)
	}

	// Defense-in-depth: immediately after migration (before ANY fresh
	// open), the migrated dbPath must not have the hard-killed source's
	// stale WAL sidecars sitting next to it.
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		t.Error("a stale -wal sidecar from the un-checkpointed source must not remain after migration")
	}
	if _, err := os.Stat(dbPath + "-shm"); err == nil {
		t.Error("a stale -shm sidecar from the un-checkpointed source must not remain after migration")
	}

	// The real-world failure: a completely FRESH store.New with the
	// correct key must succeed — not fail with "file is not a database"
	// because of a stale WAL sidecar left over from the hard-killed source.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	cfg.DedupeWindow = 0
	cfg.EncryptionEnabled = true
	withFakeKeychain(t, &fakeKeychain{hexKey: hexKey})
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("fresh reopen of the migrated store must succeed, got: %v", err)
	}
	defer s.Close()

	obs, err := s.AllObservations("engram", "project", 1000)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(obs) != 500 {
		t.Fatalf("len(obs) = %d, want 500", len(obs))
	}
}
