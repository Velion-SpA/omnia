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
// comes back as one project's memories.
//
// # History: the normalization approach and why it made things worse
//
// The first version of this package fixed domination by min-max normalizing
// each project's relevance INTERNALLY (every project's best hit rescaled to
// 1.0) before merging on that normalized scale. It shipped with a passing
// unit suite and a documented, deliberately-handled degenerate case: when a
// project contributes exactly one result, min == max trivially, and that
// result normalizes to 1.0 regardless of its absolute relevance.
//
// Wired up against the live store (~49 projects) that degenerate case turned
// out to be the COMMON case, not the edge case: with most projects
// contributing exactly one hit each, most of the merged list tied at 1.0,
// and the tie-break — UpdatedAt DESC, i.e. recency — decided nearly every
// position. Measured result: cross_project accuracy@1 went from 0.250
// (naive concatenation, the pre-existing baseline) to 0.000 (normalized
// merge). Normalization did not degrade relevance ordering, it destroyed it,
// because it threw away the one piece of cross-project ordering signal that
// was already usable and replaced it with a coin flip on recency.
//
// That usable signal: this package's typical Item.Relevance source is an
// RRF (Reciprocal Rank Fusion) score, `Σ w/(RRFK + rank)`. RRF is a
// rank-position combinator, not a magnitude of relevance — a project's #1
// hit therefore scores approximately the same as any other project's #1
// hit BY CONSTRUCTION, because both are `w/(RRFK + 1)` regardless of that
// project's absolute content volume or score scale (engram obs
// `architecture/rrf-score-is-not-a-confidence-signal`). Cross-project scores
// at equal rank are already comparable without any rescaling. Normalization
// existed to solve "the most active project dominates the merged list" — but
// that domination was never because that project's scores were higher, it
// was because it contributed MORE ITEMS. Solving an item-count problem by
// rescaling a score axis that didn't have the problem threw away real signal
// to fix a different, uninvolved axis.
//
// # The fix: cap the axis that actually causes domination, merge on the
// axis that actually carries ordering signal
//
//   - Domination is an item-COUNT problem → fix it by capping item count:
//     Merge takes a small per-project cap K (DefaultPerProjectCap) and never
//     lets a single project contribute more than K rows to the merged list,
//     regardless of how many rows that project's Group carries.
//   - Ordering is what raw Relevance already does adequately at equal rank
//     → stop transforming it. Merge sorts the capped pool by raw
//     Item.Relevance, descending. No per-group rescaling of any kind.
//
// Each mechanism does exactly one job, instead of one mechanism (min-max
// normalization) being asked to do both and doing neither well.
//
// This package does not import internal/mcp. internal/mcp pulls in
// internal/codegraph, internal/embed, internal/audit, and the MCP SDK
// itself — none of which this arithmetic needs — and internal/mcp is the
// natural future caller of this package's Merge (the fan-out layer), so
// importing it here would risk a cycle the moment that wiring lands. Instead
// this package re-derives its own minimal Item type, following
// internal/recall's package doc precedent (`internal/recall/recall.go`):
// "Callers convert their own result type into [this package's] type at the
// wiring boundary" — the fan-out layer (cmd/omnia/crossproject.go) converts
// store.SearchResult batches into Item, calls Merge, and maps MergedItem
// back.
package multiproject

import (
	"math"
	"sort"
)

// Item is one candidate result from a single project's search, as seen by
// the cross-project merge core. It carries only the fields Merge needs to
// cap, merge, and tie-break — mirroring internal/recall.LexicalHit's
// minimal-shape convention.
type Item struct {
	// ID is the observation's store ID. Used only for the final ID-ASC
	// tie-break; Merge never dereferences it.
	ID int64

	// Relevance is the raw, un-normalized relevance signal for this item as
	// computed by its own project's search — an RRF fusion score, a negated
	// FTS5 bm25 rank, or an already-ranked RankScore output. Merge is
	// agnostic to which: it only requires "higher is more relevant", the
	// same contract mcp.RankResults' `relevance` parameter already
	// documents, and (per the package doc's RRF discussion) that contract
	// already holds comparably ACROSS projects at equal rank, which is what
	// makes merging on this value directly — instead of normalizing it away
	// — correct.
	//
	// A NaN value is treated as 0 rather than propagated, following the
	// same "never let NaN poison a score" precedent as
	// internal/mcp/recall_ranking.go's clampUnit (and the salience NaN fix
	// it was introduced for) — an un-computable relevance signal must
	// degrade gracefully, not corrupt a sort via NaN's poisoning comparisons
	// (NaN < x and NaN > x are both false, so a NaN silently escapes both
	// ends of an ordering if not special-cased). 0 is a defined, stable
	// value to degrade to — it is NOT guaranteed to be the lowest possible
	// value on this raw, potentially-negative scale (a negated bm25 rank can
	// be more negative than 0), the same scope this precedent has always
	// had: no poisoning, not a guaranteed minimum.
	Relevance float64

	// UpdatedAt is a sortable timestamp (RFC3339 / SQLite datetime, the same
	// format store.Observation.UpdatedAt / recall.LexicalHit.UpdatedAt use)
	// used for the DESC tie-break.
	UpdatedAt string

	// Preempt marks a row that must never compete on relevance: the
	// topic_key exact-match sentinel (store.SearchResult.Rank == -1000) or a
	// signature-match row (store.SearchResult.SignatureMatch). Both preempt
	// ranking identically in mcp.RankResults
	// (`r.Rank == exactSentinelRank || r.SignatureMatch`) and that OR is
	// deliberately collapsed into one boolean here too: this package has no
	// use for which reason a row preempts, only that it must be excluded
	// from its group's per-project cap and raw-relevance sort entirely, and
	// surfaced ahead of every capped, sorted row.
	//
	// A preempted row's relevance is an outlier by construction (-1000, or a
	// signature match's pre-negation -500) and carries no comparative
	// meaning against ordinary rows, so it is never used to decide order —
	// only UpdatedAt/ID tie-break preempted rows against each other. Callers
	// set Preempt for either source condition.
	Preempt bool
}

// Group is one project's candidate set feeding the cross-project merge.
// Items is expected to already be that project's own top-N results (N
// chosen by the fan-out layer that calls ScopedSearcher.SearchScoped once
// per project and then ranks in-place — see the package-level doc and
// cmd/omnia/crossproject.go's own doc for what that layer supplies).
//
// Group's N and Merge's K are two independent, differently-scoped knobs —
// do not conflate them:
//
//   - N (len(Items), set by the caller before Group even reaches Merge) is a
//     FETCH-SIZE concern: how many rows are worth hydrating and ranking per
//     project at all. N is typically larger than K, both because ranking
//     needs a wider pool to choose from and because the fan-out layer may
//     want headroom for Merge to pick the true top-K BY RAW RELEVANCE, which
//     can differ from the RankScore order Items may already be sorted in.
//   - K (Merge's own parameter, see DefaultPerProjectCap) is an
//     ANTI-DOMINATION concern: of a project's N candidates, at most K make
//     it into the cross-project merged list, so no single project's item
//     COUNT can crowd out every other project regardless of N.
//
// N bounds what Merge is allowed to choose FROM; K bounds what Merge
// actually KEEPS. N >= K for K to have any effect; N < K makes K a no-op for
// that group (Merge never pads a short list back up to K).
type Group struct {
	// Project is the project name/slug this group's Items came from. Never
	// interpreted by Merge beyond carrying it through to MergedItem and
	// Diversity — it is opaque to the ranking arithmetic.
	Project string
	Items   []Item
}

// MergedItem is one Item after cross-project capping and merge, carrying
// the project it came from and the score it was ranked on.
type MergedItem struct {
	Item
	Project string

	// Score is the value Merge ranked this row on: Item.Relevance, cleaned
	// of NaN (see Item.Relevance's doc) but otherwise untransformed — for
	// every row, preempted or not.
	//
	// Score deliberately duplicates the embedded Item.Relevance field
	// (modulo the NaN clean) rather than being dropped now that Merge no
	// longer rescales it. This field used to hold something Item.Relevance
	// did NOT — a [0,1] min-max-normalized value — and that WAS the defect
	// (see package doc): the field's name asserted a transformation whose
	// output actively hurt ranking. Now it holds exactly what it says it
	// holds and nothing more. Keeping the field (rather than deleting it and
	// forcing every caller to know "Score happens to equal Relevance right
	// now") insulates callers from that being an implementation detail
	// rather than a promise: if this package's ranking arithmetic changes
	// again, Score is still "the value Merge ranked this row on", by
	// definition, and Item.Relevance is still "the raw input signal".
	Score float64
}

// DefaultPerProjectCap is the per-project contribution cap Merge applies
// when the caller passes k <= 0.
//
// Chosen as the LARGEST cap that still preserves a provable property over
// the plan's own measurement gate ("project diversity >= 2 in top-4",
// crossProjectDiversityWindow in cmd/omnia/crossproject.go): with K <= 3, no
// single project can supply more than 3 of the top 4 merged rows (a project
// contributes at most K rows to the ENTIRE merged output, so it can occupy
// at most min(K, 4) of any 4-row window) — which structurally forces
// Distinct >= 2 in the top-4 whenever at least one other project also has a
// contributing row. K == 4 would forfeit that property outright (one project
// could legitimately fill all 4 slots). K == 2 keeps the same property with
// more margin but caps a genuinely-dominant, genuinely-correct project down
// to 2 surfaced results even when 3 of its own results are the right answer
// — a real recall cost for no additional guarantee. 3 is the point where
// tightening further stops buying anything.
//
// This is a default, not a hard limit: callers needing a different
// recall/anti-domination trade-off pass their own k to Merge. See Merge's
// own doc for exactly what guarantee survives a caller override.
const DefaultPerProjectCap = 3

// Merge caps each group's contribution to at most k rows (DefaultPerProjectCap
// if k <= 0), then merges the capped pool into one ranked list by raw
// Item.Relevance, descending — no normalization, no rescaling. See the
// package doc for the measured reason this replaced per-group min-max
// normalization (cross_project accuracy@1 0.250 -> 0.000 under
// normalization) and why raw relevance is the right merge axis (RRF's
// rank-position construction already makes cross-project scores comparable
// at equal rank).
//
// Preempted rows (Item.Preempt) from every group are excluded from their
// group's cap and sort entirely, then placed ahead of every other row in the
// output, sorted UpdatedAt DESC then ID ASC — the same deterministic order
// recall.Fuse already uses for its own exact-sentinel rows. Preempted rows
// never compete against each other or against normal rows on score.
//
// Normal rows are sorted by Score DESC, then UpdatedAt DESC, then ID ASC —
// recall.Fuse's tie-break convention (score, then presence-in-both-lists,
// then UpdatedAt DESC, then ID ASC) adapted to this domain: Fuse's middle
// "presence in both lists" dimension has no analog here, because every Item
// originates from exactly one project's search rather than from two
// parallel lexical/semantic lists, so that dimension collapses out and the
// remaining two (UpdatedAt DESC, ID ASC) carry the tie-break exactly as Fuse
// defines them. sort.SliceStable keeps ties beyond that fully deterministic
// across runs, since it never depends on map iteration order — Merge
// touches no maps at all.
//
// The cap only engages when MORE THAN ONE group contributes at least one
// normal (non-preempted) row. With zero or one contending group there is no
// domination to protect against — capping anyway would only throw away that
// one project's own legitimately-relevant results for no benefit — so
// Merge's single-group behavior stays byte-for-byte identical to sorting
// that one group's Items by raw Relevance (same tie-break) and not calling
// Merge at all. TestMerge_SingleGroup_MatchesNoMergeOrder asserts this
// directly.
//
// # What the cap does and does not guarantee
//
// The cap bounds a project's contribution to the ENTIRE merged output at k
// rows — this is unconditional (given k <= len(that group's normal items),
// obviously) and holds regardless of the data. What it does NOT guarantee
// unconditionally is the plan's top-4 diversity gate: that follows only when
// (a) k <= 3 (see DefaultPerProjectCap's doc for why 3 is the boundary) and
// (b) at least one OTHER project also has a normal row to contribute. If a
// query is genuinely single-project — no other project's search actually
// returned anything relevant — no merge strategy can conjure diversity out
// of data that isn't there, and Distinct == 1 in that case is the CORRECT
// answer, not a defect. TestMerge_CapGuaranteesTopFourDiversityWhenMultipleProjectsContend
// exercises the guarantee's positive case; it is not a claim that every
// cross-project query will hit it.
//
// # Degenerate cases, all handled explicitly (not left to fall out of the
// arithmetic by accident)
//
//   - Exactly one contending group: see above, no cap applied, same order as
//     sorting by raw Relevance and not merging at all.
//   - An empty Group (Items is nil or len 0): contributes nothing to the
//     output, and is not counted as a contending group. Not an error.
//   - A Group whose Items are ALL preempted (no normal rows at all): not
//     counted as a contending group either, same as an empty Group.
//   - A group with exactly one normal item, or many groups each contributing
//     exactly one item (the ~49-projects-mostly-one-hit-each shape that
//     broke the normalization approach in production): every item keeps its
//     own raw Relevance, so the highest-relevance single-item group wins —
//     see TestMerge_ManySingleItemGroups_HighestRelevanceWinsNotMostRecent,
//     the regression test for exactly this production failure.
func Merge(groups []Group, k int) []MergedItem {
	if k <= 0 {
		k = DefaultPerProjectCap
	}

	type normalGroup struct {
		project string
		items   []Item
	}

	var preempted []MergedItem
	var normalGroups []normalGroup

	for _, g := range groups {
		var items []Item
		for _, it := range g.Items {
			if it.Preempt {
				preempted = append(preempted, MergedItem{
					Item:    it,
					Project: g.Project,
					Score:   cleanRelevance(it.Relevance),
				})
				continue
			}
			items = append(items, it)
		}
		if len(items) == 0 {
			continue
		}
		normalGroups = append(normalGroups, normalGroup{project: g.Project, items: items})
	}

	// The cap only has anti-domination work to do when >= 2 groups are
	// actually contending for the merged list — see Merge's own doc.
	applyCap := len(normalGroups) > 1

	var normal []MergedItem
	for _, ng := range normalGroups {
		items := ng.items
		sort.SliceStable(items, func(i, j int) bool {
			return scoreOrder(
				cleanRelevance(items[i].Relevance), items[i].UpdatedAt, items[i].ID,
				cleanRelevance(items[j].Relevance), items[j].UpdatedAt, items[j].ID,
			)
		})
		if applyCap && len(items) > k {
			items = items[:k]
		}
		for _, it := range items {
			normal = append(normal, MergedItem{
				Item:    it,
				Project: ng.project,
				Score:   cleanRelevance(it.Relevance),
			})
		}
	}

	sort.SliceStable(preempted, func(i, j int) bool {
		return lessPreempted(preempted[i], preempted[j])
	})
	sort.SliceStable(normal, func(i, j int) bool {
		return scoreOrder(
			normal[i].Score, normal[i].UpdatedAt, normal[i].ID,
			normal[j].Score, normal[j].UpdatedAt, normal[j].ID,
		)
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

// scoreOrder reports whether (scoreA, updatedAtA, idA) sorts strictly before
// (scoreB, updatedAtB, idB) under Merge's shared tie-break: score DESC, then
// UpdatedAt DESC, then ID ASC. Shared between the per-group cap-selection
// sort and the final cross-group sort so both stages use exactly the same
// ordering rule — there is only one place that rule is spelled out.
func scoreOrder(scoreA float64, updatedAtA string, idA int64, scoreB float64, updatedAtB string, idB int64) bool {
	if scoreA != scoreB {
		return scoreA > scoreB
	}
	if updatedAtA != updatedAtB {
		return updatedAtA > updatedAtB
	}
	return idA < idB
}

// cleanRelevance returns v, or 0 if v is NaN. See Item.Relevance's doc for
// why 0 (a defined, non-poisoning value, not a claimed guaranteed minimum)
// is the precedent this package reuses rather than inventing a new one.
func cleanRelevance(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
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
