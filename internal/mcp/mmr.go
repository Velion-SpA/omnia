package mcp

// mmr.go — Omnia v0.3 "Context Economy" PR4 (design obs #1643 section 3.3,
// ADR-5; spec obs #1642 injection-diversity domain, all 6 REQs): a read-time
// MMR (maximal marginal relevance) style re-rank over already-ranked,
// hydrated content that demotes/hard-drops rows near-duplicating an
// already-selected higher-ranked row — so a budget-limited result set isn't
// clogged with repeats — without adding new embedding calls or model
// dependencies (spec: Cheap Lexical Similarity Only), and without disturbing
// sentinel/signature pre-emption (spec: Sentinel and Signature Pre-Emption
// Invariant; see preemption_invariant_test.go's shared adversarial table).
//
// ApplyMMR mirrors RankResults/ApplyStalenessDownrank/ApplyTokenBudget's own
// gated-no-op, partition-pre-empted-rows-out-first shape (recall_ranking.go,
// anchor_adapter.go, token_budget.go): it is wired in handleSearch's pipeline
// AFTER RankResults/ApplyStalenessDownrank (priority is already decided) and
// BEFORE ApplyTokenBudget (dedup before trim — the budget is never spent on
// near-duplicates).

import (
	"regexp"
	"strings"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// mmrTokenRe extracts Unicode letter/digit runs as word tokens for
// jaccardSimilarity — punctuation and whitespace are separators, not
// content.
var mmrTokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// tokenizeForSimilarity splits s into lowercased word tokens (design section
// 3.3: "token-set Jaccard on lowercased word tokens"). Case and punctuation
// are deliberately insensitive: "Hello, World!" and "hello world" tokenize
// identically, so near-duplicate detection isn't fooled by capitalization or
// punctuation drift between two renderings of the same fact.
//
// KNOWN LIMITATION: scripts written without inter-word whitespace (Chinese,
// Japanese, ...) tokenize as one giant letter-run per sentence, so two
// near-duplicate CJK sentences look completely disjoint (Jaccard 0) unless
// byte-identical — MMR dedup is effectively inert for such content. Accepted
// for v0.3 (the corpus is ES/EN); a bigram fallback is the follow-up if CJK
// content ever matters.
func tokenizeForSimilarity(s string) []string {
	return mmrTokenRe.FindAllString(strings.ToLower(s), -1)
}

// jaccardSimilarity returns the token-set Jaccard similarity |A∩B| / |A∪B|
// between two already-tokenized word-token slices (design ADR-5): cheap,
// deterministic, O(n+m), allocation-light, no new embeddings or model
// dependency (spec: Cheap Lexical Similarity Only). Two token sets where
// either is empty return 0, not 1 — a deliberate conservative choice so
// empty/near-empty content never looks like a spurious "duplicate" of other
// empty/near-empty content and gets dropped by accident.
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}

	intersection := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}
	union := len(setA)
	for t := range setB {
		if _, ok := setA[t]; !ok {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// mmrRelevanceOrRankProxy returns a rel(d) score in [0,1] for each row in
// rest, for the greedy argmax formula below: the batch-normalized relevance
// signal (MinMaxNormalizeRelevance, reused from recall_ranking.go — spec
// Post-Pass Over Existing Ranking: MMR doesn't recompute base relevance,
// just consumes it) when relevance carries real per-row signal for this
// batch, or a descending rank-position proxy (1.0 for the first/
// highest-priority row in rest, sliding down to 0.0 for the last) when
// relevance carries NO signal at all for any row in rest — "relevance is
// unavailable," the documented fallback in design section 3.3. rest is
// assumed already ordered by the prior ranking/staleness passes, so its own
// position is a reasonable proxy for priority when no numeric relevance was
// supplied.
func mmrRelevanceOrRankProxy(rest []store.SearchResult, relevance map[int64]float64) map[int64]float64 {
	// "Signal" means the caller supplied an entry for at least one row —
	// presence, not value: a batch whose real scores all tie at exactly 0 is
	// still "relevance available" (MinMaxNormalizeRelevance handles the tie
	// by treating all rows as equally relevant), NOT a cue to invent a fake
	// rank-position gradient.
	hasSignal := false
	for _, r := range rest {
		if _, ok := relevance[r.ID]; ok {
			hasSignal = true
			break
		}
	}
	if hasSignal {
		return MinMaxNormalizeRelevance(rest, relevance)
	}

	out := make(map[int64]float64, len(rest))
	n := len(rest)
	for i, r := range rest {
		if n == 1 {
			out[r.ID] = 1
			continue
		}
		out[r.ID] = float64(n-1-i) / float64(n-1)
	}
	return out
}

// ApplyMMR re-ranks results via greedy MMR reselection (spec: Read-Time
// Diversity Re-Rank Over Hydrated Content) when cfg.Enabled and there are at
// least 2 results to compare. It is a gated no-op — results is returned
// completely untouched, same slice, same order — otherwise (spec: Disabled
// by Default, No-Op When Off).
//
// The topic_key exact-match sentinel (Rank == exactSentinelRank) and any
// SignatureMatch row are partitioned OUT first and never enter MMR at all
// (spec: Sentinel and Signature Pre-Emption Invariant): they are always
// emitted first, complete, and untouched, mirroring
// RankResults/ApplyStalenessDownrank/ApplyTokenBudget's own pre-emption of
// both.
//
// Only "rest" competes: starting from the top-ranked row (rest[0] — the
// prior pass's own top pick), ApplyMMR greedily reselects the remaining rows
// one at a time via argmax [cfg.Lambda*rel(d) - (1-cfg.Lambda)*maxSim(d,
// selected)], where rel(d) comes from mmrRelevanceOrRankProxy and maxSim is
// the highest jaccardSimilarity between d and any row already selected.
// Before each round's argmax, any remaining candidate whose maxSim against
// the CURRENT selected set already meets or exceeds cfg.SimilarityThreshold
// is hard-dropped (spec: Near-duplicate demoted — "no dupes") rather than
// merely deprioritized: since similarity to an ever-growing selected set is
// monotonically non-decreasing, once a candidate crosses the threshold it
// can never fall back below it, so dropping it permanently (rather than
// re-testing it every round) is safe and matches "the lower-ranked
// near-duplicate is demoted or suppressed" exactly.
func ApplyMMR(results []store.SearchResult, relevance map[int64]float64, cfg config.DiversityConfig) []store.SearchResult {
	if !cfg.Enabled || len(results) < 2 {
		return results
	}

	var preempted, rest []store.SearchResult
	for _, r := range results {
		if r.Rank == exactSentinelRank || r.SignatureMatch {
			preempted = append(preempted, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(rest) < 2 {
		return results
	}

	rel := mmrRelevanceOrRankProxy(rest, relevance)
	tokens := make([][]string, len(rest))
	for i, r := range rest {
		tokens[i] = tokenizeForSimilarity(r.Content)
	}

	selected := []int{0} // start from the top-ranked row (design section 3.3)
	remaining := make([]int, 0, len(rest)-1)
	for i := 1; i < len(rest); i++ {
		remaining = append(remaining, i)
	}

	for len(remaining) > 0 {
		var survivors []int
		maxSims := make(map[int]float64, len(remaining))
		for _, candidate := range remaining {
			maxSim := 0.0
			for _, s := range selected {
				if sim := jaccardSimilarity(tokens[candidate], tokens[s]); sim > maxSim {
					maxSim = sim
				}
			}
			if maxSim >= cfg.SimilarityThreshold {
				continue // hard-drop: near-duplicate of an already-selected row
			}
			survivors = append(survivors, candidate)
			maxSims[candidate] = maxSim
		}
		if len(survivors) == 0 {
			break
		}

		best := survivors[0]
		bestScore := cfg.Lambda*rel[rest[best].ID] - (1-cfg.Lambda)*maxSims[best]
		for _, candidate := range survivors[1:] {
			score := cfg.Lambda*rel[rest[candidate].ID] - (1-cfg.Lambda)*maxSims[candidate]
			if score > bestScore {
				best = candidate
				bestScore = score
			}
		}

		selected = append(selected, best)
		remaining = removeIntValue(remaining, best)
	}

	out := make([]store.SearchResult, 0, len(preempted)+len(selected))
	out = append(out, preempted...)
	for _, idx := range selected {
		out = append(out, rest[idx])
	}
	return out
}

// removeIntValue returns a copy of ints with every occurrence of v removed,
// preserving the relative order of the rest. (The only caller passes slices
// of unique indices, where "every" and "first" coincide.)
func removeIntValue(ints []int, v int) []int {
	out := make([]int, 0, len(ints)-1)
	for _, n := range ints {
		if n == v {
			continue
		}
		out = append(out, n)
	}
	return out
}
