// Package recall is the pure, dependency-free ranking core for Omnia's
// hybrid recall (design D6). It fuses a lexical (FTS5/BM25) ranked list with
// a semantic (cosine) ranked list via reciprocal-rank fusion (RRF, design
// D1) behind an adaptive relevance floor (design D2).
//
// This package imports only the standard library and internal/embed's
// Searcher port/Hit type — never internal/store or internal/cloud/* — so it
// stays a reusable leaf both mem_search recall (internal/mcp, PR3) and
// memory-conflict-semantic's deferred FindCandidates can depend on without
// coupling to a concrete lexical-store implementation. Callers convert their
// own result types (e.g. store.SearchResult) into LexicalHit at the wiring
// boundary.
package recall

import "sort"

// LexicalHit is one lexical (FTS5/BM25) candidate as seen by the fusion
// core. It carries only the fields Fuse needs to rank and tie-break.
type LexicalHit struct {
	ID        int64
	UpdatedAt string // sortable (RFC3339 / SQLite datetime); used for DESC tie-break
	Exact     bool   // true for the topic_key exact-match sentinel (store.Search rank -1000)
}

// SemanticHit is one semantic (cosine) candidate. Callers are expected to
// pass hits already ranked by descending Score (embed.Searcher.Search /
// embed.Store.Search already return them this way) — Fuse does not resort.
type SemanticHit struct {
	ID        int64
	UpdatedAt string
	Score     float32
}

// Result is one fused, ranked candidate. Callers resolve ID back to a full
// display record; Fuse itself never touches storage.
type Result struct {
	ID    int64
	Score float64
	Exact bool // true if this result came from the topic_key sentinel, not RRF
}

// Weight keys for FuseParams.Weights.
const (
	WeightLexical  = "lexical"
	WeightSemantic = "semantic"
)

// FuseParams configures Fuse's RRF ranking (design D1) and adaptive
// relevance floor (design D2).
type FuseParams struct {
	RRFK        int                // RRF k constant (D1 default: 60)
	DenseK      int                // "many strong hits" threshold (D2 default: 5)
	MaxResults  int                // cap on the final fused result count (D2 default: 50); <= 0 disables the cap
	StrongFloor float32            // "precise" score threshold (D2 default: 0.65)
	BaseFloor   float32            // "widen" score threshold (D2 default: 0.55)
	Weights     map[string]float32 // optional per-list weight override (keys: WeightLexical/WeightSemantic); default 1
}

// DefaultFuseParams returns the design-approved defaults (D1: rrf_k=60; D2:
// strong_floor=0.65, base_floor=0.55, dense_k=5, max_results=50). PR3 wires
// these into RecallConfig; PR2 keeps them here as the single source of
// truth so tests never duplicate magic numbers.
func DefaultFuseParams() FuseParams {
	return FuseParams{
		RRFK:        60,
		DenseK:      5,
		MaxResults:  50,
		StrongFloor: 0.65,
		BaseFloor:   0.55,
	}
}

func (p FuseParams) weight(list string) float64 {
	if w, ok := p.Weights[list]; ok && w != 0 {
		return float64(w)
	}
	return 1
}

// AdaptiveFloor computes the score floor to apply to a semantic hit list,
// per design D2: tighten to strongFloor once at least denseK hits already
// clear it (many strong hits → be precise); otherwise widen to baseFloor so
// a sparse result set isn't filtered down to nothing. denseK <= 0 disables
// tightening entirely (always widen to baseFloor).
func AdaptiveFloor(hits []SemanticHit, strongFloor, baseFloor float32, denseK int) float32 {
	if denseK <= 0 {
		return baseFloor
	}
	strong := 0
	for _, h := range hits {
		if h.Score >= strongFloor {
			strong++
		}
	}
	if strong >= denseK {
		return strongFloor
	}
	return baseFloor
}

// candidate accumulates one deduped ID's fusion state across both input lists.
type candidate struct {
	id        int64
	updatedAt string
	lexRank   int // 1-based; 0 = absent from the lexical list
	semRank   int // 1-based; 0 = absent from the (floor-filtered) semantic list
	rrf       float64
}

func (c *candidate) bothLists() bool { return c.lexRank > 0 && c.semRank > 0 }

// getOrCreate returns the existing candidate for id, or creates and appends
// it to order (tracking first-seen insertion so downstream iteration is
// deterministic regardless of Go's randomized map iteration order).
func getOrCreate(cands map[int64]*candidate, order *[]int64, id int64, updatedAt string) *candidate {
	if c, ok := cands[id]; ok {
		return c
	}
	c := &candidate{id: id, updatedAt: updatedAt}
	cands[id] = c
	*order = append(*order, id)
	return c
}

// Fuse merges a pre-ranked lexical list and a pre-ranked semantic list into
// one fused, deduped, ranked list via reciprocal-rank fusion (RRF):
// score(d) = Σ_lists w_i / (RRFK + rank_i(d)), rank_i starting at 1. Both
// input slices are trusted to already be in rank order (position == rank);
// Fuse performs no I/O and does not resort its inputs.
//
// The topic_key exact-match sentinel (LexicalHit.Exact) always pre-empts RRF
// ranking: exact rows are returned first (sorted UpdatedAt DESC, then ID ASC
// for determinism) and are deduped out of the RRF portion entirely, even if
// the same ID also appears in the semantic list.
//
// The semantic list is first restricted to the adaptive floor (design D2,
// AdaptiveFloor) before it competes in RRF, so weak semantic noise never
// out-ranks a real lexical hit.
//
// RRF ties are broken by (1) presence in both lists beating a single list,
// (2) UpdatedAt DESC, (3) ID ASC — fully deterministic. The final list
// (sentinel rows + RRF-ranked rows) is capped at MaxResults.
func Fuse(lexical []LexicalHit, semantic []SemanticHit, p FuseParams) []Result {
	if p.RRFK <= 0 {
		p.RRFK = 60
	}

	// 1. Split out the exact sentinel rows; they pre-empt and never compete
	// on RRF, and are deduped out of both input lists below.
	exactIDs := make(map[int64]bool)
	var exact []LexicalHit
	var lexRanked []LexicalHit
	for _, h := range lexical {
		if h.Exact {
			exact = append(exact, h)
			exactIDs[h.ID] = true
		} else {
			lexRanked = append(lexRanked, h)
		}
	}
	sort.SliceStable(exact, func(i, j int) bool {
		if exact[i].UpdatedAt != exact[j].UpdatedAt {
			return exact[i].UpdatedAt > exact[j].UpdatedAt
		}
		return exact[i].ID < exact[j].ID
	})

	results := make([]Result, 0, len(exact))
	for _, h := range exact {
		results = append(results, Result{ID: h.ID, Exact: true})
	}

	// 2. Adaptive floor restricts the semantic list before it competes.
	floor := AdaptiveFloor(semantic, p.StrongFloor, p.BaseFloor, p.DenseK)

	// 3. Assign 1-based ranks per list and accumulate RRF scores keyed by ID.
	cands := make(map[int64]*candidate)
	var order []int64

	lexRank := 0
	for _, h := range lexRanked {
		if exactIDs[h.ID] {
			continue
		}
		lexRank++
		c := getOrCreate(cands, &order, h.ID, h.UpdatedAt)
		c.lexRank = lexRank
		if h.UpdatedAt > c.updatedAt {
			c.updatedAt = h.UpdatedAt
		}
	}

	semRank := 0
	for _, h := range semantic {
		if h.Score < floor || exactIDs[h.ID] {
			continue
		}
		semRank++
		c := getOrCreate(cands, &order, h.ID, h.UpdatedAt)
		c.semRank = semRank
		if h.UpdatedAt > c.updatedAt {
			c.updatedAt = h.UpdatedAt
		}
	}

	for _, id := range order {
		c := cands[id]
		var score float64
		if c.lexRank > 0 {
			score += p.weight(WeightLexical) / float64(p.RRFK+c.lexRank)
		}
		if c.semRank > 0 {
			score += p.weight(WeightSemantic) / float64(p.RRFK+c.semRank)
		}
		c.rrf = score
	}

	sort.SliceStable(order, func(i, j int) bool {
		a, b := cands[order[i]], cands[order[j]]
		if a.rrf != b.rrf {
			return a.rrf > b.rrf
		}
		if a.bothLists() != b.bothLists() {
			return a.bothLists()
		}
		if a.updatedAt != b.updatedAt {
			return a.updatedAt > b.updatedAt
		}
		return a.id < b.id
	})

	for _, id := range order {
		c := cands[id]
		results = append(results, Result{ID: c.id, Score: c.rrf})
	}

	if p.MaxResults > 0 && len(results) > p.MaxResults {
		results = results[:p.MaxResults]
	}
	return results
}
