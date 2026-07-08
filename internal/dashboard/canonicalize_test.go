package dashboard

import (
	"testing"
)

func TestCaseFoldKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Homelab", "homelab"},
		{"HOMELAB", "homelab"},
		{"homelab", "homelab"},
		{"  Homelab  ", "homelab"},
		{"velion-web", "velion-web"},
	}
	for _, c := range cases {
		if got := caseFoldKey(c.in); got != c.want {
			t.Errorf("caseFoldKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalizeProject_AliasOverride(t *testing.T) {
	aliases := map[string]string{"NUDGE Sistema": "nudge"}
	if got := canonicalizeProject("NUDGE Sistema", aliases); got != "nudge" {
		t.Errorf("alias override: got %q, want %q", got, "nudge")
	}
	// Non-aliased names fall through to case-fold
	if got := canonicalizeProject("Homelab", aliases); got != "homelab" {
		t.Errorf("case-fold fallback: got %q, want %q", got, "homelab")
	}
}

func TestCanonicalizeProject_VelionDistinct(t *testing.T) {
	// velion and velion-web must NOT collapse to the same canonical.
	a := canonicalizeProject("velion", nil)
	b := canonicalizeProject("velion-web", nil)
	if a == b {
		t.Errorf("velion and velion-web must not collapse: both got %q", a)
	}
}

func TestHiddenSet_CanonicalizeEntries(t *testing.T) {
	hidden := hiddenSet([]string{"Homelab", "WORKLY"}, nil)
	if _, ok := hidden["homelab"]; !ok {
		t.Error("hidden set should contain 'homelab' (canonicalized from 'Homelab')")
	}
	if _, ok := hidden["workly"]; !ok {
		t.Error("hidden set should contain 'workly' (canonicalized from 'WORKLY')")
	}
}

func TestFilterHidden_RemovesHidden(t *testing.T) {
	hidden := map[string]struct{}{"omnia-test": {}, "ea1": {}}
	names := []string{"homelab", "omnia", "omnia-test", "workly", "ea1"}
	got := filterHidden(names, hidden)
	want := []string{"homelab", "omnia", "workly"}
	if len(got) != len(want) {
		t.Fatalf("filterHidden: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("index %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestCanonicalizerFunc_MatchesCanonicalizeProject(t *testing.T) {
	aliases := map[string]string{"foo": "bar"}
	f := canonicalizerFunc(aliases)
	if got := f("foo"); got != "bar" {
		t.Errorf("canonicalizerFunc: alias: got %q, want 'bar'", got)
	}
	if got := f("Baz"); got != "baz" {
		t.Errorf("canonicalizerFunc: case-fold: got %q, want 'baz'", got)
	}
}

// TestCanonicalizeProject_CaseFoldAliasLookup verifies that a raw name whose
// case-fold form appears in the alias map resolves correctly even when the exact
// raw name is not an alias map key. This covers the "01.- Velion" → "velion"
// case where only "01.- velion" (lowercase) is stored in the map.
func TestCanonicalizeProject_CaseFoldAliasLookup(t *testing.T) {
	aliases := map[string]string{
		"01.- velion": "velion",
		"velion":      "velion",
	}
	cases := []struct{ in, want string }{
		// Exact alias key hit
		{"01.- velion", "velion"},
		{"velion", "velion"},
		// Case-fold alias hit: "01.- Velion" → caseFold → "01.- velion" → alias → "velion"
		{"01.- Velion", "velion"},
		{"01.- VELION", "velion"},
		// Non-aliased: falls through to case-fold
		{"Homelab", "homelab"},
		{"HOMELAB", "homelab"},
		// Must NOT collapse velion-web into velion
		{"velion-web", "velion-web"},
	}
	for _, c := range cases {
		got := canonicalizeProject(c.in, aliases)
		if got != c.want {
			t.Errorf("canonicalizeProject(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRawProjectsForCanonical_AliasExpansion verifies that rawProjectsForCanonical
// correctly collects all raw DB names for a canonical, including case-fold variants
// and alias-only names, while excluding structurally distinct projects.
func TestRawProjectsForCanonical_AliasExpansion(t *testing.T) {
	aliases := map[string]string{
		"01.- velion": "velion",
		"velion":      "velion",
		"Velion":      "velion",
	}
	rawNames := []string{
		"01.- velion", "01.- Velion", // alias targets via case-fold lookup
		"velion", "Velion", // direct alias hits
		"velion-web",         // must NOT be pulled in
		"homelab",            // unrelated
	}

	got := rawProjectsForCanonical("velion", rawNames, aliases)
	wantSet := map[string]bool{
		"01.- velion": true,
		"01.- Velion": true,
		"velion":      true,
		"Velion":      true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("rawProjectsForCanonical(velion): got %v (len=%d), want %v", got, len(got), wantSet)
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("unexpected name %q in velion expansion", name)
		}
	}

	// velion-web must resolve only to itself
	gotWeb := rawProjectsForCanonical("velion-web", rawNames, aliases)
	if len(gotWeb) != 1 || gotWeb[0] != "velion-web" {
		t.Errorf("rawProjectsForCanonical(velion-web): got %v, want [velion-web]", gotWeb)
	}
}

// TestRawProjectsForCanonical_HiddenNotExpanded verifies that hidden projects
// still appear in rawProjectsForCanonical (they are excluded at a higher layer
// by filterHidden after canonicalization — not by this function).
func TestRawProjectsForCanonical_NonAliasedFallsToSelf(t *testing.T) {
	rawNames := []string{"homelab", "Homelab", "HOMELAB", "workly"}
	// No aliases — homelab variants all case-fold to "homelab"
	got := rawProjectsForCanonical("homelab", rawNames, nil)
	wantSet := map[string]bool{
		"homelab": true, "Homelab": true, "HOMELAB": true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("rawProjectsForCanonical(homelab, nil): got %v, want %v", got, wantSet)
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("unexpected %q in homelab expansion", name)
		}
	}
}
