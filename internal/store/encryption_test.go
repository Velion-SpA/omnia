package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/keychain"
)

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func containsString(data []byte, s string) bool {
	return strings.Contains(string(data), s)
}

func hasSQLiteHeader(data []byte) bool {
	return len(data) >= 16 && string(data[:15]) == "SQLite format 3"
}

// fakeKeychain lets tests script GetOrCreateHexKey responses without
// shelling out to a real keychain — mirrors internal/embed's fake-runner
// test doubles for injected dependencies.
type fakeKeychain struct {
	hexKey  string
	created bool
	err     error
}

func (f *fakeKeychain) GetOrCreateHexKey(ctx context.Context, service, account string) (string, bool, error) {
	return f.hexKey, f.created, f.err
}

func withFakeKeychain(t *testing.T, f *fakeKeychain) {
	t.Helper()
	orig := newKeychainClient
	newKeychainClient = func() keychain.Resolver { return f }
	t.Cleanup(func() { newKeychainClient = orig })
}

func captureAuditEntries(t *testing.T) *[]audit.Entry {
	t.Helper()
	var entries []audit.Entry
	orig := auditAppend
	auditAppend = func(e audit.Entry) { entries = append(entries, e) }
	t.Cleanup(func() { auditAppend = orig })
	return &entries
}

// ─── 4.3/4.4 [RED→GREEN]: encryption.enabled=true opens via ncruces+adiantum;
// a written observation reads back identical to the plaintext path (REQ-432) ───

func TestNew_EncryptionEnabled_OpensViaAdiantumAndRoundTripsData(t *testing.T) {
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
	defer s.Close()

	if err := s.CreateSession("enc-sess", "engram", "/tmp/enc"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "enc-sess", Type: "manual", Title: "encrypted round trip",
		Content: "content that must round-trip", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Content != "content that must round-trip" {
		t.Errorf("Content = %q, want round-tripped content", obs.Content)
	}
}

// TestNew_EncryptionEnabled_FileBytesAreNotPlaintext confirms the on-disk
// file itself is not readable SQLite plaintext (spec REQ-432).
func TestNew_EncryptionEnabled_FileBytesAreNotPlaintext(t *testing.T) {
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	withFakeKeychain(t, &fakeKeychain{hexKey: hexKey})

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.EncryptionEnabled = true

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New (encrypted): %v", err)
	}
	if err := s.CreateSession("enc-sess", "engram", "/tmp/enc"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "enc-sess", Type: "manual", Title: "secret-marker-title",
		Content: "super-secret-plaintext-marker", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	s.Close()

	raw := mustReadFile(t, filepath.Join(cfg.DataDir, "omnia.db"))
	if containsString(raw, "super-secret-plaintext-marker") {
		t.Fatal("encrypted omnia.db contains the plaintext marker — encryption did not take effect")
	}
}

// ─── 4.5/4.6 [RED→GREEN]: keychain-unavailable degradation ───

func TestNew_EncryptionEnabled_KeychainUnavailable_DefaultRefusesToOpen(t *testing.T) {
	withFakeKeychain(t, &fakeKeychain{err: errors.New("keychain: no supported CLI found on PATH: exec: \"security\": executable file not found in $PATH")})

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.EncryptionEnabled = true
	// AllowPlaintextFallback intentionally left at its zero value (false).

	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected New to refuse opening when the keychain is unavailable and allow_plaintext_fallback=false")
	}
	if !errors.Is(err, ErrEncryptionKeyUnavailable) {
		t.Errorf("error = %v, want wrapping ErrEncryptionKeyUnavailable", err)
	}
}

func TestNew_EncryptionEnabled_KeychainUnavailable_FallbackOpensUnencryptedWithAudit(t *testing.T) {
	withFakeKeychain(t, &fakeKeychain{err: errors.New("keychain: no supported CLI found on PATH")})
	entries := captureAuditEntries(t)

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.EncryptionEnabled = true
	cfg.EncryptionAllowPlaintextFallback = true

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New (fallback): %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("fallback-sess", "engram", "/tmp/fb"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "fallback-sess", Type: "manual", Title: "fallback write",
		Content: "content", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation must still succeed on fallback: %v", err)
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry for the degradation, got %d: %+v", len(*entries), *entries)
	}
	if (*entries)[0].Action != audit.ActionEncryptionFallback {
		t.Errorf("Action = %q, want %q", (*entries)[0].Action, audit.ActionEncryptionFallback)
	}
}

// ─── 4.8 Verification: disabled path is byte-for-byte (REQ-430) ───

func TestNew_EncryptionDisabled_IsByteForByteUnaffected(t *testing.T) {
	// A bare Config{} (EncryptionEnabled zero value = false) must never
	// consult the keychain seam at all.
	withFakeKeychain(t, &fakeKeychain{err: errors.New("must never be called")})

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New (disabled): %v", err)
	}
	defer s.Close()

	raw := mustReadFile(t, filepath.Join(cfg.DataDir, "omnia.db"))
	if !hasSQLiteHeader(raw) {
		t.Fatal("disabled-path omnia.db does not start with the plain SQLite header — disabled path must be byte-for-byte the pre-v0.4 modernc format")
	}
}
