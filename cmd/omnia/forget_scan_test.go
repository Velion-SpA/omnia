package main

// forget_scan_test.go — CLI tests for `omnia forget-scan`.
// Follows strict TDD RED -> GREEN -> REFACTOR, mirroring conflicts_test.go's
// pattern: testConfig -> seed -> withArgs -> captureOutput -> assert.
//
// Coverage: flag parsing (--project/--apply/--dry-run/--repo), dry-run vs
// apply behavior against a REAL git repo (forgetScanAnchorAdapter shells out
// to the real `git` binary, so this exercises the actual production
// AnchorProvider rather than a fake), and not-a-repo/no-git graceful
// degradation.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/store"
)

// ─── git fixture helpers ──────────────────────────────────────────────────────

// initForgetScanGitRepo creates a git repo in dir with a committer identity
// configured, so `git commit` works without relying on the host's global
// git config (mirrors internal/mcp/mcp_test.go's initTestGitRepo).
func initForgetScanGitRepo(t *testing.T, dir string) {
	t.Helper()
	runForgetScanGit(t, dir, "init")
	runForgetScanGit(t, dir, "config", "user.email", "test@example.com")
	runForgetScanGit(t, dir, "config", "user.name", "Test User")
}

func runForgetScanGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// writeAndCommitForgetScan writes content to file (relative to dir) and
// commits it.
func writeAndCommitForgetScan(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	full := filepath.Join(dir, file)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	runForgetScanGit(t, dir, "add", file)
	runForgetScanGit(t, dir, "commit", "-m", msg)
}

// mustSeedObsSyncID seeds a session + observation for project and returns
// the observation's sync_id (needed to link a memory_anchors row).
func mustSeedObsSyncID(t *testing.T, cfg store.Config, project, title string) string {
	t.Helper()
	id := mustSeedObservation(t, cfg, "ses-"+project, project, "bugfix", title, title+" content", "project")
	db := openTestDB(t, cfg)
	var syncID string
	if err := db.QueryRow(`SELECT sync_id FROM observations WHERE id=?`, id).Scan(&syncID); err != nil {
		t.Fatalf("get obs sync_id: %v", err)
	}
	return syncID
}

// mustUpsertAnchor seeds a memory_anchors row via the public store API.
func mustUpsertAnchor(t *testing.T, cfg store.Config, p store.UpsertAnchorParams) string {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	syncID, err := s.UpsertAnchor(p)
	if err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	return syncID
}

// anchorStatusCLI reads anchor_status for syncID directly (cmd/omnia has no
// exported single-anchor getter; mirrors internal/store's anchorStatus test helper).
func anchorStatusCLI(t *testing.T, cfg store.Config, syncID string) string {
	t.Helper()
	db := openTestDB(t, cfg)
	var status string
	if err := db.QueryRow(`SELECT anchor_status FROM memory_anchors WHERE sync_id = ?`, syncID).Scan(&status); err != nil {
		t.Fatalf("query anchor_status: %v", err)
	}
	return status
}

// ─── flag parsing ─────────────────────────────────────────────────────────────

// TestCmdForgetScan_FlagParsing_DryRunDefault verifies that omitting --apply
// defaults to a dry run (matches cmdConflictsScan's dry-run-by-default UX).
func TestCmdForgetScan_FlagParsing_DryRunDefault(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "forget-scan", "--project", "noanchorsproj")
	stdout, stderr := captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "dry_run:  true") {
		t.Errorf("expected dry_run: true by default; got: %q", stdout)
	}
	if !strings.Contains(stdout, "checked:  0") {
		t.Errorf("expected zero anchors checked for a project with none; got: %q", stdout)
	}
}

// TestCmdForgetScan_FlagParsing_ApplyFlag verifies --apply flips the reported
// dry_run flag to false.
func TestCmdForgetScan_FlagParsing_ApplyFlag(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "forget-scan", "--project", "applyflagproj", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "dry_run:  false") {
		t.Errorf("expected dry_run: false with --apply; got: %q", stdout)
	}
}

// TestCmdForgetScan_FlagParsing_ProjectFlag verifies --project routes to the
// right project's anchors instead of relying on cwd auto-detection.
func TestCmdForgetScan_FlagParsing_ProjectFlag(t *testing.T) {
	cfg := testConfig(t)
	syncID := mustSeedObsSyncID(t, cfg, "projflagproj", "Project flag anchor")
	mustUpsertAnchor(t, cfg, store.UpsertAnchorParams{
		ObsSyncID: syncID, RepoRoot: "/nonexistent-repo", FilePath: "f.go", Symbol: "F",
		LineStart: 1, LineEnd: 2, ContentHash: "hash-v1",
	})

	withArgs(t, "engram", "forget-scan", "--project", "projflagproj")
	stdout, stderr := captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "Forget Scan (project: projflagproj)") {
		t.Errorf("expected project header for projflagproj; got: %q", stdout)
	}
	// /nonexistent-repo has no .git -> Recheck errors -> surfaced as errors, not checked.
	if !strings.Contains(stdout, "errors:   1") {
		t.Errorf("expected 1 recheck error for the nonexistent repo; got: %q", stdout)
	}
	if !strings.Contains(stdout, "checked:  0") {
		t.Errorf("expected the errored anchor NOT to count as checked; got: %q", stdout)
	}
}

// ─── dry-run vs apply (real git) ──────────────────────────────────────────────

// TestCmdForgetScan_DryRun_ThenApply_MaterialChangeStales exercises the
// PRODUCTION AnchorProvider (forgetScanAnchorAdapter wrapping a real
// anchor.Probe) against an actual git repo: captures a real anchor, edits
// the function body at the SAME location, commits, then verifies dry-run
// reports staled=1 without writing, and --apply reports staled=1 AND
// persists anchor_status=stale.
func TestCmdForgetScan_DryRun_ThenApply_MaterialChangeStales(t *testing.T) {
	repoDir := t.TempDir()
	initForgetScanGitRepo(t, repoDir)

	const file = "foo.go"
	original := "package widget\n\nfunc Foo() int {\n\treturn 1\n}\n"
	writeAndCommitForgetScan(t, repoDir, file, original, "initial")

	ctx := context.Background()
	probe := anchor.NewProbe()
	const lineStart, lineEnd = 3, 5

	hash1, err := probe.RangeHash(ctx, repoDir, file, lineStart, lineEnd)
	if err != nil {
		t.Fatalf("RangeHash (initial): %v", err)
	}
	blame1, err := probe.Blame(ctx, repoDir, file, lineStart, lineEnd)
	if err != nil {
		t.Fatalf("Blame (initial): %v", err)
	}

	cfg := testConfig(t)
	syncID := mustSeedObsSyncID(t, cfg, "materialchangeproj", "Material change anchor")
	anchorID := mustUpsertAnchor(t, cfg, store.UpsertAnchorParams{
		ObsSyncID: syncID, RepoRoot: repoDir, FilePath: file, Symbol: "Foo",
		LineStart: lineStart, LineEnd: lineEnd,
		BlameSHA: blame1.SHA, BlameAt: blame1.At, ContentHash: hash1,
	})

	// Materially change the body at the SAME location (same start/end).
	changed := "package widget\n\nfunc Foo() int {\n\treturn 2\n}\n"
	writeAndCommitForgetScan(t, repoDir, file, changed, "change body")

	// ── dry-run: must report staled=1 but NOT write ──
	withArgs(t, "engram", "forget-scan", "--project", "materialchangeproj", "--repo", repoDir)
	stdout, stderr := captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr on dry-run: %q", stderr)
	}
	if !strings.Contains(stdout, "staled:   1") {
		t.Errorf("expected staled: 1 on dry-run; got: %q", stdout)
	}
	if !strings.Contains(stdout, "dry_run:  true") {
		t.Errorf("expected dry_run: true; got: %q", stdout)
	}
	if !strings.Contains(stdout, "errors:   0") {
		t.Errorf("expected errors: 0; got: %q", stdout)
	}
	if got := anchorStatusCLI(t, cfg, anchorID); got != store.AnchorStatusActive {
		t.Errorf("dry-run must not write: expected anchor_status=active; got %q", got)
	}

	// ── apply: must persist staled=1 ──
	withArgs(t, "engram", "forget-scan", "--project", "materialchangeproj", "--repo", repoDir, "--apply")
	stdout, stderr = captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr on apply: %q", stderr)
	}
	if !strings.Contains(stdout, "staled:   1") {
		t.Errorf("expected staled: 1 on apply; got: %q", stdout)
	}
	if !strings.Contains(stdout, "dry_run:  false") {
		t.Errorf("expected dry_run: false; got: %q", stdout)
	}
	if got := anchorStatusCLI(t, cfg, anchorID); got != store.AnchorStatusStale {
		t.Errorf("apply must persist anchor_status=stale; got %q", got)
	}
}

// ─── not-a-repo / no-git graceful degradation ─────────────────────────────────

// TestCmdForgetScan_NotAGitRepo_GracefulDegradation verifies that when an
// anchor's RepoRoot is NOT a git repository at all, `forget-scan` exits
// cleanly (no crash/panic, no fatal stderr) and surfaces the recheck failure
// via the errors counter rather than silently miscounting it as checked.
func TestCmdForgetScan_NotAGitRepo_GracefulDegradation(t *testing.T) {
	notARepoDir := t.TempDir() // deliberately never `git init`-ed

	cfg := testConfig(t)
	syncID := mustSeedObsSyncID(t, cfg, "notarepoproj", "Not a repo anchor")
	anchorID := mustUpsertAnchor(t, cfg, store.UpsertAnchorParams{
		ObsSyncID: syncID, RepoRoot: notARepoDir, FilePath: "whatever.go", Symbol: "Whatever",
		LineStart: 1, LineEnd: 2, ContentHash: "hash-v1",
	})

	withArgs(t, "engram", "forget-scan", "--project", "notarepoproj", "--repo", notARepoDir, "--apply")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdForgetScan(cfg) })
	if recovered != nil {
		t.Fatalf("cmdForgetScan panicked on a non-git repo dir: %v", recovered)
	}
	if stderr != "" {
		t.Fatalf("expected clean exit (no fatal error) for a non-git repo; got stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "errors:   1") {
		t.Errorf("expected the recheck failure surfaced as errors: 1; got: %q", stdout)
	}
	if !strings.Contains(stdout, "checked:  0") {
		t.Errorf("expected the errored anchor NOT counted as checked; got: %q", stdout)
	}
	// Never staled/traveled on a bare Recheck error, and no write occurred.
	if !strings.Contains(stdout, "staled:   0") || !strings.Contains(stdout, "traveled: 0") {
		t.Errorf("expected no staling/travel from a recheck error; got: %q", stdout)
	}
	if got := anchorStatusCLI(t, cfg, anchorID); got != store.AnchorStatusActive {
		t.Errorf("expected anchor to remain untouched/active; got %q", got)
	}
}

// ─── --repo filter ────────────────────────────────────────────────────────────

// TestCmdForgetScan_RepoFlag_FiltersAnchors verifies --repo restricts the
// recheck pass to anchors whose stored RepoRoot matches exactly, using two
// distinct nonexistent repo roots (both error on Recheck) as a proxy: only
// the targeted repo's anchor should be attempted (errors: 1, not 2).
func TestCmdForgetScan_RepoFlag_FiltersAnchors(t *testing.T) {
	cfg := testConfig(t)
	syncA := mustSeedObsSyncID(t, cfg, "repofilterproj", "Repo A anchor")
	syncB := mustSeedObsSyncID(t, cfg, "repofilterproj", "Repo B anchor")
	mustUpsertAnchor(t, cfg, store.UpsertAnchorParams{
		ObsSyncID: syncA, RepoRoot: "/nonexistent-repo-a", FilePath: "a.go", Symbol: "A",
		LineStart: 1, LineEnd: 2, ContentHash: "hash-a",
	})
	mustUpsertAnchor(t, cfg, store.UpsertAnchorParams{
		ObsSyncID: syncB, RepoRoot: "/nonexistent-repo-b", FilePath: "b.go", Symbol: "B",
		LineStart: 1, LineEnd: 2, ContentHash: "hash-b",
	})

	withArgs(t, "engram", "forget-scan", "--project", "repofilterproj", "--repo", "/nonexistent-repo-a")
	stdout, stderr := captureOutput(t, func() { cmdForgetScan(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "errors:   1") {
		t.Errorf("expected exactly 1 error (only repo-a's anchor rechecked); got: %q", stdout)
	}
}
