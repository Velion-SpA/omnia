package repodoc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/source/repodoc"
)

// stubState is a minimal in-memory core.StateStore, mirroring
// internal/source/github/github_test.go's stubState so cursor behavior is
// tested the same way the rest of the codebase tests it.
type stubState struct {
	cursors map[string]string
}

func newStubState() *stubState { return &stubState{cursors: map[string]string{}} }

func (s *stubState) GetCursor(source, key string) (string, bool) {
	v, ok := s.cursors[source+":"+key]
	return v, ok
}

func (s *stubState) SetCursor(source, key, value string) error {
	s.cursors[source+":"+key] = value
	return nil
}

func (s *stubState) Flush() error { return nil }

// fakeSink simulates Engram's topic_key upsert semantics (the property the
// package's TopicKey scheme relies on) so the idempotent-reingest test can
// assert "zero duplicates" the same way a real sink would: last write per
// topic_key wins, keyed by (project, topic_key).
type fakeSink struct {
	byTopicKey map[string]core.Item
	writes     int // total Write calls, including revisions — separate from len(byTopicKey)
}

func newFakeSink() *fakeSink { return &fakeSink{byTopicKey: map[string]core.Item{}} }

func (f *fakeSink) apply(items []core.Item) {
	for _, item := range items {
		f.writes++
		f.byTopicKey[item.Project+"|"+item.TopicKey] = item
	}
}

// requireGit skips the test if git is not on PATH — anchor-block tests need
// a real git repository; the package itself degrades gracefully without
// git (see TestFetch_WorksWithoutGitRepo), so this skip is a test-harness
// concern, not a package limitation.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initGitRepo creates a git repository at dir with a committer identity set
// (required for `git commit` to succeed in a bare CI environment with no
// global user.name/user.email configured).
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", msg)

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const readmeV1 = `# Fixture Project

## Overview

This project does a thing.

## Non-goals

Not a database.
`

const docsAV1 = `# Guide

## Setup

Run the installer.
`

func TestFetch_BasicIngestAndAllowlistScope(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "docs/a.md", docsAV1)
	writeFile(t, dir, "docs/notes.txt", "not markdown, must be excluded")
	writeFile(t, dir, "src/main.go", "package main\n\nfunc main() {}\n")
	commitAll(t, dir, "initial")

	src := repodoc.New(dir, "testproj", nil, newStubState())
	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// README.md: preamble("Fixture Project" H1 has no body before Overview,
	// so no preamble chunk) + Overview + Non-goals = 2 sections.
	// docs/a.md: Guide(no body) + Setup = ... let's just assert on totals
	// and content instead of a brittle exact count.
	if len(items) == 0 {
		t.Fatal("expected at least one item")
	}

	for _, item := range items {
		if item.Type != "doc" {
			t.Errorf("item %q Type = %q, want %q", item.Title, item.Type, "doc")
		}
		if item.Source != "ingest:doc" {
			t.Errorf("item %q Source = %q, want %q", item.Title, item.Source, "ingest:doc")
		}
		if item.Project != "testproj" {
			t.Errorf("item %q Project = %q, want %q", item.Title, item.Project, "testproj")
		}
		if strings.Contains(item.Content, "package main") {
			t.Errorf("item %q leaked source code content from an out-of-scope file", item.Title)
		}
		if !strings.HasPrefix(item.TopicKey, "repodoc:testproj:") {
			t.Errorf("item %q TopicKey = %q, want prefix %q", item.Title, item.TopicKey, "repodoc:testproj:")
		}
	}

	// Every emitted TopicKey must reference an allowlisted file only.
	for _, item := range items {
		if strings.Contains(item.TopicKey, "notes.txt") || strings.Contains(item.TopicKey, "main.go") {
			t.Errorf("out-of-scope file leaked into TopicKey: %q", item.TopicKey)
		}
	}
}

func TestFetch_IdempotentReingest_NoDuplicateTopicKeys(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", readmeV1)
	commitAll(t, dir, "initial")

	state := newStubState()
	src := repodoc.New(dir, "testproj", nil, state)
	sink := newFakeSink()

	// Run 1: full ingest.
	items1, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch run1: %v", err)
	}
	if len(items1) == 0 {
		t.Fatal("expected items on first run")
	}
	sink.apply(items1)
	keysAfterRun1 := sink.writes
	distinctAfterRun1 := len(sink.byTopicKey)
	if keysAfterRun1 != distinctAfterRun1 {
		t.Fatalf("run1 already produced duplicate topic keys within a single Fetch: %d writes, %d distinct", keysAfterRun1, distinctAfterRun1)
	}

	// Run 2: nothing changed — must be a true no-op (cursor hit).
	items2, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch run2: %v", err)
	}
	if len(items2) != 0 {
		t.Fatalf("expected 0 items on unchanged re-run, got %d", len(items2))
	}

	// Mutate the file's existing "Overview" section body (heading structure
	// unchanged) and re-ingest.
	mutated := strings.Replace(readmeV1, "This project does a thing.", "This project does an UPDATED thing.", 1)
	writeFile(t, dir, "README.md", mutated)
	commitAll(t, dir, "update overview")

	items3, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch run3: %v", err)
	}
	if len(items3) == 0 {
		t.Fatal("expected items after mutating the file")
	}
	sink.apply(items3)

	// The whole point of the TopicKey scheme: re-ingesting an unchanged
	// heading structure must UPSERT the same keys, never add new ones.
	if len(sink.byTopicKey) != distinctAfterRun1 {
		t.Errorf("distinct topic keys changed after a same-structure mutation: run1=%d, after mutation=%d (keys: %v)",
			distinctAfterRun1, len(sink.byTopicKey), sortedKeys(sink.byTopicKey))
	}

	// And the revised content must actually be the new content (proves
	// "upsert", not "silently ignored").
	var found bool
	for _, item := range sink.byTopicKey {
		if strings.Contains(item.Content, "UPDATED thing") {
			found = true
		}
	}
	if !found {
		t.Error("expected the revised chunk's updated content to be present in the sink after re-ingest")
	}
}

func sortedKeys(m map[string]core.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestFetch_MutatingOneFileDoesNotReingestOthers(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "docs/a.md", docsAV1)
	commitAll(t, dir, "initial")

	state := newStubState()
	src := repodoc.New(dir, "testproj", nil, state)

	if _, err := src.Fetch(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Fetch run1: %v", err)
	}

	writeFile(t, dir, "docs/a.md", strings.Replace(docsAV1, "Run the installer.", "Run the new installer.", 1))
	commitAll(t, dir, "update docs/a.md only")

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch run2: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected items for the changed file")
	}
	for _, item := range items {
		if strings.Contains(item.TopicKey, "readme.md") || strings.Contains(strings.ToLower(item.TopicKey), "repodoc:testproj:readme") {
			t.Errorf("unchanged README.md was re-ingested: %q", item.TopicKey)
		}
	}
}

func TestPreview_DoesNotWriteStateAndMatchesFetchCounts(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", readmeV1)
	commitAll(t, dir, "initial")

	state := newStubState()
	src := repodoc.New(dir, "testproj", nil, state)

	if _, ok := state.GetCursor("repodoc", "README.md"); ok {
		t.Fatal("cursor should not exist before any run")
	}

	stats, err := src.Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if stats.FilesMatched != 1 {
		t.Errorf("FilesMatched = %d, want 1", stats.FilesMatched)
	}
	if stats.FilesChanged != 1 {
		t.Errorf("FilesChanged = %d, want 1", stats.FilesChanged)
	}
	if stats.ChunksTotal == 0 {
		t.Error("ChunksTotal = 0, want > 0")
	}

	// Preview must be side-effect-free: no cursor written.
	if _, ok := state.GetCursor("repodoc", "README.md"); ok {
		t.Error("Preview wrote a state cursor — it must be a dry-run")
	}

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != stats.ChunksTotal {
		t.Errorf("Preview.ChunksTotal = %d, actual Fetch produced %d items", stats.ChunksTotal, len(items))
	}
}

func TestFetch_AnchorBlockCarriesRealGitBlame(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", readmeV1)
	sha := commitAll(t, dir, "initial")

	src := repodoc.New(dir, "testproj", nil, newStubState())
	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected items")
	}

	var sawAnchor bool
	for _, item := range items {
		a, ok := repodoc.ParseAnchorBlock(item.Content)
		if !ok {
			continue
		}
		sawAnchor = true
		if a.File != "README.md" {
			t.Errorf("anchor File = %q, want %q", a.File, "README.md")
		}
		if a.BlameSHA != sha {
			t.Errorf("anchor BlameSHA = %q, want %q (the commit SHA)", a.BlameSHA, sha)
		}
		if a.LineStart < 1 || a.LineEnd < a.LineStart {
			t.Errorf("anchor line range invalid: [%d,%d]", a.LineStart, a.LineEnd)
		}
		if a.ContentHash == "" {
			t.Error("anchor ContentHash must not be empty")
		}
	}
	if !sawAnchor {
		t.Error("expected at least one item to carry a repodoc-anchor block")
	}
}

func TestFetch_WorksWithoutGitRepo(t *testing.T) {
	// No git init at all — the package must still ingest (cursor is pure
	// Go; anchor capture just degrades to "no anchor block" per
	// internal/anchor's own graceful-degradation doctrine).
	dir := t.TempDir()
	writeFile(t, dir, "README.md", readmeV1)

	src := repodoc.New(dir, "testproj", nil, newStubState())
	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch should not fail outside a git repo: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected items even without a git repository")
	}
	for _, item := range items {
		if _, ok := repodoc.ParseAnchorBlock(item.Content); ok {
			t.Errorf("expected no anchor block without a git repo, but found one on %q", item.Title)
		}
	}
}

func TestFetch_DocsDefaultIsNonRecursive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "docs/top-level.md", "# Top Level\n\nAt docs/ root.\n")
	writeFile(t, dir, "docs/nested/deep.md", "# Deep\n\nNested under docs/.\n")

	src := repodoc.New(dir, "testproj", nil, newStubState())
	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var sawTopLevel, sawNested bool
	for _, item := range items {
		if strings.Contains(item.TopicKey, "docs/top-level.md") {
			sawTopLevel = true
		}
		if strings.Contains(item.TopicKey, "docs/nested/deep.md") {
			sawNested = true
		}
	}
	if !sawTopLevel {
		t.Error("expected docs/top-level.md (docs/*.md) to be ingested by default")
	}
	if sawNested {
		t.Error("expected docs/nested/deep.md to be EXCLUDED by default (docs/** narrowed to docs/*.md)")
	}
}

func TestFetch_ExcludesLegacyArchiveDeprecatedBetaBySegment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "docs/legacy/old-architecture.md", "# Old Architecture\n\nStale.\n")
	writeFile(t, dir, "docs/archive/2020-plan.md", "# 2020 Plan\n\nStale.\n")
	writeFile(t, dir, "docs/deprecated/removed-api.md", "# Removed API\n\nStale.\n")
	writeFile(t, dir, "docs/beta/experimental.md", "# Experimental\n\nStale.\n")
	writeFile(t, dir, "docs/current.md", "# Current\n\nStill accurate.\n")

	// Use an explicit recursive allowlist override (docs/**/*.md) to prove
	// the exclusion is independent of the allowlist, not merely a side
	// effect of the non-recursive default (allowlist.go's
	// DefaultExcludedPathSegments doc comment: "applied to EVERY allowlist,
	// including a caller-supplied override, and including an explicit
	// opt-in into recursive scope").
	src := repodoc.New(dir, "testproj", []string{"README*", "docs/**/*.md"}, newStubState())
	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, item := range items {
		for _, stale := range []string{"legacy", "archive", "deprecated", "beta"} {
			if strings.Contains(strings.ToLower(item.TopicKey), "docs/"+stale+"/") {
				t.Errorf("item %q leaked from an excluded %q path segment despite a recursive allowlist override", item.TopicKey, stale)
			}
		}
	}

	var sawCurrent bool
	for _, item := range items {
		if strings.Contains(item.TopicKey, "docs/current.md") {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Error("expected docs/current.md (not excluded) to still be ingested under the recursive override")
	}
}

func TestFetch_ExcludedPathSegmentsOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "docs/legacy/old.md", "# Old\n\nExplicitly re-enabled by the caller.\n")

	src := repodoc.New(dir, "testproj", []string{"README*", "docs/**/*.md"}, newStubState())
	src.SetExcludedPathSegments(nil) // explicit opt-out, see SetExcludedPathSegments' doc comment

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var sawLegacy bool
	for _, item := range items {
		if strings.Contains(item.TopicKey, "docs/legacy/old.md") {
			sawLegacy = true
		}
	}
	if !sawLegacy {
		t.Error("expected SetExcludedPathSegments(nil) to re-enable docs/legacy/old.md")
	}
}

func TestFetch_CustomAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", readmeV1)
	writeFile(t, dir, "wiki/setup.md", "# Setup\n\n## Install\n\nRun make.\n")

	// Default allowlist excludes wiki/**.
	def := repodoc.New(dir, "testproj", nil, newStubState())
	items, err := def.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.Contains(item.TopicKey, "wiki") {
			t.Errorf("default allowlist should not include wiki/**, got %q", item.TopicKey)
		}
	}

	// Custom allowlist scoped to wiki/** only.
	custom := repodoc.New(dir, "testproj", []string{"wiki/**/*.md"}, newStubState())
	items2, err := custom.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) == 0 {
		t.Fatal("expected items from custom wiki allowlist")
	}
	for _, item := range items2 {
		if strings.Contains(item.TopicKey, "readme") || strings.Contains(strings.ToLower(item.TopicKey), ":readme") {
			t.Errorf("custom allowlist should not include README.md, got %q", item.TopicKey)
		}
	}
}
