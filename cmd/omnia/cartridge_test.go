package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func writeCartridgeConfig(t *testing.T, enabled bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "cartridge:\n  enabled: " + boolYAML(enabled) + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cartridgeCLIRepo creates a throwaway git repo (mirroring blameCLIRepo's
// established fixture pattern) and returns its canonical root.
func cartridgeCLIRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	file := filepath.Join(repo, "service.go")
	if err := os.WriteFile(file, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "service.go"}, {"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestCmdCartridgeBuildDisabledIsNoop is the RED test for task 11.9:
// cartridge.enabled=false must report the capability disabled and never
// write a cartridge file (REQ-450).
func TestCmdCartridgeBuildDisabledIsNoop(t *testing.T) {
	cfg := testConfig(t)
	path := writeCartridgeConfig(t, false)
	withArgs(t, "omnia", "cartridge", "build", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected disabled message, stdout=%q stderr=%q", stdout, stderr)
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.DataDir, "cartridges")); len(entries) != 0 {
		t.Fatalf("expected no cartridge files written, found %d", len(entries))
	}
}

func TestCmdCartridgeLoadDisabledIsNoop(t *testing.T) {
	cfg := testConfig(t)
	path := writeCartridgeConfig(t, false)
	withArgs(t, "omnia", "cartridge", "load", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected disabled message, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdCartridgeBuildMissingConfigFileDegradesToDisabled matches the
// established fix (already applied to omnia blame/consolidate/rank-train):
// a missing config.yaml — the common fresh-install case — must degrade to
// disabled, never a fatal exit.
func TestCmdCartridgeBuildMissingConfigFileDegradesToDisabled(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	withArgs(t, "omnia", "cartridge", "build", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected disabled message (missing config file must degrade, not fatal), stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdCartridgeBuildOutsideGitRepoDegradesToColdStart(t *testing.T) {
	cfg := testConfig(t)
	path := writeCartridgeConfig(t, true)
	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", t.TempDir())
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "cold start") {
		t.Fatalf("expected a graceful cold-start message outside a git repo, stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdCartridgeLoadOutsideGitRepoDegradesToColdStart(t *testing.T) {
	cfg := testConfig(t)
	path := writeCartridgeConfig(t, true)
	withArgs(t, "omnia", "cartridge", "load", "--config", path, "--repo", t.TempDir())
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "cold start") {
		t.Fatalf("expected a graceful cold-start message outside a git repo, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdCartridgeBuildAndLoadRoundTrip is the end-to-end happy path: build
// inside a real repo with a seeded memory, then load reports warm.
func TestCmdCartridgeBuildAndLoadRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := writeCartridgeConfig(t, true)

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("cartridge-cli-session", "cartridge-cli-project", repo); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "cartridge-cli-session", Type: "decision", Title: "Use SQLite",
		Content: "We chose SQLite for local-first storage.", Project: "cartridge-cli-project", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo, "--project", "cartridge-cli-project")
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "built") {
		t.Fatalf("expected a successful build message, stdout=%q stderr=%q", stdout, stderr)
	}

	withArgs(t, "omnia", "cartridge", "load", "--config", path, "--repo", repo, "--project", "cartridge-cli-project")
	stdout, stderr = captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "warm") {
		t.Fatalf("expected a warm-start message, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdCartridgeLoadStaleCommitReportsStale builds at one commit, advances
// HEAD with a second commit, then confirms load reports stale rather than
// serving the old digest as current (REQ-452).
func TestCmdCartridgeLoadStaleCommitReportsStale(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := writeCartridgeConfig(t, true)

	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "built") {
		t.Fatalf("expected a successful build message, stdout=%q stderr=%q", stdout, stderr)
	}

	if err := os.WriteFile(filepath.Join(repo, "service2.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write second fixture: %v", err)
	}
	for _, args := range [][]string{{"add", "service2.go"}, {"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "second"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	withArgs(t, "omnia", "cartridge", "load", "--config", path, "--repo", repo)
	stdout, stderr = captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "stale") {
		t.Fatalf("expected a stale message after HEAD advanced, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdCartridgeLoadMissingReportsColdStart confirms the no-cartridge-yet
// case inside a real repo degrades cleanly (REQ-453).
func TestCmdCartridgeLoadMissingReportsColdStart(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := writeCartridgeConfig(t, true)

	withArgs(t, "omnia", "cartridge", "load", "--config", path, "--repo", repo)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "cold start") {
		t.Fatalf("expected a cold-start message, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdCartridgeBuildDefaultsToDetectedProjectNotEveryProject is the RED
// test for the review's BLOCKER finding: `omnia cartridge build` with no
// --project flag (the most natural, default invocation) MUST scope to the
// detected project only — never silently digest every project's memories
// in the store into one file. Before the fix, cmdCartridgeBuild passed the
// empty --project string straight through to cartridge.Build, and
// store.NormalizeProject("") is treated by AllObservations/
// CodeDecisionGraph/ListProcedures as "no filter — every project," so this
// would have reported 2 memories (both project-a's and project-b's) instead
// of just project-a's.
func TestCmdCartridgeBuildDefaultsToDetectedProjectNotEveryProject(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := writeCartridgeConfig(t, true)

	oldDetect := detectProject
	detectProject = func(string) string { return "project-a" }
	t.Cleanup(func() { detectProject = oldDetect })

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("session-a", "project-a", repo); err != nil {
		t.Fatalf("CreateSession (project-a): %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "session-a", Type: "decision", Title: "A decision",
		Content: "project A content", Project: "project-a", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation (project-a): %v", err)
	}
	if err := s.CreateSession("session-b", "project-b", repo); err != nil {
		t.Fatalf("CreateSession (project-b): %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "session-b", Type: "decision", Title: "B decision",
		Content: "project B content", Project: "project-b", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation (project-b): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Deliberately NO --project flag: this is the default, most natural
	// invocation, and must fall back to the detected project ("project-a")
	// rather than aggregating both projects' memories.
	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo)
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "built") {
		t.Fatalf("expected a successful build message, stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, "1 memories") {
		t.Fatalf("expected exactly 1 memory (only the detected project's, not both projects'), stdout=%q", stdout)
	}
}

// TestCmdCartridgeLoadFiltersByProjectAvoidingCrossProjectLeak is the RED
// test for the review's collision + dead-flag finding: building two
// projects at the same repo+commit must produce independently loadable
// cartridges, and `omnia cartridge load --project X` must actually honor X
// rather than silently discarding the flag (previously: `cmdCartridgeLoad`
// parsed --project into `_` and never used it, AND the on-disk file was
// keyed only by repo+commit, so project-b's build would have clobbered
// project-a's file — loading --project project-a afterward would have
// wrongly returned project-b's single memory instead of project-a's two).
func TestCmdCartridgeLoadFiltersByProjectAvoidingCrossProjectLeak(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := writeCartridgeConfig(t, true)

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("session-a", "project-a", repo); err != nil {
		t.Fatalf("CreateSession (project-a): %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "session-a", Type: "decision", Title: fmt.Sprintf("A decision %d", i),
			Content: fmt.Sprintf("project A content %d", i), Project: "project-a", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation (project-a): %v", err)
		}
	}
	if err := s.CreateSession("session-b", "project-b", repo); err != nil {
		t.Fatalf("CreateSession (project-b): %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "session-b", Type: "decision", Title: "B decision",
		Content: "project B content", Project: "project-b", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation (project-b): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo, "--project", "project-a")
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "built") {
		t.Fatalf("expected a successful build message (project-a), stdout=%q stderr=%q", stdout, stderr)
	}

	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo, "--project", "project-b")
	stdout, stderr = captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "built") {
		t.Fatalf("expected a successful build message (project-b), stdout=%q stderr=%q", stdout, stderr)
	}

	// If project-b's build had clobbered project-a's on-disk file (the
	// collision bug), or if --project were discarded on load (the dead-flag
	// bug), this would report project-b's single memory instead of
	// project-a's two.
	withArgs(t, "omnia", "cartridge", "load", "--config", path, "--repo", repo, "--project", "project-a")
	stdout, stderr = captureOutput(t, func() { cmdCartridge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "warm") {
		t.Fatalf("expected a warm-start message for project-a, stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, "2 memories") {
		t.Fatalf("expected project-a's own 2 memories, got a result contaminated by project-b, stdout=%q", stdout)
	}
}

// TestCmdCartridgeBuildRefusesPlaintextWhenEncryptionEnabled is the RED test
// for the review's at-rest-encryption-bypass finding: `omnia cartridge
// build` must fail closed (exit 1, no file written) rather than silently
// writing a plaintext digest when encryption.enabled is set, since cartridge
// export has no encrypted-output path yet.
func TestCmdCartridgeBuildRefusesPlaintextWhenEncryptionEnabled(t *testing.T) {
	cfg := testConfig(t)
	repo := cartridgeCLIRepo(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "cartridge:\n  enabled: true\nencryption:\n  enabled: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldExit := exitFunc
	var exitCode int
	exitFunc = func(code int) { exitCode = code }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "omnia", "cartridge", "build", "--config", path, "--repo", repo, "--project", "project-a")
	stdout, stderr := captureOutput(t, func() { cmdCartridge(cfg) })
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d (stdout=%q stderr=%q)", exitCode, stdout, stderr)
	}
	if !strings.Contains(stderr, "encryption") {
		t.Fatalf("expected error message to mention encryption, stderr=%q", stderr)
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.DataDir, "cartridges")); len(entries) != 0 {
		t.Fatalf("expected no cartridge file written when encryption is enabled and unsupported, found %d", len(entries))
	}
}
