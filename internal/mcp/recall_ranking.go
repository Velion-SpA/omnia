package mcp

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// This file is the memory-recall-ranking wiring boundary (spec:
// memory-recall-ranking): an optional recency x importance x relevance
// ranking pass over already-fused/searched results, plus the per-hit
// score-breakdown ("receipt") surfaced by mem_search's explain arg / `omnia
// search --explain`. It lives entirely in internal/mcp — never inside
// internal/recall or internal/store — so both of those stay untouched, pure
// leaves (design D6): internal/recall.Fuse's RRF fusion and
// internal/store.Store.Search's FTS5 query are unchanged by this slice.

// RankScore combines three normalized [0,1] components — relevance, recency,
// and importance — into one final ranking score via a weighted sum
// (Generative Agents' retrieval formula shape: score =
// w.Relevance*relevance + w.Recency*recency + w.Importance*importance).
// This is the single arithmetic primitive RankResults and the explain/
// score-breakdown surface both call, so the two can never drift apart.
func RankScore(relevance, recency, importance float64, w config.RankingWeights) float64 {
	return float64(w.Relevance)*relevance + float64(w.Recency)*recency + float64(w.Importance)*importance
}

// recencyTimeLayouts mirrors internal/store's own parseObservationTime
// accepted formats. Duplicated here (not imported — that helper is
// unexported) so ComputeRecency stays a self-contained primitive, testable
// without a live *store.Store, following the same "duplicate the constant,
// document why" convention internal/config.RecallConfig's doc already uses
// for internal/recall.DefaultFuseParams().
var recencyTimeLayouts = []string{"2006-01-02 15:04:05", time.RFC3339, time.RFC3339Nano, "2006-01-02"}

// ComputeRecency scores how fresh updatedAt is relative to now as an
// exponential half-life decay: 0.5^(elapsedDays/halfLifeDays). This is
// monotonically decreasing and always > 0 for any finite elapsed time
// (Requirement: Recency Decay Never Hard-Filters — recency alone must never
// be able to exclude a result), equal to exactly 1.0 at elapsed=0 and 0.5 at
// elapsed=halfLifeDays.
//
// ok is false when updatedAt cannot be parsed (e.g. empty or malformed).
// Callers MUST treat that as "component unavailable" and degrade gracefully
// (Requirement: Ranking and Explain Degrade Gracefully) rather than treating
// a zero-value recency as meaningful data.
func ComputeRecency(updatedAt string, now time.Time, halfLifeDays float64) (float64, bool) {
	t, err := parseRecencyTime(updatedAt)
	if err != nil {
		return 0, false
	}
	if halfLifeDays <= 0 {
		halfLifeDays = 14
	}
	elapsedDays := now.Sub(t).Hours() / 24
	if elapsedDays < 0 {
		elapsedDays = 0
	}
	return math.Pow(0.5, elapsedDays/halfLifeDays), true
}

func parseRecencyTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range recencyTimeLayouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

// ImportanceScore normalizes obsType's configured importance weight into
// [0,1] by dividing by the maximum configured weight across all types
// (Requirement: Importance Heuristic By Observation Type) — so
// decision/architecture (the highest default-weighted types) land at 1.0,
// and lower-weighted types land proportionally below that.
func ImportanceScore(obsType string, cfg config.RankingConfig) float64 {
	max := maxImportanceWeight(cfg)
	if max <= 0 {
		return 0
	}
	return float64(effectiveImportanceWeight(obsType, cfg)) / float64(max)
}

// effectiveImportanceWeight returns cfg.ImportanceOverrides[obsType] when
// the operator configured one, else config.DefaultImportanceWeight's
// heuristic-by-type default.
func effectiveImportanceWeight(obsType string, cfg config.RankingConfig) float32 {
	if w, ok := cfg.ImportanceOverrides[obsType]; ok {
		return w
	}
	return config.DefaultImportanceWeight(obsType)
}

// maxImportanceWeight is the ceiling ImportanceScore normalizes against: the
// highest of the three default heuristic tiers (config.DefaultImportanceWeight
// tops out at 3, for decision/architecture) or any override configuring a
// weight higher than that.
func maxImportanceWeight(cfg config.RankingConfig) float32 {
	max := config.DefaultImportanceWeight("decision")
	for _, w := range cfg.ImportanceOverrides {
		if w > max {
			max = w
		}
	}
	return max
}

// exactSentinelRank mirrors internal/store's unexported topicKeySentinelRank
// (-1000) — the same cross-package contract internal/mcp/recall_adapter.go's
// `r.Rank == -1000` check and internal/recall's LexicalHit.Exact already
// rely on. It MUST stay exactly -1000.
const exactSentinelRank = -1000.0

// RankResults re-sorts a fused/hydrated result set by RankScore (relevance x
// recency x importance — Requirement: Ranking Combines Relevance, Recency,
// and Importance) when cfg.Enabled. When cfg.Enabled is false (the default),
// results is returned completely untouched — same slice, same order — which
// is what makes handleSearch's always-called ranking pass byte-for-byte
// backward compatible (Requirement: Backward-Compatible Default Behavior).
//
// relevance supplies each result's un-normalized relevance signal, keyed by
// Observation.ID: RRF fusion Score for the hybrid recall path, or negated
// FTS5 bm25 Rank for the FTS5-only path (both wired in handleSearch) — higher
// is always more relevant regardless of source. relevance is min-max
// normalized across the batch before scoring so RankScore always sees a
// [0,1] relevance component no matter which source produced it.
//
// The topic_key exact-match sentinel (SearchResult.Rank == exactSentinelRank)
// always pre-empts ranking: sentinel rows stay first, in their original
// relative order, and never compete on RankScore — mirroring how they
// pre-empt RRF fusion in recall.Fuse itself.
//
// Signature-match rows (SearchResult.SignatureMatch, store's error-signature
// lane — design obs #1498, slice 1) pre-empt ranking the SAME way: on the
// FTS5-only relevance path, their Rank is store's signatureMatchRank (-500),
// so a naively-computed relevance[id] = -rank turns into a ~500 outlier that
// would pin itself to 1.0 and crush every ordinary row's normalized
// relevance toward 0 in the min-max batch below. Excluding them here (like
// the topic_key sentinel) keeps that outlier out of the normalization batch
// entirely, so ordinary rows' relevance keeps its real weight in RankScore's
// weighted sum instead of being silently overridden by recency/importance.
func RankResults(results []store.SearchResult, relevance map[int64]float64, cfg config.RankingConfig, now time.Time) []store.SearchResult {
	if !cfg.Enabled || len(results) == 0 {
		return results
	}

	var preempted []store.SearchResult
	var rest []store.SearchResult
	for _, r := range results {
		if r.Rank == exactSentinelRank || r.SignatureMatch {
			preempted = append(preempted, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(rest) == 0 {
		return results
	}

	normalized := MinMaxNormalizeRelevance(rest, relevance)

	type scoredResult struct {
		result store.SearchResult
		score  float64
	}
	scored := make([]scoredResult, 0, len(rest))
	for _, r := range rest {
		recency, ok := ComputeRecency(r.UpdatedAt, now, cfg.RecencyHalfLifeDays)
		if !ok {
			// Missing/unparseable UpdatedAt degrades to "no recency signal"
			// rather than failing the whole ranking pass (Requirement:
			// Ranking and Explain Degrade Gracefully).
			recency = 0
		}
		importance := ImportanceScore(r.Type, cfg)
		score := RankScore(normalized[r.ID], recency, importance, cfg.Weights)
		scored = append(scored, scoredResult{result: r, score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	out := make([]store.SearchResult, 0, len(results))
	out = append(out, preempted...)
	for _, s := range scored {
		out = append(out, s.result)
	}
	return out
}

// MinMaxNormalizeRelevance rescales relevance's values for exactly the IDs
// present in results into [0,1]. When every value is equal (or relevance has
// no entry for some ID, which defaults to 0 — a graceful-degradation
// convention, not a distinct case), every result normalizes to 1.0 rather
// than dividing by zero, so recency/importance alone break the tie instead
// of relevance silently zeroing out the whole score.
//
// Callers MUST exclude any pre-empting row (the topic_key exact-match
// sentinel, or a signature-match row) from results before calling this —
// mirroring RankResults' own exclusion — since either sentinel's relevance
// signal is an outlier by construction (not a real bm25/RRF score) and would
// otherwise pin the batch's max and crush every other row's normalized value
// toward 0.
//
// Exported (structural nit, memory-recall-ranking-receipts review) so
// cmd/omnia's `omnia search --explain` reuses this exact primitive instead of
// hand-rolling its own copy that can silently drift out of sync.
func MinMaxNormalizeRelevance(results []store.SearchResult, relevance map[int64]float64) map[int64]float64 {
	min, max := math.MaxFloat64, -math.MaxFloat64
	for _, r := range results {
		v := relevance[r.ID]
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	out := make(map[int64]float64, len(results))
	for _, r := range results {
		if max <= min {
			out[r.ID] = 1
			continue
		}
		out[r.ID] = (relevance[r.ID] - min) / (max - min)
	}
	return out
}

// BuildReceipt assembles the per-hit score-breakdown ("receipt") requested by
// mem_search's explain arg / `omnia search --explain` (Requirement: Per-Hit
// Score Breakdown). Every component is independently nullable via a pointer:
// a nil pointer means that component could not be computed at the call site
// (e.g. semantic recall disabled, or — for the hybrid recall path — the raw
// per-hit lexical/semantic sub-scores recall.Service/Fuse compute internally
// but do not currently expose past their combined RRF fusion score) and
// BuildReceipt nulls/omits it rather than guessing (Requirement: Ranking and
// Explain Degrade Gracefully).
//
// staleness_penalty is always 0 this slice — a reserved, forward-compatible
// slot for memory-structural-forgetting's downrank (obs #1595, Requirement
// 6) to populate later without a schema change here.
func BuildReceipt(lexicalRank *float64, exactMatch bool, semanticScore, fusionScore, recency, importance, final *float64) map[string]any {
	return map[string]any{
		"lexical":           lexicalComponent(lexicalRank, exactMatch),
		"semantic":          nullableFloat(semanticScore),
		"fusion":            nullableFloat(fusionScore),
		"recency":           nullableFloat(recency),
		"importance":        nullableFloat(importance),
		"final":             nullableFloat(final),
		"staleness_penalty": 0,
	}
}

// BuildResultReceipt assembles the explain score_breakdown for a single
// search result row. lexical/fusion sourcing depends on whether fusion
// actually ran for this query:
//
//   - Lexical-only path (fusionRan == false: recall disabled, or a hybrid
//     recall.Service configured but its search fell back to plain FTS5 mid-
//     query — see cmd/omnia's recallOrFTSSearchWithRelevance): r.Rank holds
//     the real FTS5 bm25 rank for a non-sentinel row, so lexical is
//     populated; fusion is nil (no RRF fusion ran this query — Requirement:
//     Ranking and Explain Degrade Gracefully's "semantic disabled" scenario).
//     fusionRan MUST reflect whether fusion actually produced these results,
//     not merely whether a recall.Service was configured — a configured
//     service that errored and fell back to lexical must still report
//     fusionRan=false, or explain mislabels a lexical value as fusion.
//   - Hybrid recall path (fusionRan == true): relevance[r.ID] holds the
//     already-fused RRF Score, so fusion is populated; lexical is nil — the
//     raw per-hit lexical rank recall.Fuse computes internally isn't
//     preserved past fusion/hydration (HydrateFusedResults only carries the
//     final ranked ID list), so it degrades to null rather than guessing.
//
// semantic is always nil this slice: the per-hit semantic cosine score is
// likewise internal to recall.Service.semanticHits and not currently
// exposed past the combined RRF fusion score — surfacing it would require
// extending recall.Service's public surface, deliberately deferred to keep
// internal/recall fully untouched this slice (design D6).
//
// recency/importance/final are always computed from rankingCfg and
// normalizedRelevance regardless of whether ranking is actually enabled, so
// explain stays useful as a standalone diagnostic even with ranking off —
// exact sentinel and signature-match rows are both treated as maximally
// relevant (1.0), mirroring RankResults' own pre-emption of both (see
// RankResults' doc): a caller that correctly excludes them from the
// normalization batch never populates normalizedRelevance[r.ID] for them, so
// this treats that omission as "maximally relevant" rather than the
// missing-map-key zero value, which would otherwise silently tank their
// "final" score.
//
// Exported (structural nit, memory-recall-ranking-receipts review) so
// cmd/omnia's `omnia search --explain` reuses this exact primitive instead of
// hand-rolling its own copy that can silently drift out of sync — hence
// fusionRan/rankingCfg are plain primitives rather than the internal/mcp-only
// MCPConfig type, so the CLI (which has no MCPConfig) can call this directly.
func BuildResultReceipt(r store.SearchResult, fusionRan bool, rankingCfg config.RankingConfig, relevance, normalizedRelevance map[int64]float64, now time.Time) map[string]any {
	exact := r.Rank == exactSentinelRank
	preempted := exact || r.SignatureMatch

	var lexicalRank *float64
	var fusionScore *float64
	if fusionRan {
		if !exact {
			if v, ok := relevance[r.ID]; ok {
				fusionScore = &v
			}
		}
	} else if !exact {
		rank := r.Rank
		lexicalRank = &rank
	}

	recency, recencyOK := ComputeRecency(r.UpdatedAt, now, rankingCfg.RecencyHalfLifeDays)
	var recencyPtr *float64
	recencyForScore := 0.0
	if recencyOK {
		recencyPtr = &recency
		recencyForScore = recency
	}

	importance := ImportanceScore(r.Type, rankingCfg)

	normRel := 1.0
	if !preempted {
		normRel = normalizedRelevance[r.ID]
	}
	final := RankScore(normRel, recencyForScore, importance, rankingCfg.Weights)

	return BuildReceipt(lexicalRank, exact, nil, fusionScore, recencyPtr, &importance, &final)
}

func lexicalComponent(rank *float64, exact bool) map[string]any {
	return map[string]any{
		"rank":        nullableFloat(rank),
		"exact_match": exact,
	}
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
