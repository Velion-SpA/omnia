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
var DefaultAllowlist = []string{
	"README*",
	"VISION*",
	"ARCHITECTURE*",
	"CONTRIBUTING*",
	"docs/**/*.md",
	"adr/**/*.md",
	"openspec/specs/**/*.md",
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
