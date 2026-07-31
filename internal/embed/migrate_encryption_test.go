package embed

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testMigrateHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// ─── 5.1/5.2 [RED→GREEN]: migrate-on-first-enable (embeddings.db) ───

func TestMigrateToEncrypted_SeededPlaintextEmbeddingsStore_EncryptsInPlace(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		row := unitRow(string(rune('a'+i)), i, []float32{1, 0, 0})
		if err := s.Upsert(ctx, row); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	s.Close()

	result, err := MigrateToEncrypted(ctx, path, testMigrateHexKey, false)
	if err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}
	if result.RowsBefore != 10 || result.RowsAfter != 10 {
		t.Fatalf("row count mismatch: %+v", result)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a non-empty BackupPath")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("migrated embeddings.db must not start with the plain SQLite header")
	}

	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: testMigrateHexKey})
	reopened, err := OpenStore(path, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("reopen encrypted store failed: %v", err)
	}
	defer reopened.Close()
	n, err := reopened.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 10 {
		t.Fatalf("Count = %d, want 10", n)
	}
}

func TestMigrateToEncrypted_AlreadyEncryptedEmbeddingsStore_IsNoop(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Close()

	ctx := context.Background()
	if _, err := MigrateToEncrypted(ctx, path, testMigrateHexKey, false); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	result, err := MigrateToEncrypted(ctx, path, testMigrateHexKey, false)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if !result.AlreadyEncrypted {
		t.Error("expected AlreadyEncrypted=true")
	}
}

// ─── 5.3/5.4 [RED→GREEN]: reversible decrypt (embeddings.db) ───

func TestMigrateToPlaintext_EncryptedEmbeddingsStore_DecryptsRestoringRowCount(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Upsert(ctx, unitRow("a", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s.Close()

	if _, err := MigrateToEncrypted(ctx, path, testMigrateHexKey, false); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}
	result, err := MigrateToPlaintext(ctx, path, testMigrateHexKey, false)
	if err != nil {
		t.Fatalf("MigrateToPlaintext: %v", err)
	}
	if result.RowsBefore != 1 || result.RowsAfter != 1 {
		t.Fatalf("row count mismatch: %+v", result)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("decrypted embeddings.db must start with the plain SQLite header")
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen via default path: %v", err)
	}
	defer reopened.Close()
	n, err := reopened.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
}

func TestMigrateToPlaintext_AlreadyPlaintextEmbeddingsStore_IsNoop(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Close()

	result, err := MigrateToPlaintext(context.Background(), path, testMigrateHexKey, false)
	if err != nil {
		t.Fatalf("MigrateToPlaintext: %v", err)
	}
	if !result.AlreadyPlaintext {
		t.Error("expected AlreadyPlaintext=true")
	}
}

// ─── rotate-key: direct old-key-source -> new-key-target re-encryption ───

func TestRotateKey_EncryptedEmbeddingsStore_ReencryptsWithNewKey(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Upsert(ctx, unitRow("a", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s.Close()

	oldKey := testMigrateHexKey
	newKey := "f0e0d0c0b0a090807060504030201000f0e0d0c0b0a090807060504030201000"[:64]
	if _, err := MigrateToEncrypted(ctx, path, oldKey, false); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}

	result, err := RotateKey(ctx, path, oldKey, newKey, false)
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if result.RowsBefore != 1 || result.RowsAfter != 1 {
		t.Fatalf("row count mismatch: %+v", result)
	}

	if _, err := countRowsWith(ctx, "file:"+path+"?vfs=adiantum", migrateInit(oldKey, false), embeddingsRowCountQuery); err == nil {
		t.Fatal("expected the OLD key to fail against the rotated file")
	}
	n, err := countRowsWith(ctx, "file:"+path+"?vfs=adiantum", migrateInit(newKey, false), embeddingsRowCountQuery)
	if err != nil {
		t.Fatalf("countRowsWith(newKey): %v", err)
	}
	if n != 1 {
		t.Fatalf("rotated row count = %d, want 1", n)
	}
}

func TestRotateKey_AlreadyPlaintextEmbeddingsStore_ReturnsError(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Close()

	if _, err := RotateKey(context.Background(), path, "irrelevant", "irrelevant2", false); err == nil {
		t.Fatal("expected RotateKey to error on a plaintext (not-yet-encrypted) file")
	}
}

// ─── security regression: never embed the key in VACUUM INTO URI text ───

// TestMigrateEncryption_SourceNeverEmbedsHexKeyInVacuumIntoURI mirrors
// internal/store's own regression guard of the same name — see that file
// for the full rationale (adiantum's own docs warn a URI-embedded key is
// visible via vfs.Filename.URIParameters; a PRAGMA on a directly-controlled
// connection never appears there).
func TestMigrateEncryption_SourceNeverEmbedsHexKeyInVacuumIntoURI(t *testing.T) {
	src, err := os.ReadFile("migrate_encryption.go")
	if err != nil {
		t.Fatalf("read migrate_encryption.go: %v", err)
	}
	if strings.Contains(string(src), "vfs=adiantum&hexkey=") {
		t.Fatal("migrate_encryption.go must never embed the encryption key in a VACUUM INTO URI's hexkey= parameter — use PRAGMA hexkey on a directly-controlled connection instead")
	}
}

func TestRotateKey_MissingEmbeddingsFile_IsNoop(t *testing.T) {
	dir := t.TempDir()
	result, err := RotateKey(context.Background(), dir+"/does-not-exist.db", "irrelevant", "irrelevant2", false)
	if err != nil {
		t.Fatalf("RotateKey on a missing file must be a no-op, got: %v", err)
	}
	if result.RowsBefore != 0 || result.RowsAfter != 0 {
		t.Errorf("expected zero-value result for a missing file, got %+v", result)
	}
}

// ─── WAL-sidecar regression: stale -wal/-shm after a hard-stopped writer ───
//
// Mirrors internal/store's own WAL-sidecar regression test (see that
// file's doc comment for the full real-incident rationale): MigrateToEncrypted
// et al. must checkpoint+truncate the SOURCE's WAL before reading its row
// count, and defensively remove any stale dbPath-wal/dbPath-shm immediately
// before the final rename-into-place, so a source left with a live,
// un-checkpointed WAL (e.g. a daemon stopped via SIGKILL/`launchctl
// bootout` without a final checkpoint) never leaves stale sidecars sitting
// next to the NEW file that takes over dbPath's name.

// TestHelperEmbedProcess is not a real test: it is the subprocess body
// invoked by spawnEmbedWALWriterAndHardKill below via the standard Go
// "re-exec the test binary as a helper process" idiom. It no-ops unless
// GO_WANT_HELPER_PROCESS=1 is set.
//
// It reuses the package's own OpenStore/Upsert path (not raw SQL) so the
// resulting WAL matches a real embed run's actual write shape, commits a
// batch of writes WITHOUT ever checkpointing, then signals readiness and
// blocks forever so the parent test can SIGKILL it — a real hard stop,
// never a graceful Close() (which risks SQLite's own checkpoint-on-close
// cleaning up the WAL for us and masking the bug).
func TestHelperEmbedProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	path := os.Getenv("HELPER_EMB_PATH")
	s, err := OpenStore(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: OpenStore:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		syncID := fmt.Sprintf("wal-probe-%d", i)
		if err := s.Upsert(ctx, unitRow(syncID, i, []float32{1, 0, 0})); err != nil {
			fmt.Fprintln(os.Stderr, "helper: Upsert:", i, err)
			os.Exit(1)
		}
	}
	fmt.Println("ready")
	select {} // block until SIGKILLed by the parent test — never a graceful close
}

// spawnEmbedWALWriterAndHardKill starts the real OS subprocess described
// above against path, waits for it to signal readiness, then SIGKILLs it —
// leaving a genuine, live, non-empty `path-wal` sidecar on disk with no
// process holding it open anymore.
func spawnEmbedWALWriterAndHardKill(t *testing.T, path string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperEmbedProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_EMB_PATH="+path)
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

	walPath := path + "-wal"
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("expected a live WAL sidecar after hard-killing the writer, got: %v\nstderr: %s", err, stderr.String())
	}
	if info.Size() == 0 {
		t.Fatalf("expected a non-empty WAL sidecar after hard-killing the writer, got zero bytes\nstderr: %s", stderr.String())
	}
}

// assertNoLiveWALSidecar fails the test if path+"-wal" exists with non-zero
// size — a stale, non-empty WAL sidecar sitting next to a fixed-name
// temp/intermediate migration path that a subsequent call will reuse
// verbatim (RotateKey's tmpPath/tmpPlainPath never have randomized names).
func assertNoLiveWALSidecar(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if err != nil {
		return // no sidecar at all — the good case
	}
	if info.Size() > 0 {
		t.Errorf("stale non-empty WAL sidecar left at %s-wal (size=%d)", path, info.Size())
	}
}

// TestRotateKey_CalledTwiceInARow_SecondCallSucceedsCleanly mirrors
// internal/store's own regression test of the same name — see that file
// for the full BLOCKER rationale: RotateKey's two fixed-name intermediate
// temp paths are reused verbatim across separate invocations, and rotating
// the key twice in a row is a normal, supported real-world sequence.
func TestRotateKey_CalledTwiceInARow_SecondCallSucceedsCleanly(t *testing.T) {
	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		syncID := fmt.Sprintf("double-rotate-%d", i)
		if err := s.Upsert(ctx, unitRow(syncID, i, []float32{1, 0, 0})); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	s.Close()

	key1 := testMigrateHexKey
	key2 := "f0e0d0c0b0a090807060504030201000f0e0d0c0b0a090807060504030201000"[:64]
	key3 := "1111111111111111111111111111111111111111111111111111111111111111"[:64]

	if _, err := MigrateToEncrypted(ctx, path, key1, false); err != nil {
		t.Fatalf("MigrateToEncrypted: %v", err)
	}

	if _, err := RotateKey(ctx, path, key1, key2, false); err != nil {
		t.Fatalf("first RotateKey: %v", err)
	}

	tmpPath := path + ".rotating.tmp"
	tmpPlainPath := path + ".rotating.plain.tmp"
	assertNoLiveWALSidecar(t, tmpPath)
	assertNoLiveWALSidecar(t, tmpPlainPath)

	result, err := RotateKey(ctx, path, key2, key3, false)
	if err != nil {
		t.Fatalf("second RotateKey (immediately after the first, same path): %v", err)
	}
	if result.RowsBefore != 6 || result.RowsAfter != 6 {
		t.Fatalf("row count mismatch on second rotation: %+v", result)
	}

	assertNoLiveWALSidecar(t, tmpPath)
	assertNoLiveWALSidecar(t, tmpPlainPath)

	// Behavioral assertion: a completely fresh open with the FINAL key must
	// succeed and see all rows.
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: key3})
	reopened, err := OpenStore(path, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("fresh reopen after double rotation: %v", err)
	}
	defer reopened.Close()
	n, err := reopened.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 6 {
		t.Fatalf("Count after double rotation = %d, want 6", n)
	}
}

// TestMigrateToEncrypted_EmbedSourceWithUncheckpointedWAL_FreshReopenSucceeds
// is the deterministic reproduction of the real-incident bug pattern for
// embeddings.db: it must fail against the unfixed migration (stale
// dbPath-wal/dbPath-shm survive the migration) and pass once the source
// WAL is checkpointed/truncated before the row count is read, and any
// stale sidecar is removed before the final rename.
func TestMigrateToEncrypted_EmbedSourceWithUncheckpointedWAL_FreshReopenSucceeds(t *testing.T) {
	path := t.TempDir() + "/emb.db"

	spawnEmbedWALWriterAndHardKill(t, path)

	ctx := context.Background()
	result, err := MigrateToEncrypted(ctx, path, testMigrateHexKey, false)
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
	// open), the migrated path must not have the hard-killed source's
	// stale WAL sidecars sitting next to it.
	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Error("a stale -wal sidecar from the un-checkpointed source must not remain after migration")
	}
	if _, err := os.Stat(path + "-shm"); err == nil {
		t.Error("a stale -shm sidecar from the un-checkpointed source must not remain after migration")
	}

	// The real-world failure: a completely FRESH OpenStore with the
	// correct key must succeed — not fail with "file is not a database"
	// because of a stale WAL sidecar left over from the hard-killed source.
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: testMigrateHexKey})
	reopened, err := OpenStore(path, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("fresh reopen of the migrated embeddings store must succeed, got: %v", err)
	}
	defer reopened.Close()

	n, err := reopened.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 500 {
		t.Fatalf("Count = %d, want 500", n)
	}
}
