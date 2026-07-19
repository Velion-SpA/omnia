package recall

import "testing"

func idsOf(results []Result) []int64 {
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

func assertIDOrder(t *testing.T, got []Result, want []int64) {
	t.Helper()
	gotIDs := idsOf(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("result count: got %v (len %d), want ids %v (len %d)", gotIDs, len(gotIDs), want, len(want))
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("result[%d]: got id %d, want id %d (full got=%v want=%v)", i, gotIDs[i], want[i], gotIDs, want)
		}
	}
}

// TestFuse_RRFOrder verifies plain reciprocal-rank fusion order: a candidate
// present in BOTH lists must outrank one present in only a single list, per
// design D1's score(d) = Σ w_i/(k+rank_i(d)).
func TestFuse_RRFOrder(t *testing.T) {
	lexical := []LexicalHit{
		{ID: 1, UpdatedAt: "2024-01-01"}, // rank 1, lexical-only
		{ID: 2, UpdatedAt: "2024-01-01"}, // rank 2, also in semantic
	}
	semantic := []SemanticHit{
		{ID: 2, UpdatedAt: "2024-01-01", Score: 0.9}, // rank 1, also in lexical
		{ID: 3, UpdatedAt: "2024-01-01", Score: 0.8}, // rank 2, semantic-only
	}
	params := FuseParams{RRFK: 60, BaseFloor: 0, DenseK: 0, MaxResults: 0}

	got := Fuse(lexical, semantic, params)

	// id 2: 1/62 (lex rank2) + 1/61 (sem rank1) ≈ 0.032523 — highest.
	// id 1: 1/61 (lex rank1 only)              ≈ 0.016393
	// id 3: 1/62 (sem rank2 only)               ≈ 0.016129
	assertIDOrder(t, got, []int64{2, 1, 3})
}

// TestFuse_TieBreak_BothListsBeatsSingleList constructs an exact RRF-score
// tie (k=1) between a candidate present in both lists and one present in a
// single list, verifying the "present in both" tie-break wins first, then
// UpdatedAt DESC, then ID ASC for any remaining ties.
func TestFuse_TieBreak_BothListsBeatsSingleList(t *testing.T) {
	lexical := []LexicalHit{
		{ID: 1, UpdatedAt: "2024-01-03"},   // rank1 → 1/(1+1) = 0.5   (lexical-only)
		{ID: 901, UpdatedAt: "2024-01-01"}, // rank2 → 1/(1+2) = 0.333 (lexical-only)
		{ID: 2, UpdatedAt: "2024-01-02"},   // rank3 → 1/(1+3) = 0.25  (also in semantic)
	}
	semantic := []SemanticHit{
		{ID: 902, UpdatedAt: "2024-01-01", Score: 0.9}, // rank1 → 1/(1+1) = 0.5   (semantic-only)
		{ID: 903, UpdatedAt: "2024-01-01", Score: 0.8}, // rank2 → 1/(1+2) = 0.333 (semantic-only)
		{ID: 2, UpdatedAt: "2024-01-02", Score: 0.7},   // rank3 → 1/(1+3) = 0.25  (also in lexical)
	}
	// id 2 total = 0.25 + 0.25 = 0.5   (present in BOTH lists)
	// id 1 total = 0.5                (lexical-only)
	// id 902 total = 0.5              (semantic-only)
	// → three-way tie at 0.5: id 2 wins (both lists), then id 1 vs id 902 by
	//   UpdatedAt DESC ("2024-01-03" > "2024-01-01") → id 1 next, id 902 last.
	//
	// id 901 total = 0.333, id 903 total = 0.333 → tie, equal UpdatedAt, ID ASC.
	params := FuseParams{RRFK: 1, BaseFloor: 0, DenseK: 0, MaxResults: 0}

	got := Fuse(lexical, semantic, params)

	assertIDOrder(t, got, []int64{2, 1, 902, 901, 903})
}

// TestFuse_TieBreak_UpdatedAtThenID isolates the UpdatedAt DESC / ID ASC
// tie-break rules using two candidates that are symmetrically present in
// both lists (so RRF scores are identical regardless of RRFK).
func TestFuse_TieBreak_UpdatedAtThenID(t *testing.T) {
	t.Run("updated_at desc wins", func(t *testing.T) {
		lexical := []LexicalHit{
			{ID: 10, UpdatedAt: "2024-01-01"}, // rank1
			{ID: 20, UpdatedAt: "2024-01-05"}, // rank2
		}
		semantic := []SemanticHit{
			{ID: 20, Score: 0.9}, // rank1
			{ID: 10, Score: 0.9}, // rank2
		}
		got := Fuse(lexical, semantic, DefaultFuseParams())
		// Both ids get 1/61+1/62 — identical score, both present in both lists.
		// id 20's UpdatedAt (2024-01-05) > id 10's (2024-01-01) → 20 first.
		assertIDOrder(t, got, []int64{20, 10})
	})

	t.Run("id asc wins when updated_at ties", func(t *testing.T) {
		lexical := []LexicalHit{
			{ID: 10, UpdatedAt: "2024-01-01"},
			{ID: 20, UpdatedAt: "2024-01-01"},
		}
		semantic := []SemanticHit{
			{ID: 20, Score: 0.9},
			{ID: 10, Score: 0.9},
		}
		got := Fuse(lexical, semantic, DefaultFuseParams())
		assertIDOrder(t, got, []int64{10, 20})
	})
}

// TestFuse_MaxResultsCap verifies the fused output is capped at MaxResults.
func TestFuse_MaxResultsCap(t *testing.T) {
	lexical := []LexicalHit{
		{ID: 1, UpdatedAt: "2024-01-01"},
		{ID: 2, UpdatedAt: "2024-01-01"},
		{ID: 3, UpdatedAt: "2024-01-01"},
		{ID: 4, UpdatedAt: "2024-01-01"},
		{ID: 5, UpdatedAt: "2024-01-01"},
	}
	params := FuseParams{RRFK: 60, MaxResults: 3}

	got := Fuse(lexical, nil, params)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (MaxResults cap)", len(got))
	}
	assertIDOrder(t, got, []int64{1, 2, 3})
}

// TestFuse_TopicKeySentinelPreempts verifies store.Search's topic_key exact
// match sentinel (rank -1000, modeled here as LexicalHit.Exact) always sorts
// first, ahead of any RRF-ranked result, is ordered UpdatedAt DESC/ID ASC
// among themselves, and is deduped out of the RRF portion even if the same
// ID also appears in the semantic list.
func TestFuse_TopicKeySentinelPreempts(t *testing.T) {
	lexical := []LexicalHit{
		{ID: 99, UpdatedAt: "2024-06-01", Exact: true},
		{ID: 50, UpdatedAt: "2024-06-01", Exact: true}, // same UpdatedAt, lower ID
		{ID: 1, UpdatedAt: "2024-01-01"},               // rank1, RRF-ranked
		{ID: 2, UpdatedAt: "2024-01-01"},               // rank2, RRF-ranked
	}
	semantic := []SemanticHit{
		{ID: 99, UpdatedAt: "2024-06-01", Score: 0.99}, // duplicate of an exact hit
	}
	params := FuseParams{RRFK: 60, BaseFloor: 0, DenseK: 0}

	got := Fuse(lexical, semantic, params)

	// Exact rows first (50 before 99: same UpdatedAt, ID ASC), then RRF order.
	assertIDOrder(t, got, []int64{50, 99, 1, 2})
	if !got[0].Exact || !got[1].Exact {
		t.Fatalf("expected first two results to be Exact sentinel matches, got %+v", got[:2])
	}
	if got[2].Exact || got[3].Exact {
		t.Fatalf("expected RRF-ranked results to not be Exact, got %+v", got[2:])
	}
}

// --- Adaptive floor (design D2) ---------------------------------------------

func TestAdaptiveFloor(t *testing.T) {
	const strongFloor, baseFloor = 0.65, 0.55

	tests := []struct {
		name    string
		hits    []SemanticHit
		denseK  int
		wantFlr float32
	}{
		{
			name:    "sparse strong hits widen to base floor",
			hits:    []SemanticHit{{Score: 0.9}, {Score: 0.6}, {Score: 0.5}},
			denseK:  5,
			wantFlr: baseFloor,
		},
		{
			name: "dense strong hits tighten to strong floor",
			hits: []SemanticHit{
				{Score: 0.9}, {Score: 0.85}, {Score: 0.8}, {Score: 0.75}, {Score: 0.7}, // 5 strong
				{Score: 0.6},
			},
			denseK:  5,
			wantFlr: strongFloor,
		},
		{
			name:    "exactly denseK strong hits tightens (boundary is inclusive)",
			hits:    []SemanticHit{{Score: 0.65}, {Score: 0.65}, {Score: 0.65}, {Score: 0.65}, {Score: 0.65}},
			denseK:  5,
			wantFlr: strongFloor,
		},
		{
			name:    "denseK <= 0 always widens",
			hits:    []SemanticHit{{Score: 0.99}, {Score: 0.98}, {Score: 0.97}, {Score: 0.96}, {Score: 0.95}, {Score: 0.94}},
			denseK:  0,
			wantFlr: baseFloor,
		},
		{
			name:    "empty hits widen to base floor",
			hits:    nil,
			denseK:  5,
			wantFlr: baseFloor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdaptiveFloor(tt.hits, strongFloor, baseFloor, tt.denseK)
			if got != tt.wantFlr {
				t.Errorf("AdaptiveFloor() = %v, want %v", got, tt.wantFlr)
			}
		})
	}
}

// TestFuse_AdaptiveFloor_FiltersSemanticNoise proves Fuse actually applies the
// adaptive floor to the semantic list before ranking: a mid-range hit (0.6) is
// kept when hits are sparse (floor widens to 0.55) but dropped once enough
// strong hits are present to tighten the floor to 0.65.
func TestFuse_AdaptiveFloor_FiltersSemanticNoise(t *testing.T) {
	params := FuseParams{RRFK: 60, StrongFloor: 0.65, BaseFloor: 0.55, DenseK: 5, MaxResults: 0}

	t.Run("sparse: mid-range hit survives via base floor", func(t *testing.T) {
		semantic := []SemanticHit{
			{ID: 1, Score: 0.9},
			{ID: 2, Score: 0.6}, // below strongFloor, above baseFloor
		}
		got := Fuse(nil, semantic, params)
		ids := idsOf(got)
		if len(ids) != 2 {
			t.Fatalf("got %v, want both id 1 and id 2 kept (sparse → base floor 0.55)", ids)
		}
	})

	t.Run("dense: mid-range hit is dropped once floor tightens", func(t *testing.T) {
		semantic := []SemanticHit{
			{ID: 1, Score: 0.9}, {ID: 2, Score: 0.85}, {ID: 3, Score: 0.8},
			{ID: 4, Score: 0.75}, {ID: 5, Score: 0.7}, // 5 strong hits → tighten
			{ID: 6, Score: 0.6}, // below strongFloor 0.65 → dropped
		}
		got := Fuse(nil, semantic, params)
		for _, r := range got {
			if r.ID == 6 {
				t.Fatalf("id 6 (score 0.6) should be dropped once floor tightens to 0.65, got %v", idsOf(got))
			}
		}
		if len(got) != 5 {
			t.Fatalf("len(got) = %d, want 5 (the 5 strong hits, id 6 excluded)", len(got))
		}
	})
}

// TestFuse_EmptyInputs_ReturnsEmptyNotError proves Fuse degrades cleanly to an
// empty result set — never a panic or a sentinel error value — when both
// input lists are empty.
func TestFuse_EmptyInputs_ReturnsEmptyNotError(t *testing.T) {
	got := Fuse(nil, nil, DefaultFuseParams())
	if len(got) != 0 {
		t.Fatalf("Fuse(nil, nil, ...) = %v, want empty", got)
	}
}
