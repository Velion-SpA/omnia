package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func TestCmdBlameDisabledDoesNotOpenAnchorStore(t *testing.T) {
	cfg := testConfig(t)
	old := loadCodeGraphConfig
	loadCodeGraphConfig = func() bool { return false }
	t.Cleanup(func() { loadCodeGraphConfig = old })
	withArgs(t, "omnia", "blame", "outside.go:1")
	stdout, stderr := captureOutput(t, func() { cmdBlame(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected structured disabled response, stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestLoadCodeGraphConfigDegradesToDisabledWhenConfigFileIsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if loadCodeGraphConfig() {
		t.Fatal("expected disabled when no config.yaml exists (fresh install), got enabled")
	}
}

func TestCmdBlameOutsideGitRepoReturnsEmptyReason(t *testing.T) {
	cfg := testConfig(t)
	old := loadCodeGraphConfig
	loadCodeGraphConfig = func() bool { return true }
	t.Cleanup(func() { loadCodeGraphConfig = old })
	withArgs(t, "omnia", "blame", t.TempDir()+"/outside.go:1")
	stdout, stderr := captureOutput(t, func() { cmdBlame(cfg) })
	if stderr != "" || !strings.Contains(stdout, "no repo context") || !strings.Contains(stdout, "hits: 0") {
		t.Fatalf("expected empty no-repo response, stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdBlameEnabledPrintsMatchingMemory(t *testing.T) {
	cfg := testConfig(t)
	repo, file := blameCLIRepo(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("blame-cli-session", "blame-cli-project", repo); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "blame-cli-session", Type: "bugfix", Title: "Fix blame output", Content: "private full observation content", Project: "blame-cli-project", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if _, err := s.UpsertAnchor(store.UpsertAnchorParams{ObsSyncID: obs.SyncID, RepoRoot: repo, FilePath: "service.go", Symbol: "Run", LineStart: 1, LineEnd: 1, ContentHash: "hash"}); err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	old := loadCodeGraphConfig
	loadCodeGraphConfig = func() bool { return true }
	t.Cleanup(func() { loadCodeGraphConfig = old })
	withArgs(t, "omnia", "blame", file+":1", "--repo", repo)
	stdout, stderr := captureOutput(t, func() { cmdBlame(cfg) })
	if stderr != "" || !strings.Contains(stdout, "hits: 1") || !strings.Contains(stdout, "Fix blame output") || strings.Contains(stdout, "private full observation content") {
		t.Fatalf("unexpected enabled blame output stdout=%q stderr=%q", stdout, stderr)
	}
}

func blameCLIRepo(t *testing.T) (string, string) {
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
	return strings.TrimSpace(string(out)), file
}

var _ = store.AnchorStatusActive
