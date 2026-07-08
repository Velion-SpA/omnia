package dashboard

import (
	"log/slog"
	"testing"
)

// silentLogger returns a discard logger for use in tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, nil))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- newGroupIndex ---

func TestNewGroupIndex_Empty(t *testing.T) {
	g := newGroupIndex(nil, nil)
	if len(g.parents) != 0 || len(g.childOf) != 0 {
		t.Fatal("expected empty GroupIndex for nil input")
	}
}

func TestNewGroupIndex_Basic(t *testing.T) {
	raw := map[string][]string{
		"velion":  {"velion-web", "velionflow"},
		"workly":  {"workly-marketing"},
	}
	g := newGroupIndex(raw, silentLogger())

	if !g.IsParent("velion") {
		t.Error("velion should be a parent")
	}
	if !g.IsParent("workly") {
		t.Error("workly should be a parent")
	}
	if !g.IsChild("velion-web") {
		t.Error("velion-web should be a child")
	}
	if !g.IsChild("velionflow") {
		t.Error("velionflow should be a child")
	}
	if !g.IsChild("workly-marketing") {
		t.Error("workly-marketing should be a child")
	}
	if g.IsParent("velion-web") {
		t.Error("velion-web should NOT be a parent")
	}
	if g.IsChild("velion") {
		t.Error("velion should NOT be a child")
	}
}

func TestNewGroupIndex_SelfReferential(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion", "velion-web"},
	}
	g := newGroupIndex(raw, silentLogger())

	// Self-referential "velion" child must be dropped.
	if g.IsChild("velion") {
		t.Error("self-referential entry should be ignored: velion must not be a child of itself")
	}
	// The valid child "velion-web" must still be registered.
	if !g.IsChild("velion-web") {
		t.Error("velion-web should still be a child after filtering self-referential entry")
	}
}

func TestNewGroupIndex_ChildIsAlsoParent(t *testing.T) {
	raw := map[string][]string{
		"velion":    {"velion-web", "workly"}, // "workly" is also a parent key
		"workly":    {"workly-marketing"},
	}
	g := newGroupIndex(raw, silentLogger())

	// "workly" can't be both a parent and a child.
	if g.IsChild("workly") {
		t.Error("workly is also a parent key; it must not be registered as a child")
	}
	if !g.IsParent("workly") {
		t.Error("workly should remain a parent")
	}
	if !g.IsChild("workly-marketing") {
		t.Error("workly-marketing should be a child of workly")
	}
	// velion-web is still a valid child of velion.
	if !g.IsChild("velion-web") {
		t.Error("velion-web should be a child of velion")
	}
}

func TestNewGroupIndex_ChildWithMultipleParents(t *testing.T) {
	raw := map[string][]string{
		"velion":  {"shared-lib"},
		"workly":  {"shared-lib"}, // duplicate child — second parent must be rejected
	}
	g := newGroupIndex(raw, silentLogger())

	// shared-lib must have exactly one parent.
	if !g.IsChild("shared-lib") {
		t.Error("shared-lib should be registered as a child")
	}
	parent := g.ParentOf("shared-lib")
	if parent == "" {
		t.Fatal("shared-lib should have a parent")
	}
	// Whichever parent was registered first, only one should exist.
	// The rejected parent must not have shared-lib in its children list.
	if parent != "velion" && parent != "workly" {
		t.Errorf("unexpected parent %q for shared-lib", parent)
	}
	otherParent := "workly"
	if parent == "workly" {
		otherParent = "velion"
	}
	for _, child := range g.Children(otherParent) {
		if child == "shared-lib" {
			t.Errorf("shared-lib should not appear in %q children (already assigned to %q)", otherParent, parent)
		}
	}
}

// --- IsParent / IsChild / Children / ParentOf ---

func TestGroupIndex_Accessors(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion-web", "velionflow"},
	}
	g := newGroupIndex(raw, nil)

	children := g.Children("velion")
	if len(children) != 2 {
		t.Errorf("expected 2 children for velion, got %d", len(children))
	}

	if got := g.ParentOf("velion-web"); got != "velion" {
		t.Errorf("ParentOf(velion-web): want velion, got %q", got)
	}
	if got := g.ParentOf("velionflow"); got != "velion" {
		t.Errorf("ParentOf(velionflow): want velion, got %q", got)
	}
	if got := g.ParentOf("unknown"); got != "" {
		t.Errorf("ParentOf(unknown): want \"\", got %q", got)
	}
}

func TestGroupIndex_NilReceiver(t *testing.T) {
	var g *GroupIndex
	if g.IsParent("x") {
		t.Error("nil GroupIndex.IsParent should return false")
	}
	if g.IsChild("x") {
		t.Error("nil GroupIndex.IsChild should return false")
	}
	if got := g.Children("x"); got != nil {
		t.Error("nil GroupIndex.Children should return nil")
	}
	if got := g.ParentOf("x"); got != "" {
		t.Error("nil GroupIndex.ParentOf should return empty string")
	}
}

// --- groupRawNames / coreRawNames ---

func TestGroupIndex_GroupRawNames(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion-web"},
	}
	g := newGroupIndex(raw, nil)

	aliases := map[string]string{
		"01.- velion": "velion",
		"velion-web":  "velion-web",
	}
	rawAll := []string{"01.- velion", "velion-web", "omnia", "workly"}

	got := g.groupRawNames("velion", rawAll, aliases)
	want := map[string]bool{"01.- velion": true, "velion-web": true}

	if len(got) != len(want) {
		t.Errorf("groupRawNames: want %d names, got %d: %v", len(want), len(got), got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("groupRawNames: unexpected name %q", name)
		}
	}
}

func TestGroupIndex_CoreRawNames(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion-web"},
	}
	g := newGroupIndex(raw, nil)

	aliases := map[string]string{
		"01.- velion": "velion",
	}
	rawAll := []string{"01.- velion", "velion-web", "omnia"}

	got := g.coreRawNames("velion", rawAll, aliases)
	if len(got) != 1 || got[0] != "01.- velion" {
		t.Errorf("coreRawNames: want [\"01.- velion\"], got %v", got)
	}
}

// --- filterGroupChildren ---

func TestFilterGroupChildren(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion-web", "velionflow"},
		"workly": {"workly-marketing"},
	}
	g := newGroupIndex(raw, nil)

	input := []string{"omnia", "velion", "velion-web", "velionflow", "workly", "workly-marketing", "saluvita"}
	got := filterGroupChildren(input, g)

	want := map[string]bool{"omnia": true, "velion": true, "workly": true, "saluvita": true}
	if len(got) != len(want) {
		t.Errorf("filterGroupChildren: want %d names, got %d: %v", len(want), len(got), got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("filterGroupChildren: unexpected name %q in output", name)
		}
	}
}

func TestFilterGroupChildren_NilIndex(t *testing.T) {
	input := []string{"a", "b", "c"}
	got := filterGroupChildren(input, nil)
	if len(got) != len(input) {
		t.Errorf("filterGroupChildren(nil): want input unchanged, got %v", got)
	}
}

// --- computeGroupProjectStats ---

func TestComputeGroupProjectStats(t *testing.T) {
	raw := map[string][]string{
		"velion": {"velion-web"},
	}
	g := newGroupIndex(raw, nil)

	views := []ObsView{
		{Obs: Observation{ID: 1, Project: "velion"}},
		{Obs: Observation{ID: 2, Project: "velion"}},
		{Obs: Observation{ID: 3, Project: "velion-web"}},
		{Obs: Observation{ID: 4, Project: "01.- velion"}},
	}

	aliases := map[string]string{
		"01.- velion": "velion",
	}

	stats := computeGroupProjectStats("velion", views, g, aliases)

	if !stats.IsGroup {
		t.Error("IsGroup should be true")
	}
	if stats.Total != 4 {
		t.Errorf("Total: want 4, got %d", stats.Total)
	}
	// CoreCount: obs whose project canonicalizes to "velion" (IDs 1, 2, 4 = 3)
	if stats.CoreCount != 3 {
		t.Errorf("CoreCount: want 3, got %d", stats.CoreCount)
	}
	if len(stats.SubProjects) != 1 {
		t.Fatalf("SubProjects: want 1, got %d", len(stats.SubProjects))
	}
	sp := stats.SubProjects[0]
	if sp.Name != "velion-web" {
		t.Errorf("SubProject name: want velion-web, got %q", sp.Name)
	}
	if sp.Count != 1 {
		t.Errorf("SubProject count: want 1, got %d", sp.Count)
	}
}

// --- dedupeViews ---

func TestDedupeViews(t *testing.T) {
	views := []ObsView{
		{Obs: Observation{ID: 1}},
		{Obs: Observation{ID: 2}},
		{Obs: Observation{ID: 1}}, // duplicate
		{Obs: Observation{ID: 3}},
		{Obs: Observation{ID: 2}}, // duplicate
	}
	got := dedupeViews(views)
	if len(got) != 3 {
		t.Errorf("dedupeViews: want 3, got %d", len(got))
	}
	seen := map[int]bool{}
	for _, v := range got {
		if seen[v.Obs.ID] {
			t.Errorf("dedupeViews: duplicate ID %d in output", v.Obs.ID)
		}
		seen[v.Obs.ID] = true
	}
}

func TestDedupeViews_Empty(t *testing.T) {
	got := dedupeViews(nil)
	if len(got) != 0 {
		t.Errorf("dedupeViews(nil): want 0, got %d", len(got))
	}
}
