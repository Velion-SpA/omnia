package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/velion/omnia/internal/similarity"
	"github.com/velion/omnia/internal/store"
)

// dedupe.go — `omnia dedupe`: an offline, candidate-filtered near-duplicate
// scan over the EXISTING observation base (omnia-0.3.1-write-hygiene PR8,
// design obs #1668 D9, spec dedupe-scan). This PR is PROPOSE-ONLY: there is
// no mutation code anywhere in this file. `--apply <cluster-id>` (the
// supersede + soft-delete mutation) is a SEPARATE, later PR (PR9) — every
// path below only reads the store.

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

// dedupeDryRunNote is the explicit, printed-every-time statement that this
// release of `omnia dedupe` never mutates anything (spec dedupe-scan REQ
// "Propose-Only By Default"; this PR ships NO --apply code path at all).
const dedupeDryRunNote = "This is a dry-run proposal only. No observation was merged, deleted, or otherwise mutated. --apply <cluster-id> is not implemented in this release."

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
	// ClusterID is derived from the cluster's SMALLEST member id (tasks 8.1:
	// "cluster id (stable/deterministic, e.g. derived from member IDs)") —
	// deliberately NOT union-find's internal root (arbitrary, order-
	// dependent) and NOT scan/processing order, so the SAME underlying data
	// always proposes the SAME cluster id across repeated runs (required so
	// a future `--apply <cluster-id>` stays stable across re-scans, per the
	// tasks.md PR9 note this PR must not break).
	ClusterID   string                  `json:"cluster_id"`
	CanonicalID int64                   `json:"canonical_id"`
	Members     []dedupeClusterMember   `json:"members"`
	Evidence    []dedupeClusterEvidence `json:"evidence"`
}

// dedupeReport is the full `omnia dedupe` proposal (and `--json` payload).
type dedupeReport struct {
	Project       string          `json:"project,omitempty"`
	Scanned       int             `json:"scanned"`
	ClustersFound int             `json:"clusters_found"`
	Clusters      []dedupeCluster `json:"clusters"`
	DryRun        bool            `json:"dry_run"`
	Note          string          `json:"note"`

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
func runDedupeScan(s *store.Store, project string) (dedupeReport, error) {
	observations, err := s.AllObservations(project, "", dedupeScanLimit)
	if err != nil {
		return dedupeReport{}, fmt.Errorf("runDedupeScan: list observations: %w", err)
	}

	report := dedupeReport{
		Project: project,
		Scanned: len(observations),
		DryRun:  true,
		Note:    dedupeDryRunNote,
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
			Limit:      dedupeCandidateLimit,
			BM25Floor:  &dedupeCandidateBM25Floor,
			SkipInsert: true,
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
		minID := ids[0]

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
			ClusterID:   fmt.Sprintf("d%d", minID),
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

// cmdDedupe is `omnia dedupe`: the propose-only near-duplicate scan CLI
// (design D9). Mirrors cmdReviewDue's flag-parsing + storeNew + defer Close
// + human/--json output shape (the newest CLI subcommand shape at the time
// of this PR). No --apply flag is parsed in this release — see
// dedupeDryRunNote and this file's package doc comment.
func cmdDedupe(cfg store.Config) {
	args := os.Args[2:]
	projectFlag := ""
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	report, err := runDedupeScan(s, projectFlag)
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

	if projectFlag != "" {
		fmt.Printf("Dedupe Scan (project: %s)\n", projectFlag)
	} else {
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
