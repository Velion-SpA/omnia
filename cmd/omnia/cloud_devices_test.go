package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type devicesCapture struct {
	authHeader     string
	scopedID       string
	scopedProjects []string
	deletedID      string
}

// newStubDevicesServer spins an httptest.Server exposing the device-management
// endpoints the CLI talks to.
func newStubDevicesServer(t *testing.T, devices []cloudDeviceInfo) (*httptest.Server, *devicesCapture) {
	t.Helper()
	cap := &devicesCapture{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, r *http.Request) {
		cap.authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(devices)
	})
	mux.HandleFunc("POST /devices/{id}/scope", func(w http.ResponseWriter, r *http.Request) {
		cap.scopedID = r.PathValue("id")
		var body struct {
			Projects []string `json:"projects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.scopedProjects = body.Projects
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("DELETE /devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		cap.deletedID = r.PathValue("id")
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestCloudDevicesListShowsScopeAndLastSeen(t *testing.T) {
	devices := []cloudDeviceInfo{
		{ID: "10", Name: "notebook-a", ScopeProjects: []string{"proj-a"}, LastSeenAt: "2026-07-06T10:00:00Z"},
		{ID: "11", Name: "notebook-b", ScopeProjects: nil, LastSeenAt: ""},
	}
	srv, cap := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "list")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if cap.authHeader != "Bearer acct-token-abc" {
		t.Fatalf("expected bearer token header, got %q", cap.authHeader)
	}
	for _, want := range []string{"notebook-a", "proj-a", "notebook-b", "(unrestricted)", "2026-07-06T10:00:00Z", "never"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("list output missing %q; got:\n%s", want, stdout)
		}
	}
}

func TestCloudDevicesScopeMapsNameToID(t *testing.T) {
	devices := []cloudDeviceInfo{{ID: "10", Name: "notebook-a"}}
	srv, cap := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "scope", "notebook-a", "--projects", "proj-a,proj-b")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if cap.scopedID != "10" {
		t.Fatalf("expected scope on device id 10, got %q", cap.scopedID)
	}
	if strings.Join(cap.scopedProjects, ",") != "proj-a,proj-b" {
		t.Fatalf("expected projects proj-a,proj-b, got %v", cap.scopedProjects)
	}
}

func TestCloudDevicesScopeEmptyMakesUnrestricted(t *testing.T) {
	devices := []cloudDeviceInfo{{ID: "10", Name: "notebook-a", ScopeProjects: []string{"proj-a"}}}
	srv, cap := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "scope", "notebook-a", "--projects", "")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if len(cap.scopedProjects) != 0 {
		t.Fatalf("expected empty scope (unrestricted), got %v", cap.scopedProjects)
	}
	if !strings.Contains(stdout, "unrestricted") {
		t.Fatalf("expected 'unrestricted' in output, got: %q", stdout)
	}
}

func TestCloudDevicesScopeRequiresProjectsFlag(t *testing.T) {
	devices := []cloudDeviceInfo{{ID: "10", Name: "notebook-a"}}
	srv, _ := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "scope", "notebook-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "--projects") {
		t.Fatalf("expected '--projects' hint in stderr, got: %q", stderr)
	}
}

func TestCloudDevicesRevokeMapsNameToID(t *testing.T) {
	devices := []cloudDeviceInfo{{ID: "11", Name: "notebook-b"}}
	srv, cap := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "revoke", "notebook-b")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if cap.deletedID != "11" {
		t.Fatalf("expected delete of device id 11, got %q", cap.deletedID)
	}
	if !strings.Contains(stdout, "revoked") {
		t.Fatalf("expected 'revoked' in output, got: %q", stdout)
	}
}

func TestCloudDevicesUnknownNameExitsNonZero(t *testing.T) {
	devices := []cloudDeviceInfo{{ID: "10", Name: "notebook-a"}}
	srv, _ := newStubDevicesServer(t, devices)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL, Token: "acct-token-abc"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "revoke", "ghost-device")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "ghost-device") || !strings.Contains(stderr, "known devices") {
		t.Fatalf("expected unknown-device error listing known devices, got: %q", stderr)
	}
}

func TestCloudDevicesNotLoggedInExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	// Store a server URL but NO token → device management must refuse.
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "http://127.0.0.1:9"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "devices", "list")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudDevices(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "not logged in") {
		t.Fatalf("expected 'not logged in' in stderr, got: %q", stderr)
	}
}
