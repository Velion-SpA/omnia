package codegraph

import "testing"

func TestNormalizeResolvesRelativeFileAgainstProvidedRepoRoot(t *testing.T) {
	repo := t.TempDir()
	gotRepo, gotFile, err := Normalize(repo, "internal/store/anchors.go")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if gotRepo != repo || gotFile != "internal/store/anchors.go" {
		t.Fatalf("got repo=%q file=%q, want repo=%q file=%q", gotRepo, gotFile, repo, "internal/store/anchors.go")
	}
}
