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

// TestMerge_SingleGroup_MatchesNoMergeOrder is the degenerate case called
// out explicitly in the P5 work item: with exactly one contending project,
// Merge must not cap it (there is nothing to protect against) and must
// produce the byte-for-byte same order as sorting that group's Items by raw
// Relevance DESC (same tie-break) and not calling Merge at all. The group
// below has more items than DefaultPerProjectCap specifically to prove the
// cap does NOT fire in the single-group case.
func TestMerge_SingleGroup_MatchesNoMergeOrder(t *testing.T) {
	items := []Item{
		{ID: 1, Relevance: 3.2, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 2, Relevance: 9.9, UpdatedAt: "2026-08-02T00:00:00Z"},
		{ID: 3, Relevance: 1.0, UpdatedAt: "2026-08-03T00:00:00Z"},
		{ID: 4, Relevance: 7.5, UpdatedAt: "2026-07-30T00:00:00Z"},
		{ID: 5, Relevance: 9.9, UpdatedAt: "2026-08-04T00:00:00Z"}, // ties item 2 on raw relevance
	}
	if len(items) <= DefaultPerProjectCap {
		t.Fatalf("test setup: need more items than DefaultPerProjectCap (%d) to prove the cap does not fire here", DefaultPerProjectCap)
	}

	// Expected order without any merge at all: sort by raw Relevance DESC,
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

	got := Merge([]Group{{Project: "solo", Items: items}}, DefaultPerProjectCap)
	if !reflect.DeepEqual(idOrder(got), wantIDs) {
		t.Fatalf("single-group Merge order = %v, want %v (no-merge order)", idOrder(got), wantIDs)
	}
	if len(got) != len(items) {
		t.Fatalf("single-group Merge returned %d rows, want all %d (cap must not fire with one contending group)", len(got), len(items))
	}
}

// TestMerge_ManySingleItemGroups_HighestRelevanceWinsNotMostRecent is the
// regression test for the production failure this redesign exists to fix:
// wired against the live store (~49 projects, most contributing exactly one
// hit), the OLD per-group-normalization design tied every single-item
// group's result at 1.0 and let the UpdatedAt tie-break (recency) decide
// order, driving cross_project accuracy@1 from 0.250 to 0.000. This
// constructs that exact shape — many single-item groups with DIFFERENT
// relevances, deliberately timestamped so recency order is the REVERSE of
// relevance order — and asserts the highest-relevance item wins. If this
// test ever starts passing under a design that ties everything and falls
// back to UpdatedAt, that IS the 0.000 regression again.
func TestMerge_ManySingleItemGroups_HighestRelevanceWinsNotMostRecent(t *testing.T) {
	groups := []Group{
		{Project: "p1", Items: []Item{{ID: 1, Relevance: 1.0, UpdatedAt: "2026-08-05T00:00:00Z"}}}, // most recent, lowest relevance
		{Project: "p2", Items: []Item{{ID: 2, Relevance: 2.0, UpdatedAt: "2026-08-04T00:00:00Z"}}},
		{Project: "p3", Items: []Item{{ID: 3, Relevance: 3.0, UpdatedAt: "2026-08-03T00:00:00Z"}}},
		{Project: "p4", Items: []Item{{ID: 4, Relevance: 4.0, UpdatedAt: "2026-08-02T00:00:00Z"}}},
		{Project: "p5", Items: []Item{{ID: 5, Relevance: 5.0, UpdatedAt: "2026-08-01T00:00:00Z"}}}, // least recent, highest relevance
	}

	got := Merge(groups, DefaultPerProjectCap)

	wantIDs := []int64{5, 4, 3, 2, 1} // strictly by descending relevance
	if !reflect.DeepEqual(idOrder(got), wantIDs) {
		t.Fatalf("Merge order = %v, want %v (highest raw relevance first, recency must not decide when relevance differs)", idOrder(got), wantIDs)
	}
	if got[0].ID != 5 || got[0].Project != "p5" {
		t.Fatalf("got[0] = {ID:%d Project:%s}, want the highest-relevance single-item group (p5, ID 5) to win despite being the least recently updated", got[0].ID, got[0].Project)
	}
	if got[0].Score != 5.0 {
		t.Fatalf("got[0].Score = %v, want 5.0 (raw relevance, unnormalized)", got[0].Score)
	}
}

// TestMerge_CapLimitsPerProjectContribution asserts the cap mechanism
// itself: a project with far more normal items than k must never contribute
// more than k rows to Merge's output, even when every one of its items
// outscores everything else in play.
func TestMerge_CapLimitsPerProjectContribution(t *testing.T) {
	var dominant []Item
	for i := int64(1); i <= 10; i++ {
		dominant = append(dominant, Item{ID: i, Relevance: 100 + float64(i), UpdatedAt: "2026-08-01T00:00:00Z"})
	}
	groups := []Group{
		{Project: "dominant", Items: dominant},
		{Project: "quiet", Items: []Item{{ID: 999, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z"}}},
	}

	got := Merge(groups, 3)

	counts := map[string]int{}
	for _, it := range got {
		counts[it.Project]++
	}
	if counts["dominant"] != 3 {
		t.Fatalf("dominant project contributed %d rows, want exactly 3 (the cap)", counts["dominant"])
	}
	if counts["quiet"] != 1 {
		t.Fatalf("quiet project contributed %d rows, want 1", counts["quiet"])
	}
	// The cap must keep the dominant project's OWN top 3 by relevance (IDs
	// 10, 9, 8 have the highest Relevance: 110, 109, 108), not an arbitrary
	// prefix of the input slice.
	var dominantIDs []int64
	for _, it := range got {
		if it.Project == "dominant" {
			dominantIDs = append(dominantIDs, it.ID)
		}
	}
	wantDominantIDs := []int64{10, 9, 8}
	if !reflect.DeepEqual(dominantIDs, wantDominantIDs) {
		t.Fatalf("dominant project's surfaced IDs = %v, want %v (its own highest-relevance items)", dominantIDs, wantDominantIDs)
	}
}

// TestMerge_CapGuaranteesTopFourDiversityWhenMultipleProjectsContend
// exercises the structural guarantee documented on Merge and
// DefaultPerProjectCap: with k <= 3, a single project can occupy at most 3
// of the top-4 merged rows, so when at least one other project also
// contributes a normal row, Diversity's top-4 window must show >= 2 distinct
// projects — even in the adversarial case where the dominant project's
// items genuinely outscore everything else. This is NOT a claim that every
// cross-project query hits this; see Merge's doc for the single-project
// case where Distinct == 1 is correct.
func TestMerge_CapGuaranteesTopFourDiversityWhenMultipleProjectsContend(t *testing.T) {
	groups := []Group{
		{Project: "dominant", Items: []Item{
			{ID: 1, Relevance: 100, UpdatedAt: "2026-08-01T00:00:00Z"},
			{ID: 2, Relevance: 99, UpdatedAt: "2026-08-01T00:00:00Z"},
			{ID: 3, Relevance: 98, UpdatedAt: "2026-08-01T00:00:00Z"},
			{ID: 4, Relevance: 97, UpdatedAt: "2026-08-01T00:00:00Z"},
			{ID: 5, Relevance: 96, UpdatedAt: "2026-08-01T00:00:00Z"},
		}},
		{Project: "other", Items: []Item{
			{ID: 6, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z"},
		}},
	}

	got := Merge(groups, DefaultPerProjectCap)
	div := Diversity(got, 4)
	if div.Distinct < 2 {
		t.Fatalf("Diversity(top 4).Distinct = %d, want >= 2 (the cap must leave room for the other project)", div.Distinct)
	}
	if got[3].Project != "other" {
		t.Fatalf("got[3].Project = %q, want %q (dominant exhausted its cap of %d, so the 4th slot must come from elsewhere)", got[3].Project, "other", DefaultPerProjectCap)
	}
}

// TestMerge_PreemptionExcludedFromCapAndSort asserts the rule the task
// called out as "the single easiest thing to get wrong": a preempted row's
// relevance (an outlier by construction — a topic_key exact-match sentinel
// or a signature-match rank, not a real bm25/RRF score) must never enter its
// group's cap-selection or raw-relevance sort, or it would consume a cap
// slot and/or distort ordering among ordinary rows.
func TestMerge_PreemptionExcludedFromCapAndSort(t *testing.T) {
	normal := []Item{
		{ID: 2, Relevance: 10, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 3, Relevance: 5, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 4, Relevance: 0, UpdatedAt: "2026-08-01T00:00:00Z"},
	}
	preempted := Item{ID: 1, Relevance: 999999, UpdatedAt: "2026-07-01T00:00:00Z", Preempt: true}

	withPreempt := append([]Item{preempted}, normal...)
	got := Merge([]Group{{Project: "p", Items: withPreempt}}, DefaultPerProjectCap)

	// The preempted row must lead, regardless of its outlandish raw
	// relevance.
	if got[0].ID != 1 {
		t.Fatalf("got[0].ID = %d, want 1 (the preempted row first)", got[0].ID)
	}

	// The normal rows' order must be IDENTICAL to what it would be if the
	// outlier had never been in the group at all.
	wantOnlyNormal := Merge([]Group{{Project: "p", Items: normal}}, DefaultPerProjectCap)
	gotNormalOnly := got[1:]
	if len(gotNormalOnly) != len(wantOnlyNormal) {
		t.Fatalf("len(normal rows) = %d, want %d", len(gotNormalOnly), len(wantOnlyNormal))
	}
	for i := range wantOnlyNormal {
		if gotNormalOnly[i].ID != wantOnlyNormal[i].ID || gotNormalOnly[i].Score != wantOnlyNormal[i].Score {
			t.Fatalf("normal row %d = {ID:%d Score:%v}, want {ID:%d Score:%v} (outlier must not affect normal-row order)",
				i, gotNormalOnly[i].ID, gotNormalOnly[i].Score, wantOnlyNormal[i].ID, wantOnlyNormal[i].Score)
		}
	}
}

// TestMerge_MultiplePreemptedRowsAcrossProjects_DeterministicOrder confirms
// preempted rows from DIFFERENT projects merge into one deterministic block
// (UpdatedAt DESC, ID ASC) ahead of every normal row, with no project-level
// priority among them, and that preempted rows do not consume cap slots
// (both projects' normal rows still surface).
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
	got := Merge(groups, DefaultPerProjectCap)
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4 (preempted rows must not consume cap slots)", len(got))
	}
	if got[0].ID != 20 || got[1].ID != 10 {
		t.Fatalf("preempted block order = %v, want [20 10 ...] (UpdatedAt DESC across projects)", idOrder(got))
	}
	if !got[0].Preempt || !got[1].Preempt {
		t.Fatalf("expected first two rows to carry Preempt=true")
	}
	if got[2].Preempt || got[3].Preempt {
		t.Fatalf("normal rows must follow the preempted block, got Preempt=true within indices 2-3")
	}
}

// TestMerge_Determinism runs the same input through Merge twice (from
// independently built, non-aliased inputs) and asserts byte-for-byte
// identical output — the eval harness reports +/-0.000 across runs, and
// this package's output feeds that harness, so it must be exactly as
// deterministic. Merge touches no maps at all (only slices), so there is no
// hidden map-iteration nondeterminism to catch here, but the test guards the
// contract directly rather than relying on that implementation detail
// staying true.
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

	first := Merge(build(), DefaultPerProjectCap)
	for i := 0; i < 10; i++ {
		again := Merge(build(), DefaultPerProjectCap)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d diverged from run 0:\n  run0=%+v\n  runN=%+v", i, first, again)
		}
	}
}

// TestMerge_DegenerateCases covers the explicit degenerate inputs called out
// in the work item: empty groups slice, a group with no items, a group with
// a single item, a group where every item shares the same relevance (a tie,
// broken by UpdatedAt/ID), and a group whose items are ALL preempted (no
// normal rows at all, so it does not count as a contending group).
func TestMerge_DegenerateCases(t *testing.T) {
	tests := []struct {
		name    string
		groups  []Group
		wantIDs []int64
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
			name:    "single item in a group",
			groups:  []Group{{Project: "p", Items: []Item{{ID: 1, Relevance: 42, UpdatedAt: "2026-08-01T00:00:00Z"}}}},
			wantIDs: []int64{1},
		},
		{
			name: "every item in the group shares the same relevance",
			groups: []Group{{Project: "p", Items: []Item{
				{ID: 1, Relevance: 7, UpdatedAt: "2026-08-01T00:00:00Z"},
				{ID: 2, Relevance: 7, UpdatedAt: "2026-08-02T00:00:00Z"},
				{ID: 3, Relevance: 7, UpdatedAt: "2026-08-03T00:00:00Z"},
			}}},
			wantIDs: []int64{3, 2, 1}, // score ties -> UpdatedAt DESC decides
		},
		{
			name: "group is entirely preempted rows, no normal rows",
			groups: []Group{{Project: "p", Items: []Item{
				{ID: 1, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z", Preempt: true},
				{ID: 2, Relevance: 2, UpdatedAt: "2026-08-02T00:00:00Z", Preempt: true},
			}}},
			wantIDs: []int64{2, 1}, // UpdatedAt DESC among preempted rows
		},
		{
			name: "two groups, one empty and one entirely preempted -- still not a contending pair",
			groups: []Group{
				{Project: "empty", Items: nil},
				{Project: "preempted-only", Items: []Item{
					{ID: 5, Relevance: 1, UpdatedAt: "2026-08-01T00:00:00Z", Preempt: true},
				}},
			},
			wantIDs: []int64{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Merge(tt.groups, DefaultPerProjectCap)
			gotIDs := idOrder(got)
			if len(gotIDs) == 0 && len(tt.wantIDs) == 0 {
				// both nil/empty; fine regardless of nil vs empty-slice shape
			} else if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Fatalf("IDs = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

// TestMerge_NaNRelevanceTreatedAsZero asserts a NaN Relevance value degrades
// to 0 rather than poisoning the sort or the affected row's own Score with
// NaN — the same "never propagate NaN into a score" precedent as
// internal/mcp/recall_ranking.go's clampUnit (introduced for the salience
// NaN fix; see internal/multiproject's package doc).
func TestMerge_NaNRelevanceTreatedAsZero(t *testing.T) {
	groups := []Group{{Project: "p", Items: []Item{
		{ID: 1, Relevance: math.NaN(), UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 2, Relevance: 10, UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: 3, Relevance: -5, UpdatedAt: "2026-08-01T00:00:00Z"}, // legitimately below the NaN's clamped 0
	}}}

	got := Merge(groups, DefaultPerProjectCap)

	for _, it := range got {
		if math.IsNaN(it.Score) {
			t.Fatalf("ID %d Score is NaN, want it clamped to a real number", it.ID)
		}
	}
	wantIDs := []int64{2, 1, 3} // 10, then NaN-clamped-to-0, then the legitimately negative -5
	if !reflect.DeepEqual(idOrder(got), wantIDs) {
		t.Fatalf("IDs = %v, want %v", idOrder(got), wantIDs)
	}
	for _, it := range got {
		if it.ID == 1 && it.Score != 0 {
			t.Fatalf("ID 1 (NaN input) Score = %v, want 0", it.Score)
		}
	}
}

// TestDiversity covers the diversity metric's own contract: distinct
// project count and per-project counts over a window, with k clamped to
// len(merged) rather than panicking on an out-of-range or non-positive k.
// Diversity's contract is unaffected by the normalization -> cap redesign
// (it only ever reads MergedItem.Project), so this test is unchanged in
// substance from the prior design.
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
