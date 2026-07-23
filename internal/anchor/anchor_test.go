package anchor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeGit lets tests script runGit responses per call without spawning a
// real git process — mirrors internal/llm/claude_test.go's fake runCLI
// pattern for ClaudeRunner.
type fakeGit struct {
	calls []fakeGitCall
	// responses is consumed in order; each call to runGit pops the next one.
	responses []fakeGitResponse
}

type fakeGitCall struct {
	dir   string
	args  []string
	stdin string
}

type fakeGitResponse struct {
	out []byte
	err error
}

func (f *fakeGit) run(ctx context.Context, dir string, args []string, stdin string) ([]byte, error) {
	f.calls = append(f.calls, fakeGitCall{dir: dir, args: append([]string(nil), args...), stdin: stdin})
	if len(f.responses) == 0 {
		return nil, errors.New("fakeGit: no more scripted responses")
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	return next.out, next.err
}

func newFakeProbe(f *fakeGit) *Probe {
	return &Probe{runGit: f.run}
}

// ─── 1.1: Blame parses `git blame -L --porcelain` into BlameRange{SHA,At} ───

func TestBlame_ParsesPorcelainSingleCommit(t *testing.T) {
	porcelain := strings.Join([]string{
		"d8d74ffa1489ad18fa062f76a5455cf41f22ea9d 1 1 3",
		"author Test Author",
		"author-mail <test@example.com>",
		"author-time 1700000000",
		"author-tz -0400",
		"committer Test Author",
		"committer-mail <test@example.com>",
		"committer-time 1700000000",
		"committer-tz -0400",
		"summary initial commit",
		"filename foo.go",
		"\tpackage foo",
		"d8d74ffa1489ad18fa062f76a5455cf41f22ea9d 2 2",
		"\t",
		"d8d74ffa1489ad18fa062f76a5455cf41f22ea9d 3 3",
		"\tfunc Foo() {}",
		"",
	}, "\n")

	f := &fakeGit{responses: []fakeGitResponse{{out: []byte(porcelain)}}}
	p := newFakeProbe(f)

	br, err := p.Blame(context.Background(), "/repo", "foo.go", 1, 3)
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if br.SHA != "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d" {
		t.Errorf("expected SHA d8d74ff..., got %q", br.SHA)
	}
	if br.At != "2023-11-14T22:13:20Z" {
		t.Errorf("expected committer time 2023-11-14T22:13:20Z, got %q", br.At)
	}

	if len(f.calls) != 1 {
		t.Fatalf("expected 1 git call, got %d", len(f.calls))
	}
	call := f.calls[0]
	if call.dir != "/repo" {
		t.Errorf("expected dir /repo, got %q", call.dir)
	}
	wantArgs := []string{"blame", "-L", "1,3", "--porcelain", "--", "foo.go"}
	if strings.Join(call.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("expected args %v, got %v", wantArgs, call.args)
	}
}

// TestBlame_PicksMostRecentCommitAcrossMultipleAuthors verifies that when a
// range spans lines touched by different commits, Blame reduces to the
// single MOST RECENT (highest committer-time) commit — the one the recall
// receipt "changed <old→new sha>" (Phase 6, later slice) will compare against.
func TestBlame_PicksMostRecentCommitAcrossMultipleAuthors(t *testing.T) {
	porcelain := strings.Join([]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 10 10 1",
		"author A",
		"author-mail <a@example.com>",
		"author-time 1600000000",
		"author-tz +0000",
		"committer A",
		"committer-mail <a@example.com>",
		"committer-time 1600000000",
		"committer-tz +0000",
		"summary older change",
		"filename bar.go",
		"\told line",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 11 11 1",
		"author B",
		"author-mail <b@example.com>",
		"author-time 1700000000",
		"author-tz +0000",
		"committer B",
		"committer-mail <b@example.com>",
		"committer-time 1700000000",
		"committer-tz +0000",
		"summary newer change",
		"filename bar.go",
		"\tnewer line",
		"",
	}, "\n")

	f := &fakeGit{responses: []fakeGitResponse{{out: []byte(porcelain)}}}
	p := newFakeProbe(f)

	br, err := p.Blame(context.Background(), "/repo", "bar.go", 10, 11)
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if br.SHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("expected the most recent commit SHA (b...), got %q", br.SHA)
	}
}

func TestBlame_RejectsInvalidRange(t *testing.T) {
	f := &fakeGit{}
	p := newFakeProbe(f)

	if _, err := p.Blame(context.Background(), "/repo", "foo.go", 5, 3); err == nil {
		t.Fatalf("expected error for end < start")
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no git invocation for an invalid range, got %d calls", len(f.calls))
	}
}

// ─── 1.3: HeadSHA + RangeHash fixtures ────────────────────────────────────────

func TestHeadSHA_ParsesRevParseOutput(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{{out: []byte("e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4\n")}}}
	p := newFakeProbe(f)

	sha, err := p.HeadSHA(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha != "e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4" {
		t.Errorf("unexpected SHA: %q", sha)
	}
	if len(f.calls) != 1 || strings.Join(f.calls[0].args, " ") != "rev-parse HEAD" {
		t.Fatalf("unexpected git invocation: %+v", f.calls)
	}
}

func TestRangeHash_HashesNormalizedFileRange(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\nline4\n"
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	f := &fakeGit{responses: []fakeGitResponse{{out: []byte("abc123hash\n")}}}
	p := newFakeProbe(f)

	hash, err := p.RangeHash(context.Background(), dir, "foo.go", 2, 3)
	if err != nil {
		t.Fatalf("RangeHash: %v", err)
	}
	if hash != "abc123hash" {
		t.Errorf("expected hash-object output trimmed, got %q", hash)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 git call, got %d", len(f.calls))
	}
	call := f.calls[0]
	if strings.Join(call.args, " ") != "hash-object --stdin" {
		t.Errorf("expected hash-object --stdin, got %v", call.args)
	}
	if call.stdin != "line2\nline3" {
		t.Errorf("expected normalized range piped to stdin, got %q", call.stdin)
	}
}

func TestRangeHash_OutOfBoundsRangeErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("only one line\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	f := &fakeGit{}
	p := newFakeProbe(f)

	if _, err := p.RangeHash(context.Background(), dir, "foo.go", 1, 5); err == nil {
		t.Fatalf("expected out-of-bounds error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no git invocation when the range is out of bounds")
	}
}

// TestRangeHash_RejectsPathTraversal proves RangeHash cannot be used to read
// files outside dir. file is LLM-controlled (mem_save's code_anchors[].file)
// and RangeHash is a public exported primitive PR2's ScanProject re-check
// will also call — the traversal guard must live in RangeHash itself, not
// rely on any caller's incidental ordering.
func TestRangeHash_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	secretPath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top secret\n"), 0o644); err != nil {
		t.Fatalf("write secret fixture: %v", err)
	}

	// Derive a relative "../" traversal path from dir to the secret file
	// (portable across machines/CI, unlike a hardcoded "../../../../etc/hosts").
	traversal, err := filepath.Rel(dir, secretPath)
	if err != nil {
		t.Fatalf("compute relative traversal path: %v", err)
	}

	f := &fakeGit{}
	p := newFakeProbe(f)

	if _, err := p.RangeHash(context.Background(), dir, traversal, 1, 1); err == nil {
		t.Fatalf("expected RangeHash to reject a path traversal outside dir, got nil error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no git invocation for a rejected traversal path, got %d calls", len(f.calls))
	}
}

// ─── 1.5: no-git / not-a-repo degradation ─────────────────────────────────────

func TestCapture_NoGitBinaryDegradesGracefully(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{{err: ErrGitNotInstalled}}}
	p := newFakeProbe(f)

	_, err := p.Capture(context.Background(), "/repo", "foo.go", "Foo", 1, 3)
	if !errors.Is(err, ErrGitNotInstalled) {
		t.Fatalf("expected ErrGitNotInstalled, got %v", err)
	}
}

func TestCapture_NotARepoDegradesGracefully(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{
		{err: &gitExitError{exitCode: 128, stderr: "fatal: not a git repository (or any of the parent directories): .git"}},
	}}
	p := newFakeProbe(f)

	_, err := p.Capture(context.Background(), "/repo", "foo.go", "Foo", 1, 3)
	if !errors.Is(err, ErrNotAGitRepo) {
		t.Fatalf("expected ErrNotAGitRepo, got %v", err)
	}
}

func TestCapture_NeverPanicsOnGitFailure(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Capture panicked: %v", r)
		}
	}()

	f := &fakeGit{responses: []fakeGitResponse{{err: errors.New("boom")}}}
	p := newFakeProbe(f)

	if _, err := p.Capture(context.Background(), "/repo", "foo.go", "Foo", 1, 3); err == nil {
		t.Fatalf("expected an error to propagate")
	}
}

func TestCapture_HappyPath(t *testing.T) {
	dir := t.TempDir()
	content := "package foo\n\nfunc Foo() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	porcelain := strings.Join([]string{
		"d8d74ffa1489ad18fa062f76a5455cf41f22ea9d 3 3 1",
		"author Test Author",
		"author-mail <test@example.com>",
		"author-time 1700000000",
		"author-tz -0400",
		"committer Test Author",
		"committer-mail <test@example.com>",
		"committer-time 1700000000",
		"committer-tz -0400",
		"summary initial commit",
		"filename foo.go",
		"\tfunc Foo() {}",
		"",
	}, "\n")

	f := &fakeGit{responses: []fakeGitResponse{
		{out: []byte(dir + "\n")},       // repoRoot: rev-parse --show-toplevel
		{out: []byte(porcelain)},        // Blame
		{out: []byte("deadbeefhash\n")}, // RangeHash: hash-object --stdin
	}}
	p := newFakeProbe(f)

	anc, err := p.Capture(context.Background(), dir, "foo.go", "Foo", 3, 3)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if anc.File != "foo.go" || anc.Symbol != "Foo" || anc.LineStart != 3 || anc.LineEnd != 3 {
		t.Errorf("unexpected anchor fields: %+v", anc)
	}
	if anc.BlameSHA != "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d" {
		t.Errorf("unexpected BlameSHA: %q", anc.BlameSHA)
	}
	if anc.ContentHash != "deadbeefhash" {
		t.Errorf("unexpected ContentHash: %q", anc.ContentHash)
	}
	if anc.RepoRoot != dir {
		t.Errorf("expected RepoRoot %q, got %q", dir, anc.RepoRoot)
	}
}

// ─── 1.7 / 1.8: Locate(symbol) relocation ─────────────────────────────────────

func TestLocate_FindsSymbolInSameFile(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{
		{out: []byte("foo.go:42:func Foo() {\n")},
	}}
	p := newFakeProbe(f)

	file, line, err := p.Locate(context.Background(), "/repo", "foo.go", "Foo")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if file != "foo.go" || line != 42 {
		t.Errorf("expected foo.go:42, got %s:%d", file, line)
	}
}

func TestLocate_FallsBackToRepoWideSearch(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{
		{err: &gitExitError{exitCode: 1}},        // no match in the original file
		{out: []byte("bar.go:7:func Foo() {\n")}, // repo-wide fallback finds it elsewhere
	}}
	p := newFakeProbe(f)

	file, line, err := p.Locate(context.Background(), "/repo", "foo.go", "Foo")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if file != "bar.go" || line != 7 {
		t.Errorf("expected bar.go:7, got %s:%d", file, line)
	}
}

func TestLocate_SymbolNotFoundAnywhere(t *testing.T) {
	f := &fakeGit{responses: []fakeGitResponse{
		{err: &gitExitError{exitCode: 1}},
		{err: &gitExitError{exitCode: 1}},
	}}
	p := newFakeProbe(f)

	_, _, err := p.Locate(context.Background(), "/repo", "foo.go", "Ghost")
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Fatalf("expected ErrSymbolNotFound, got %v", err)
	}
}

func TestLocate_EmptySymbolIsNotFound(t *testing.T) {
	f := &fakeGit{}
	p := newFakeProbe(f)

	if _, _, err := p.Locate(context.Background(), "/repo", "foo.go", "  "); !errors.Is(err, ErrSymbolNotFound) {
		t.Fatalf("expected ErrSymbolNotFound for empty symbol, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no git invocation for an empty symbol")
	}
}

// ─── declarationPattern: ERE-portable word boundaries (regression) ─────────────

// TestDeclarationPattern_EmulatesWordBoundaryWithoutBackslashB pins the fix for
// the \b portability bug: POSIX-ERE git grep (macOS stock git, no libpcre) does
// not support \b, so the old pattern never matched there and symbol relocation
// silently broke. The replacement must match real declarations, reject
// substrings, and contain no \b.
func TestDeclarationPattern_EmulatesWordBoundaryWithoutBackslashB(t *testing.T) {
	pat := declarationPattern("Add")
	if strings.Contains(pat, `\b`) {
		t.Fatalf("pattern must not use \\b (unsupported by POSIX-ERE git grep): %q", pat)
	}
	re := regexp.MustCompile(pat)

	matches := []string{
		"func Add(a, b int) int {",     // plain function
		"func (r *Repo) Add(id int) {", // method with receiver + generics-ish middle
		"type Add struct {",            // type decl
		"var Add = compute()",          // package var
		"func Add {",                   // symbol immediately after the keyword separator
	}
	for _, line := range matches {
		if !re.MatchString(line) {
			t.Errorf("expected declaration to match: %q", line)
		}
	}

	nonMatches := []string{
		"type AddRequest struct {",       // substring suffix
		"const myAdd = 1",                // substring prefix
		"result := myAddHelper(x)",       // no keyword + substring
		"return added(x)",                // no declaration keyword
		"somevar Add = 1",                // "var" is a substring of "somevar" (leading boundary)
		"// prototype Add stub function", // "type" inside "prototype" (leading boundary)
	}
	for _, line := range nonMatches {
		if re.MatchString(line) {
			t.Errorf("expected NO match (substring/non-decl): %q", line)
		}
	}
}

// TestLocate_FindsSymbolViaRealGitGrep exercises Locate against a REAL
// `git grep -E` in a throwaway repo — the coverage gap that let the \b bug
// ship: every other Locate test injects a fakeGit and never runs the pattern.
// With the old \b pattern this test fails on POSIX-ERE git (never matches).
func TestLocate_FindsSymbolViaRealGitGrep(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, kv := range [][2]string{
		{"GIT_AUTHOR_NAME", "T"}, {"GIT_AUTHOR_EMAIL", "t@e.co"},
		{"GIT_COMMITTER_NAME", "T"}, {"GIT_COMMITTER_EMAIL", "t@e.co"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// AddRequest (line 3) is a decoy that must not shadow func Add (line 5).
	src := "package foo\n" +
		"\n" +
		"type AddRequest struct{}\n" +
		"\n" +
		"func Add(a, b int) int {\n" +
		"\treturn a + b\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGit("init", "-q")
	runGit("add", "foo.go")
	runGit("commit", "-q", "-m", "init")

	file, line, err := NewProbe().Locate(context.Background(), dir, "foo.go", "Add")
	if err != nil {
		t.Fatalf("Locate against real git: %v", err)
	}
	if file != "foo.go" || line != 5 {
		t.Errorf("expected foo.go:5 (the func Add decl), got %s:%d", file, line)
	}
}
