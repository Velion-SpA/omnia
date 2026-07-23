package mcp

// anchor_downrank_test.go — RED→GREEN tests for omnia-structural-forgetting
// PR2 Phase 6: the recall stale-downrank + receipt wiring boundary.
// internal/recall and internal/store stay untouched, pure leaves (design D6)
// — every function under test here lives in internal/mcp only.

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// ─── StalenessPenaltyFor ──────────────────────────────────────────────────────

func TestStalenessPenaltyFor_NoAnchors_ReturnsZero(t *testing.T) {
	if got := StalenessPenaltyFor(nil); got != 0 {
		t.Errorf("StalenessPenaltyFor(nil) = %v, want 0", got)
	}
}

func TestStalenessPenaltyFor_ActiveOrTraveledOnly_ReturnsZero(t *testing.T) {
	anchors := []store.MemoryAnchor{
		{AnchorStatus: store.AnchorStatusActive},
		{AnchorStatus: store.AnchorStatusTraveled},
	}
	if got := StalenessPenaltyFor(anchors); got != 0 {
		t.Errorf("StalenessPenaltyFor(active/traveled) = %v, want 0", got)
	}
}

func TestStalenessPenaltyFor_HasStale_ReturnsDefaultPenalty(t *testing.T) {
	anchors := []store.MemoryAnchor{
		{AnchorStatus: store.AnchorStatusActive},
		{AnchorStatus: store.AnchorStatusStale},
	}
	if got := StalenessPenaltyFor(anchors); got != DefaultStalenessPenalty {
		t.Errorf("StalenessPenaltyFor(stale) = %v, want %v", got, DefaultStalenessPenalty)
	}
}

// ─── BuildStaleReceipt ────────────────────────────────────────────────────────

func TestBuildStaleReceipt_FormatsFileLinesOldNewSHA(t *testing.T) {
	newSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a := store.MemoryAnchor{
		AnchorStatus: store.AnchorStatusStale,
		FilePath:     "internal/foo/bar.go",
		LineStart:    10,
		LineEnd:      20,
		BlameSHA:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NewBlameSHA:  &newSHA,
	}
	got := BuildStaleReceipt(a)
	if !strings.Contains(got, "internal/foo/bar.go:10-20") {
		t.Errorf("receipt missing file:lines: %q", got)
	}
	if !strings.Contains(got, "aaaaaaaa") || !strings.Contains(got, "bbbbbbbb") {
		t.Errorf("receipt missing old/new SHA prefixes: %q", got)
	}
	if !strings.Contains(got, "->") {
		t.Errorf("receipt missing old->new arrow: %q", got)
	}
}

func TestBuildStaleReceipt_NoNewSHA_FallsBackToOriginalForBoth(t *testing.T) {
	a := store.MemoryAnchor{
		AnchorStatus: store.AnchorStatusStale,
		FilePath:     "gone.go",
		LineStart:    1,
		LineEnd:      2,
		BlameSHA:     "cccccccccccccccccccccccccccccccccccccccc",
	}
	got := BuildStaleReceipt(a)
	if got == "" {
		t.Fatal("expected non-empty receipt even without a recorded new_blame_sha")
	}
	if !strings.Contains(got, "cccccccc") {
		t.Errorf("receipt should still show the original SHA on both sides: %q", got)
	}
}

func TestBuildStaleReceipt_ActiveAnchor_ReturnsEmpty(t *testing.T) {
	a := store.MemoryAnchor{AnchorStatus: store.AnchorStatusActive}
	if got := BuildStaleReceipt(a); got != "" {
		t.Errorf("BuildStaleReceipt(active) = %q, want empty", got)
	}
}

// ─── ApplyStalenessDownrank ───────────────────────────────────────────────────

func downrankIDs(results []store.SearchResult) []int64 {
	out := make([]int64, len(results))
	for i, r := range results {
		out[i] = r.ID
	}
	return out
}

func equalInt64s(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// drSR builds a store.SearchResult for ApplyStalenessDownrank tests. ID and
// SyncID live on the embedded store.Observation, so they cannot be set via a
// flat composite literal (Go disallows promoted-field names in a struct
// literal) — this helper keeps every call site below a plain, flat
// (id, syncID) pair, mirroring recall_ranking_test.go's own `sr` helper.
func drSR(id int64, syncID string) store.SearchResult {
	return store.SearchResult{Observation: store.Observation{ID: id, SyncID: syncID}}
}

func TestApplyStalenessDownrank_NoStaleAnchors_OrderUnchanged(t *testing.T) {
	results := []store.SearchResult{drSR(1, "a"), drSR(2, "b")}
	anchors := map[string][]store.MemoryAnchor{"a": {{AnchorStatus: store.AnchorStatusActive}}}
	got := ApplyStalenessDownrank(results, anchors)
	if !equalInt64s(downrankIDs(got), []int64{1, 2}) {
		t.Errorf("expected unchanged order, got %v", downrankIDs(got))
	}
}

func TestApplyStalenessDownrank_EmptyAnchorsMap_NoOp(t *testing.T) {
	results := []store.SearchResult{drSR(1, "a"), drSR(2, "b")}
	got := ApplyStalenessDownrank(results, nil)
	if !equalInt64s(downrankIDs(got), []int64{1, 2}) {
		t.Errorf("expected results unchanged (regression: memory with no anchor unaffected); got %v", downrankIDs(got))
	}
}

func TestApplyStalenessDownrank_StaleSinksAfterFresh_PreservesRelativeOrder(t *testing.T) {
	results := []store.SearchResult{
		drSR(1, "stale-a"),
		drSR(2, "fresh-a"),
		drSR(3, "stale-b"),
		drSR(4, "fresh-b"),
	}
	anchors := map[string][]store.MemoryAnchor{
		"stale-a": {{AnchorStatus: store.AnchorStatusStale}},
		"stale-b": {{AnchorStatus: store.AnchorStatusStale}},
	}
	got := ApplyStalenessDownrank(results, anchors)
	want := []int64{2, 4, 1, 3}
	if !equalInt64s(downrankIDs(got), want) {
		t.Errorf("expected fresh rows first (relative order kept) then stale rows (relative order kept); got %v want %v", downrankIDs(got), want)
	}
}

func TestApplyStalenessDownrank_SentinelAndSignatureRowsNeverMoved(t *testing.T) {
	sentinel := drSR(99, "exact")
	sentinel.Rank = exactSentinelRank
	sig := drSR(50, "sig")
	sig.SignatureMatch = true
	results := []store.SearchResult{
		sentinel,
		sig,
		drSR(1, "stale-a"),
		drSR(2, "fresh-a"),
	}
	anchors := map[string][]store.MemoryAnchor{
		"stale-a": {{AnchorStatus: store.AnchorStatusStale}},
	}
	got := ApplyStalenessDownrank(results, anchors)
	want := []int64{99, 50, 2, 1}
	if !equalInt64s(downrankIDs(got), want) {
		t.Errorf("expected sentinel/signature rows pre-empted (unmoved) then fresh then stale; got %v want %v", downrankIDs(got), want)
	}
}

// ─── BuildReceipt / BuildResultReceipt now surface a real staleness_penalty ──

func TestBuildReceipt_StalenessPenaltyPassthrough(t *testing.T) {
	receipt := BuildReceipt(floatPtr(-1), true, floatPtr(0.5), floatPtr(0.02), floatPtr(0.9), floatPtr(1.0), floatPtr(0.95), DefaultStalenessPenalty)
	if receipt["staleness_penalty"] != DefaultStalenessPenalty {
		t.Errorf("receipt[staleness_penalty] = %v, want %v (passthrough, no longer hardcoded 0)", receipt["staleness_penalty"], DefaultStalenessPenalty)
	}

	receiptZero := BuildReceipt(nil, false, nil, nil, nil, nil, nil, 0)
	if receiptZero["staleness_penalty"] != float64(0) {
		t.Errorf("receipt[staleness_penalty] (no stale anchor) = %v, want 0", receiptZero["staleness_penalty"])
	}
}

// ─── handleSearch integration: structural_forgetting.enabled gate ───────────

func mustObsSyncID(t *testing.T, s *store.Store, id int64) string {
	t.Helper()
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation(%d): %v", id, err)
	}
	return obs.SyncID
}

// TestHandleSearch_StructuralForgettingDisabled_NoDownrankNoReceipt is the
// flag-OFF regression pin: with cfg.StructuralForgetting.Enabled == false
// (the default), a memory with a stale anchor gets NO anchor_receipt and no
// reordering — handleSearch's behavior is byte-for-byte identical to before
// this slice existed (Regression: memory with no anchor unaffected extends
// to "flag off" too).
func TestHandleSearch_StructuralForgettingDisabled_NoDownrankNoReceipt(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-sf-off", "sfproj-off", "/tmp/sfproj-off"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-sf-off", Type: "bugfix", Title: "Auth token missing", Content: "auth token missing from requests",
		Project: "sfproj-off", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obsSyncID := mustObsSyncID(t, s, obsID)
	anchorSyncID, err := s.UpsertAnchor(store.UpsertAnchorParams{
		ObsSyncID: obsSyncID, FilePath: "auth.go", Symbol: "CheckToken",
		LineStart: 1, LineEnd: 5, ContentHash: "h1",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if err := s.MarkAnchorStale(anchorSyncID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	search := handleSearch(s, MCPConfig{StructuralForgetting: config.StructuralForgettingConfig{Enabled: false}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "auth token", "project": "sfproj-off", "scope": "project", "limit": 5.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("expected non-empty results, got %#v", body["results"])
	}
	entry := results[0].(map[string]any)
	if _, present := entry["anchor_receipt"]; present {
		t.Errorf("anchor_receipt present with structural_forgetting disabled: %#v", entry["anchor_receipt"])
	}
}

// TestHandleSearch_StructuralForgettingEnabled_StaleMemoryDownrankedWithReceipt
// is the flag-ON happy path (Requirement 6): a stale-anchored memory ranks
// below an equally-relevant fresh one and carries an anchor_receipt + a
// populated score_breakdown.staleness_penalty.
func TestHandleSearch_StructuralForgettingEnabled_StaleMemoryDownrankedWithReceipt(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-sf-on", "sfproj-on", "/tmp/sfproj-on"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	staleID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-sf-on", Type: "bugfix", Title: "JWT auth token check", Content: "JWT auth token check implementation",
		Project: "sfproj-on", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation stale: %v", err)
	}
	freshID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-sf-on", Type: "bugfix", Title: "JWT auth token validate", Content: "JWT auth token validate implementation",
		Project: "sfproj-on", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation fresh: %v", err)
	}

	staleSyncID := mustObsSyncID(t, s, staleID)
	anchorSyncID, err := s.UpsertAnchor(store.UpsertAnchorParams{
		ObsSyncID: staleSyncID, FilePath: "jwt.go", Symbol: "CheckJWT",
		LineStart: 1, LineEnd: 5, ContentHash: "h1",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if err := s.MarkAnchorStale(anchorSyncID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	search := handleSearch(s, MCPConfig{StructuralForgetting: config.StructuralForgettingConfig{Enabled: true}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "JWT auth token", "project": "sfproj-on", "scope": "project", "limit": 5.0, "explain": true,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("expected 2 results, got %#v", body["results"])
	}
	first := results[0].(map[string]any)
	if int64(first["id"].(float64)) != freshID {
		t.Errorf("expected fresh memory (id %d) ranked first; got id=%v", freshID, first["id"])
	}
	second := results[1].(map[string]any)
	if int64(second["id"].(float64)) != staleID {
		t.Fatalf("expected stale memory (id %d) ranked second; got id=%v", staleID, second["id"])
	}
	if _, present := second["anchor_receipt"]; !present {
		t.Errorf("expected anchor_receipt present on the stale memory's entry")
	}
	breakdown, ok := second["score_breakdown"].(map[string]any)
	if !ok {
		t.Fatalf("expected score_breakdown on stale entry, got %#v", second["score_breakdown"])
	}
	if breakdown["staleness_penalty"] != DefaultStalenessPenalty {
		t.Errorf("breakdown[staleness_penalty] = %v, want %v", breakdown["staleness_penalty"], DefaultStalenessPenalty)
	}
	firstBreakdown, ok := first["score_breakdown"].(map[string]any)
	if !ok {
		t.Fatalf("expected score_breakdown on fresh entry")
	}
	if firstBreakdown["staleness_penalty"] != float64(0) {
		t.Errorf("fresh entry breakdown[staleness_penalty] = %v, want 0", firstBreakdown["staleness_penalty"])
	}
}
