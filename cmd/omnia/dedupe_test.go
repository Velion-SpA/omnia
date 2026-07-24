package main

// dedupe_test.go — RED→GREEN CLI tests for `omnia dedupe` (propose-only near-
// duplicate scan, omnia-0.3.1-write-hygiene PR8, design D9, spec
// dedupe-scan). Mirrors review_due_test.go's withArgs/captureOutput/
// testConfig style, plus mustSeedObservation (main_test.go) for fixtures.
//
// IMPORTANT fixture note: testConfig(t) leaves store.Config.WriteHygieneEnabled
// at its Go zero value (false) — store.DefaultConfig() never sets it true,
// only cmd/omnia's production config-wiring does (PR4). This is deliberate:
// `omnia dedupe`'s whole premise is scanning an EXISTING base that may
// contain duplicate rows predating the write-gate (or inserted through a path
// that bypassed it) — so fixtures here insert genuine duplicate ROWS via
// plain AddObservation, exactly like a pre-write-hygiene corpus would have.
// Fixture pairs always use DIFFERENT titles so the pre-existing (unconditional)
// 15-minute hash-window dedupe — which requires an EXACT title match — never
// collapses them into one row before dedupe-scan ever gets to run.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/store"
)

// backdateObservationCreatedAt directly rewrites created_at (mirrors
// review_due_test.go's review_after backdating convention) so canonical-
// selection tests ("newest survives") don't depend on real wall-clock delay
// between two AddObservation calls.
func backdateObservationCreatedAt(t *testing.T, cfg store.Config, id int64, ts time.Time) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`UPDATE observations SET created_at = ? WHERE id = ?`,
		ts.UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}
}

// ─── 8.1 RED cases ───────────────────────────────────────────────────────────

func TestCmdDedupeBareInvocationMutatesNothing(t *testing.T) {
	cfg := testConfig(t)
	id1 := mustSeedObservation(t, cfg, "s1", "dedupenoopproj", "discovery",
		"Dedupe Noop Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	id2 := mustSeedObservation(t, cfg, "s1", "dedupenoopproj", "discovery",
		"Dedupe Noop Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")

	withArgs(t, "omnia", "dedupe", "--project", "dedupenoopproj")
	stdout, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, dedupeDryRunNote) {
		t.Fatalf("expected explicit dry-run-only footer note, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("dedupenoopproj", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected DB unchanged (2 rows), got %d", len(observations))
	}
	for _, obs := range observations {
		if obs.ID != id1 && obs.ID != id2 {
			t.Fatalf("unexpected observation id %d in unchanged DB", obs.ID)
		}
		if obs.DeletedAt != nil {
			t.Fatalf("observation #%d was deleted by a dry-run scan", obs.ID)
		}
	}
}

func TestRunDedupeScanClustersNearDuplicatesAboveThreshold(t *testing.T) {
	cfg := testConfig(t)
	newerID := mustSeedObservation(t, cfg, "s1", "dedupeclusterproj", "discovery",
		"Dedupe Cluster Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	olderID := mustSeedObservation(t, cfg, "s1", "dedupeclusterproj", "discovery",
		"Dedupe Cluster Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")
	mustSeedObservation(t, cfg, "s1", "dedupeclusterproj", "discovery",
		"Totally Unrelated Row",
		"Completely different unrelated filler text about something else entirely.",
		"project")
	backdateObservationCreatedAt(t, cfg, olderID, time.Now().UTC().Add(-48*time.Hour))

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	report, err := runDedupeScan(s, "dedupeclusterproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.Scanned != 3 {
		t.Fatalf("expected 3 scanned observations, got %d", report.Scanned)
	}
	if report.ClustersFound != 1 {
		t.Fatalf("expected exactly 1 cluster, got %d: %+v", report.ClustersFound, report.Clusters)
	}
	cluster := report.Clusters[0]
	if len(cluster.Members) != 2 {
		t.Fatalf("expected 2 cluster members, got %d", len(cluster.Members))
	}
	if len(cluster.Evidence) != 1 || cluster.Evidence[0].Jaccard < dedupeScanThreshold {
		t.Fatalf("expected 1 evidence edge >= %.2f, got %+v", dedupeScanThreshold, cluster.Evidence)
	}
	if cluster.CanonicalID != newerID {
		t.Fatalf("expected canonical = newest (#%d), got #%d", newerID, cluster.CanonicalID)
	}
	var canonicalSeen, loserSeen bool
	for _, m := range cluster.Members {
		if m.ID == newerID {
			if !m.Canonical {
				t.Fatalf("expected newest member #%d flagged canonical", newerID)
			}
			canonicalSeen = true
		}
		if m.ID == olderID {
			if m.Canonical {
				t.Fatalf("expected older member #%d NOT flagged canonical", olderID)
			}
			loserSeen = true
		}
	}
	if !canonicalSeen || !loserSeen {
		t.Fatalf("expected both known members present in cluster: %+v", cluster.Members)
	}
}

func TestRunDedupeScanCrossTypePairsNotClustered(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "dedupecrosstypeproj", "discovery",
		"Dedupe CrossType Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	mustSeedObservation(t, cfg, "s1", "dedupecrosstypeproj", "decision",
		"Dedupe CrossType Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	report, err := runDedupeScan(s, "dedupecrosstypeproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 0 {
		t.Fatalf("expected same-type-only clustering to exclude a cross-type pair, got %d clusters: %+v",
			report.ClustersFound, report.Clusters)
	}
}

func TestRunDedupeScanEmptyDBNoClusters(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	report, err := runDedupeScan(s, "dedupeemptyproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.Scanned != 0 || report.ClustersFound != 0 || len(report.Clusters) != 0 {
		t.Fatalf("expected empty-DB no-op report, got %+v", report)
	}
}

func TestRunDedupeScanNoClustersWhenAllDistinct(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "dedupedistinctproj", "discovery", "First Distinct Row", "Alpha bravo charlie delta echo foxtrot.", "project")
	mustSeedObservation(t, cfg, "s1", "dedupedistinctproj", "discovery", "Second Distinct Row", "Golf hotel india juliet kilo lima.", "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	report, err := runDedupeScan(s, "dedupedistinctproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 0 {
		t.Fatalf("expected 0 clusters among fully-distinct content, got %d: %+v", report.ClustersFound, report.Clusters)
	}
}

func TestRunDedupeScanExcludesDeletedObservations(t *testing.T) {
	cfg := testConfig(t)
	keepID := mustSeedObservation(t, cfg, "s1", "dedupedeletedproj", "discovery",
		"Dedupe Deleted Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	deletedID := mustSeedObservation(t, cfg, "s1", "dedupedeletedproj", "discovery",
		"Dedupe Deleted Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.DeleteObservationWithActor(deletedID, false, "test"); err != nil {
		t.Fatalf("DeleteObservationWithActor: %v", err)
	}

	report, err := runDedupeScan(s, "dedupedeletedproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.Scanned != 1 {
		t.Fatalf("expected 1 active observation scanned (soft-deleted excluded), got %d", report.Scanned)
	}
	if report.ClustersFound != 0 {
		t.Fatalf("expected 0 clusters once the pair partner is soft-deleted, got %d: %+v", report.ClustersFound, report.Clusters)
	}
	_ = keepID
}

func TestRunDedupeScanDeterministicAcrossRuns(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "dedupedetermproj", "discovery",
		"Dedupe Determinism Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	mustSeedObservation(t, cfg, "s1", "dedupedetermproj", "discovery",
		"Dedupe Determinism Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")
	mustSeedObservation(t, cfg, "s1", "dedupedetermproj", "discovery",
		"Dedupe Determinism Gamma",
		"Unrelated filler content about a totally different topic than the pair above.",
		"project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	report1, err := runDedupeScan(s, "dedupedetermproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan (1st): %v", err)
	}
	report2, err := runDedupeScan(s, "dedupedetermproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan (2nd): %v", err)
	}
	if !reflect.DeepEqual(report1, report2) {
		t.Fatalf("expected identical reports for the same DB state:\n1st: %+v\n2nd: %+v", report1, report2)
	}
	if len(report1.Clusters) == 0 || report1.Clusters[0].ClusterID == "" {
		t.Fatalf("expected a non-empty, non-blank cluster id, got %+v", report1)
	}
	stableClusterID := report1.Clusters[0].ClusterID

	// Regression pin (review fix: membership-drift TOCTOU). A cluster id
	// must stay STABLE across reruns of an UNCHANGED DB (asserted above via
	// report1 == report2) but MUST CHANGE the moment a new near-duplicate
	// joins the same cluster — the id is content-addressed over the FULL
	// member set (dedupeClusterID), not just the smallest member id, so a
	// stale `--apply` using the pre-join id can never still match
	// afterward.
	mustSeedObservation(t, cfg, "s1", "dedupedetermproj", "discovery",
		"Dedupe Determinism Delta",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker!!!",
		"project")
	report3, err := runDedupeScan(s, "dedupedetermproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan (3rd, after new member joins): %v", err)
	}
	if report3.ClustersFound != 1 || len(report3.Clusters[0].Members) != 3 {
		t.Fatalf("expected the new member to join the SAME cluster (now 3 members), got %+v", report3.Clusters)
	}
	if report3.Clusters[0].ClusterID == stableClusterID {
		t.Fatalf("expected cluster id to CHANGE once membership changes (content-addressed id), got the same id %q before and after the join", stableClusterID)
	}
}

func TestCmdDedupeJSONOutputStableShape(t *testing.T) {
	cfg := testConfig(t)
	olderID := mustSeedObservation(t, cfg, "s1", "dedupejsonproj", "discovery",
		"Dedupe JSON Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	newerID := mustSeedObservation(t, cfg, "s1", "dedupejsonproj", "discovery",
		"Dedupe JSON Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")
	backdateObservationCreatedAt(t, cfg, olderID, time.Now().UTC().Add(-48*time.Hour))

	withArgs(t, "omnia", "dedupe", "--project", "dedupejsonproj", "--json")
	stdout, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	var report dedupeReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("unmarshal report: %v\noutput: %s", err, stdout)
	}
	if report.ClustersFound != 1 || len(report.Clusters) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	cluster := report.Clusters[0]
	if cluster.ClusterID == "" {
		t.Fatalf("expected non-empty cluster_id, got %+v", cluster)
	}
	if cluster.CanonicalID != newerID {
		t.Fatalf("expected canonical_id=%d, got %d", newerID, cluster.CanonicalID)
	}
	if len(cluster.Members) != 2 || len(cluster.Evidence) != 1 {
		t.Fatalf("unexpected cluster shape: %+v", cluster)
	}
	if !report.DryRun {
		t.Fatalf("expected dry_run=true in JSON payload, got %+v", report)
	}

	// Re-run and assert byte-identical JSON (stable shape across runs).
	withArgs(t, "omnia", "dedupe", "--project", "dedupejsonproj", "--json")
	stdout2, _ := captureOutput(t, func() { cmdDedupe(cfg) })
	if stdout != stdout2 {
		t.Fatalf("expected byte-identical --json output across runs:\n1st: %s\n2nd: %s", stdout, stdout2)
	}
}

// ─── Real-data validation (obs #1683 battery I, issue #171 / PR13) ──────────

// TestRunDedupeScanCrossProjectDuplicatesRequireAllProjectsFlag reproduces
// battery I's HIGH product-gap finding (obs #1683): 6 REAL byte-identical
// duplicate pairs sat invisible in the real corpus — the same GitHub PR
// ingested under two different project keys — because candidate generation
// never widened across projects. The default scan (project == "") already
// LISTS observations from every project (AllObservations' own "" == no
// filter convention), but each observation's own candidate query still only
// ever matched within its own project. The same near-duplicate pair, seeded
// under two DIFFERENT project keys here, must be invisible to the default
// scan and found as exactly one cluster once --all-projects widens
// candidate generation — the exact "invisible without, one cluster with it"
// shape the battery found.
func TestRunDedupeScanCrossProjectDuplicatesRequireAllProjectsFlag(t *testing.T) {
	cfg := testConfig(t)
	idA := mustSeedObservation(t, cfg, "s1", "dedupecrossproj1", "discovery",
		"Cross Project Duplicate Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	idB := mustSeedObservation(t, cfg, "s1", "dedupecrossproj2", "discovery",
		"Cross Project Duplicate Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Default: project == "" already scans BOTH observations (no project
	// filter), but candidate generation stays siloed per each observation's
	// own project — battery I's exact "invisible" shape.
	defaultReport, err := runDedupeScan(s, "", false)
	if err != nil {
		t.Fatalf("runDedupeScan (default): %v", err)
	}
	if defaultReport.Scanned != 2 {
		t.Fatalf("expected 2 scanned observations, got %d", defaultReport.Scanned)
	}
	if defaultReport.ClustersFound != 0 {
		t.Fatalf("expected the cross-project duplicate to stay INVISIBLE without --all-projects, got %d cluster(s): %+v",
			defaultReport.ClustersFound, defaultReport.Clusters)
	}

	// --all-projects: candidate generation widens across every project —
	// the same pair now clusters.
	widenedReport, err := runDedupeScan(s, "", true)
	if err != nil {
		t.Fatalf("runDedupeScan (--all-projects): %v", err)
	}
	if !widenedReport.AllProjects {
		t.Fatalf("expected report.AllProjects=true, got false")
	}
	if widenedReport.ClustersFound != 1 {
		t.Fatalf("expected exactly 1 cluster once --all-projects widens candidate matching, got %d: %+v",
			widenedReport.ClustersFound, widenedReport.Clusters)
	}
	cluster := widenedReport.Clusters[0]
	if len(cluster.Members) != 2 {
		t.Fatalf("expected 2 cluster members, got %d", len(cluster.Members))
	}
	gotProjects := dedupeClusterProjects(cluster.Members)
	if len(gotProjects) != 2 || gotProjects[0] != "dedupecrossproj1" || gotProjects[1] != "dedupecrossproj2" {
		t.Fatalf("expected members annotated with BOTH distinct projects, got %v", gotProjects)
	}
	var sawA, sawB bool
	for _, m := range cluster.Members {
		if m.ID == idA {
			sawA = true
			if m.Project != "dedupecrossproj1" {
				t.Fatalf("expected member #%d annotated with its own project %q, got %q", idA, "dedupecrossproj1", m.Project)
			}
		}
		if m.ID == idB {
			sawB = true
			if m.Project != "dedupecrossproj2" {
				t.Fatalf("expected member #%d annotated with its own project %q, got %q", idB, "dedupecrossproj2", m.Project)
			}
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected both known members present in the cross-project cluster: %+v", cluster.Members)
	}
}

// TestCmdDedupeAllProjectsAndProjectFlagsAreMutuallyExclusive pins the CLI
// validation: --all-projects widens matching across every project, --project
// narrows the scan to exactly one — combining both is self-contradictory and
// must be rejected before the store is ever opened.
func TestCmdDedupeAllProjectsAndProjectFlagsAreMutuallyExclusive(t *testing.T) {
	cfg := testConfig(t)
	exitCode := stubExit(t)

	withArgs(t, "omnia", "dedupe", "--all-projects", "--project", "someproj")
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "cannot be combined") {
		t.Errorf("expected --all-projects/--project conflict error, got: %s", stderr)
	}
}

// TestCmdDedupeApplyRefusesCrossProjectCluster pins the conservative product
// decision for --apply on a cross-project cluster (design note, obs #1683
// battery I / issue #171): merging across projects changes ownership
// semantics, so --apply REFUSES with a clear message instead of merging —
// report-only for that case, with NO mutation performed.
func TestCmdDedupeApplyRefusesCrossProjectCluster(t *testing.T) {
	cfg := testConfig(t)
	idA := mustSeedObservation(t, cfg, "s1", "dedupecrossapplyA", "discovery",
		"Cross Project Apply Alpha",
		"This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		"project")
	idB := mustSeedObservation(t, cfg, "s1", "dedupecrossapplyB", "discovery",
		"Cross Project Apply Beta",
		"This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		"project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "", true)
	if err != nil {
		t.Fatalf("runDedupeScan (--all-projects): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close scan store: %v", err)
	}
	if report.ClustersFound != 1 {
		t.Fatalf("expected exactly 1 cross-project cluster to apply against, got %d: %+v", report.ClustersFound, report.Clusters)
	}
	clusterID := report.Clusters[0].ClusterID

	exitCode := stubExit(t)
	withArgs(t, "omnia", "dedupe", "--all-projects", "--apply", clusterID)
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "cross-project merges change ownership; not supported") {
		t.Errorf("expected the cross-project refusal message, got: %s", stderr)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s2.Close()
	for _, id := range []int64{idA, idB} {
		obs, err := s2.GetObservation(id)
		if err != nil {
			t.Fatalf("GetObservation(#%d): %v", id, err)
		}
		if obs.DeletedAt != nil {
			t.Fatalf("cross-project refusal must not mutate anything, but observation #%d was deleted", id)
		}
	}
}

// TestRunDedupeScanCandidatePreFilterBoundedAtScale pins spec dedupe-scan's
// REQ "Candidate Pre-Filter, Not All-Pairs": at a synthetic 1600+-observation
// scale, the number of candidate pairs scored MUST stay bounded by
// scanned*dedupeCandidateLimit (FTS-blocked), never approach the O(n^2)
// all-pairs figure — while still finding the one genuine near-duplicate pair
// seeded among the filler rows.
func TestRunDedupeScanCandidatePreFilterBoundedAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-scale dedupe scan test in -short mode")
	}
	cfg := testConfig(t)

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("scale-sess", "dedupescaleproj", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const fillerCount = 1650
	for i := 0; i < fillerCount; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "scale-sess",
			Type:      "discovery",
			Title:     fmt.Sprintf("Filler Entry %d", i),
			Content:   fmt.Sprintf("system log entry number %d for project telemetry batch", i),
			Project:   "dedupescaleproj",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("seed filler %d: %v", i, err)
		}
	}
	// "Alpha" is inserted first (chronologically OLDER); "Beta" second
	// (chronologically NEWER) — Beta is the expected canonical survivor
	// (design D9: newest by created_at, tie-break max id).
	_, err = s.AddObservation(store.AddObservationParams{
		SessionID: "scale-sess",
		Type:      "discovery",
		Title:     "Scale Pair Alpha",
		Content:   "This service handles user login and session validation for the auth module dedupe-fixture-marker.",
		Project:   "dedupescaleproj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed scale pair alpha: %v", err)
	}
	newerID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "scale-sess",
		Type:      "discovery",
		Title:     "Scale Pair Beta",
		Content:   "This service handles user login, and session validation for the auth module dedupe-fixture-marker!",
		Project:   "dedupescaleproj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed scale pair beta: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seeding store: %v", err)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (scan): %v", err)
	}
	defer s2.Close()

	report, err := runDedupeScan(s2, "dedupescaleproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.Scanned != fillerCount+2 {
		t.Fatalf("expected %d scanned observations, got %d", fillerCount+2, report.Scanned)
	}
	maxPairs := report.Scanned * dedupeCandidateLimit
	if report.PairsScored > maxPairs {
		t.Fatalf("candidate generation was NOT FTS-blocked: scored %d pairs, expected <= %d (scanned*limit) at %d-row scale",
			report.PairsScored, maxPairs, report.Scanned)
	}
	if report.ClustersFound != 1 {
		t.Fatalf("expected the one genuine near-duplicate pair to still be found among %d fillers, got %d clusters",
			fillerCount, report.ClustersFound)
	}
	if report.Clusters[0].CanonicalID != newerID {
		t.Fatalf("expected canonical=#%d in the scale test, got #%d", newerID, report.Clusters[0].CanonicalID)
	}
}

// ─── PR9: `omnia dedupe --apply <cluster-id>` ───────────────────────────────
//
// Fixture convention carried over from PR8's tests above: fixture pairs use
// DIFFERENT titles (so the pre-existing 15-minute exact-title hash-window
// dedupe never collapses them before dedupe-scan gets to run) and hyphen-free
// project identifiers (FTS5 tokenizer gotcha, see TEST FIXTURE GOTCHA note in
// apply-progress obs #1674 / tasks.md PR8 notes).

// seedDedupePair seeds a canonical/loser near-duplicate pair in the given
// project (both type "discovery"), backdates the loser 48h so canonical
// selection ("newest survives") never depends on real wall-clock delay
// between two AddObservation calls, and returns (canonicalID, loserID).
func seedDedupePair(t *testing.T, cfg store.Config, project, marker string) (canonicalID, loserID int64) {
	t.Helper()
	canonicalID = mustSeedObservation(t, cfg, "s1", project, "discovery",
		"Dedupe Apply "+marker+" New",
		"This service handles user login and session validation for the auth module "+marker+"-dedupe-fixture-marker.",
		"project")
	loserID = mustSeedObservation(t, cfg, "s1", project, "discovery",
		"Dedupe Apply "+marker+" Old",
		"This service handles user login, and session validation for the auth module "+marker+"-dedupe-fixture-marker!",
		"project")
	backdateObservationCreatedAt(t, cfg, loserID, time.Now().UTC().Add(-48*time.Hour))
	return canonicalID, loserID
}

// stubExit installs a non-panicking exitFunc stub (mirrors
// TestCmdProcedure_UnknownSubcommand's convention) and returns the captured
// exit code by pointer, restoring the original exitFunc via t.Cleanup.
func stubExit(t *testing.T) *int {
	t.Helper()
	old := exitFunc
	code := 0
	exitFunc = func(c int) { code = c }
	t.Cleanup(func() { exitFunc = old })
	return &code
}

// ─── 9.1 RED cases ───────────────────────────────────────────────────────────

func TestCmdDedupeApplyBareFlagRequiresExplicitClusterID(t *testing.T) {
	cfg := testConfig(t)
	exitCode := stubExit(t)

	withArgs(t, "omnia", "dedupe", "--apply")
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "requires an explicit cluster id") {
		t.Errorf("expected explicit-cluster-id usage error, got: %s", stderr)
	}
}

func TestCmdDedupeApplyAllIsNotSupported(t *testing.T) {
	cfg := testConfig(t)
	exitCode := stubExit(t)

	withArgs(t, "omnia", "dedupe", "--apply", "all")
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "--apply all is not supported") {
		t.Errorf("expected --apply-all-not-supported error, got: %s", stderr)
	}
}

func TestCmdDedupeApplyAndDryRunAreMutuallyExclusive(t *testing.T) {
	cfg := testConfig(t)
	exitCode := stubExit(t)

	withArgs(t, "omnia", "dedupe", "--apply", "d1", "--dry-run")
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "cannot be combined") {
		t.Errorf("expected --apply/--dry-run conflict error, got: %s", stderr)
	}
}

func TestCmdDedupeApplyStaleClusterIDFailsCleanlyNoMutation(t *testing.T) {
	cfg := testConfig(t)
	soloID := mustSeedObservation(t, cfg, "s1", "dedupestaleproj", "discovery",
		"Dedupe Stale Solo", "A single unrelated observation, no duplicate pair exists at all.", "project")
	exitCode := stubExit(t)

	withArgs(t, "omnia", "dedupe", "--project", "dedupestaleproj", "--apply", "d999999")
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", *exitCode)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected a clean 'not found' refusal, got: %s", stderr)
	}
	if !strings.Contains(stderr, "membership may have changed") || !strings.Contains(stderr, "fully or partially applied") {
		t.Errorf("expected the refusal to name BOTH possibilities (membership change, fully/partially applied) and a recovery step, got: %s", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	obs, err := s.GetObservation(soloID)
	if err != nil {
		t.Fatalf("expected the untouched observation still active, got err: %v", err)
	}
	if obs.DeletedAt != nil {
		t.Fatalf("stale --apply must not mutate anything, but observation #%d was deleted", soloID)
	}
}

func TestCmdDedupeApplyIsolatesNamedClusterOnly(t *testing.T) {
	cfg := testConfig(t)
	aCanonicalID, aLoserID := seedDedupePair(t, cfg, "dedupeisolateproj", "Alpha")
	bCanonicalID, bLoserID := seedDedupePair(t, cfg, "dedupeisolateproj", "Bravo")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupeisolateproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 2 {
		t.Fatalf("expected 2 independent clusters (Alpha, Bravo), got %d: %+v", report.ClustersFound, report.Clusters)
	}
	var clusterAID string
	for _, c := range report.Clusters {
		if c.CanonicalID == aCanonicalID {
			clusterAID = c.ClusterID
		}
	}
	if clusterAID == "" {
		t.Fatalf("could not locate cluster A (canonical #%d) in scan: %+v", aCanonicalID, report.Clusters)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	withArgs(t, "omnia", "dedupe", "--project", "dedupeisolateproj", "--apply", clusterAID)
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s2.Close()

	// Cluster A: loser soft-deleted, canonical untouched.
	if _, err := s2.GetObservation(aLoserID); err == nil {
		t.Fatalf("expected cluster A's loser #%d to be soft-deleted (excluded from GetObservation)", aLoserID)
	}
	if _, err := s2.GetObservation(aCanonicalID); err != nil {
		t.Fatalf("expected cluster A's canonical #%d to remain active: %v", aCanonicalID, err)
	}

	// Cluster B: FULLY untouched (explicit per-cluster isolation).
	if _, err := s2.GetObservation(bCanonicalID); err != nil {
		t.Fatalf("cluster B canonical #%d must remain untouched: %v", bCanonicalID, err)
	}
	if _, err := s2.GetObservation(bLoserID); err != nil {
		t.Fatalf("cluster B loser #%d must remain untouched — --apply must isolate the named cluster only: %v", bLoserID, err)
	}
}

func TestCmdDedupeApplyIdempotentReApplyRefusesCleanly(t *testing.T) {
	cfg := testConfig(t)
	canonicalID, loserID := seedDedupePair(t, cfg, "dedupeidemproj", "Idem")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupeidemproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 1 {
		t.Fatalf("expected exactly 1 cluster, got %d: %+v", report.ClustersFound, report.Clusters)
	}
	clusterID := report.Clusters[0].ClusterID
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	// 1st apply: succeeds.
	withArgs(t, "omnia", "dedupe", "--project", "dedupeidemproj", "--apply", clusterID)
	_, stderr1 := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr1 != "" {
		t.Fatalf("unexpected stderr on 1st apply: %s", stderr1)
	}

	// 2nd apply on the SAME (now-consumed) cluster id: must refuse cleanly,
	// never double-supersede.
	exitCode := stubExit(t)
	withArgs(t, "omnia", "dedupe", "--project", "dedupeidemproj", "--apply", clusterID)
	_, stderr2 := captureOutput(t, func() { cmdDedupe(cfg) })
	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1 on idempotent re-apply", *exitCode)
	}
	if !strings.Contains(stderr2, "not found") {
		t.Errorf("expected a clean 'not found' refusal on re-apply, got: %s", stderr2)
	}
	if !strings.Contains(stderr2, "membership may have changed") || !strings.Contains(stderr2, "fully or partially applied") {
		t.Errorf("expected the refusal to name BOTH possibilities (membership change, fully/partially applied) and a recovery step, got: %s", stderr2)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s2.Close()
	canonicalObs, err := s2.GetObservation(canonicalID)
	if err != nil {
		t.Fatalf("GetObservation(canonical): %v", err)
	}
	rels, err := s2.GetRelationsForObservations([]string{canonicalObs.SyncID})
	if err != nil {
		t.Fatalf("GetRelationsForObservations: %v", err)
	}
	supersedeCount := 0
	for _, r := range rels[canonicalObs.SyncID].AsSource {
		if r.Relation == store.RelationSupersedes {
			supersedeCount++
		}
	}
	if supersedeCount != 1 {
		t.Fatalf("expected exactly 1 supersedes relation (never double-supersede), got %d", supersedeCount)
	}
	_ = loserID
}

// ─── 9.2 RED cases ───────────────────────────────────────────────────────────

func TestCmdDedupeApplyCreatesSupersedeRelationAndSoftDeletesLoser(t *testing.T) {
	cfg := testConfig(t)
	canonicalID, loserID := seedDedupePair(t, cfg, "dedupeapplyrelproj", "Rel")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupeapplyrelproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 1 {
		t.Fatalf("expected exactly 1 cluster, got %d: %+v", report.ClustersFound, report.Clusters)
	}
	clusterID := report.Clusters[0].ClusterID
	canonicalBefore, err := s.GetObservation(canonicalID)
	if err != nil {
		t.Fatalf("GetObservation(canonical) before apply: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	withArgs(t, "omnia", "dedupe", "--project", "dedupeapplyrelproj", "--apply", clusterID)
	stdout, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, fmt.Sprintf("#%d", loserID)) {
		t.Fatalf("expected apply output to report the merged loser #%d, got: %s", loserID, stdout)
	}
	if !strings.Contains(stdout, fmt.Sprintf("#%d", canonicalID)) {
		t.Fatalf("expected apply output to report the surviving canonical #%d, got: %s", canonicalID, stdout)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s2.Close()

	// Canonical untouched: same updated_at as before the apply.
	canonicalAfter, err := s2.GetObservation(canonicalID)
	if err != nil {
		t.Fatalf("GetObservation(canonical) after apply: %v", err)
	}
	if canonicalAfter.UpdatedAt != canonicalBefore.UpdatedAt {
		t.Fatalf("expected canonical #%d untouched (same updated_at), before=%q after=%q",
			canonicalID, canonicalBefore.UpdatedAt, canonicalAfter.UpdatedAt)
	}

	// Loser soft-deleted (not hard-deleted): row still present with
	// deleted_at set and content intact — "referenced history".
	var deletedAt *string
	var loserContent string
	if err := s2.DB().QueryRow(`SELECT deleted_at, content FROM observations WHERE id = ?`, loserID).
		Scan(&deletedAt, &loserContent); err != nil {
		t.Fatalf("query loser row directly: %v", err)
	}
	if deletedAt == nil || *deletedAt == "" {
		t.Fatalf("expected loser #%d to have deleted_at set (soft-deleted), got nil/empty", loserID)
	}
	if loserContent == "" {
		t.Fatalf("expected loser #%d content to remain intact (referenced history, not hard-deleted)", loserID)
	}

	// Supersede relation: canonical -> loser, judged, marked_by system:dedupe.
	rels, err := s2.GetRelationsForObservations([]string{canonicalAfter.SyncID})
	if err != nil {
		t.Fatalf("GetRelationsForObservations: %v", err)
	}
	var found *store.Relation
	for i, r := range rels[canonicalAfter.SyncID].AsSource {
		if r.Relation == store.RelationSupersedes {
			found = &rels[canonicalAfter.SyncID].AsSource[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a supersedes relation with canonical #%d as source, got: %+v", canonicalID, rels)
	}
	if found.JudgmentStatus != store.JudgmentStatusJudged {
		t.Errorf("expected judgment_status=judged, got %q", found.JudgmentStatus)
	}
	if found.MarkedByActor == nil || *found.MarkedByActor != dedupeApplyActor {
		t.Errorf("expected marked_by_actor=%q, got %v", dedupeApplyActor, found.MarkedByActor)
	}
	if found.MarkedByKind == nil || *found.MarkedByKind != "system" {
		t.Errorf("expected marked_by_kind=system, got %v", found.MarkedByKind)
	}
	if !found.TargetMissing {
		// GetRelationsForObservations' own doc comment: "Missing or
		// soft-deleted observations set the corresponding *Missing flag to
		// true" — TargetMissing=true here just means "not currently active",
		// same as any soft-deleted row; it is NOT the same signal as
		// judgment_status='orphaned' (which only a HARD delete ever sets —
		// asserted separately above via JudgmentStatus==judged). The relation
		// row itself, and the loser's content, remain fully present/queryable
		// — this is the "referenced history" contract, not erasure.
		t.Errorf("expected TargetMissing=true (target is soft-deleted, per GetRelationsForObservations' own contract)")
	}
}

func TestCmdDedupeApplyJSONReportsClusterCanonicalAndLosers(t *testing.T) {
	cfg := testConfig(t)
	canonicalID, loserID := seedDedupePair(t, cfg, "dedupeapplyjsonproj", "JSON")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupeapplyjsonproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	clusterID := report.Clusters[0].ClusterID
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	withArgs(t, "omnia", "dedupe", "--project", "dedupeapplyjsonproj", "--apply", clusterID, "--json")
	stdout, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	var result dedupeApplyResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("unmarshal apply result: %v\noutput: %s", err, stdout)
	}
	if result.ClusterID != clusterID {
		t.Errorf("cluster_id = %q; want %q", result.ClusterID, clusterID)
	}
	if result.CanonicalID != canonicalID {
		t.Errorf("canonical_id = %d; want %d", result.CanonicalID, canonicalID)
	}
	if !result.Applied {
		t.Errorf("expected applied=true")
	}
	if len(result.Losers) != 1 {
		t.Fatalf("expected exactly 1 loser reported, got %d: %+v", len(result.Losers), result.Losers)
	}
	if result.Losers[0].ID != loserID {
		t.Errorf("loser id = %d; want %d", result.Losers[0].ID, loserID)
	}
	if result.Losers[0].RelationSyncID == "" {
		t.Errorf("expected a non-empty relation_sync_id for the merged loser")
	}
}

// ─── PR9 review fix: membership-drift TOCTOU + partial-apply observability ──

// TestCmdDedupeApplyRefusesWhenClusterMembershipChangedAfterReview reproduces
// the adversarial-review scenario verbatim: an operator reviews a 2-member
// cluster (recording its cluster id), then a THIRD near-duplicate joins that
// SAME cluster before --apply runs (the TOCTOU window) — --apply using the
// id the operator actually reviewed MUST refuse, never silently merge the
// never-reviewed joiner. Before the content-addressed cluster-id fix, this
// scenario merged all 3 members under the unchanged "smallest member id"
// cluster id; it now refuses via the ordinary "cluster not found" path.
func TestCmdDedupeApplyRefusesWhenClusterMembershipChangedAfterReview(t *testing.T) {
	cfg := testConfig(t)
	canonicalID, loserID := seedDedupePair(t, cfg, "dedupetoctouproj", "Toctou")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupetoctouproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 1 || len(report.Clusters[0].Members) != 2 {
		t.Fatalf("expected exactly 1 two-member cluster before the join, got %+v", report.Clusters)
	}
	reviewedClusterID := report.Clusters[0].ClusterID

	// A THIRD near-duplicate joins the SAME cluster AFTER the operator
	// reviewed the 2-member proposal above but BEFORE --apply runs.
	// Backdated (but less than the existing loser) so the canonical
	// selection (newest survives) stays exactly what was reviewed — this
	// test is about membership drift, not a canonical flip.
	joinerID := mustSeedObservation(t, cfg, "s1", "dedupetoctouproj", "discovery",
		"Dedupe Apply Toctou Joiner",
		"This service handles user login and session validation for the auth module Toctou-dedupe-fixture-marker???",
		"project")
	backdateObservationCreatedAt(t, cfg, joinerID, time.Now().UTC().Add(-24*time.Hour))
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	// Sanity: confirm the joiner really did land in the SAME cluster the
	// operator reviewed (now 3 members) — otherwise this test would not be
	// exercising the TOCTOU scenario at all.
	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	freshReport, err := runDedupeScan(s2, "dedupetoctouproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan (fresh, after join): %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("close fresh-scan store: %v", err)
	}
	if freshReport.ClustersFound != 1 || len(freshReport.Clusters[0].Members) != 3 {
		t.Fatalf("expected the joiner to land in a single 3-member cluster, got %+v", freshReport.Clusters)
	}
	if freshReport.Clusters[0].CanonicalID != canonicalID {
		t.Fatalf("expected canonical to remain #%d (unaffected by the join), got #%d", canonicalID, freshReport.Clusters[0].CanonicalID)
	}

	// The operator now runs --apply using the id THEY reviewed (2 members).
	// This MUST refuse — never silently merge the un-reviewed joiner.
	exitCode := stubExit(t)
	withArgs(t, "omnia", "dedupe", "--project", "dedupetoctouproj", "--apply", reviewedClusterID)
	_, stderr := captureOutput(t, func() { cmdDedupe(cfg) })
	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1 — membership drift must refuse, never silently merge", *exitCode)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected a clean 'not found' refusal on membership drift, got: %s", stderr)
	}
	if !strings.Contains(stderr, "membership may have changed") {
		t.Errorf("expected the refusal to name membership change as a possibility, got: %s", stderr)
	}

	s3, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s3.Close()
	for _, id := range []int64{canonicalID, loserID, joinerID} {
		obs, err := s3.GetObservation(id)
		if err != nil {
			t.Fatalf("expected observation #%d to remain untouched by the refused apply, got err: %v", id, err)
		}
		if obs.DeletedAt != nil {
			t.Fatalf("observation #%d was mutated by a stale --apply — membership-drift TOCTOU reproduced", id)
		}
	}
}

// TestCmdDedupeApplyPrintsPartialProgressBeforeFatalOnMidClusterError pins
// the review fix: when the per-loser loop errors partway through a
// multi-loser cluster, cmdDedupe must print what was ALREADY merged (id,
// title, relation) before the fatal error line and non-zero exit — never
// silently discard that progress. Uses dedupeApplyLoserFault, a test-only
// seam (see its doc comment), since applyDedupeCluster's own fresh re-scan
// makes a genuine mid-loop failure impossible to trigger deterministically
// from a single-threaded test otherwise.
func TestCmdDedupeApplyPrintsPartialProgressBeforeFatalOnMidClusterError(t *testing.T) {
	cfg := testConfig(t)
	canonicalID := mustSeedObservation(t, cfg, "s1", "dedupepartialproj", "discovery",
		"Dedupe Apply Partial New",
		"This service handles user login and session validation for the auth module partial-dedupe-fixture-marker.",
		"project")
	loserAID := mustSeedObservation(t, cfg, "s1", "dedupepartialproj", "discovery",
		"Dedupe Apply Partial OldA",
		"This service handles user login, and session validation for the auth module partial-dedupe-fixture-marker!",
		"project")
	loserBID := mustSeedObservation(t, cfg, "s1", "dedupepartialproj", "discovery",
		"Dedupe Apply Partial OldB",
		"This service handles user login and session validation for the auth module partial-dedupe-fixture-marker???",
		"project")
	backdateObservationCreatedAt(t, cfg, loserAID, time.Now().UTC().Add(-48*time.Hour))
	backdateObservationCreatedAt(t, cfg, loserBID, time.Now().UTC().Add(-47*time.Hour))

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	report, err := runDedupeScan(s, "dedupepartialproj", false)
	if err != nil {
		t.Fatalf("runDedupeScan: %v", err)
	}
	if report.ClustersFound != 1 || len(report.Clusters[0].Members) != 3 {
		t.Fatalf("expected 1 three-member cluster, got %+v", report.Clusters)
	}
	if report.Clusters[0].CanonicalID != canonicalID {
		t.Fatalf("expected canonical #%d, got #%d", canonicalID, report.Clusters[0].CanonicalID)
	}
	clusterID := report.Clusters[0].ClusterID
	if err := s.Close(); err != nil {
		t.Fatalf("close scanning store: %v", err)
	}

	old := dedupeApplyLoserFault
	dedupeApplyLoserFault = func(loserID int64) error {
		if loserID == loserBID {
			return fmt.Errorf("forced failure for test")
		}
		return nil
	}
	t.Cleanup(func() { dedupeApplyLoserFault = old })

	exitCode := stubExit(t)
	withArgs(t, "omnia", "dedupe", "--project", "dedupepartialproj", "--apply", clusterID)
	stdout, stderr := captureOutput(t, func() { cmdDedupe(cfg) })

	if *exitCode != 1 {
		t.Errorf("exitCode = %d; want 1 on forced mid-cluster failure", *exitCode)
	}
	if !strings.Contains(stderr, "forced failure for test") {
		t.Errorf("expected the underlying error surfaced, got stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Partial apply") {
		t.Errorf("expected a partial-apply progress header on stdout, got: %s", stdout)
	}
	if !strings.Contains(stdout, fmt.Sprintf("#%d", loserAID)) {
		t.Errorf("expected partial-progress output to report the already-merged loser #%d, got stdout: %s", loserAID, stdout)
	}
	if strings.Contains(stdout, fmt.Sprintf("#%d", loserBID)) {
		t.Errorf("loser B caused the failure and was never merged — must NOT appear in the partial-progress report, got: %s", stdout)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s2.Close()
	if _, err := s2.GetObservation(loserAID); err == nil {
		t.Fatalf("expected loser A to be soft-deleted (excluded from GetObservation) after the partial apply")
	}
	obsB, err := s2.GetObservation(loserBID)
	if err != nil {
		t.Fatalf("expected loser B to remain untouched (the fault fired before its mutation), got err: %v", err)
	}
	if obsB.DeletedAt != nil {
		t.Fatalf("loser B must not have been mutated by the forced failure")
	}
}
