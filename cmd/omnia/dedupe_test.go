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

	report, err := runDedupeScan(s, "dedupeclusterproj")
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

	report, err := runDedupeScan(s, "dedupecrosstypeproj")
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

	report, err := runDedupeScan(s, "dedupeemptyproj")
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

	report, err := runDedupeScan(s, "dedupedistinctproj")
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

	report, err := runDedupeScan(s, "dedupedeletedproj")
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

	report1, err := runDedupeScan(s, "dedupedetermproj")
	if err != nil {
		t.Fatalf("runDedupeScan (1st): %v", err)
	}
	report2, err := runDedupeScan(s, "dedupedetermproj")
	if err != nil {
		t.Fatalf("runDedupeScan (2nd): %v", err)
	}
	if !reflect.DeepEqual(report1, report2) {
		t.Fatalf("expected identical reports for the same DB state:\n1st: %+v\n2nd: %+v", report1, report2)
	}
	if len(report1.Clusters) == 0 || report1.Clusters[0].ClusterID == "" {
		t.Fatalf("expected a non-empty, non-blank cluster id, got %+v", report1)
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

	report, err := runDedupeScan(s2, "dedupescaleproj")
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
