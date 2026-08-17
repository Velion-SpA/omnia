package main

// This file is the acceptance test for engram eval/http-search-as-of-isolation
// — the fix for a measured contamination bug: `omnia eval --profile
// conversational --target http` reads GET /search against the SAME live
// store the measuring session's own mem_save calls write to, and this
// project's memory convention (a trailing `Keywords:` line quoting exact
// strings so memories stay findable) plants the eval corpus's own gold
// answers into the corpus under test. Engram #2633 traced this by hand
// through three headline figures (0.625 -> 0.250 -> 0.000 identity
// grounding) and concluded no textual heuristic can fix it — only store
// isolation can (see that observation's own "Learned" section).
//
// This test is DELIBERATELY not part of the normal `go test ./...` run: it
// opens the REAL, encrypted, live omnia store at the machine's configured
// data dir (config.DefaultPath()) and starts a real HTTP server in front of
// it, which no other machine (and no CI runner) has. It is read-only in
// intent — every call below is Search/SearchAsOf through GET /search; none
// of this file ever calls AddObservation, POST /observations, `omnia
// collect`, or `doctor repair --apply`. Gated behind an explicit opt-in env
// var, mirroring this codebase's existing convention for live-service tests
// (internal/recall/bilingual_test.go, internal/embed/model_ab_test.go's
// OLLAMA_LIVE_TEST=1).

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/server"
	"github.com/velion/omnia/internal/store"
)

// Known contaminants, traced by hand in engram #2633 and resolved here via
// mem_search against their stable topic keys (bug/repodoc-chunk-title-from-
// code-fence and eval/identity-grounding-retraction) rather than derived at
// test time from a fragile text search — sync_id is the identifier this
// codebase treats as stable across replicas (see AnswerSource's own doc:
// "an integer id was verified to name different observations across
// different replicas").
const (
	// contaminantRepodocBugSyncID is obs #2568 ("Fixed repodoc chunk-title-
	// from-code-fence bug..."), created 2026-08-14 03:11:54 — quotes the
	// corpus's expected facts at 98% of its body inside a Keywords: trailer
	// and again at 70% inside a results table.
	contaminantRepodocBugSyncID = "obs-63d497779f8e8b3d"
	// contaminantRetractionSyncID is obs #2633 itself ("Final: identity
	// grounding is 0.000..."), created 2026-08-15 00:34:47 — the note
	// documenting the FIRST contamination, which then quoted a fact at 33%
	// of its own body and became the SECOND false pass.
	contaminantRetractionSyncID = "obs-8db5b563fd1bbd8f"
	// isolationCutoff predates BOTH contaminants (earliest 2026-08-14
	// 03:11:54 UTC) with over ten minutes of margin: a recorded-time view
	// of the store as it stood before this whole investigation began
	// writing its own findings into the very corpus it was measuring.
	isolationCutoff = "2026-08-14T03:00:00Z"
)

// TestConversationalEvalIdentityIsolationAcceptance is the acceptance test
// specified for this fix: re-run the identity cases through the isolation
// seam and prove, concretely —
//
//  1. Neither contaminating observation appears in the retrieved results
//     for ANY identity case.
//  2. Report the resulting (trustworthy) identity grounding number.
//  3. delta/rationale/open_items/status accuracy@1 are logged against
//     engram #2628's baseline for transparency (their gold is an
//     observation_id, which no note-quoting can forge), and the ORDINARY
//     non-as_of GET /search code path is checked directly against
//     store.Search to prove this fix did not alter it — see that section's
//     own doc for why asserting exact equality against a three-day-old
//     baseline would not be a valid regression check on a live store.
func TestConversationalEvalIdentityIsolationAcceptance(t *testing.T) {
	if os.Getenv("OMNIA_LIVE_STORE_ACCEPTANCE_TEST") != "1" {
		t.Skip("set OMNIA_LIVE_STORE_ACCEPTANCE_TEST=1 to run the store-isolation acceptance test against the real, live, encrypted omnia store (read-only intent: Search/SearchAsOf via GET /search only)")
	}
	// This package's TestMain pins OMNIA_DATA_DIR to an isolated temp
	// directory for every other test, by design (testmain_test.go's own
	// doc: "guards the entire cmd/omnia test package against ever touching
	// the user's real ~/.engram or ~/.omnia"). This test's whole point is
	// to touch the real one — t.Setenv is that file's own documented
	// escape hatch ("Tests that specifically exercise data-dir resolution
	// override this with t.Setenv"), auto-restored when this test ends.
	t.Setenv("OMNIA_DATA_DIR", "")

	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("resolve store config: %v", err)
	}
	appCfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatalf("load live config at %s: %v", config.DefaultPath(), err)
	}
	s, err := storeNew(evalStoreConfig(cfg, appCfg))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	defer s.Close()

	if !s.TimeTravelEnabled() {
		t.Fatal("live store does not have time_travel enabled — this acceptance test cannot verify isolation without it")
	}

	// A bare server.New with no SetSearch call: the as_of branch in
	// handleSearch bypasses any SearchFunc anyway (searchRecordedTime's own
	// doc), so this is the minimal wiring that exercises the REAL
	// production endpoint code without touching autoEmbedWorker,
	// consolidation, or any other background job cmdServe normally wires —
	// nothing here can write to the store.
	srv := server.New(s, 0)
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	cases, err := loadConversationalCorpus("")
	if err != nil {
		t.Fatalf("load embedded conversational corpus: %v", err)
	}

	fetch := conversationalHTTPFetcher(httpSrv.Client(), httpSrv.URL, false, isolationCutoff)

	// ── Acceptance criterion 1: contaminant absence across every identity case ──
	var identityCases []eval.ConversationalCase
	for _, c := range cases {
		if c.Kind == eval.KindIdentity {
			identityCases = append(identityCases, c)
		}
	}
	if len(identityCases) == 0 {
		t.Fatal("corpus has no identity-kind cases — cannot run the acceptance check")
	}

	for _, c := range identityCases {
		rc, err := fetch(context.Background(), c)
		if err != nil {
			t.Fatalf("fetch identity case %q: %v", c.ID, err)
		}
		for _, id := range rc.RankedObservationIDs {
			if id == contaminantRepodocBugSyncID {
				t.Errorf("case %q: contaminating observation %s (bug/repodoc-chunk-title-from-code-fence, engram #2568) appeared in retrieved results — isolation failed", c.ID, id)
			}
			if id == contaminantRetractionSyncID {
				t.Errorf("case %q: contaminating observation %s (eval/identity-grounding-retraction, engram #2633) appeared in retrieved results — isolation failed", c.ID, id)
			}
		}
	}
	if !t.Failed() {
		t.Logf("verified: neither %s (repodoc bug memory) nor %s (retraction memory) appears in the retrieved results of any of the %d identity cases", contaminantRepodocBugSyncID, contaminantRetractionSyncID, len(identityCases))
	}

	// ── Acceptance criterion 2: the isolated identity grounding number ──
	//
	// isolationCutoff (2026-08-14T03:00:00Z) predates both contaminants by
	// design, but it ALSO predates a great deal of this store's genuinely
	// legitimate content — including, it turns out, several of the gold
	// observations the delta/rationale/open_items/status cases below
	// resolve against (their own creation/last-update timestamps fall AFTER
	// this cutoff). That is expected and correct: isolation's job is to
	// exclude the measuring session's own contamination, not to reproduce
	// "the store as it looked on an arbitrary day." Scoring identity
	// grounding under this cutoff is exactly right (the two contaminants
	// must not be visible); scoring the OTHER four kinds under the SAME
	// cutoff would conflate "isolated" with "missing gold data" and produce
	// a meaningless number. So identity is measured HERE, isolated; the
	// other four are measured separately below, live (see that section's
	// own doc for why that is still the correct acceptance check).
	isolatedSummary, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		Target:      "http",
		HTTPBaseURL: httpSrv.URL,
		AsOf:        isolationCutoff,
		Runs:        eval.MinRuns,
	})
	if err != nil {
		t.Fatalf("run isolated conversational eval: %v", err)
	}
	identity := isolatedSummary.ByKind[eval.KindIdentity]

	// This is the number the whole exercise exists to produce honestly —
	// logged, never asserted against a target: engram #2633 is explicit
	// that 0.000 here would finally be a TRUSTWORTHY zero, and that no
	// value should be tuned to move it.
	t.Logf("ISOLATED (as_of=%s) identity grounding = %.3f (n=%d, %d reproducibility runs)", isolationCutoff, identity.GroundingRate.Mean, identity.GroundingRate.N, eval.MinRuns)

	// ── Acceptance criterion 3: delta/rationale/open_items/status, LIVE ──
	//
	// Deliberately NOT run through --as-of: engram #2628's baseline
	// (0.700/0.625/0.250/0.100) was itself measured live, and these four
	// kinds are immune to the contamination bug in the first place — their
	// gold is an observation_id, matched exactly, and no amount of a
	// session's own notes quoting text can forge an exact ID match.
	// Re-running the SAME plain, unisolated path today is the intended
	// regression check.
	//
	// It does NOT hold to three decimal places three days later, and — this
	// matters — NOT because of anything this fix touched. Traced by hand
	// (case delta-blame-config-es): the gold bugfix memory now ranks #2
	// behind a docs/conversational-retrieval-plan.md chunk
	// ("P2 — Query intent classification and routing") that FTS5's BM25
	// scores higher today than it did on 2026-08-14/15 — not because that
	// chunk or the gold memory changed, but because BM25's IDF term weights
	// are a function of the WHOLE corpus, and the store has kept growing
	// (real, ongoing usage) in the three days since. That is expected
	// ranking drift on a live store, present with or without this fix —
	// asserting exact equality against a fixed historical baseline would be
	// testing the store's growth rate, not this change.
	//
	// The invariant that actually matters — that this fix left the
	// ORDINARY, non-as_of code path byte-for-byte itself — is checked
	// directly below by cross-referencing GET /search's plain response
	// against a raw store.Search call for the same query: identical
	// RankedObservationIDs proves handleSearch's non-as_of branches
	// (unmodified by this change; see searchRecordedTime's own doc for
	// what WAS added) still return exactly what the store itself computes.
	// The accuracy@1 numbers are logged for transparency, not asserted.
	liveSummary, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		Target:      "http",
		HTTPBaseURL: httpSrv.URL,
		Runs:        eval.MinRuns,
	})
	if err != nil {
		t.Fatalf("run live conversational eval: %v", err)
	}
	logLiveKind := func(name string, kind eval.QuestionKind, baseline float64) {
		got := liveSummary.ByKind[kind].AccuracyAt1.Mean
		t.Logf("live %s accuracy@1 = %.3f (engram #2628 baseline: %.3f, informational only — see this test's own doc on BM25 corpus-growth drift)", name, got, baseline)
	}
	logLiveKind("delta", eval.KindDelta, 0.700)
	logLiveKind("rationale", eval.KindRationale, 0.625)
	logLiveKind("open_items", eval.KindOpenItems, 0.250)
	logLiveKind("status", eval.KindStatus, 0.100)

	// The actual regression check: GET /search's plain (non-as_of) branch
	// must return exactly what store.Search itself computes, for the SAME
	// live-store queries used above — proving this fix's handleSearch
	// changes did not alter the ordinary path's behavior.
	plainFetch := conversationalHTTPFetcher(httpSrv.Client(), httpSrv.URL, false, "")
	for _, c := range cases {
		if c.Kind != eval.KindDelta && c.Kind != eval.KindRationale && c.Kind != eval.KindOpenItems && c.Kind != eval.KindStatus {
			continue
		}
		httpResult, err := plainFetch(context.Background(), c)
		if err != nil {
			t.Fatalf("plain fetch case %q: %v", c.ID, err)
		}
		direct, err := s.Search(c.Query, store.SearchOptions{Project: c.Project, Limit: rankCandidateDepth})
		if err != nil {
			t.Fatalf("direct store.Search case %q: %v", c.ID, err)
		}
		directIDs := rankedSyncIDs(direct)
		if !stringSlicesEqual(httpResult.RankedObservationIDs, directIDs) {
			t.Errorf("case %q: GET /search (plain) returned %v, but a direct store.Search returned %v — the non-as_of code path diverged from the store", c.ID, httpResult.RankedObservationIDs, directIDs)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestConversationalEvalIsolationAcceptance_ContaminantsExistLive is a
// sanity check on the acceptance test's OWN premise: without a cutoff (a
// live, unisolated search), the two named contaminants must actually exist
// and be findable in this store — otherwise the isolation test above would
// trivially "pass" for the wrong reason (the contaminants gone/renamed)
// instead of because --as-of genuinely excluded them.
func TestConversationalEvalIsolationAcceptance_ContaminantsExistLive(t *testing.T) {
	if os.Getenv("OMNIA_LIVE_STORE_ACCEPTANCE_TEST") != "1" {
		t.Skip("set OMNIA_LIVE_STORE_ACCEPTANCE_TEST=1 to run against the real live store")
	}
	// See TestConversationalEvalIdentityIsolationAcceptance's own comment on
	// this line — testmain_test.go's documented escape hatch.
	t.Setenv("OMNIA_DATA_DIR", "")

	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("resolve store config: %v", err)
	}
	appCfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatalf("load live config: %v", err)
	}
	s, err := storeNew(evalStoreConfig(cfg, appCfg))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	defer s.Close()

	// GetObservationBySyncID is this package's own read path, unchanged by
	// this fix, used here only to confirm the fixtures are real rows in the
	// live store before trusting the isolation test above.
	for _, syncID := range []string{contaminantRepodocBugSyncID, contaminantRetractionSyncID} {
		obs, err := s.GetObservationBySyncID(syncID)
		if err != nil || obs == nil {
			t.Fatalf("expected contaminant %s to exist live (unisolated) — the isolation test's premise depends on it: %v", syncID, err)
		}
		t.Logf("confirmed live: %s = %q (created %s)", syncID, obs.Title, obs.CreatedAt)
	}
}
