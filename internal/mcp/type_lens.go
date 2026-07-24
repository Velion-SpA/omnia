package mcp

// type_lens.go — Omnia v0.3 "Context Economy" PR5 (design obs #1643 section
// 3.5, ADR-6; spec obs #1642 type-as-lens domain, all 6 REQs): a read-time
// situational type BOOST over already-ranked, hydrated content — inferring
// from the query text that a memory type is more likely relevant (e.g. an
// error-ish query implies bugfix-type memories) and lifting matching rows
// above non-matching ones, on top of the existing 3-tier importance
// weighting (spec: Composes With Existing Importance Tiers), WITHOUT
// excluding anything and WITHOUT disturbing sentinel/signature pre-emption
// (spec: Sentinel and Signature Pre-Emption Invariant; see
// preemption_invariant_test.go's shared adversarial table).
//
// Decision (design section 3.5): the lens is a RANKING-LAYER BOOST, never a
// hard opts.Type filter — mirrors the codebase's existing "downrank/boost,
// NEVER hard-filter" philosophy (ComputeRecency, AdaptiveFloor,
// StalenessPenaltyFor). A situational type inference is a heuristic guess;
// hard-filtering on it would exclude relevant non-matching-type memories on
// that guess — the wrong failure mode for an opt-in, eval-tunable feature.
//
// ApplyTypeLens mirrors ApplyStalenessDownrank's own lift/sink
// stable-partition shape (it lifts; ApplyStalenessDownrank sinks) and is
// wired in handleSearch's pipeline BEFORE ApplyMMR/ApplyTokenBudget (it
// changes which rows are "top," so it must run before diversity dedup and
// the final trim — design section 2).

import (
	"regexp"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// lensSignal pairs a case-insensitive substring regex with the lens type it
// implies (design section 3.5's ordered EN+ES signal table). Order matters:
// InferLensType returns the FIRST matching entry's lens type — "first match
// wins; the table is a small ordered rule list" (design section 3.5). This
// is a heuristic, eval-measured, opt-in classifier — not a claim of correct
// classification (design: "not a claim of correct classification").
//
// Signals match on WORD BOUNDARIES (\b), not bare substrings, so everyday
// dev vocabulary like "prefix", "suffix", "fixture", "affix" never trips the
// "fix" signal (adversarial-review finding, v0.3). Inflected forms are
// spelled out explicitly (fix/fixes/fixed/fixing, error/errors/errores, ...)
// because \b would otherwise reject plurals. `arregl` stays a stem —
// RE2's ASCII \b treats a following accented rune (arreglé) as a boundary,
// so the stem still matches all its conjugations.
//
// KNOWN LIMITATION (narrower than before, still real): ambiguous FULL words
// still fire — "fixed size", "design tokens", "error budget" carry no
// bug/architecture intent but match legitimately spelled signals. Accepted
// for v0.3, same accepted-heuristic posture as mmr.go's tokenizer note;
// eval-driven tuning is the follow-up if false positives matter in practice.
var lensSignals = []struct {
	pattern  *regexp.Regexp
	lensType string
}{
	// error/panic/exception/crash/stack(-| )trace/falla/fallo (design row 1)
	// + fix/broken/not working/no funciona/arregl (design row 2) both map to
	// "bugfix" — combined into one entry since first-match-wins only cares
	// about which lensType a row resolves to, not which sub-signal fired.
	// Full words carry a closing \b too (so "fixtures" can't match via bare
	// "fix" + empty optional suffix); the `arregl` STEM is its own
	// alternative with only a leading \b, since its conjugations
	// (arreglar/arreglé/arregla) must keep matching.
	{regexp.MustCompile(`(?i)(\b(error(s|es)?|panic(s|ked|king)?|exception(s|es)?|crash(es|ed|ing)?|stack ?trace|falla(s|ndo)?|fallos?|fix(es|ed|ing)?|broken|not working|no funciona)\b|\barregl)`), "bugfix"},
	{regexp.MustCompile(`(?i)\b(decide(s|d)?|decision(s|es)?|decisi[oó]n|tradeoffs?|choose|chose|elegir|decidir)\b`), "decision"},
	{regexp.MustCompile(`(?i)\b(architectures?|architectural|design(s|ed|ing)?|patterns?|arquitecturas?|patr[oó]n|patrones|dise[nñ]os?)\b`), "architecture"},
	{regexp.MustCompile(`(?i)\b(how to|steps?|procedures?|c[oó]mo|pasos?|procedimientos?)\b`), "pattern"},
}

// InferLensType infers a situational type lens from query (design section
// 3.5, spec: Situational Type Boost/Filter). explicitType is the caller's
// own opts.Type filter, if any — when non-empty, the user already expressed
// intent, so the lens ALWAYS stands down (spec: Explicit User Filter Always
// Wins), returning "" regardless of any signal query itself carries.
// Returns "" (no lens) when no table entry matches — a neutral/unrecognized
// query leaves ranking unchanged (spec: Neutral Context Leaves Ranking
// Unchanged).
func InferLensType(query, explicitType string) string {
	if explicitType != "" {
		return ""
	}
	for _, sig := range lensSignals {
		if sig.pattern.MatchString(query) {
			return sig.lensType
		}
	}
	return ""
}

// ApplyTypeLens stable-partitions results into
// [preempted, matchesLens, rest] so rows whose Type equals lensType are
// lifted above non-matching rows (spec: Situational Type Boost/Filter),
// mirroring ApplyStalenessDownrank's own lift/sink stable-partition shape
// in anchor_adapter.go (that pass sinks stale rows; this one lifts matching
// rows) — WITHOUT excluding anything (spec: the lens boosts, it never
// hard-filters) and WITHOUT disturbing pre-emption (spec: Sentinel and
// Signature Pre-Emption Invariant). Relative order is preserved within each
// partition, so the boost composes with — never replaces — whatever
// baseline order (importance tier, recency, relevance) the prior passes
// already established (spec: Composes With Existing Importance Tiers).
//
// It is a gated no-op — results returned completely untouched, same slice,
// same order — when !cfg.Enabled, lensType == "" (explicit-filter-wins or
// no situational signal detected — spec: Explicit User Filter Always
// Wins / Neutral Context Leaves Ranking Unchanged), or results is empty
// (spec: Disabled by Default, No-Op When Off). It is also untouched when
// lensType matches no row at all — nothing to lift, so the original slice
// is returned rather than a reconstructed-but-identical copy.
func ApplyTypeLens(results []store.SearchResult, lensType string, cfg config.TypeLensConfig) []store.SearchResult {
	if !cfg.Enabled || lensType == "" || len(results) == 0 {
		return results
	}

	var preempted, matches, rest []store.SearchResult
	for _, r := range results {
		switch {
		case r.Rank == exactSentinelRank || r.SignatureMatch:
			preempted = append(preempted, r)
		case r.Type == lensType:
			matches = append(matches, r)
		default:
			rest = append(rest, r)
		}
	}
	if len(matches) == 0 {
		return results
	}

	out := make([]store.SearchResult, 0, len(results))
	out = append(out, preempted...)
	out = append(out, matches...)
	out = append(out, rest...)
	return out
}
