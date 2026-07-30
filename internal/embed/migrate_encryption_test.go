package embed

import (
	"context"
	"os"
	"strings"
	"testing"
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
