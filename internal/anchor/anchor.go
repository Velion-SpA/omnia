// Package anchor resolves cheap CODE ANCHORS (file + symbol + git-blame line
// range + blame SHA + content hash) by shelling out to the system `git`
// binary. It is a dependency-free leaf: no cgo, no go-git, no tree-sitter —
// exactly the "shell to git" de-risk decision locked in design obs #1594.
//
// Graceful degradation is load-bearing (spec REQ-002): when `git` is not on
// PATH, or the working directory is not inside a git repository, every
// method here returns a plain error (ErrGitNotInstalled / ErrNotAGitRepo).
// Callers (internal/mcp's mem_save wiring, a later slice) MUST treat both as
// "skip anchoring" and never let anchoring fail the surrounding operation.
package anchor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ─── Sentinel errors (REQ-002 graceful degradation) ───────────────────────────

// ErrGitNotInstalled is returned when the `git` binary is not found on PATH.
var ErrGitNotInstalled = errors.New("anchor: git binary not found on PATH")

// ErrNotAGitRepo is returned when the target directory is not inside a git
// working tree.
var ErrNotAGitRepo = errors.New("anchor: not a git repository")

// ErrSymbolNotFound is returned by Locate when symbol cannot be found
// anywhere in the repository via the cheap grep-based search.
var ErrSymbolNotFound = errors.New("anchor: symbol not found")

// ─── Probe ────────────────────────────────────────────────────────────────────

// Probe resolves code anchors by shelling out to `git`. runGit is injectable
// (mirrors internal/llm/claude.go's ClaudeRunner.runCLI/defaultRunCLI
// pattern): tests inject a fake to avoid spawning real git processes; real
// usage gets defaultRunGit via NewProbe.
type Probe struct {
	// runGit executes `git <args...>` with working directory dir, optionally
	// piping stdin, and returns combined stdout. Defaults to defaultRunGit.
	runGit func(ctx context.Context, dir string, args []string, stdin string) ([]byte, error)
}

// NewProbe constructs a Probe backed by the real exec.CommandContext
// implementation. Tests should construct &Probe{runGit: fake} directly.
func NewProbe() *Probe {
	return &Probe{runGit: defaultRunGit}
}

// Anchor is a fully-resolved code anchor: file + symbol + git-blame line
// range + blame SHA/time + a normalized content hash of the range's current
// bytes. RepoRoot is included (beyond design obs #1594's minimal contract
// sketch) because internal/store's memory_anchors.repo_root column needs it
// and Capture already resolves it for free.
type Anchor struct {
	File        string
	Symbol      string
	LineStart   int
	LineEnd     int
	BlameSHA    string
	BlameAt     string // RFC3339 commit time of BlameSHA
	ContentHash string // normalized-content hash of the range (see RangeHash)
	RepoRoot    string
}

// BlameRange is the outcome of blaming a single line range: the commit that
// most recently touched any line within it (by committer-time), plus that
// commit's RFC3339 time.
type BlameRange struct {
	SHA string
	At  string
}

// ─── gitExitError ─────────────────────────────────────────────────────────────

// gitExitError carries a failed git invocation's exit code and stderr so
// callers can distinguish specific outcomes (e.g. git grep's exit code 1 =
// "no matches", not a real failure) without parsing free-form error strings.
type gitExitError struct {
	exitCode int
	stderr   string
}

func (e *gitExitError) Error() string {
	return fmt.Sprintf("git exited %d: %s", e.exitCode, strings.TrimSpace(e.stderr))
}

// ─── defaultRunGit ─────────────────────────────────────────────────────────────

// defaultRunGit executes `git <args...>` with cmd.Dir=dir, optionally piping
// stdin, and returns stdout. It translates exec.ErrNotFound into
// ErrGitNotInstalled, mirroring internal/llm/claude.go's defaultRunCLI.
func defaultRunGit(ctx context.Context, dir string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &gitExitError{exitCode: exitErr.ExitCode(), stderr: string(exitErr.Stderr)}
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitNotInstalled
		}
		return nil, err
	}
	return out, nil
}

// translateGitError maps a raw runGit error into the graceful-degradation
// sentinels (ErrGitNotInstalled / ErrNotAGitRepo) whenever recognizable,
// otherwise returns err unchanged.
func translateGitError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrGitNotInstalled) {
		return err
	}
	var exitErr *gitExitError
	if errors.As(err, &exitErr) && strings.Contains(exitErr.stderr, "not a git repository") {
		return ErrNotAGitRepo
	}
	return err
}

// ─── repoRoot / HeadSHA ────────────────────────────────────────────────────────

// repoRoot resolves the absolute path to the git working tree root
// containing dir, via `git rev-parse --show-toplevel`.
func (p *Probe) repoRoot(ctx context.Context, dir string) (string, error) {
	out, err := p.runGit(ctx, dir, []string{"rev-parse", "--show-toplevel"}, "")
	if err != nil {
		return "", translateGitError(err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", ErrNotAGitRepo
	}
	return root, nil
}

// HeadSHA returns the current HEAD commit SHA for the repo containing dir,
// via `git rev-parse HEAD`.
func (p *Probe) HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := p.runGit(ctx, dir, []string{"rev-parse", "HEAD"}, "")
	if err != nil {
		return "", translateGitError(err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("anchor: git rev-parse HEAD returned empty output")
	}
	return sha, nil
}

// ─── Blame ─────────────────────────────────────────────────────────────────────

// blameHeaderRE matches a `git blame --porcelain` per-line header:
// "<40-hex-sha> <orig-line> <final-line>" with an optional trailing group
// count (present only on the first line of a same-commit contiguous group).
var blameHeaderRE = regexp.MustCompile(`^([0-9a-f]{40}) (\d+) (\d+)(?: \d+)?$`)

// blameLineEntry is one parsed source line from `git blame --porcelain`.
type blameLineEntry struct {
	sha           string
	committerTime int64
}

// Blame runs `git blame -L start,end --porcelain -- file` and reduces the
// per-line result to the single commit that most recently touched any line
// in [start,end] (by committer-time) — the commit the stale receipt
// ("anchor <file>:<lines> changed <old→new sha>", a later slice) compares.
func (p *Probe) Blame(ctx context.Context, dir, file string, start, end int) (BlameRange, error) {
	if start <= 0 || end <= 0 || end < start {
		return BlameRange{}, fmt.Errorf("anchor: invalid line range [%d,%d]", start, end)
	}

	out, err := p.runGit(ctx, dir, []string{"blame", "-L", fmt.Sprintf("%d,%d", start, end), "--porcelain", "--", file}, "")
	if err != nil {
		return BlameRange{}, translateGitError(err)
	}

	entries, err := parseBlamePorcelain(out)
	if err != nil {
		return BlameRange{}, err
	}
	if len(entries) == 0 {
		return BlameRange{}, fmt.Errorf("anchor: git blame returned no lines for %s:%d-%d", file, start, end)
	}

	latest := entries[0]
	for _, e := range entries[1:] {
		if e.committerTime > latest.committerTime {
			latest = e
		}
	}
	return BlameRange{SHA: latest.sha, At: formatCommitterTime(latest.committerTime)}, nil
}

// parseBlamePorcelain parses `git blame --porcelain` output into one
// blameLineEntry per source line. Every source line gets its own header
// line (SHA + orig-line + final-line) followed eventually by a tab-prefixed
// content line; the FULL metadata block (author/committer/summary/filename)
// is only emitted the first time a given commit is seen within this
// invocation — subsequent lines from the same commit get an abbreviated
// header with no metadata, so committer-time is cached per SHA.
func parseBlamePorcelain(raw []byte) ([]blameLineEntry, error) {
	lines := strings.Split(string(raw), "\n")
	committerTimeCache := map[string]int64{}
	var entries []blameLineEntry

	var curSHA string
	var curCommitterTime int64
	haveHeader := false

	for _, line := range lines {
		if line == "" {
			continue
		}
		if m := blameHeaderRE.FindStringSubmatch(line); m != nil {
			curSHA = m[1]
			curCommitterTime = committerTimeCache[curSHA] // 0 if not yet cached
			haveHeader = true
			continue
		}
		if !haveHeader {
			continue // defensive: ignore stray lines before the first header
		}
		if rest, ok := strings.CutPrefix(line, "committer-time "); ok {
			ct, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("anchor: parse committer-time: %w", err)
			}
			curCommitterTime = ct
			committerTimeCache[curSHA] = ct
			continue
		}
		if strings.HasPrefix(line, "\t") {
			// Content line — completes this source line's record.
			entries = append(entries, blameLineEntry{sha: curSHA, committerTime: curCommitterTime})
			haveHeader = false
			continue
		}
		// Any other metadata line (author, author-mail, author-time,
		// author-tz, committer, committer-mail, committer-tz, summary,
		// previous, boundary, filename) is informational only — skip.
	}
	return entries, nil
}

func formatCommitterTime(unixSeconds int64) string {
	return time.Unix(unixSeconds, 0).UTC().Format(time.RFC3339)
}

// ─── RangeHash ─────────────────────────────────────────────────────────────────

// resolveInRepo validates that file — joined with dir and cleaned — stays
// inside dir, and returns the resulting absolute path. file is LLM-supplied
// (mem_save's code_anchors[].file, and later PR2's ScanProject re-check), so
// this is the security boundary for every filesystem read keyed by it: a
// bare `filepath.Join(dir, file)` + os.ReadFile does NOT stop `file` from
// containing "../" segments that escape dir entirely (proven: file =
// "../../../../etc/hosts" reads and hashes /etc/hosts). Every method reading
// file bytes directly off disk (currently only RangeHash — Blame and Locate
// shell out to `git`, which enforces its own repository boundary) MUST
// resolve through here first.
//
// Returns an error (never a panic) when file resolves outside dir. Callers
// MUST treat that error like any other RangeHash/Capture failure — "skip
// this anchor" (REQ-002 graceful degradation), never a hard failure.
func resolveInRepo(dir, file string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("anchor: resolve repo dir %s: %w", dir, err)
	}
	absDir = filepath.Clean(absDir)

	cleaned := filepath.Clean(filepath.Join(absDir, file))
	if cleaned != absDir && !strings.HasPrefix(cleaned, absDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("anchor: file %q escapes repo root %s", file, absDir)
	}
	return cleaned, nil
}

// RangeHash computes a content hash of file's current [start,end] line range
// (1-based, inclusive), reading the working-tree file directly off disk (the
// current reality at capture/recheck time, not a committed blob) and hashing
// via `git hash-object --stdin` (no -w — this never writes to the object
// database, it only computes a hash). The range is normalized first
// (normalizeRangeContent) so whitespace-only edits never change the hash —
// see IsMaterialChange for why that matters (REQ-003).
func (p *Probe) RangeHash(ctx context.Context, dir, file string, start, end int) (string, error) {
	resolved, err := resolveInRepo(dir, file)
	if err != nil {
		return "", err
	}

	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("anchor: read %s: %w", file, err)
	}

	rangeContent, err := extractLineRange(string(raw), start, end)
	if err != nil {
		return "", err
	}
	normalized := normalizeRangeContent(rangeContent)

	out, err := p.runGit(ctx, dir, []string{"hash-object", "--stdin"}, normalized)
	if err != nil {
		return "", translateGitError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// extractLineRange returns the 1-based, inclusive [start,end] line slice of
// content, joined by "\n".
func extractLineRange(content string, start, end int) (string, error) {
	lines := strings.Split(content, "\n")
	if start < 1 || end < start || end > len(lines) {
		return "", fmt.Errorf("anchor: line range [%d,%d] out of bounds (file has %d lines)", start, end, len(lines))
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}

// ─── Capture ────────────────────────────────────────────────────────────────────

// Capture resolves a full Anchor for (file, symbol, start, end) inside the
// repo containing dir. Graceful degradation (REQ-002): when git is missing
// or dir is not inside a git repo, Capture returns (Anchor{},
// ErrGitNotInstalled) or (Anchor{}, ErrNotAGitRepo) — callers (internal/mcp's
// mem_save wiring) MUST treat both as "skip anchoring, never fail the save."
// Capture never panics; any error is returned, not raised.
func (p *Probe) Capture(ctx context.Context, dir, file, symbol string, start, end int) (Anchor, error) {
	if strings.TrimSpace(file) == "" {
		return Anchor{}, fmt.Errorf("anchor: file is required")
	}
	if start <= 0 || end <= 0 || end < start {
		return Anchor{}, fmt.Errorf("anchor: invalid line range [%d,%d]", start, end)
	}

	root, err := p.repoRoot(ctx, dir)
	if err != nil {
		return Anchor{}, err
	}

	br, err := p.Blame(ctx, root, file, start, end)
	if err != nil {
		return Anchor{}, err
	}

	hash, err := p.RangeHash(ctx, root, file, start, end)
	if err != nil {
		return Anchor{}, err
	}

	return Anchor{
		File:        file,
		Symbol:      symbol,
		LineStart:   start,
		LineEnd:     end,
		BlameSHA:    br.SHA,
		BlameAt:     br.At,
		ContentHash: hash,
		RepoRoot:    root,
	}, nil
}

// ─── Locate ────────────────────────────────────────────────────────────────────

// declarationPattern builds a cheap, language-agnostic "looks like a
// declaration of symbol" regex for `git grep -n -E`. This is intentionally
// NOT a real AST/tree-sitter parse (explicitly deferred — design obs #1594);
// it is a best-effort heuristic for the explicit-anchor v1 slice.
//
// Word boundaries are emulated with non-word character classes instead of
// \b: POSIX ERE (used by `git grep -E` on platforms whose git has no
// libpcre, e.g. macOS's stock git) does not support \b, so a pattern using
// it never matches there — silently breaking symbol relocation. Groups are
// plain capturing groups, never (?:...), which POSIX ERE also lacks.
//
// The keyword is bounded on the left (line start or a non-word char, so a
// substring keyword like the "var" inside "somevar" does not match) and the
// symbol is bounded by a mandatory non-word separator after the keyword
// (with an optional non-word-terminated middle, so receivers/generics like
// `func (r *Repo) Add(` still match) and a trailing non-word char or line end.
//
// This stays a loose best-effort heuristic, not an AST parse: it still admits
// a same-line non-declaration use (e.g. a field/key inside a one-line struct
// literal). Word classes are ASCII-oriented; matching near non-ASCII
// identifiers is git-locale-dependent (Go RE2's [[:alnum:]] is always ASCII).
func declarationPattern(symbol string) string {
	return fmt.Sprintf(
		`(^|[^[:alnum:]_])(func|def|class|type|const|var|let)[^[:alnum:]_](.*[^[:alnum:]_])?%s([^[:alnum:]_]|$)`,
		regexp.QuoteMeta(symbol),
	)
}

// Locate performs a cheap, git-grep-based search for symbol's declaration:
// first within file (the anchor's original file — the common case for a
// pure line-shift refactor), then repo-wide as a fallback (the symbol moved
// to a different file). Returns the matched file (relative to dir, as
// reported by git grep) and 1-based line number of the first match.
//
// Returns ErrSymbolNotFound (never a panic) when nothing matches anywhere —
// callers (a later ScanProject{Source:anchor} pass) treat this as
// "relocation failed, proceed to staleness evaluation" (REQ-004).
func (p *Probe) Locate(ctx context.Context, dir, file, symbol string) (matchedFile string, line int, err error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return "", 0, ErrSymbolNotFound
	}
	pattern := declarationPattern(symbol)

	if strings.TrimSpace(file) != "" {
		out, err := p.runGit(ctx, dir, []string{"grep", "-n", "-E", pattern, "--", file}, "")
		if err == nil {
			if f, l, ok := firstGrepMatch(out); ok {
				return f, l, nil
			}
		} else if !isNoMatchExit(err) {
			return "", 0, translateGitError(err)
		}
	}

	// Repo-wide fallback: symbol moved to a different file (or no file was given).
	out, err := p.runGit(ctx, dir, []string{"grep", "-n", "-E", pattern}, "")
	if err != nil {
		if isNoMatchExit(err) {
			return "", 0, ErrSymbolNotFound
		}
		return "", 0, translateGitError(err)
	}
	if f, l, ok := firstGrepMatch(out); ok {
		return f, l, nil
	}
	return "", 0, ErrSymbolNotFound
}

// isNoMatchExit reports whether err is `git grep`'s exit code 1, which means
// "no matches found" — not a real failure.
func isNoMatchExit(err error) bool {
	var exitErr *gitExitError
	return errors.As(err, &exitErr) && exitErr.exitCode == 1
}

// firstGrepMatch parses `git grep -n` output (lines shaped
// "<file>:<line>:<content>") and returns the first match's file and line.
func firstGrepMatch(out []byte) (file string, line int, ok bool) {
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return "", 0, false
	}
	firstLine := strings.SplitN(text, "\n", 2)[0]
	parts := strings.SplitN(firstLine, ":", 3)
	if len(parts) < 2 {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, false
	}
	return parts[0], n, true
}
