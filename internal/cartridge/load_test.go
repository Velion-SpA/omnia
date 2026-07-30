package cartridge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/velion/omnia/internal/cartridge"
	"github.com/velion/omnia/internal/config"
)

func buildAndSave(t *testing.T, dir, repoRoot, project, headSHA string) *cartridge.Cartridge {
	t.Helper()
	s := newCartridgeTestStore(t)
	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: project, RepoRoot: repoRoot, HeadSHA: headSHA,
		TopN: 10, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := cartridge.Save(dir, c, config.EncryptionConfig{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return c
}

// TestLoadDetectsStaleCommitMismatch is the RED test for task 11.3: a
// cartridge built at abc123, loaded with a live HEAD of def456, MUST report
// stale (commit mismatch) — never served as current (REQ-452).
func TestLoadDetectsStaleCommitMismatch(t *testing.T) {
	dir := t.TempDir()
	buildAndSave(t, dir, "/repo", "cartridge-project", "abc123")

	result := cartridge.Load(dir, "/repo", "def456", "cartridge-project")
	if result.Fresh {
		t.Fatal("expected stale result, got Fresh=true")
	}
	if result.Reason != cartridge.ReasonStaleCommit {
		t.Fatalf("Reason = %q, want %q", result.Reason, cartridge.ReasonStaleCommit)
	}
	if result.Cartridge == nil || result.Cartridge.HeadSHA != "abc123" {
		t.Fatalf("expected the stale cartridge's original HeadSHA to be surfaced, got %+v", result.Cartridge)
	}
}

// TestLoadFreshCommitServesWarm confirms the happy path (REQ-453): a
// cartridge whose HeadSHA matches the live HEAD loads warm.
func TestLoadFreshCommitServesWarm(t *testing.T) {
	dir := t.TempDir()
	buildAndSave(t, dir, "/repo", "cartridge-project", "abc123")

	result := cartridge.Load(dir, "/repo", "abc123", "cartridge-project")
	if !result.Fresh || result.Cartridge == nil {
		t.Fatalf("expected a fresh, warm result, got %+v", result)
	}
	if result.Reason != "" {
		t.Fatalf("Reason = %q, want empty for a fresh result", result.Reason)
	}
}

// TestLoadMissingCartridgeDegradesToColdStart is the RED test for task
// 11.5: no cartridge file exists for the repo -> cold-query fallback, never
// an error (REQ-453).
func TestLoadMissingCartridgeDegradesToColdStart(t *testing.T) {
	dir := t.TempDir()
	result := cartridge.Load(dir, "/repo-with-no-cartridge", "abc123", "cartridge-project")
	if result.Fresh {
		t.Fatal("expected a missing/cold-start result, got Fresh=true")
	}
	if result.Reason != cartridge.ReasonMissing {
		t.Fatalf("Reason = %q, want %q", result.Reason, cartridge.ReasonMissing)
	}
	if result.Cartridge != nil {
		t.Fatalf("expected nil Cartridge for a missing result, got %+v", result.Cartridge)
	}
}

// TestLoadCorruptFileDegradesToColdStart confirms a corrupt/unreadable
// cartridge file never surfaces as an error to the caller — it degrades to
// cold-start exactly like a missing file (task 11.6).
func TestLoadCorruptFileDegradesToColdStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, cartridge.FileName("/repo", "cartridge-project", "abc123"))
	if err := os.WriteFile(path, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}
	result := cartridge.Load(dir, "/repo", "abc123", "cartridge-project")
	if result.Fresh {
		t.Fatal("expected a corrupt/cold-start result, got Fresh=true")
	}
	if result.Reason != cartridge.ReasonCorrupt {
		t.Fatalf("Reason = %q, want %q", result.Reason, cartridge.ReasonCorrupt)
	}
}

// TestLoadRejectsOldSchemaVersion is the RED test for task 11.7: an
// older-format cartridge is detected and rejected, falling back to
// cold-start rather than misreading it (REQ-456).
func TestLoadRejectsOldSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	old := cartridge.Cartridge{SchemaVersion: cartridge.SchemaVersion - 1, RepoRoot: "/repo", Project: "cartridge-project", HeadSHA: "abc123", BuiltAt: "2020-01-01T00:00:00Z"}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, cartridge.FileName("/repo", "cartridge-project", "abc123"))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write old-format fixture: %v", err)
	}

	result := cartridge.Load(dir, "/repo", "abc123", "cartridge-project")
	if result.Fresh {
		t.Fatal("expected an old-schema-version/cold-start result, got Fresh=true")
	}
	if result.Reason != cartridge.ReasonOldSchemaVersion {
		t.Fatalf("Reason = %q, want %q", result.Reason, cartridge.ReasonOldSchemaVersion)
	}
}

// TestLoadPicksMostRecentlyBuiltCartridgeForRepo confirms Load resolves the
// repo's latest artifact (by build recency) before comparing HeadSHA,
// rather than requiring the caller to already know which commit produced
// the file it should read.
func TestLoadPicksMostRecentlyBuiltCartridgeForRepo(t *testing.T) {
	dir := t.TempDir()
	buildAndSave(t, dir, "/repo", "cartridge-project", "abc123")
	time.Sleep(10 * time.Millisecond)
	buildAndSave(t, dir, "/repo", "cartridge-project", "def456")

	result := cartridge.Load(dir, "/repo", "def456", "cartridge-project")
	if !result.Fresh {
		t.Fatalf("expected the newer commit's cartridge to be found fresh, got %+v", result)
	}
}

// TestLoadScopesToRequestedProjectAvoidingCrossProjectCollision is the RED
// test for the review's dead-flag finding: two different projects built at
// the same repo+commit MUST each be loadable independently by --project,
// rather than the second Save silently shadowing the first (which
// previously made the --project flag on `omnia cartridge load` a no-op).
func TestLoadScopesToRequestedProjectAvoidingCrossProjectCollision(t *testing.T) {
	dir := t.TempDir()
	buildAndSave(t, dir, "/repo", "project-a", "abc123")
	buildAndSave(t, dir, "/repo", "project-b", "abc123")

	resultA := cartridge.Load(dir, "/repo", "abc123", "project-a")
	if !resultA.Fresh || resultA.Cartridge == nil || resultA.Cartridge.Project != "project-a" {
		t.Fatalf("expected project-a's own cartridge, got %+v", resultA)
	}
	resultB := cartridge.Load(dir, "/repo", "abc123", "project-b")
	if !resultB.Fresh || resultB.Cartridge == nil || resultB.Cartridge.Project != "project-b" {
		t.Fatalf("expected project-b's own cartridge, got %+v", resultB)
	}
}

// TestLoadReportsProjectMismatchForTamperedCartridge confirms the
// defense-in-depth check: even if a cartridge file exists at the path a
// project's Load call expects, Load must never serve it if the file's own
// embedded Project field says otherwise — it must report ReasonProjectMismatch
// (cold start) rather than silently returning the wrong project's memories.
func TestLoadReportsProjectMismatchForTamperedCartridge(t *testing.T) {
	dir := t.TempDir()
	wrong := cartridge.Cartridge{
		SchemaVersion: cartridge.SchemaVersion, RepoRoot: "/repo", Project: "project-b",
		HeadSHA: "abc123", BuiltAt: "2026-01-01T00:00:00Z",
	}
	data, err := json.Marshal(wrong)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, cartridge.FileName("/repo", "project-a", "abc123"))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write tampered fixture: %v", err)
	}

	result := cartridge.Load(dir, "/repo", "abc123", "project-a")
	if result.Fresh {
		t.Fatal("expected a project-mismatch result, got Fresh=true")
	}
	if result.Reason != cartridge.ReasonProjectMismatch {
		t.Fatalf("Reason = %q, want %q", result.Reason, cartridge.ReasonProjectMismatch)
	}
}
