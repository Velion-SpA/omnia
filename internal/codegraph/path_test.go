package codegraph

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNormalizeResolvesRelativeFileAgainstProvidedRepoRoot(t *testing.T) {
	repo := codegraphTestRepo(t)
	gotRepo, gotFile, err := Normalize(repo, "internal/store/anchors.go")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if gotRepo != repo || gotFile != "internal/store/anchors.go" {
		t.Fatalf("got repo=%q file=%q, want repo=%q file=%q", gotRepo, gotFile, repo, "internal/store/anchors.go")
	}
}

func codegraphTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestNormalizeRejectsExplicitNonRepositoryRoot(t *testing.T) {
	if _, _, err := Normalize(t.TempDir(), "internal/store/anchors.go"); err == nil {
		t.Fatal("expected explicit non-repository root to be rejected")
	}
}
