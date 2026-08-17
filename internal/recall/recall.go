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

	// SemanticScore is this candidate's raw semantic (cosine) similarity —
	// the same per-hit value SemanticHit.Score carries in, surfaced past
	// fusion instead of being discarded (engram obs #2585/#2612: RRF's Score
	// above is a rank-position combinator, not comparable across queries, so
	// it cannot answer "how relevant is this, really" — raw cosine is the
	// one quantity in this package with real cross-query magnitude, which is
	// exactly why AdaptiveFloor's StrongFloor/BaseFloor thresholds it at
	// all).
	//
	// nil means "no cosine for this row", NOT "cosine 0.0" — two genuinely
	// different situations a caller must not conflate (a nil SemanticScore
	// reading as 0 would make a lexical-only row look maximally IRRELEVANT
	// on the one axis that has real magnitude, which is worse than not
	// reporting it at all). nil covers three cases, all indistinguishable
	// past this point and all correctly "no signal":
	//   - the semantic leg was never run (Semantic == nil, EmbedQuery/Search
	//     failed — Service.semanticHits degrades to an empty slice), so this
	//     ID never had a candidate cosine to carry;
	//   - this ID appeared in the semantic list but scored below
	//     AdaptiveFloor's floor, so it was filtered out before competing in
	//     RRF and never received a semRank (see the semantic loop below);
	//   - this result reached the output via the lexical leg only — a real,
	//     common case (design D2), not an error.
	// A non-nil value is only ever set from a hit that both passed the floor
	// and was not deduped into the exact-sentinel lane.
	SemanticScore *float64
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
	StrongFloor float32            // "precise" score threshold (D2 default: 0.35, jina-calibrated — see below)
	BaseFloor   float32            // "widen" score threshold (D2 default: 0.25, jina-calibrated — see below)
	Weights     map[string]float32 // optional per-list weight override (keys: WeightLexical/WeightSemantic); default 1
}

// DefaultFuseParams returns the design-approved defaults (D1: rrf_k=60; D2:
// strong_floor=0.35, base_floor=0.25, dense_k=5, max_results=50). PR3 wires
// these into RecallConfig; PR2 keeps them here as the single source of
// truth so tests never duplicate magic numbers.
//
// StrongFloor/BaseFloor were originally 0.65/0.55, tuned against a
// different embedding model's cosine-similarity distribution. Omnia's
// default model is jina/jina-embeddings-v2-base-es, whose similarity
// scores run lower — those constants starved recall even with embeddings
// enabled on a fresh install. 0.35/0.25 is the jina-calibrated value the
// live instance was empirically tuned to (issue #83; engram/omnia memory
// #1434), now shipped as the default everywhere this function is the
// source of truth (internal/config.applyDefaults duplicates it for
// config.yaml's recall.strong_floor/base_floor; internal/cloud/clouddash
// calls this function directly for the cloud dashboard's semantic fusion).
func DefaultFuseParams() FuseParams {
	return FuseParams{
		RRFK:        60,
		DenseK:      5,
		MaxResults:  50,
		StrongFloor: 0.35,
		BaseFloor:   0.25,
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

	// semScore/semScoreOK carry the raw cosine value through to Result.
	// SemanticScore, set alongside semRank (same guard: passed the floor,
	// not the exact sentinel) — semScoreOK distinguishes "scored 0.0" from
	// "never had a semantic hit", the same nil-means-absent contract
	// Result.SemanticScore documents.
	semScore   float32
	semScoreOK bool
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
//
// Each non-sentinel Result also carries SemanticScore — the raw cosine
// value the winning semantic hit (if any) contributed, surfaced past RRF
// rather than discarded (see Result.SemanticScore's own doc for the nil
// contract). This is pure plumbing: it does not change rrf, floor
// filtering, ordering, or MaxResults truncation in any way.
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
		c.semScore = h.Score
		c.semScoreOK = true
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
		var semScore *float64
		if c.semScoreOK {
			v := float64(c.semScore)
			semScore = &v
		}
		results = append(results, Result{ID: c.id, Score: c.rrf, SemanticScore: semScore})
	}

	if p.MaxResults > 0 && len(results) > p.MaxResults {
		results = results[:p.MaxResults]
	}
	return results
}
