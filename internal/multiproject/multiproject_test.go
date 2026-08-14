package multiproject

import (
	"math"
	"reflect"
	"sort"
	"testing"
)

// idOrder extracts the ID sequence from a MergedItem slice, the shape most
// of these tests assert on: Merge's whole job is producing the right ORDER,
// not new data, so comparing ID sequences is the most direct assertion.
func idOrder(items []MergedItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// TestMerge_SingleProject_MatchesNoNormalizationOrder is the degenerate case
// called out explicitly in the P5 work item: with exactly one project in
// play, min-max normalizing within that one group and merging must produce
// the byte-for-byte same order as not normalizing at all, because min-max
// rescaling is monotonic — there is nothing to normalize AGAINST with only
// one group. This asserts that directly rather than trusting the
// monotonicity argument.
func TestMerge_SingleProject_MatchesNoNormalizationOrder(t *testing.T) {
	items := []Item{
		{ID: 1, Relevance: 3.2, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 2, Relevance: 9.9, UpdatedAt: "2026-08-02T00:00:00Z"},
		{ID: 3, Relevance: 1.0, UpdatedAt: "2026-08-03T00:00:00Z"},
		{ID: 4, Relevance: 7.5, UpdatedAt: "2026-07-30T00:00:00Z"},
		{ID: 5, Relevance: 9.9, UpdatedAt: "2026-08-04T00:00:00Z"}, // ties item 2 on raw relevance
	}

	// Expected order without any normalization: sort by raw Relevance DESC,
	// UpdatedAt DESC, ID ASC — the same tie-break Merge documents.
	want := make([]Item, len(items))
	copy(want, items)
	sort.SliceStable(want, func(i, j int) bool {
		if want[i].Relevance != want[j].Relevance {
			return want[i].Relevance > want[j].Relevance
		}
		if want[i].UpdatedAt != want[j].UpdatedAt {
			return want[i].UpdatedAt > want[j].UpdatedAt
		}
		return want[i].ID < want[j].ID
	})
	wantIDs := make([]int64, len(want))
	for i, it := range want {
		wantIDs[i] = it.ID
	}

	got := Merge([]Group{{Project: "solo", Items: items}})
	if diff := !reflect.DeepEqual(idOrder(got), wantIDs); diff {
		t.Fatalf("single-project Merge order = %v, want %v (no-normalization order)", idOrder(got), wantIDs)
	}
}

// TestMerge_MultiProject_NormalizationPreventsDomination is the whole point
// of the package: a dominant project's absolute score scale must not bury a
// quiet project's single relevant result. projectA has five results with
// high absolute relevance; projectB has exactly one result with much lower
// absolute relevance but is, within its own project, the best possible
// match (min==max -> normalizes to 1.0, tying projectA's best result).
func TestMerge_MultiProject_NormalizationPreventsDomination(t *testing.T) {
	groups := []Group{
		{
			Project: "active-project",
			Items: []Item{
				{ID: 101, Relevance: 100, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 102, Relevance: 90, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 103, Relevance: 80, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 104, Relevance: 70, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 105, Relevance: 60, UpdatedAt: "2026-08-01T00:00:00Z"},
			},
		},
		{
			Project: "quiet-project",
			Items: []Item{
				{ID: 201, Relevance: 5, UpdatedAt: "2026-08-05T00:00:00Z"}, // most recent -> wins the 1.0 tie
			},
		},
	}

	got := Merge(groups)

	if len(got) != 6 {
		t.Fatalf("len(got) = %d, want 6", len(got))
	}
	// quiet-project's only result normalizes to 1.0 (min==max) and ties
	// active-project's best (also 1.0 after normalization): the tie-break
	// is UpdatedAt DESC, and quiet-project's row is more recent, so it must
	// rank first despite an absolute relevance of 5 vs 100.
	if got[0].ID != 201 || got[0].Project != "quiet-project" {
		t.Fatalf("got[0] = {ID:%d Project:%s}, want the quiet project's single result to win the normalized tie", got[0].ID, got[0].Project)
	}
	if got[0].NormalizedScore != 1.0 {
		t.Fatalf("got[0].NormalizedScore = %v, want 1.0", got[0].NormalizedScore)
	}
	if got[1].ID != 101 {
		t.Fatalf("got[1].ID = %d, want 101 (active-project's own best, also normalized to 1.0)", got[1].ID)
	}

	// Diversity in the top-2 must now be 2 distinct projects — this is
	// exactly the plan's "project diversity >= 2 in top-4" gate, satisfied
	// here in the top-2 because normalization, not luck, put the quiet
	// project's result in contention.
	div := Diversity(got, 2)
	if div.Distinct != 2 {
		t.Fatalf("Diversity(top 2).Distinct = %d, want 2; counts=%v", div.Distinct, div.Counts)
	}
}

// TestMerge_PreemptionExcludedFromGroupNormalization asserts the rule the
// task called out as "the single easiest thing to get wrong": a preempted
// row's relevance (an outlier by construction — a topic_key exact-match
// sentinel or a signature-match rank, not a real bm25/RRF score) must never
// enter its group's min-max batch, or it would pin the group's max and
// crush every ordinary row toward 0.
func TestMerge_PreemptionExcludedFromGroupNormalization(t *testing.T) {
	normal := []Item{
		{ID: 2, Relevance: 10, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 3, Relevance: 5, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 4, Relevance: 0, UpdatedAt: "2026-08-01T00:00:00Z"},
	}
	preempted := Item{ID: 1, Relevance: 999999, UpdatedAt: "2026-07-01T00:00:00Z", Preempt: true}

	withPreempt := append([]Item{preempted}, normal...)
	got := Merge([]Group{{Project: "p", Items: withPreempt}})

	// The preempted row must lead, with NormalizedScore 1.0, regardless of
	// its outlandish raw relevance.
	if got[0].ID != 1 || got[0].NormalizedScore != 1.0 {
		t.Fatalf("got[0] = {ID:%d Score:%v}, want the preempted row first with score 1.0", got[0].ID, got[0].NormalizedScore)
	}

	// The normal rows' normalization must be IDENTICAL to what it would be
	// if the outlier had never been in the group at all.
	wantOnlyNormal := Merge([]Group{{Project: "p", Items: normal}})
	gotNormalOnly := got[1:]
	if len(gotNormalOnly) != len(wantOnlyNormal) {
		t.Fatalf("len(normal rows) = %d, want %d", len(gotNormalOnly), len(wantOnlyNormal))
	}
	for i := range wantOnlyNormal {
		if gotNormalOnly[i].ID != wantOnlyNormal[i].ID || gotNormalOnly[i].NormalizedScore != wantOnlyNormal[i].NormalizedScore {
			t.Fatalf("normal row %d = {ID:%d Score:%v}, want {ID:%d Score:%v} (outlier must not affect normal-row normalization)",
				i, gotNormalOnly[i].ID, gotNormalOnly[i].NormalizedScore, wantOnlyNormal[i].ID, wantOnlyNormal[i].NormalizedScore)
		}
	}
}

// TestMerge_MultiplePreemptedRowsAcrossProjects_DeterministicOrder confirms
// preempted rows from DIFFERENT projects merge into one deterministic block
// (UpdatedAt DESC, ID ASC) ahead of every normalized row, with no
// project-level priority among them.
func TestMerge_MultiplePreemptedRowsAcrossProjects_DeterministicOrder(t *testing.T) {
	groups := []Group{
		{Project: "a", Items: []Item{
			{ID: 10, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z", Preempt: true},
			{ID: 11, Relevance: 2, UpdatedAt: "2026-08-01T00:00:00Z"},
		}},
		{Project: "b", Items: []Item{
			{ID: 20, Relevance: 1, UpdatedAt: "2026-08-03T00:00:00Z", Preempt: true},
			{ID: 21, Relevance: 2, UpdatedAt: "2026-08-01T00:00:00Z"},
		}},
	}
	got := Merge(groups)
	if len(got) < 2 || got[0].ID != 20 || got[1].ID != 10 {
		t.Fatalf("preempted block order = %v, want [20 10 ...] (UpdatedAt DESC across projects)", idOrder(got))
	}
	if !got[0].Preempt || !got[1].Preempt {
		t.Fatalf("expected first two rows to carry Preempt=true")
	}
	if got[2].Preempt {
		t.Fatalf("normalized rows must follow the preempted block, got Preempt=true at index 2")
	}
}

// TestMerge_Determinism runs the same input through Merge twice (from
// independently built, non-aliased inputs) and asserts byte-for-byte
// identical output — the eval harness reports +/-0.000 across runs, and
// this package's output feeds that harness, so it must be exactly as
// deterministic. Merge touches no maps by range (only by keyed lookup over
// an already-ordered slice), so there is no hidden map-iteration
// nondeterminism to catch here, but the test guards the contract directly
// rather than relying on that implementation detail staying true.
func TestMerge_Determinism(t *testing.T) {
	build := func() []Group {
		return []Group{
			{Project: "alpha", Items: []Item{
				{ID: 1, Relevance: 5, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 2, Relevance: 5, UpdatedAt: "2026-08-01T00:00:00Z"}, // exact tie on everything but ID
				{ID: 3, Relevance: 9, UpdatedAt: "2026-08-02T00:00:00Z", Preempt: true},
			}},
			{Project: "beta", Items: []Item{
				{ID: 4, Relevance: 3, UpdatedAt: "2026-08-01T00:00:00Z"},
			}},
			{Project: "gamma", Items: nil}, // empty group
		}
	}

	first := Merge(build())
	for i := 0; i < 10; i++ {
		again := Merge(build())
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d diverged from run 0:\n  run0=%+v\n  runN=%+v", i, first, again)
		}
	}
}

// TestMerge_DegenerateCases covers the explicit degenerate inputs called
// out in the work item: empty groups slice, a group with no items, a group
// with a single item (min==max), a group where every item shares the same
// relevance (also min==max, but with more than one row), and a group whose
// items are ALL preempted (no normalization batch at all).
func TestMerge_DegenerateCases(t *testing.T) {
	tests := []struct {
		name     string
		groups   []Group
		wantIDs  []int64
		wantAll1 bool // every returned row's NormalizedScore should be exactly 1.0
	}{
		{
			name:    "no groups at all",
			groups:  nil,
			wantIDs: []int64{},
		},
		{
			name:    "single empty group",
			groups:  []Group{{Project: "empty", Items: nil}},
			wantIDs: []int64{},
		},
		{
			name:     "single item in a group",
			groups:   []Group{{Project: "p", Items: []Item{{ID: 1, Relevance: 42, UpdatedAt: "2026-08-01T00:00:00Z"}}}},
			wantIDs:  []int64{1},
			wantAll1: true,
		},
		{
			name: "every item in the group shares the same relevance",
			groups: []Group{{Project: "p", Items: []Item{
				{ID: 1, Relevance: 7, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 2, Relevance: 7, UpdatedAt: "2026-08-02T00:00:00Z"},
				{ID: 3, Relevance: 7, UpdatedAt: "2026-08-03T00:00:00Z"},
			}}},
			wantIDs:  []int64{3, 2, 1}, // score ties -> UpdatedAt DESC decides
			wantAll1: true,
		},
		{
			name: "group is entirely preempted rows, no normal rows",
			groups: []Group{{Project: "p", Items: []Item{
				{ID: 1, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z", Preempt: true},
				{ID: 2, Relevance: 2, UpdatedAt: "2026-08-02T00:00:00Z", Preempt: true},
			}}},
			wantIDs:  []int64{2, 1}, // UpdatedAt DESC among preempted rows
			wantAll1: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Merge(tt.groups)
			gotIDs := idOrder(got)
			if len(gotIDs) == 0 && len(tt.wantIDs) == 0 {
				// both nil/empty; fine regardless of nil vs empty-slice shape
			} else if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Fatalf("IDs = %v, want %v", gotIDs, tt.wantIDs)
			}
			if tt.wantAll1 {
				for _, it := range got {
					if it.NormalizedScore != 1.0 {
						t.Fatalf("ID %d NormalizedScore = %v, want 1.0", it.ID, it.NormalizedScore)
					}
				}
			}
		})
	}
}

// TestNormalizeGroup_NaNTreatedAsZero asserts a NaN Relevance value degrades
// to 0 rather than poisoning the group's min/max scan or the affected row's
// own normalized value with NaN — the same "never propagate NaN into a
// score" precedent as internal/mcp/recall_ranking.go's clampUnit (introduced
// for the salience NaN fix; see internal/multiproject's package doc).
func TestNormalizeGroup_NaNTreatedAsZero(t *testing.T) {
	items := []Item{
		{ID: 1, Relevance: math.NaN()},
		{ID: 2, Relevance: 10},
		{ID: 3, Relevance: 5},
	}
	scores := normalizeGroup(items)

	if math.IsNaN(scores[1]) {
		t.Fatalf("scores[1] is NaN, want it clamped to a real number")
	}
	if scores[1] != 0 {
		t.Fatalf("scores[1] = %v, want 0 (NaN relevance clamped to the lowest value, tying the true min)", scores[1])
	}
	if scores[2] != 1 {
		t.Fatalf("scores[2] = %v, want 1 (true max, unaffected by the NaN row)", scores[2])
	}
	if scores[3] != 0.5 {
		t.Fatalf("scores[3] = %v, want 0.5", scores[3])
	}
}

// TestDiversity covers the diversity metric's own contract: distinct
// project count and per-project counts over a window, with k clamped to
// len(merged) rather than panicking on an out-of-range or non-positive k.
func TestDiversity(t *testing.T) {
	merged := []MergedItem{
		{Item: Item{ID: 1}, Project: "a"},
		{Item: Item{ID: 2}, Project: "a"},
		{Item: Item{ID: 3}, Project: "b"},
		{Item: Item{ID: 4}, Project: "c"},
	}

	tests := []struct {
		name         string
		k            int
		wantDistinct int
		wantCounts   map[string]int
	}{
		{name: "top 1 is single-project", k: 1, wantDistinct: 1, wantCounts: map[string]int{"a": 1}},
		{name: "top 2 still single-project (domination case)", k: 2, wantDistinct: 1, wantCounts: map[string]int{"a": 2}},
		{name: "top 3 crosses into a second project", k: 3, wantDistinct: 2, wantCounts: map[string]int{"a": 2, "b": 1}},
		{name: "top 4 spans three projects", k: 4, wantDistinct: 3, wantCounts: map[string]int{"a": 2, "b": 1, "c": 1}},
		{name: "k larger than list clamps to len", k: 100, wantDistinct: 3, wantCounts: map[string]int{"a": 2, "b": 1, "c": 1}},
		{name: "k <= 0 clamps to len", k: 0, wantDistinct: 3, wantCounts: map[string]int{"a": 2, "b": 1, "c": 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := Diversity(merged, tt.k)
			if report.Distinct != tt.wantDistinct {
				t.Fatalf("Distinct = %d, want %d", report.Distinct, tt.wantDistinct)
			}
			if !reflect.DeepEqual(report.Counts, tt.wantCounts) {
				t.Fatalf("Counts = %v, want %v", report.Counts, tt.wantCounts)
			}
		})
	}

	t.Run("empty merged list", func(t *testing.T) {
		report := Diversity(nil, 4)
		if report.Distinct != 0 || len(report.Counts) != 0 {
			t.Fatalf("Diversity(nil, 4) = %+v, want Distinct 0 and empty Counts", report)
		}
	})
}
