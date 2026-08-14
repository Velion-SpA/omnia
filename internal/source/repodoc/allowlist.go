package repodoc

import (
	"path/filepath"
	"strings"
)

// DefaultAllowlist is the default set of glob patterns repodoc ingests.
//
// This is deliberately narrow (plan P1's "Scope" requirement): repository
// documentation, never the whole tree, and explicitly never source code
// (that is internal/codegraph's problem, a different source entirely). A
// caller may widen or narrow this via New's allowlist parameter without a
// code change here — the config wiring that exposes that knob belongs to
// whatever calls New (not this package; see the package doc comment in
// repodoc.go for the ownership boundary).
//
// "openspec/specs/**/*.md" was REMOVED from this default (bugfix). Measured
// against this repo, Preview() with the plan's original seven-pattern list
// returned 56 files / 937 chunks — root docs (55 files) + docs/** (427
// chunks) + openspec/specs/** alone contributing 455 chunks, pushing the
// combined total to ~66% of the project's ~479 delta memories. That is more
// than double the plan's own "30% means the allowlist is too wide" ceiling
// (repodoc.go's Stats doc comment) and would swamp the delta log the plan
// explicitly forbids regressing. openspec/specs/** is internal
// change-proposal bookkeeping — it answers "what did we decide to build for
// change X", never "what IS this project" (P1's only job, see repodoc.go's
// package doc comment) — so it is the wrong content for this source even
// before the budget problem. It is not gone forever: a caller can still opt
// it back in via New's allowlist parameter, same as any other custom scope;
// it is only removed from the un-configured default.
//
// "docs/**/*.md" was NARROWED to "docs/*.md" (bugfix, follow-up decision
// after the openspec removal above still measured at 60.96% of 479 delta
// memories — see repodoc.go's package doc comment for that number and the
// reasoning). A deep docs/ tree is reference material — implementation
// notes, subsystem playbooks, troubleshooting runbooks — that answers "how
// does X work", not "what IS this project" (identity questions are answered
// by a project's TOP-LEVEL docs, mirroring DefaultMaxHeadingDepth's same
// argument one level up: chunk.go's doc comment makes the identical case for
// heading depth within a single file — this is that case for directory
// depth across files). Recursive docs/** ingestion stays available as an
// explicit New(...) allowlist override.
var DefaultAllowlist = []string{
	"README*",
	"VISION*",
	"ARCHITECTURE*",
	"CONTRIBUTING*",
	"docs/*.md",
	"adr/**/*.md",
}

// DefaultExcludedPathSegments hard-excludes a file from repodoc's corpus
// whenever any path segment matches one of these words case-insensitively —
// applied to EVERY allowlist, including a caller-supplied override, and
// including an explicit opt-in into recursive scope (New's allowlist
// parameter widening the glob does NOT widen this exclusion; the two are
// independent filters, both must pass).
//
// # Why this exists (read before removing or widening it)
//
// Every other narrowing in this package (DefaultAllowlist's openspec
// removal above, DefaultMaxHeadingDepth in chunk.go) exists to solve corpus
// SIZE: too many chunks would swamp the delta log. This filter solves a
// different, more important problem: CORRECTNESS.
//
// Plan P1's entire argument for ingesting documents instead of writing a
// hand-authored identity definition is that a document's staleness is
// DETECTABLE — every chunk carries a git-blame staleness anchor
// (anchor.go), and ApplyStalenessDownrank (internal/mcp, outside this
// package) can act on it once the underlying line range changes. That
// mechanism protects against a document going stale AFTER ingestion. It
// does nothing for a document that is ALREADY self-declared obsolete at
// ingestion time — a file living under docs/legacy/, docs/archive/,
// docs/deprecated/, or docs/beta/ is not "possibly stale", it is
// affirmatively telling you, via its own path, "do not treat this as
// current". Ranking a memory sourced from docs/legacy/ARCHITECTURE.md as
// evidence for "what is this project" is worse than having no identity
// answer at all: it is confidently wrong in exactly the way P1 exists to
// prevent (the brief's original "Omnia es un cliente de línea de comandos
// para GitLab" confabulation case — a wrong answer stated with confidence).
// A stale-but-unlabeled doc is P1's problem to solve via staleness anchors;
// a doc that has ALREADY labeled itself stale is not evidence to begin with
// and should never enter the corpus.
//
// This is why the exclusion is independent of the allowlist rather than
// folded into it as "just don't list docs/legacy/** in DefaultAllowlist":
// an operator who explicitly opts into recursive docs/** (or any other
// wider scope) via New's allowlist parameter gets the exclusion too, by
// design — widening WHERE repodoc looks must never silently widen WHAT
// counts as current documentation. To remove this protection, call
// SetExcludedPathSegments(nil) explicitly (see its doc comment) rather than
// widening the allowlist and expecting these files back.
var DefaultExcludedPathSegments = []string{"legacy", "archive", "deprecated", "beta"}

// isExcludedPath reports whether relPath (repo-root-relative,
// forward-slash-separated) has any path segment that case-insensitively
// equals one of excluded — a directory name anywhere in the path, OR the
// file's own basename with its extension stripped (so both
// "docs/legacy/foo.md" and a file literally named "docs/legacy.md" are
// caught: "a document that declares itself obsolete in its own path", per
// DefaultExcludedPathSegments' doc comment, covers either shape). This is
// an exact per-segment match, not a substring search — "docs/BETA_TESTING.md"
// does NOT match "beta" (its basename segment is "BETA_TESTING", not
// "beta"), deliberately: a substring match would also catch a file like
// "docs/legacy-format-migration-guide.md" that documents CURRENT behavior
// and merely mentions a legacy format, which is exactly the over-exclusion
// this function must avoid — the "empty default on a small repo" failure
// mode is symmetric with "corpus too big" and just as real.
func isExcludedPath(relPath string, excluded []string) bool {
	if len(excluded) == 0 {
		return false
	}
	segments := strings.Split(relPath, "/")
	for i, seg := range segments {
		if i == len(segments)-1 {
			seg = strings.TrimSuffix(seg, filepath.Ext(seg))
		}
		for _, ex := range excluded {
			if strings.EqualFold(seg, ex) {
				return true
			}
		}
	}
	return false
}

// matches reports whether relPath (repo-root-relative, forward-slash
// separated, e.g. "docs/architecture.md") matches any pattern in patterns.
func matches(patterns []string, relPath string) bool {
	for _, p := range patterns {
		if matchOne(p, relPath) {
			return true
		}
	}
	return false
}

// matchOne matches a single glob pattern against relPath. A pattern
// containing no "/" (e.g. "README*") only matches a file directly at the
// repo root — the plan's scope list names these as top-level project files;
// a nested "docs/README.md" is already covered by docs/**/*.md if it is
// markdown, and deliberately NOT covered otherwise (this package's whole
// point is curated top-level identity documents, not "every file that
// starts with README anywhere in the tree").
func matchOne(pattern, relPath string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(relPath, "/"))
}

// matchSegments implements a small, dependency-free "**"-aware glob matcher
// over path segments. The stdlib's path/filepath.Match has no "**" support
// and this repo's own de-risk precedent (internal/anchor's package doc:
// "no cgo, no go-git, no tree-sitter") favors a small from-scratch
// implementation over pulling in a globbing dependency for seven patterns.
//
// "**" matches zero or more whole path segments; every other pattern
// segment is matched against exactly one path segment via
// filepath.Match (so "*.md" still means "no slash in this segment").
func matchSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if matchSegments(pat[1:], path) {
			return true
		}
		if len(path) == 0 {
			return false
		}
		return matchSegments(pat, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], path[1:])
}
