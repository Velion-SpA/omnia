package store

import (
	"context"
	"fmt"
	"os"
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
