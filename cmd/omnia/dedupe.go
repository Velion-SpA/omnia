package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/similarity"
	"github.com/velion/omnia/internal/store"
)

// dedupe.go — `omnia dedupe`: an offline, candidate-filtered near-duplicate
// scan over the EXISTING observation base (omnia-0.3.1-write-hygiene, design
// obs #1668 D9, spec dedupe-scan). PR8 shipped the propose-only (dry-run)
// scan; PR9 (this addition) adds the ONLY mutation path: `--apply
// <cluster-id>`, which merges exactly one named cluster — supersede relation
// + soft-delete per loser, canonical left untouched. There is no `--apply
// all`: the resolved scope decision (tasks.md PR9 note, obs #1663/#1665
// context) narrows the spec's original "(or explicit 'all')" wording to a
// single explicit cluster id per invocation, superseding that spec text.

// dedupeScanThreshold is the minimum content Jaccard similarity for two
// observations to be unioned into the same proposed cluster (design D9 /
// tasks 8.1: "union-find clusters pairs with Jaccard >= 0.9"). This is
// deliberately INCLUSIVE (>=), unlike the live write-gate's AUTO-UPDATE
// boundary (store.evaluateWriteGate uses a strict > 0.9) — dedupe-scan is a
// coarser, explicit, offline reconciliation pass over data that may predate
// the write-gate entirely (or was inserted through a path that bypassed it),
// not the per-save ladder, so the spec/design pin their own scan threshold
// rather than reusing store's unexported update-threshold constant.
const dedupeScanThreshold = 0.9

// dedupeCandidateLimit mirrors ScanProject's own hardcoded FindCandidates
// Limit (internal/store/relations.go, Phase 3 "walk observations" loop) —
// the FTS-blocked pre-filter cap that keeps this scan O(n * limit), never an
// O(n^2) all-pairs comparison, at real-corpus scale (obs #1662: 1660+
// observations; spec dedupe-scan REQ "Candidate Pre-Filter, Not All-Pairs").
const dedupeCandidateLimit = 10

// dedupeScanLimit is passed to Store.AllObservations so a scan sees the
// ENTIRE corpus for the requested scope, not AllObservations' small
// MaxContextResults-derived default (20, tuned for recall/context assembly,
// not a full reconcile pass). `omnia dedupe` is an explicit, offline,
// user-initiated command — an intentionally large, effectively-unbounded cap
// here is correct and does not affect any read-time caller elsewhere.
const dedupeScanLimit = 1_000_000

// dedupeCandidateBM25Floor is deliberately far more permissive than
// FindCandidates' own default floor (-2.0, tuned for the live single-write
// write-gate decision against typically-similar-length prose). Dedupe-scan
// is an offline, exhaustive reconciliation pass over an existing corpus of
// unknown shape/uniformity; BM25's IDF component grows with corpus size for
// any genuinely RARE (but real) title term, which can push a true
// near-duplicate pair's score below a tight floor at 1600+-observation scale
// even though it is exactly the kind of match dedupe-scan exists to find.
// The per-observation candidate LIMIT (dedupeCandidateLimit) — not this
// floor — is what keeps the scan bounded (never O(n^2)); relaxing the floor
// only widens WHICH of the top-limit-ranked candidates are considered, it
// does not change the scan's complexity. False positives here are cheap: a
// candidate still has to clear the separate, much stricter content-Jaccard
// >= dedupeScanThreshold test before it is ever unioned into a cluster. A
// var (not const) because store.CandidateOptions.BM25Floor is a *float64.
var dedupeCandidateBM25Floor = -1000.0

// dedupeDryRunNote is the explicit, printed-every-time statement on the
// propose-only path that nothing was mutated (spec dedupe-scan REQ
// "Propose-Only By Default"). The mutation path (`--apply <cluster-id>`)
// never prints this note — see cmdDedupe.
const dedupeDryRunNote = "This is a dry-run proposal only. No observation was merged, deleted, or otherwise mutated. Re-run with --apply <cluster-id> (exactly one cluster id, no --apply all) to merge a specific cluster."

// dedupeApplyActor identifies this command as the actor of record on both
// halves of an apply mutation: the soft-delete
// (Store.DeleteObservationWithActor's actor param) and the supersede
// relation's marked_by_actor (design D9: "marked_by system:dedupe"). Reused
// verbatim for both so the two mutation primitives attribute to the same
// caller.
const dedupeApplyActor = "system:dedupe"

// dedupeClusterMember is one observation inside a proposed merge cluster.
type dedupeClusterMember struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Project   string `json:"project"`
	CreatedAt string `json:"created_at"`
	// Canonical marks the proposed survivor (design D9: NEWEST by created_at,
	// tie-break max id — see runDedupeScan's rankOf doc comment).
	Canonical bool `json:"canonical"`
}

// dedupeClusterEvidence is one pairwise content-Jaccard score that
// contributed to a cluster's formation — the similarity evidence a human (or
// a future --apply) reviews before ever merging anything.
type dedupeClusterEvidence struct {
	AID     int64   `json:"a_id"`
	BID     int64   `json:"b_id"`
	Jaccard float64 `json:"jaccard"`
}

// dedupeCluster is one proposed near-duplicate merge group.
type dedupeCluster struct {
	// ClusterID is CONTENT-ADDRESSED: derived (via dedupeClusterID) from a
	// sha256 hash of the cluster's FULL, sorted member-id set — not just the
	// smallest member id (tasks 8.1's original "derived from member IDs"
	// wording, tightened by a PR9 adversarial-review fix: "membership-drift
	// TOCTOU"). Deliberately NOT union-find's internal root (arbitrary,
	// order-dependent) and NOT scan/processing order, so the SAME underlying
	// data always proposes the SAME cluster id across repeated runs — AND,
	// just as importantly, ANY membership change (a new near-duplicate
	// joining, an existing member leaving, or being soft-deleted) changes
	// every hash input and therefore the id.
	//
	// This closes a real TOCTOU: under the OLD smallest-member-id scheme, a
	// cluster id could stay identical even after a brand-new near-duplicate
	// joined it, because a new row almost always gets the LARGEST id, never
	// the smallest. An operator could review a 2-member cluster, then a
	// third near-duplicate could land before they ran `--apply`, and the
	// SAME old cluster id would still resolve — now against 3 members — so
	// `--apply` would silently merge the never-reviewed third member. With
	// the id now covering the WHOLE member set, that fresh member changes
	// the id, so a stale `--apply <old-id>` refuses via
	// applyDedupeCluster's existing "cluster not found" path (spec
	// dedupe-scan "Stale Cluster Apply Fails Safely") instead of silently
	// merging it.
	ClusterID   string                  `json:"cluster_id"`
	CanonicalID int64                   `json:"canonical_id"`
	Members     []dedupeClusterMember   `json:"members"`
	Evidence    []dedupeClusterEvidence `json:"evidence"`
}

// dedupeClusterProjects returns the sorted, deduplicated set of distinct
// project names among a cluster's members (real-data validation obs #1683
// battery I / issue #171: --apply's cross-project refusal check). Members
// are already annotated with their own project (dedupeClusterMember.Project,
// populated regardless of scan mode), so this needs no store access.
func dedupeClusterProjects(members []dedupeClusterMember) []string {
	seen := map[string]bool{}
	var projects []string
	for _, m := range members {
		if !seen[m.Project] {
			seen[m.Project] = true
			projects = append(projects, m.Project)
		}
	}
	sort.Strings(projects)
	return projects
}

// dedupeReport is the full `omnia dedupe` proposal (and `--json` payload).
type dedupeReport struct {
	Project       string          `json:"project,omitempty"`
	Scanned       int             `json:"scanned"`
	ClustersFound int             `json:"clusters_found"`
	Clusters      []dedupeCluster `json:"clusters"`
	DryRun        bool            `json:"dry_run"`
	Note          string          `json:"note"`
	// AllProjects mirrors the --all-projects flag that produced this report
	// (real-data validation obs #1683 battery I / issue #171): omitted
	// (false) keeps existing --json output byte-for-byte unchanged.
	AllProjects bool `json:"all_projects,omitempty"`

	// PairsScored is a diagnostic proving candidate generation stayed
	// FTS-blocked (O(n*dedupeCandidateLimit)) rather than all-pairs
	// (O(n^2)) at production scale (spec dedupe-scan REQ "Candidate
	// Pre-Filter, Not All-Pairs"). Not part of the public --json contract.
	PairsScored int `json:"-"`
}

// dedupeUnionFind is a minimal disjoint-set over int64 observation IDs, with
// path compression and union by rank.
type dedupeUnionFind struct {
	parent map[int64]int64
	rank   map[int64]int
}

func newDedupeUnionFind() *dedupeUnionFind {
	return &dedupeUnionFind{parent: map[int64]int64{}, rank: map[int64]int{}}
}

func (u *dedupeUnionFind) find(x int64) int64 {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
		return x
	}
	if u.parent[x] != x {
		u.parent[x] = u.find(u.parent[x])
	}
	return u.parent[x]
}

func (u *dedupeUnionFind) union(a, b int64) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if u.rank[ra] < u.rank[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	if u.rank[ra] == u.rank[rb] {
		u.rank[ra]++
	}
}

// dedupeClusterID derives a CONTENT-ADDRESSED cluster id from the FULL,
// sorted set of member observation ids (see dedupeCluster.ClusterID's doc
// comment for the "membership-drift TOCTOU" rationale this closes). sortedIDs
// MUST already be sorted ascending by the caller so the hash input — and
// therefore the id — never depends on scan/processing order, only on WHICH
// ids are members. "d" + the first 10 hex chars of sha256(joined ids) keeps
// ids short while remaining effectively collision-free for any realistic
// corpus size.
func dedupeClusterID(sortedIDs []int64) string {
	parts := make([]string, len(sortedIDs))
	for i, id := range sortedIDs {
		parts[i] = strconv.FormatInt(id, 10)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return "d" + hex.EncodeToString(sum[:])[:10]
}

// runDedupeScan is the testable core of `omnia dedupe`: FTS-blocked candidate
// generation + union-find clustering + canonical selection. It performs NO
// writes whatsoever — this PR's entire scope is propose-only (design D9;
// `--apply` lands in a later PR).
//
// Candidate generation reuses Store.FindCandidates' own shape with
// SkipInsert=true (so a scan never writes a memory_relations row): FTS5
// MATCH narrowed to the same project+scope as the observation's own row, a
// BM25 floor, and a hard per-observation candidate-limit cap. That cap is
// exactly what keeps this scan O(n*limit), never O(n^2), at 1600+-observation
// scale (spec dedupe-scan REQ "Candidate Pre-Filter, Not All-Pairs").
//
// Cross-type pairs are never clustered together: this mirrors the live
// write-gate's own type-gating precedent (internal/store's
// evaluateWriteGate — a high-similarity match against a DIFFERENT type is
// downgraded to relate, never silently merged). Two near-identical-content
// observations of different types are treated as related facts, not
// duplicates, so dedupe-scan leaves cross-type pairs out of every cluster.
// Neither the dedupe-scan spec nor design D9 states this explicitly for the
// scan path (only the write-gate's own spec does, for the live save path) —
// this is a deliberate consistency choice with that already-spec'd,
// existing behavior, not a silent, undocumented departure invented here.
//
// allProjects (real-data validation obs #1683 battery I / issue #171)
// widens candidate generation to match across EVERY project, not just each
// observation's own — the fix for a real, source-verified gap: even the
// pre-existing default (project == "") already scans observations from every
// project (AllObservations' own "" == no filter convention), but the
// PER-OBSERVATION candidate query still only ever matched within that
// observation's own project, because it never overrode CandidateOptions.
// Project. That silently hid genuine cross-project duplicates (battery I
// found 6 real byte-identical pairs: the same GitHub PR ingested under two
// different project keys) from every prior scan, --all-projects or not.
// False (the default) preserves that same-project-only candidate behavior
// byte-for-byte; project must be "" when allProjects is true (enforced by
// cmdDedupe before this function is ever called).
func runDedupeScan(s *store.Store, project string, allProjects bool) (dedupeReport, error) {
	observations, err := s.AllObservations(project, "", dedupeScanLimit)
	if err != nil {
		return dedupeReport{}, fmt.Errorf("runDedupeScan: list observations: %w", err)
	}

	report := dedupeReport{
		Project:     project,
		Scanned:     len(observations),
		DryRun:      true,
		Note:        dedupeDryRunNote,
		AllProjects: allProjects,
	}
	if len(observations) == 0 {
		return report, nil
	}

	// obsByID + rankOf let canonical selection reuse AllObservations' own
	// `ORDER BY datetime(created_at) DESC, id DESC` — exactly the "newest by
	// created_at, tie-break max id" rule (design D9 / spec dedupe-scan REQ
	// "Canonical Survivor Is The Newest") — without this package re-parsing
	// any timestamp string itself. Within a cluster, the member with the
	// SMALLEST rank here is the newest.
	obsByID := make(map[int64]store.Observation, len(observations))
	rankOf := make(map[int64]int, len(observations))
	for i, obs := range observations {
		obsByID[obs.ID] = obs
		rankOf[obs.ID] = i
	}

	uf := newDedupeUnionFind()
	type pairKey struct{ a, b int64 }
	seenPairs := map[pairKey]bool{}
	var edges []dedupeClusterEvidence

	for _, obs := range observations {
		candidates, err := s.FindCandidates(obs.ID, store.CandidateOptions{
			Limit:       dedupeCandidateLimit,
			BM25Floor:   &dedupeCandidateBM25Floor,
			SkipInsert:  true,
			AllProjects: allProjects,
		})
		if err != nil {
			// Candidate-detection failure must never fail the whole scan —
			// mirrors FindCandidates' own doc contract for its other caller
			// (ScanProject: "detection failure must never fail the
			// originating save"; here, never fail the rest of the scan).
			continue
		}
		for _, c := range candidates {
			report.PairsScored++
			cand, ok := obsByID[c.ID]
			if !ok || cand.Type != obs.Type {
				continue
			}
			a, b := obs.ID, c.ID
			if a > b {
				a, b = b, a
			}
			key := pairKey{a, b}
			if seenPairs[key] {
				continue
			}
			score := similarity.Jaccard(similarity.Tokenize(obs.Content), similarity.Tokenize(cand.Content))
			if score < dedupeScanThreshold {
				continue
			}
			seenPairs[key] = true
			edges = append(edges, dedupeClusterEvidence{AID: a, BID: b, Jaccard: score})
			uf.union(a, b)
		}
	}

	if len(uf.parent) == 0 {
		return report, nil
	}

	membersByRoot := make(map[int64][]int64)
	for id := range uf.parent {
		root := uf.find(id)
		membersByRoot[root] = append(membersByRoot[root], id)
	}

	var clusters []dedupeCluster
	for root, ids := range membersByRoot {
		if len(ids) < 2 {
			continue
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

		canonicalID := ids[0]
		for _, id := range ids {
			if rankOf[id] < rankOf[canonicalID] {
				canonicalID = id
			}
		}

		clusterMembers := make([]dedupeClusterMember, 0, len(ids))
		for _, id := range ids {
			obs := obsByID[id]
			proj := ""
			if obs.Project != nil {
				proj = *obs.Project
			}
			clusterMembers = append(clusterMembers, dedupeClusterMember{
				ID:        id,
				Title:     obs.Title,
				Type:      obs.Type,
				Project:   proj,
				CreatedAt: obs.CreatedAt,
				Canonical: id == canonicalID,
			})
		}

		var clusterEvidence []dedupeClusterEvidence
		for _, e := range edges {
			if uf.find(e.AID) == root {
				clusterEvidence = append(clusterEvidence, e)
			}
		}
		sort.Slice(clusterEvidence, func(i, j int) bool {
			if clusterEvidence[i].AID != clusterEvidence[j].AID {
				return clusterEvidence[i].AID < clusterEvidence[j].AID
			}
			return clusterEvidence[i].BID < clusterEvidence[j].BID
		})

		clusters = append(clusters, dedupeCluster{
			ClusterID:   dedupeClusterID(ids),
			CanonicalID: canonicalID,
			Members:     clusterMembers,
			Evidence:    clusterEvidence,
		})
	}

	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ClusterID < clusters[j].ClusterID })

	report.ClustersFound = len(clusters)
	report.Clusters = clusters
	return report, nil
}

// dedupeApplyLoser is one merged-away observation reported by a successful
// `--apply` (spec dedupe-scan: report exactly what was done, per loser).
type dedupeApplyLoser struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	RelationSyncID string `json:"relation_sync_id"`
}

// dedupeApplyResult is the `--apply <cluster-id>` outcome (and `--json`
// payload) — the mutation counterpart of dedupeReport.
type dedupeApplyResult struct {
	ClusterID   string             `json:"cluster_id"`
	CanonicalID int64              `json:"canonical_id"`
	Losers      []dedupeApplyLoser `json:"losers"`
	Applied     bool               `json:"applied"`
}

// newDedupeRelationSyncID generates a unique memory_relations.sync_id for an
// apply-created supersede relation. store.SaveRelationParams.SyncID is an
// exported, caller-supplied field (the UNIQUE constraint on
// memory_relations.sync_id is the only real requirement — internal/store's
// own "rel-<16hex>" shape is an internal convention of its unexported
// newSyncID helper, not a contract cmd/omnia can call directly across the
// package boundary). This mirrors that exact shape (same prefix, same
// crypto/rand-then-nanosecond-fallback pattern) locally rather than
// inventing a different ID scheme.
func newDedupeRelationSyncID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("rel-%d", time.Now().UTC().UnixNano())
	}
	return "rel-" + hex.EncodeToString(b)
}

// applyDedupeCluster is the testable core of `--apply <cluster-id>` (design
// D9, tasks 9.2/9.3). It performs NO snapshot comparison against any earlier
// proposal: it re-runs runDedupeScan FRESH against the CURRENT store state
// and looks up clusterID in that fresh result. This is deliberate, not a
// shortcut — cluster ids are derived entirely from current DB state
// (dedupeClusterID: a content-address over the cluster's full, sorted member
// id set, current created_at-ranked canonical), so a fresh scan is
// simultaneously the staleness check AND the source of truth for who the
// current members/canonical are. THREE staleness cases all surface as the
// SAME "not found" path, with no special-casing needed:
//   - the cluster was already applied (its losers are soft-deleted, so
//     AllObservations — and therefore this fresh scan — no longer includes
//     them; the old cluster id can never recompute again since it was
//     minted from a member set that no longer scans, in whole or in part);
//   - the cluster was PARTIALLY applied (some losers merged, some not — see
//     the per-loser loop below) — the remaining member set differs from the
//     one the id was minted against, so the old id will not match either;
//   - the cluster's MEMBERSHIP changed since the id was minted — a new
//     near-duplicate joined, or an existing member left/was soft-deleted
//     through some other path — even though nothing about THIS cluster was
//     ever applied. Because the id is content-addressed over the FULL
//     member set (dedupeClusterID), any such change yields a different id,
//     closing a real time-of-check-to-time-of-use gap: under the prior
//     smallest-member-id scheme, a brand-new member (almost always minted
//     with the LARGEST id) left the id unchanged, so a stale `--apply
//     <old-id>` could silently merge a member the operator never reviewed.
//
// Because the fresh scan (and its "cluster found?" check) runs BEFORE any
// mutation call below, a stale/unknown cluster id never triggers a partial
// mutation — the whole apply is a no-op in that case (spec dedupe-scan REQ
// "Stale Cluster Apply Fails Safely"). A partial mutation CAN still occur
// from a genuine mid-loop failure ONCE a valid, freshly-matched cluster's
// per-loser loop has begun (see the loop below and cmdDedupe's partial-
// progress printing) — that is a distinct case from staleness.
//
// Cross-project refusal (real-data validation obs #1683 battery I / issue
// #171): a cluster whose members span MORE THAN ONE distinct project can
// only ever be found via an allProjects=true fresh scan — refused here,
// BEFORE any mutation call, with a clear message. Merging across projects
// changes ownership semantics (which project "owns" the surviving canonical
// row) that this conservative product decision deliberately leaves
// unsupported; --apply stays report-only for that case. A same-project
// cluster discovered via an allProjects=true scan applies normally — the
// widened scan is a strict superset of the default one, so it can also
// surface clusters that happen to already share one project.
func applyDedupeCluster(s *store.Store, project, clusterID string, allProjects bool) (dedupeApplyResult, error) {
	report, err := runDedupeScan(s, project, allProjects)
	if err != nil {
		return dedupeApplyResult{}, fmt.Errorf("apply: re-scan failed: %w", err)
	}

	var target *dedupeCluster
	for i := range report.Clusters {
		if report.Clusters[i].ClusterID == clusterID {
			target = &report.Clusters[i]
			break
		}
	}
	if target == nil {
		return dedupeApplyResult{}, fmt.Errorf(
			"cluster %q not found in a fresh scan — its membership may have changed, or it may have been fully or partially applied; run `omnia dedupe` again and use the new cluster id to review/finish any remaining merge.",
			clusterID,
		)
	}

	if projects := dedupeClusterProjects(target.Members); len(projects) > 1 {
		return dedupeApplyResult{}, fmt.Errorf(
			"cluster %q spans %d different projects (%s) — cross-project merges change ownership; not supported. Review each project's members separately, or apply only same-project clusters.",
			clusterID, len(projects), strings.Join(projects, ", "),
		)
	}

	canonicalObs, err := s.GetObservation(target.CanonicalID)
	if err != nil {
		return dedupeApplyResult{}, fmt.Errorf("apply: load canonical #%d: %w", target.CanonicalID, err)
	}

	// Applied is set to true ONLY on the final, all-losers-succeeded return
	// below — never in this initial literal — so a mid-loop error (below)
	// returns a PARTIAL result (Applied still false) that honestly reflects
	// "some losers merged, but the apply as a whole did not complete", not a
	// misleadingly-true flag on a result the caller is about to report as an
	// error.
	result := dedupeApplyResult{ClusterID: target.ClusterID, CanonicalID: target.CanonicalID}
	for _, m := range target.Members {
		if m.Canonical {
			continue
		}

		// dedupeApplyLoserFault is a test-only fault-injection seam (always
		// nil in production — see dedupe_test.go). applyDedupeCluster's own
		// fresh re-scan above already guarantees every member here was
		// present and consistent at loop-start, so there is no naturally
		// occurring, single-threaded way to force a genuine mid-loop error
		// deterministically; this seam lets a test do so, to exercise
		// cmdDedupe's "print partial apply progress before failing" path
		// (review fix: partial-apply observability).
		if dedupeApplyLoserFault != nil {
			if err := dedupeApplyLoserFault(m.ID); err != nil {
				return result, fmt.Errorf("apply: forced failure for loser #%d: %w", m.ID, err)
			}
		}

		loserObs, err := s.GetObservation(m.ID)
		if err != nil {
			return result, fmt.Errorf("apply: load loser #%d: %w", m.ID, err)
		}

		// Supersede relation FIRST, soft-delete SECOND — both reuse existing
		// store primitives (design D9, tasks 9.2/9.3), never a new mutation
		// mechanism:
		//   1. SaveRelation inserts a pending memory_relations row (the same
		//      insert-then-judge two-step FindCandidates itself uses for its
		//      own candidate rows elsewhere in this codebase).
		//   2. JudgeRelation marks it "judged", relation=supersedes, with an
		//      explicit MarkedByActor/MarkedByKind — this is the reason
		//      JudgeRelation is used here instead of JudgeBySemantic (which
		//      would also insert-or-update a judged row in one call, but
		//      hardcodes marked_by_actor="engram"/kind="system" with no way
		//      to override it; design D9 requires marked_by "system:dedupe"
		//      verbatim, so the two-step SaveRelation+JudgeRelation path —
		//      the exact same path an ordinary mem_judge verdict takes — is
		//      the one that can express that attribution).
		relSyncID := newDedupeRelationSyncID()
		if _, err := s.SaveRelation(store.SaveRelationParams{
			SyncID:   relSyncID,
			SourceID: canonicalObs.SyncID,
			TargetID: loserObs.SyncID,
		}); err != nil {
			return result, fmt.Errorf("apply: create pending relation for loser #%d: %w", m.ID, err)
		}
		reason := fmt.Sprintf(
			"omnia dedupe --apply %s: canonical #%d supersedes loser #%d (content jaccard >= %.2f)",
			target.ClusterID, target.CanonicalID, m.ID, dedupeScanThreshold,
		)
		if _, err := s.JudgeRelation(store.JudgeRelationParams{
			JudgmentID:    relSyncID,
			Relation:      store.RelationSupersedes,
			Reason:        &reason,
			MarkedByActor: dedupeApplyActor,
			MarkedByKind:  "system",
		}); err != nil {
			return result, fmt.Errorf("apply: judge supersede relation for loser #%d: %w", m.ID, err)
		}

		// Soft delete (never hard delete): the loser stays queryable/
		// restorable — "referenced history", not gone — and because it is
		// soft- not hard-deleted, deleteObservation's own hard-delete-only
		// orphaning branch never fires, so the supersede relation created
		// just above stays judgment_status='judged' (never flips to
		// 'orphaned') and remains fully navigable via the ordinary
		// GetRelation/GetRelationsForObservations paths.
		if err := s.DeleteObservationWithActor(m.ID, false, dedupeApplyActor); err != nil {
			return result, fmt.Errorf("apply: soft-delete loser #%d: %w", m.ID, err)
		}

		result.Losers = append(result.Losers, dedupeApplyLoser{ID: m.ID, Title: m.Title, RelationSyncID: relSyncID})
	}

	result.Applied = true
	return result, nil
}

// dedupeApplyLoserFault is a test-only fault-injection seam for
// applyDedupeCluster's per-loser loop (see the loop's own comment). Always
// nil in production; only dedupe_test.go ever assigns it, and always
// restores it via t.Cleanup.
var dedupeApplyLoserFault func(loserID int64) error

// cmdDedupe is `omnia dedupe`: the propose-only near-duplicate scan CLI
// (design D9), plus (PR9) its one mutation path, `--apply <cluster-id>`.
// Mirrors cmdReviewDue's flag-parsing + storeNew + defer Close +
// human/--json output shape (the newest CLI subcommand shape at the time of
// PR8).
func cmdDedupe(cfg store.Config) {
	args := os.Args[2:]
	projectFlag := ""
	jsonOut := false
	dryRunFlag := false
	allProjectsFlag := false
	applySeen := false
	applyClusterID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		case "--dry-run":
			dryRunFlag = true
		case "--all-projects":
			allProjectsFlag = true
		case "--apply":
			applySeen = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				applyClusterID = args[i+1]
				i++
			}
		}
	}

	// Safety: --apply and --dry-run are mutually exclusive (one mutates,
	// the other explicitly promises not to) — reject the combo before ever
	// opening the store.
	if applySeen && dryRunFlag {
		fatal(fmt.Errorf("omnia dedupe: --apply and --dry-run cannot be combined — --apply always mutates, --dry-run explicitly never does; pick one"))
		return
	}

	// Safety: --all-projects and --project are mutually exclusive.
	// --all-projects widens candidate matching across EVERY project
	// (real-data validation obs #1683 battery I / issue #171);
	// --project narrows the scanned set to exactly one. Combining them is
	// self-contradictory, so reject it before ever opening the store.
	if allProjectsFlag && projectFlag != "" {
		fatal(fmt.Errorf("omnia dedupe: --all-projects and --project cannot be combined — --all-projects widens candidate matching across every project; pick one"))
		return
	}

	// Safety: --apply REQUIRES an explicit cluster id. There is no
	// `--apply all` (resolved scope decision, tasks.md PR9 note) — bare
	// `--apply` (missing value, or immediately followed by another flag)
	// and the literal value "all" are both rejected with usage guidance
	// before the store is ever opened, so neither path can touch any data.
	if applySeen {
		if applyClusterID == "" {
			fatal(fmt.Errorf("omnia dedupe: --apply requires an explicit cluster id (e.g. --apply d42); there is no --apply all — usage: omnia dedupe --apply <cluster-id> [--project P] [--json]"))
			return
		}
		if applyClusterID == "all" {
			fatal(fmt.Errorf("omnia dedupe: --apply all is not supported — --apply takes exactly one explicit cluster id (e.g. --apply d42); apply each cluster individually"))
			return
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if applySeen {
		result, err := applyDedupeCluster(s, projectFlag, applyClusterID, allProjectsFlag)
		if err != nil {
			// Review fix (partial-apply observability): a mid-cluster error
			// must never silently discard progress already made on this
			// cluster. Print exactly what was ALREADY merged (id, title,
			// relation) BEFORE the fatal error line + non-zero exit below, so
			// the operator knows the store's real state instead of just
			// seeing "it failed" with no idea how far it got.
			if len(result.Losers) > 0 {
				fmt.Printf("Partial apply of cluster %s before the error below — %d loser(s) already merged:\n",
					result.ClusterID, len(result.Losers))
				for _, l := range result.Losers {
					fmt.Printf("  merged: #%d %q -> soft-deleted, superseded by #%d (relation %s)\n",
						l.ID, l.Title, result.CanonicalID, l.RelationSyncID)
				}
				fmt.Println()
			}
			fatal(err)
			return
		}

		if jsonOut {
			out, err := jsonMarshalIndent(result, "", "  ")
			if err != nil {
				fatal(err)
				return
			}
			fmt.Println(string(out))
			return
		}

		fmt.Printf("Applied cluster %s\n", result.ClusterID)
		fmt.Printf("  canonical: #%d (kept, untouched)\n", result.CanonicalID)
		for _, l := range result.Losers {
			fmt.Printf("  merged:    #%d %q -> soft-deleted, superseded by #%d (relation %s)\n",
				l.ID, l.Title, result.CanonicalID, l.RelationSyncID)
		}
		fmt.Printf("\n%d observation(s) merged into canonical #%d.\n", len(result.Losers), result.CanonicalID)
		return
	}

	report, err := runDedupeScan(s, projectFlag, allProjectsFlag)
	if err != nil {
		fatal(err)
		return
	}

	if jsonOut {
		out, err := jsonMarshalIndent(report, "", "  ")
		if err != nil {
			fatal(err)
			return
		}
		fmt.Println(string(out))
		return
	}

	switch {
	case projectFlag != "":
		fmt.Printf("Dedupe Scan (project: %s)\n", projectFlag)
	case allProjectsFlag:
		// Distinct header from the plain "(all projects)" default below —
		// --all-projects ALSO widens per-observation candidate matching
		// across every project (real-data validation obs #1683 battery I /
		// issue #171); the default without this flag only scans rows from
		// every project but still matches candidates within each
		// observation's own project only.
		fmt.Println("Dedupe Scan (all projects, cross-project matching)")
	default:
		fmt.Println("Dedupe Scan (all projects)")
	}
	fmt.Printf("  scanned:        %d\n", report.Scanned)
	fmt.Printf("  clusters found: %d\n", report.ClustersFound)

	if report.ClustersFound == 0 {
		fmt.Println("No duplicate clusters found.")
	} else {
		fmt.Println()
		for _, c := range report.Clusters {
			fmt.Printf("Cluster %s\n", c.ClusterID)
			for _, m := range c.Members {
				marker := "loser"
				if m.Canonical {
					marker = "canonical"
				}
				fmt.Printf("  [%s] #%d (%s/%s) %s\n", marker, m.ID, m.Project, m.Type, m.Title)
			}
			for _, e := range c.Evidence {
				fmt.Printf("    #%d <-> #%d jaccard=%.4f\n", e.AID, e.BID, e.Jaccard)
			}
		}
	}

	fmt.Println()
	fmt.Println(dedupeDryRunNote)
}
