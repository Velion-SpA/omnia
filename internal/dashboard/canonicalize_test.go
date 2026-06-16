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
