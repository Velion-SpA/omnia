package cloudstore

import (
	"reflect"
	"testing"
)

func TestSuggestProjectParents_SimplePrefixMatch(t *testing.T) {
	projects := []string{"workly", "workly-marketing"}
	got := SuggestProjectParents(projects, nil)
	want := map[string]string{"workly-marketing": "workly"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSuggestProjectParents_PicksLongestPrefixWhenPresent(t *testing.T) {
	projects := []string{"workly", "workly-marketing", "workly-marketing-videos"}
	got := SuggestProjectParents(projects, nil)
	if got["workly-marketing-videos"] != "workly-marketing" {
		t.Fatalf("expected workly-marketing-videos -> workly-marketing, got %+v", got)
	}
	if got["workly-marketing"] != "workly" {
		t.Fatalf("expected workly-marketing -> workly, got %+v", got)
	}
}

func TestSuggestProjectParents_FallsBackToShorterPrefixWhenLongerAbsent(t *testing.T) {
	// "workly-marketing" is NOT in the known-projects list, so the only
	// candidate parent for "workly-marketing-videos" is "workly".
	projects := []string{"workly", "workly-marketing-videos"}
	got := SuggestProjectParents(projects, nil)
	if got["workly-marketing-videos"] != "workly" {
		t.Fatalf("expected fallback to workly, got %+v", got)
	}
}

func TestSuggestProjectParents_NoSuggestionWithoutPrefixMatch(t *testing.T) {
	projects := []string{"velion", "trackly"}
	got := SuggestProjectParents(projects, nil)
	if len(got) != 0 {
		t.Fatalf("expected no suggestions, got %+v", got)
	}
}

func TestSuggestProjectParents_SkipsAlreadyLinkedChild(t *testing.T) {
	projects := []string{"workly", "workly-marketing"}
	existing := map[string]string{"workly-marketing": "workly"}
	got := SuggestProjectParents(projects, existing)
	if len(got) != 0 {
		t.Fatalf("expected no suggestion for an already-linked project, got %+v", got)
	}
}

func TestSuggestProjectParents_SkipsProjectThatIsItselfAParent(t *testing.T) {
	// "workly" is already a parent (of "workly-marketing"). Even though
	// "workly-videos" looks like a sub-project of "workly", "workly" itself
	// must never be suggested to become a CHILD of anything (2-level model).
	projects := []string{"workly", "workly-marketing", "workly-videos", "velion"}
	existing := map[string]string{"workly-marketing": "workly"}
	got := SuggestProjectParents(projects, existing)
	if _, ok := got["workly"]; ok {
		t.Fatalf("workly must never be suggested as a child, got %+v", got)
	}
	if got["workly-videos"] != "workly" {
		t.Fatalf("expected workly-videos -> workly, got %+v", got)
	}
}

func TestSuggestProjectParents_QMustNotAlreadyBeAChild(t *testing.T) {
	// "workly-marketing" is itself a linked child (of "velion", say) — it is
	// not a valid PARENT candidate for "workly-marketing-videos" even though
	// it is the longest string match. Only "workly" remains valid.
	projects := []string{"workly", "workly-marketing", "workly-marketing-videos"}
	existing := map[string]string{"workly-marketing": "velion"}
	got := SuggestProjectParents(projects, existing)
	if got["workly-marketing-videos"] != "workly" {
		t.Fatalf("expected fallback to workly (workly-marketing disqualified as a child), got %+v", got)
	}
}

func TestSuggestProjectParents_CaseInsensitiveMatch(t *testing.T) {
	projects := []string{"Workly", "workly-marketing"}
	got := SuggestProjectParents(projects, nil)
	if got["workly-marketing"] != "Workly" {
		t.Fatalf("expected case-insensitive match to Workly, got %+v", got)
	}
}

func TestSuggestProjectParents_RequiresSeparatorBoundary(t *testing.T) {
	// "workly2" is NOT "workly" + separator + text — no separator between
	// "workly" and "2", so it must not be suggested as a sub-project.
	projects := []string{"workly", "workly2"}
	got := SuggestProjectParents(projects, nil)
	if _, ok := got["workly2"]; ok {
		t.Fatalf("expected no suggestion without a separator boundary, got %+v", got)
	}
}

func TestSuggestProjectParents_EmptyInputsYieldEmptyMap(t *testing.T) {
	got := SuggestProjectParents(nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected empty map for empty input, got %+v", got)
	}
}
