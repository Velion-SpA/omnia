package repodoc

import "testing"

func TestMatches_DefaultAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		relPath string
		want    bool
	}{
		{"root README.md", "README.md", true},
		{"root README with suffix", "README-old.md", true},
		{"root VISION.md", "VISION.md", true},
		{"root ARCHITECTURE.md", "ARCHITECTURE.md", true},
		{"root CONTRIBUTING.md", "CONTRIBUTING.md", true},
		{"nested README not matched", "internal/README.md", false},
		{"docs top-level md", "docs/architecture.md", true},
		{"docs top-level md, another file", "docs/conversational-retrieval-plan.md", true},
		{"docs nested md no longer in default (bugfix, docs/** narrowed to docs/*.md)", "docs/a/x.md", false},
		{"docs deeply nested md no longer in default", "docs/a/b/c/deep.md", false},
		{"docs non-md file excluded", "docs/notes.txt", false},
		{"adr md", "adr/0001-use-repodoc.md", true},
		{"adr nested md", "adr/2024/0001-decision.md", true},
		{"openspec specs md no longer in default (bugfix, corpus-growth)", "openspec/specs/repodoc/spec.md", false},
		{"openspec changes not matched", "openspec/changes/foo/proposal.md", false},
		{"source code excluded", "internal/source/repodoc/repodoc.go", false},
		{"go.mod excluded", "go.mod", false},
		{"random root file excluded", "Makefile", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matches(DefaultAllowlist, tt.relPath)
			if got != tt.want {
				t.Errorf("matches(DefaultAllowlist, %q) = %v, want %v", tt.relPath, got, tt.want)
			}
		})
	}
}

func TestMatchSegments_DoubleStarZeroOrMoreSegments(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"docs/**/*.md", "docs/x.md", true},
		{"docs/**/*.md", "docs/a/x.md", true},
		{"docs/**/*.md", "docs/a/b/c/x.md", true},
		{"docs/**/*.md", "docs2/x.md", false},
		{"docs/**/*.md", "docs/x.txt", false},
		{"**/*.md", "x.md", true},
		{"**/*.md", "a/b/x.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"__"+tt.path, func(t *testing.T) {
			got := matchOne(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchOne(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestMatches_CustomAllowlist(t *testing.T) {
	custom := []string{"wiki/**/*.md"}
	if !matches(custom, "wiki/setup/install.md") {
		t.Error("expected custom allowlist to match wiki/setup/install.md")
	}
	if matches(custom, "README.md") {
		t.Error("expected custom allowlist to NOT match README.md (not in custom list)")
	}
}

// TestMatches_OpenspecSpecsStillAvailableAsOptIn covers the "not gone
// forever" half of the DefaultAllowlist bugfix: openspec/specs/** is
// removed from the DEFAULT but must still work when a caller explicitly
// opts back in via New's allowlist parameter.
func TestMatches_OpenspecSpecsStillAvailableAsOptIn(t *testing.T) {
	custom := []string{"openspec/specs/**/*.md"}
	if !matches(custom, "openspec/specs/repodoc/spec.md") {
		t.Error("expected an explicit opt-in allowlist to still match openspec/specs/**")
	}
}

// TestMatches_RecursiveDocsStillAvailableAsOptIn covers the same "not gone
// forever" contract for the docs/** -> docs/*.md narrowing: a caller can
// still get recursive docs ingestion by passing the old pattern explicitly.
func TestMatches_RecursiveDocsStillAvailableAsOptIn(t *testing.T) {
	custom := []string{"docs/**/*.md"}
	if !matches(custom, "docs/a/b/deep.md") {
		t.Error("expected an explicit opt-in allowlist to still match a deeply nested docs/** file")
	}
}

// TestIsExcludedPath covers DefaultExcludedPathSegments' matching contract:
// exact per-segment match (directory name anywhere in the path, or the
// file's own basename with its extension stripped), case-insensitive, NOT
// a substring search.
func TestIsExcludedPath(t *testing.T) {
	tests := []struct {
		name    string
		relPath string
		want    bool
	}{
		{"legacy directory, nested file", "docs/legacy/ARCHITECTURE.md", true},
		{"legacy directory, deeper nesting", "docs/legacy/2024/notes.md", true},
		{"archive directory", "docs/archive/old-plan.md", true},
		{"deprecated directory", "adr/deprecated/0001-old-decision.md", true},
		{"beta directory", "docs/beta/obsidian-brain.md", true},
		{"case-insensitive directory match", "docs/Legacy/ARCHITECTURE.md", true},
		{"file itself named legacy.md", "docs/legacy.md", true},
		{"file itself named BETA.MD, case-insensitive", "docs/BETA.MD", true},
		{"substring in filename does NOT match (avoid over-exclusion)", "docs/BETA_TESTING.md", false},
		{"substring in filename does NOT match, legacy-prefixed", "docs/legacy-format-migration-guide.md", false},
		{"unrelated path", "docs/architecture.md", false},
		{"root file, unrelated", "README.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExcludedPath(tt.relPath, DefaultExcludedPathSegments)
			if got != tt.want {
				t.Errorf("isExcludedPath(%q, DefaultExcludedPathSegments) = %v, want %v", tt.relPath, got, tt.want)
			}
		})
	}
}

// TestIsExcludedPath_EmptyOrCustomList covers the override contract:
// SetExcludedPathSegments(nil) must disable the filter entirely, and a
// custom word list must be honored instead of the default words.
func TestIsExcludedPath_EmptyOrCustomList(t *testing.T) {
	if isExcludedPath("docs/legacy/foo.md", nil) {
		t.Error("expected a nil excluded list to disable the filter (docs/legacy/foo.md should NOT be excluded)")
	}
	custom := []string{"obsolete"}
	if isExcludedPath("docs/legacy/foo.md", custom) {
		t.Error("expected a custom list to NOT fall back to the default words")
	}
	if !isExcludedPath("docs/obsolete/foo.md", custom) {
		t.Error("expected a custom list to exclude its own configured word")
	}
}
