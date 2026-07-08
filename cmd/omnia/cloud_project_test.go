package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type projectSyncCapture struct {
	authHeader string
	action     string // "pause" or "resume"
	project    string
	reason     string
	hadBody    bool
}

// newStubProjectSyncServer spins an httptest.Server exposing the OBL-04 admin
// pause/resume endpoints the CLI talks to.
func newStubProjectSyncServer(t *testing.T, adminToken string, status int) (*httptest.Server, *projectSyncCapture) {
	t.Helper()
	cap := &projectSyncCapture{}
	mux := http.NewServeMux()

	handle := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cap.authHeader = r.Header.Get("Authorization")
			cap.action = action
			cap.project = r.PathValue("project")
			if r.Body != nil {
				var body struct {
					PausedReason string `json:"paused_reason"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
					cap.hadBody = true
					cap.reason = body.PausedReason
				}
			}
			if cap.authHeader != "Bearer "+adminToken {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "operator credential required"})
				return
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"project": cap.project, "status": action})
		}
	}
	mux.HandleFunc("POST /admin/projects/{project}/pause", handle("pause"))
	mux.HandleFunc("POST /admin/projects/{project}/resume", handle("resume"))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestCloudProjectPauseUsesAdminTokenFlag(t *testing.T) {
	srv, cap := newStubProjectSyncServer(t, "super-secret-admin", http.StatusOK)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "pause", "proj-a", "--reason", "runaway agent", "--admin-token", "super-secret-admin")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if cap.action != "pause" || cap.project != "proj-a" {
		t.Fatalf("expected pause on proj-a, got action=%q project=%q", cap.action, cap.project)
	}
	if !cap.hadBody || cap.reason != "runaway agent" {
		t.Fatalf("expected paused_reason=%q in body, got hadBody=%v reason=%q", "runaway agent", cap.hadBody, cap.reason)
	}
	if !strings.Contains(stdout, "paused") || !strings.Contains(stdout, "runaway agent") {
		t.Fatalf("expected pause confirmation with reason in stdout, got: %q", stdout)
	}
}

func TestCloudProjectResumeUsesAdminTokenEnvFallback(t *testing.T) {
	srv, cap := newStubProjectSyncServer(t, "env-admin-token", http.StatusOK)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	t.Setenv("OMNIA_CLOUD_ADMIN", "env-admin-token")
	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "resume", "proj-b")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if cap.authHeader != "Bearer env-admin-token" {
		t.Fatalf("expected admin token from OMNIA_CLOUD_ADMIN env fallback, got auth=%q", cap.authHeader)
	}
	if cap.action != "resume" || cap.project != "proj-b" {
		t.Fatalf("expected resume on proj-b, got action=%q project=%q", cap.action, cap.project)
	}
	if !strings.Contains(stdout, "resumed") {
		t.Fatalf("expected resume confirmation in stdout, got: %q", stdout)
	}
}

func TestCloudProjectPauseRequiresAdminCredential(t *testing.T) {
	srv, _ := newStubProjectSyncServer(t, "whatever", http.StatusOK)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}
	// Ensure no ambient admin credential leaks in from the environment.
	t.Setenv("OMNIA_CLOUD_ADMIN", "")
	t.Setenv("ENGRAM_CLOUD_ADMIN", "")

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "pause", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "admin credential") {
		t.Fatalf("expected admin-credential error hint in stderr, got: %q", stderr)
	}
}

func TestCloudProjectPauseForbiddenWithWrongToken(t *testing.T) {
	srv, _ := newStubProjectSyncServer(t, "correct-token", http.StatusOK)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "pause", "proj-a", "--admin-token", "wrong-token")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "forbidden") {
		t.Fatalf("expected forbidden error in stderr, got: %q", stderr)
	}
}

func TestCloudProjectRequiresProjectName(t *testing.T) {
	srv, _ := newStubProjectSyncServer(t, "tok", http.StatusOK)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: srv.URL}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "pause", "--admin-token", "tok")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "project name is required") {
		t.Fatalf("expected project-required error in stderr, got: %q", stderr)
	}
}

func TestCloudProjectUnknownSubcommandExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "project", "bogus")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudProject(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "unknown cloud project command") {
		t.Fatalf("expected unknown-subcommand error in stderr, got: %q", stderr)
	}
}
