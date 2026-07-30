package main

import (
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
