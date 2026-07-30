package embed

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/keychain"
)

// fakeEmbedKeychain lets tests script GetOrCreateHexKey responses without
// shelling out to a real keychain — mirrors internal/store's own
// fakeKeychain test double (both share the keychain.Resolver interface).
type fakeEmbedKeychain struct {
	hexKey string
	err    error
}

func (f *fakeEmbedKeychain) GetOrCreateHexKey(ctx context.Context, service, account string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.hexKey, false, nil
}

func withFakeEmbedKeychain(t *testing.T, f *fakeEmbedKeychain) {
	t.Helper()
	orig := newKeychainClient
	newKeychainClient = func() keychain.Resolver { return f }
	t.Cleanup(func() { newKeychainClient = orig })
}

func captureEmbedAuditEntries(t *testing.T) *[]audit.Entry {
	t.Helper()
	var entries []audit.Entry
	orig := auditAppend
	auditAppend = func(e audit.Entry) { entries = append(entries, e) }
	t.Cleanup(func() { auditAppend = orig })
	return &entries
}

const testHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// ─── 4.3/4.4 [RED→GREEN]: encryption-enabled OpenStore opens via ncruces+
// adiantum; a written row reads back identical to the plaintext path (REQ-432) ───

func TestOpenStore_EncryptionEnabled_RoundTripsData(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: testHexKey})

	s, err := OpenStore(t.TempDir()+"/emb.db", WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("OpenStore (encrypted): %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.Upsert(ctx, unitRow("a", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := s.Search(ctx, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "a" {
		t.Fatalf("encrypted-path Search must round-trip like brute force: got %+v", hits)
	}
}

func TestOpenStore_EncryptionEnabled_FileBytesAreNotPlaintext(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: testHexKey})

	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path, WithEncryption(true, "omnia", false))
	if err != nil {
		t.Fatalf("OpenStore (encrypted): %v", err)
	}
	if err := s.Upsert(context.Background(), unitRow("marker-sync-id-xyz", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "marker-sync-id-xyz") {
		t.Fatal("encrypted embeddings.db contains the plaintext sync_id marker — encryption did not take effect")
	}
}

// TestOpenStore_EncryptionAndVecIndex_BothEnabled_ComposeInOneConnector
// (design: "encryption may compose its VFS initialization and vec1.Register
// in the same internal/embed connection initializer") — proves BOTH
// capabilities work simultaneously through the ncruces connector.
func TestOpenStore_EncryptionAndVecIndex_BothEnabled_ComposeInOneConnector(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{hexKey: testHexKey})

	s, err := OpenStore(t.TempDir()+"/emb.db", WithEncryption(true, "omnia", false), WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore (encrypted+vec): %v", err)
	}
	defer s.Close()
	if s.vec == nil {
		t.Fatal("expected Vec1 connector to be set up alongside encryption")
	}

	ctx := context.Background()
	if err := s.Upsert(ctx, unitRow("a", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := s.VecBackfill(ctx); err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}
	if !s.vec.usable() {
		t.Fatal("Vec1 must be usable when both encryption and vec index are enabled")
	}
	hits, err := s.Search(ctx, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "a" {
		t.Fatalf("encrypted+vec Search must still return correct hits: got %+v", hits)
	}
}

// ─── 4.5/4.6 [RED→GREEN]: keychain-unavailable degradation ───

func TestOpenStore_EncryptionEnabled_KeychainUnavailable_DefaultRefusesToOpen(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{err: errors.New("keychain: no supported CLI found on PATH")})

	_, err := OpenStore(t.TempDir()+"/emb.db", WithEncryption(true, "omnia", false))
	if err == nil {
		t.Fatal("expected OpenStore to refuse opening when the keychain is unavailable and allow_plaintext_fallback=false")
	}
	if !errors.Is(err, keychain.ErrKeyUnavailable) {
		t.Errorf("error = %v, want wrapping keychain.ErrKeyUnavailable", err)
	}
}

func TestOpenStore_EncryptionEnabled_KeychainUnavailable_FallbackOpensUnencryptedWithAudit(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{err: errors.New("keychain: no supported CLI found on PATH")})
	entries := captureEmbedAuditEntries(t)

	s, err := OpenStore(t.TempDir()+"/emb.db", WithEncryption(true, "omnia", true))
	if err != nil {
		t.Fatalf("OpenStore (fallback): %v", err)
	}
	defer s.Close()

	if err := s.Upsert(context.Background(), unitRow("a", 1, []float32{1, 0, 0})); err != nil {
		t.Fatalf("Upsert must still succeed on fallback: %v", err)
	}
	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry for the degradation, got %d: %+v", len(*entries), *entries)
	}
	if (*entries)[0].Action != audit.ActionEncryptionFallback {
		t.Errorf("Action = %q, want %q", (*entries)[0].Action, audit.ActionEncryptionFallback)
	}
}

// ─── 4.8 Verification: disabled path is byte-for-byte (REQ-430) ───

func TestOpenStore_EncryptionDisabled_NeverConsultsKeychain(t *testing.T) {
	withFakeEmbedKeychain(t, &fakeEmbedKeychain{err: errors.New("must never be called")})

	path := t.TempDir() + "/emb.db"
	s, err := OpenStore(path) // no options at all
	if err != nil {
		t.Fatalf("OpenStore (disabled): %v", err)
	}
	defer s.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("disabled-path embeddings.db must start with the plain SQLite header — byte-for-byte pre-v0.4 modernc format")
	}
}
