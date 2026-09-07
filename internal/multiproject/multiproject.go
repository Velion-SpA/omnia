// Package multiproject is the pure, dependency-free ranking core for
// cross-project retrieval (plan item P5, `docs/conversational-retrieval-plan.md`).
//
// The problem it solves: "what am I blocked on across everything" is only
// half a fan-out problem. `mem_search`'s `all_projects` and `GET /search`
// with no `project` param already run a search against every project — the
// gap is ranking. Naively concatenating each project's results and sorting
// on their raw relevance signal lets whichever project is most active (more
// hits, higher absolute bm25/RRF scores just from having more content)
// dominate the merged list, so a genuinely cross-project question still
// comes back as one project's memories. Measured baseline: cross_project
// accuracy@1 was 0.250 and did not move under any single-corpus ranking
// configuration, because the defect is structural, not a weight to tune.
//
// The fix (per `mcp.MinMaxNormalizeRelevance`, `internal/mcp/recall_ranking.go`,
// which already does this exact arithmetic over one batch): take each
// project's own top-N results, min-max normalize the relevance signal
// WITHIN each project so every project's best hit lands at the same 1.0
// ceiling regardless of that project's absolute score scale, and only then
// merge across projects on the normalized scale.
//
// This package does not import internal/mcp. internal/mcp pulls in
// internal/codegraph, internal/embed, internal/audit, and the MCP SDK
// itself — none of which this arithmetic needs — and internal/mcp is the
// natural future caller of this package's Merge (the fan-out layer), so
// importing it here would risk a cycle the moment that wiring lands. Instead
// this package re-derives the same min-max shape locally over its own
// minimal Item type, following internal/recall's package doc precedent
// (`internal/recall/recall.go`): "Callers convert their own result type
// into [this package's] type at the wiring boundary" — the fan-out layer
// (not built here; see the package's final report) converts
// store.SearchResult batches into Item, calls Merge, and maps MergedItem
// back.
package multiproject

import (
	"math"
	"sort"
)

// Item is one candidate result from a single project's search, as seen by
// the cross-project merge core. It carries only the fields Merge needs to
// normalize, merge, and tie-break — mirroring internal/recall.LexicalHit's
// minimal-shape convention.
type Item struct {
	// ID is the observation's store ID. Used only for the final ID-ASC
	// tie-break; Merge never dereferences it.
	ID int64

	// Relevance is the raw, un-normalized relevance signal for this item as
	// computed by its own project's search — an RRF fusion score, a negated
	// FTS5 bm25 rank, or an already-ranked RankScore output. Merge is
	// agnostic to which: it only requires "higher is more relevant within
	// this Item's Group", the same contract mcp.RankResults' `relevance`
	// parameter already documents. A NaN value is treated as the lowest
	// possible relevance (0 after normalization) rather than propagated,
	// following the same "never let NaN poison a weighted score" precedent
	// as internal/mcp/recall_ranking.go's clampUnit (and the salience NaN
	// fix it was introduced for) — an un-computable relevance signal must
	// degrade gracefully, not corrupt every other row's normalization via
	// NaN's poisoning comparisons (NaN < x and NaN > x are both false, so a
	// NaN silently escapes min/max tracking if not special-cased).
	Relevance float64

	// UpdatedAt is a sortable timestamp (RFC3339 / SQLite datetime, the same
	// format store.Observation.UpdatedAt / recall.LexicalHit.UpdatedAt use)
	// used for the DESC tie-break.
	UpdatedAt string

	// Preempt marks a row that must never compete on normalized relevance:
	// the topic_key exact-match sentinel (store.SearchResult.Rank == -1000)
	// or a signature-match row (store.SearchResult.SignatureMatch). Both
	// pre-empt ranking identically in mcp.RankResults
	// (`r.Rank == exactSentinelRank || r.SignatureMatch`) and that OR is
	// deliberately collapsed into one boolean here too: this package has no
	// use for which reason a row pre-empts, only that it must be excluded
	// from its group's min-max batch and surfaced ahead of every normalized
	// row. Callers set Preempt for either source condition.
	//
	// A sentinel's relevance is an outlier by construction: -1000 or a
	// signature-match's Rank (-500 pre-negation) is not a real bm25/RRF
	// score, so including it in normalization would pin the group's max (or
	// min) and crush every ordinary row toward the opposite pole. Excluding
	// it — the same rule RankResults and MinMaxNormalizeRelevance already
	// enforce for the single-project case — is the single easiest thing to
	// get wrong when generalizing that rule to N groups, because it is easy
	// to remember to exclude it from ONE group and forget the rule applies
	// per group, independently, for every project in the merge.
	Preempt bool
}

// Group is one project's candidate set feeding the cross-project merge —
// the unit Merge normalizes over. Items is expected to already be that
// project's top-N results (N chosen by the fan-out layer that calls
// ScopedSearcher.SearchScoped once per project — see the package-level doc
// and this package's final report for what that layer must supply); Merge
// itself does not truncate, since doing so would require knowing N, which
// is a fan-out concern, not a ranking-arithmetic one.
type Group struct {
	// Project is the project name/slug this group's Items came from. Never
	// interpreted by Merge beyond carrying it through to MergedItem and
	// Diversity — it is opaque to the ranking arithmetic.
	Project string
	Items   []Item
}

// MergedItem is one Item after cross-project normalization and merge,
// carrying the project it came from and the [0,1] score it was ranked on.
type MergedItem struct {
	Item
	Project string

	// NormalizedScore is the [0,1] relevance Merge ranked this row on: for a
	// non-preempted row, its Relevance min-max normalized within its own
	// Group; for a preempted row, exactly 1.0 — mirroring
	// mcp.BuildResultReceipt's convention that pre-empting rows are always
	// treated as maximally relevant rather than left at the missing-value
	// zero a normalization map would otherwise produce for an ID it
	// deliberately never populated.
	NormalizedScore float64
}

// Merge normalizes each group's relevance signal independently (min-max,
// within that group only) and merges the result into one ranked list on the
// normalized [0,1] scale, so no project's absolute score scale — a function
// of how much content that project has, not how relevant any single result
// is to THIS query — determines merge order.
//
// Preempted rows (Item.Preempt) from every group are excluded from their
// group's normalization batch, then placed ahead of every normalized row in
// the output, sorted UpdatedAt DESC then ID ASC — the same deterministic
// order recall.Fuse already uses for its own exact-sentinel rows. Preempted
// rows never compete against each other or against normalized rows on
// score; there is nothing project-specific about "this row is an exact
// topic_key hit", so no project gets priority among preempted rows either.
//
// Normalized rows are sorted by NormalizedScore DESC, then UpdatedAt DESC,
// then ID ASC — recall.Fuse's tie-break convention
// (score, then presence-in-both-lists, then UpdatedAt DESC, then ID ASC)
// adapted to this domain: Fuse's middle "presence in both lists" dimension
// has no analog here, because every Item originates from exactly one
// project's search rather than from two parallel lexical/semantic lists, so
// that dimension collapses out and the remaining two (UpdatedAt DESC, ID
// ASC) carry the tie-break exactly as Fuse defines them. sort.SliceStable
// keeps ties beyond that fully deterministic across runs, since it never
// depends on map iteration order — the only maps Merge touches are consumed
// by iterating an already-ordered slice, never ranged over directly.
//
// Degenerate cases, all handled explicitly (not left to fall out of the
// arithmetic by accident):
//
//   - Exactly one non-empty Group: min-max normalization is order-preserving
//     (a monotonic rescale), so the normalized-row portion of Merge's output
//     is byte-for-byte the same ID order as sorting that group's Items by
//     raw Relevance DESC (same tie-break) without normalizing at all — there
//     is nothing to normalize AGAINST with only one group. A determinism/
//     no-op test in multiproject_test.go asserts this directly rather than
//     trusting the monotonicity argument.
//   - A group with exactly one normalized item, or every item in a group
//     sharing the same Relevance (min == max): normalizes to 1.0 for all of
//     them rather than dividing by zero — the same convention
//     mcp.MinMaxNormalizeRelevance documents and this package deliberately
//     reuses rather than inventing a different one.
//   - An empty Group (Items is nil or len 0): contributes nothing to the
//     output. Not an error.
//   - A Group whose Items are ALL preempted (no normal rows at all):
//     normalizeGroup never runs (nothing to min-max), so there is no
//     divide-by-zero risk from an empty normalization batch either.
func Merge(groups []Group) []MergedItem {
	var preempted []MergedItem
	var normal []MergedItem

	for _, g := range groups {
		var groupNormal []Item
		for _, it := range g.Items {
			if it.Preempt {
				preempted = append(preempted, MergedItem{
					Item:            it,
					Project:         g.Project,
					NormalizedScore: 1.0,
				})
				continue
			}
			groupNormal = append(groupNormal, it)
		}
		if len(groupNormal) == 0 {
			continue
		}
		scores := normalizeGroup(groupNormal)
		for _, it := range groupNormal {
			normal = append(normal, MergedItem{
				Item:            it,
				Project:         g.Project,
				NormalizedScore: scores[it.ID],
			})
		}
	}

	sort.SliceStable(preempted, func(i, j int) bool {
		return lessPreempted(preempted[i], preempted[j])
	})
	sort.SliceStable(normal, func(i, j int) bool {
		return lessNormalized(normal[i], normal[j])
	})

	out := make([]MergedItem, 0, len(preempted)+len(normal))
	out = append(out, preempted...)
	out = append(out, normal...)
	return out
}

// lessPreempted orders two preempted rows UpdatedAt DESC, then ID ASC —
// recall.Fuse's own exact-sentinel ordering, reused verbatim since preempted
// rows never carry a meaningful score to break ties on in the first place.
func lessPreempted(a, b MergedItem) bool {
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.ID < b.ID
}

// lessNormalized orders two normalized rows by NormalizedScore DESC, then
// UpdatedAt DESC, then ID ASC — see Merge's doc for how this adapts
// recall.Fuse's three-part tie-break to a domain with no "presence in both
// lists" dimension.
func lessNormalized(a, b MergedItem) bool {
	if a.NormalizedScore != b.NormalizedScore {
		return a.NormalizedScore > b.NormalizedScore
	}
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.ID < b.ID
}

// normalizeGroup rescales items' Relevance into [0,1], keyed by ID, using
// exactly mcp.MinMaxNormalizeRelevance's shape: when every value is equal
// (including the single-item case, where min == max trivially), every item
// normalizes to 1.0 rather than dividing by zero, so a downstream tie-break
// (UpdatedAt/ID) decides order instead of relevance silently zeroing out the
// whole group. NaN relevance values (see Item.Relevance's doc) are treated
// as 0 before entering the min/max scan, since NaN's comparisons are always
// false and would otherwise silently escape both bounds and then poison the
// final (v - min) / (max - min) division for that one item.
func normalizeGroup(items []Item) map[int64]float64 {
	clean := make(map[int64]float64, len(items))
	min, max := math.MaxFloat64, -math.MaxFloat64
	for _, it := range items {
		v := it.Relevance
		if math.IsNaN(v) {
			v = 0
		}
		clean[it.ID] = v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	out := make(map[int64]float64, len(items))
	for _, it := range items {
		if max <= min {
			out[it.ID] = 1
			continue
		}
		out[it.ID] = (clean[it.ID] - min) / (max - min)
	}
	return out
}

// DiversityReport is the direct, first-class measurement of how many
// distinct projects a merged, ranked result list actually surfaces within
// its top K — the specific failure this package exists to prevent
// (single-project domination) measured directly rather than inferred from
// accuracy, per the P5 plan item's own gate: "project diversity ≥ 2 in
// top-4".
type DiversityReport struct {
	// K is the window size the report was computed over (clamped to
	// len(merged) if the caller asked for a larger K than the list has).
	K int
	// Distinct is the number of unique Project values among the top K rows.
	Distinct int
	// Counts is Project -> occurrence count within the top K rows, for
	// callers that want more than the distinct count (e.g. to confirm a
	// single project isn't still taking, say, 3 of 4 slots even though 2
	// distinct projects are technically present).
	Counts map[string]int
}

// Diversity computes a DiversityReport over the first k rows of merged (a
// Merge output, already ranked). k <= 0 or k > len(merged) is clamped to
// len(merged); an empty merged list reports Distinct: 0 with an empty
// Counts map, never a panic or a division by a zero window.
func Diversity(merged []MergedItem, k int) DiversityReport {
	if k <= 0 || k > len(merged) {
		k = len(merged)
	}
	counts := make(map[string]int)
	for _, it := range merged[:k] {
		counts[it.Project]++
	}
	return DiversityReport{K: k, Distinct: len(counts), Counts: counts}
}
