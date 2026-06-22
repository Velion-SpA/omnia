package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCloudRefreshWritesNewTokenToCloudJSON verifies that a successful refresh
// response causes the stored token to be overwritten with the new value.
func TestCloudRefreshWritesNewTokenToCloudJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "newtoken123"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := testConfig(t)
	// Pre-populate cloud.json with a fake stored token for alias "mycloud".
	if err := saveCloudConfigV2Entry(cfg, "mycloud", srv.URL, "old-token-xyz", "alice"); err != nil {
		t.Fatalf("saveCloudConfigV2Entry: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "refresh", "--cloud", "mycloud")

	_, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudRefresh(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("cmdCloudRefresh panicked with exitCode on a successful refresh")
	}

	// The stored token must now be "newtoken123".
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	entry, ok := v2.getCloud("mycloud")
	if !ok {
		t.Fatal("mycloud entry not found after refresh")
	}
	if entry.Token != "newtoken123" {
		t.Errorf("expected stored token %q, got %q", "newtoken123", entry.Token)
	}
}

// TestCloudRefreshUnauthorizedExitsNonZero verifies that a 401 from the server
// causes a non-zero exit.
func TestCloudRefreshUnauthorizedExitsNonZero(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := testConfig(t)
	if err := saveCloudConfigV2Entry(cfg, "", srv.URL, "stale-token", ""); err != nil {
		t.Fatalf("saveCloudConfigV2Entry: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "refresh")

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudRefresh(cfg)
	})
	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic on 401, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit code on unauthorized refresh")
	}
	if !strings.Contains(stderr, "refresh failed") {
		t.Errorf("expected 'refresh failed' in stderr, got: %q", stderr)
	}
}

// TestCloudRefreshNoStoredTokenExitsNonZero verifies that when no token is
// stored for the alias, the command exits non-zero before hitting the network.
func TestCloudRefreshNoStoredTokenExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	// Store an entry with a server URL but no token.
	if err := saveCloudConfigV2Entry(cfg, "", "http://localhost:9999", "", ""); err != nil {
		t.Fatalf("saveCloudConfigV2Entry: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "refresh")

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudRefresh(cfg)
	})
	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit when no token is stored")
	}
	if !strings.Contains(stderr, "no token stored") {
		t.Errorf("expected 'no token stored' in stderr, got: %q", stderr)
	}
}
