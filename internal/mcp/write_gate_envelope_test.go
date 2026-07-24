package mcp

// write_gate_envelope_test.go — RED→GREEN tests for omnia-0.3.1-write-hygiene
// PR5 (design obs #1668 D4, spec write-gate REQ "Envelope Transparency" +
// "Default-On With Kill-Switch"). handleSave must call store.SaveObservation
// (instead of the AddObservation thin wrapper) and surface the decision via
// extra["write_gate"] = {decision, target_id, similarity, reason}, gated
// entirely by MCPConfig.WriteHygieneEnabled so the envelope's own
// kill-switch can never drift from store.Config's kill-switch.

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/store"
)

// newMCPTestStoreWithWriteHygiene builds a store with the write-gate ladder
// enabled at the design defaults (obs #1668 D5: noop=0.98, update=0.9,
// shrink_guard=0.9, candidate_limit=10) — mirrors internal/store's own
// gateEnabledDefaults (write_gate_test.go) so the same content fixtures
// produce the same NOOP/AUTO-UPDATE/RELATE classifications at this layer.
func newMCPTestStoreWithWriteHygiene(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	cfg.WriteHygieneEnabled = true
	cfg.NoopThreshold = 0.98
	cfg.UpdateThreshold = 0.9
	cfg.ShrinkGuard = 0.9
	cfg.CandidateLimit = 10
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// enrollProject registers a project name as "backed by known context" (see
// s.EnrollProject's own doc) so handleSave's explicit-project validation
// accepts it without depending on git/cwd auto-detection — mirrors the
// existing `s.EnrollProject("engram")` convention already used by
// signature_outcome_test.go/provenance_test.go, needed because these tests'
// literal project names are test fixtures unrelated to this checkout's own
// cwd-detected project.
func enrollProject(t *testing.T, s *store.Store, project string) {
	t.Helper()
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project %q: %v", project, err)
	}
}

func mustHandleSave(t *testing.T, s *store.Store, cfg MCPConfig, args map[string]any) (*mcppkg.CallToolResult, map[string]any) {
	t.Helper()
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	return res, callResultJSON(t, res)
}

// resultText extracts the envelope's own "result" text field (the tool
// output message) from a decoded response body — distinct from
// callResultText, which returns the WHOLE JSON-encoded envelope.
func resultText(t *testing.T, body map[string]any) string {
	t.Helper()
	text, ok := body["result"].(string)
	if !ok {
		t.Fatalf("expected envelope to contain a string \"result\" field, got %v", body["result"])
	}
	return text
}

// ─── Task 5.1 — NOOP ─────────────────────────────────────────────────────────

// TestHandleSaveWriteGateEnvelope_NoopNamesExistingID pins: on NOOP, the
// envelope's write_gate.decision is "noop", target_id names the existing
// row, and the top-level "id" (extra["id"]) is ALSO that existing row's ID
// (not a new row) — with a human-readable notice replacing "Memory saved".
func TestHandleSaveWriteGateEnvelope_NoopNamesExistingID(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	enrollProject(t, s, "wg-mcp-noop")
	cfg := MCPConfig{WriteHygieneEnabled: true}

	_, existingBody := mustHandleSave(t, s, cfg, map[string]any{
		"title":   "Null pointer crash writeup",
		"content": "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token.",
		"type":    "bugfix",
		"project": "wg-mcp-noop",
	})
	existingID, ok := existingBody["id"].(float64)
	if !ok {
		t.Fatalf("expected existing save's id to be a number, got %v", existingBody["id"])
	}

	_, body := mustHandleSave(t, s, cfg, map[string]any{
		"title":   "Null pointer crash notes",
		"content": "fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token",
		"type":    "bugfix",
		"project": "wg-mcp-noop",
	})

	writeGate, ok := body["write_gate"].(map[string]any)
	if !ok {
		t.Fatalf("expected write_gate object in envelope, got %v", body["write_gate"])
	}
	if writeGate["decision"] != store.WriteGateDecisionNoop {
		t.Fatalf("expected decision %q, got %v", store.WriteGateDecisionNoop, writeGate["decision"])
	}
	if got, ok := writeGate["target_id"].(float64); !ok || got != existingID {
		t.Fatalf("expected target_id=%v, got %v", existingID, writeGate["target_id"])
	}
	if sim, ok := writeGate["similarity"].(float64); !ok || sim < 0.98 {
		t.Fatalf("expected similarity >= 0.98, got %v", writeGate["similarity"])
	}
	if reason, _ := writeGate["reason"].(string); !strings.Contains(reason, "near-duplicate") {
		t.Fatalf("expected reason to mention near-duplicate, got %q", reason)
	}
	if body["id"] != existingID {
		t.Fatalf("expected NOOP's top-level id to equal the existing row's id %v, got %v", existingID, body["id"])
	}
	text := resultText(t, body)
	wantNotice := "identical to existing #"
	if !strings.Contains(text, wantNotice) {
		t.Fatalf("expected result text to contain %q, got %q", wantNotice, text)
	}
	if strings.HasPrefix(text, "Memory saved:") {
		t.Fatalf("expected NOOP notice to replace the default 'Memory saved' message, got %q", text)
	}
}

// ─── Task 5.1 — AUTO-UPDATE ──────────────────────────────────────────────────

// TestHandleSaveWriteGateEnvelope_UpdateNamesTarget pins: on AUTO-UPDATE (the
// similarity-triggered ladder branch), write_gate.decision is "update",
// target_id names the extended row, and the result text states the update
// occurred against that row, distinguishable from a new-save response.
func TestHandleSaveWriteGateEnvelope_UpdateNamesTarget(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	enrollProject(t, s, "wg-mcp-update")
	cfg := MCPConfig{WriteHygieneEnabled: true}

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	revision := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical rollback path"

	_, existingBody := mustHandleSave(t, s, cfg, map[string]any{
		"title":   "DB migration runbook",
		"content": original,
		"type":    "procedural",
		"project": "wg-mcp-update",
	})
	existingID := existingBody["id"].(float64)

	_, body := mustHandleSave(t, s, cfg, map[string]any{
		"title":   "DB migration runbook v2",
		"content": revision,
		"type":    "procedural",
		"project": "wg-mcp-update",
	})

	writeGate := body["write_gate"].(map[string]any)
	if writeGate["decision"] != store.WriteGateDecisionUpdate {
		t.Fatalf("expected decision %q, got %v", store.WriteGateDecisionUpdate, writeGate["decision"])
	}
	if got := writeGate["target_id"].(float64); got != existingID {
		t.Fatalf("expected target_id=%v, got %v", existingID, got)
	}
	if sim := writeGate["similarity"].(float64); sim <= 0.9 || sim >= 0.98 {
		t.Fatalf("expected similarity strictly between 0.9 and 0.98, got %v", sim)
	}
	if body["id"] != existingID {
		t.Fatalf("expected UPDATE's top-level id to equal the existing row's id %v, got %v", existingID, body["id"])
	}
	text := resultText(t, body)
	if !strings.Contains(text, "#1") || !strings.Contains(text, "instead of duplicating") {
		t.Fatalf("expected result text to name the target and say 'instead of duplicating', got %q", text)
	}
}

// ─── Task 5.1/5.2 — RELATE ───────────────────────────────────────────────────

// TestHandleSaveWriteGateEnvelope_RelateKeepsJudgmentRequiredByteIdentical
// pins task 5.2: RELATE keeps the existing judgment_required + candidates
// flow byte-identical (write_gate is purely additive/informational on this
// path) — the new row is still inserted and FindCandidates still runs
// exactly as it did before PR5.
func TestHandleSaveWriteGateEnvelope_RelateKeepsJudgmentRequiredByteIdentical(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	// Separate projects for the gate-off and gate-on runs (rather than
	// reusing one project for both) so the gate-ON run's FTS candidate
	// search — scoped by project — never sees the gate-OFF run's own
	// "DB migration short note" row as an (identical-content) candidate,
	// which would otherwise self-match at jaccard 1.0 and misfire NOOP
	// instead of RELATE.
	enrollProject(t, s, "wg-mcp-relate-off")
	enrollProject(t, s, "wg-mcp-relate-on")

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	unrelatedEnough := "Wrote a short note about the postgres migration effort mentioning a backup step and a validation pass, but skipping most of the runbook detail, window scheduling, and smoke test checklist entirely"

	// GATE OFF run: its own existing row + relate-triggering new row, to
	// capture the pre-PR5 judgment_required/candidates shape as the
	// byte-identical baseline (write_gate must never touch it).
	mustHandleSave(t, s, MCPConfig{}, map[string]any{
		"title":   "DB migration runbook",
		"content": original,
		"type":    "procedural",
		"project": "wg-mcp-relate-off",
	})
	offRes, offBody := mustHandleSave(t, s, MCPConfig{}, map[string]any{
		"title":   "DB migration short note",
		"content": unrelatedEnough,
		"type":    "procedural",
		"project": "wg-mcp-relate-off",
	})
	if _, ok := offBody["write_gate"]; ok {
		t.Fatalf("gate off must never surface write_gate, got %v", offBody["write_gate"])
	}

	// GATE ON run: its own, independent existing row + the same
	// relate-triggering fixture shape — assert judgment_required/candidates
	// match the gate-off shape, plus the additive write_gate key.
	_, existingBody := mustHandleSave(t, s, MCPConfig{WriteHygieneEnabled: true}, map[string]any{
		"title":   "DB migration runbook",
		"content": original,
		"type":    "procedural",
		"project": "wg-mcp-relate-on",
	})
	existingID := existingBody["id"].(float64)
	onRes, onBody := mustHandleSave(t, s, MCPConfig{WriteHygieneEnabled: true}, map[string]any{
		"title":   "DB migration short note",
		"content": unrelatedEnough,
		"type":    "procedural",
		"project": "wg-mcp-relate-on",
	})

	if offBody["judgment_required"] != onBody["judgment_required"] {
		t.Fatalf("judgment_required must be byte-identical regardless of the gate: off=%v on=%v", offBody["judgment_required"], onBody["judgment_required"])
	}
	if offBody["judgment_required"] != true {
		t.Fatalf("expected fixture to trigger judgment_required=true (candidate found by FindCandidates), got %v (result: %s)", offBody["judgment_required"], callResultText(t, offRes))
	}
	offCandidates, _ := offBody["candidates"].([]any)
	onCandidates, _ := onBody["candidates"].([]any)
	if len(offCandidates) == 0 || len(offCandidates) != len(onCandidates) {
		t.Fatalf("expected the same non-empty candidates count regardless of the gate: off=%d on=%d", len(offCandidates), len(onCandidates))
	}

	writeGate, ok := onBody["write_gate"].(map[string]any)
	if !ok {
		t.Fatalf("expected write_gate object in envelope, got %v", onBody["write_gate"])
	}
	if writeGate["decision"] != store.WriteGateDecisionRelate {
		t.Fatalf("expected decision %q, got %v (result: %s)", store.WriteGateDecisionRelate, writeGate["decision"], callResultText(t, onRes))
	}
	if got := writeGate["target_id"].(float64); got != existingID {
		t.Fatalf("expected RELATE's target_id to name the existing row %v, got %v", existingID, got)
	}
	if sim := writeGate["similarity"].(float64); sim >= 0.9 {
		t.Fatalf("expected similarity well under 0.9, got %v", sim)
	}
	// RELATE inserts a NEW row — the top-level id must NOT equal target_id.
	if onBody["id"] == writeGate["target_id"] {
		t.Fatalf("expected RELATE to insert a new row (id != target_id), got id=%v", onBody["id"])
	}
}

// ─── Task 5.1 — plain SAVE (no candidate) ────────────────────────────────────

// TestHandleSaveWriteGateEnvelope_SaveOmitsTargetID pins D4: "SAVE omits
// target_id" — a save with no FTS candidate at all gets a write_gate object
// without a target_id key, and the default "Memory saved" message stays
// unchanged.
func TestHandleSaveWriteGateEnvelope_SaveOmitsTargetID(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	enrollProject(t, s, "wg-mcp-save")

	_, body := mustHandleSave(t, s, MCPConfig{WriteHygieneEnabled: true}, map[string]any{
		"title":   "Kilo Lima Mike November",
		"content": "Completely unrelated subject matter with its own distinct vocabulary.",
		"type":    "discovery",
		"project": "wg-mcp-save",
	})

	writeGate, ok := body["write_gate"].(map[string]any)
	if !ok {
		t.Fatalf("expected write_gate object in envelope, got %v", body["write_gate"])
	}
	if writeGate["decision"] != store.WriteGateDecisionSave {
		t.Fatalf("expected decision %q, got %v", store.WriteGateDecisionSave, writeGate["decision"])
	}
	if _, ok := writeGate["target_id"]; ok {
		t.Fatalf("expected target_id to be omitted for a plain SAVE, got %v", writeGate["target_id"])
	}
	text := resultText(t, body)
	if !strings.HasPrefix(text, "Memory saved:") {
		t.Fatalf("expected the default 'Memory saved' message for plain SAVE, got %q", text)
	}
}

// ─── Task 5.4 — kill-switch byte-identical envelope ──────────────────────────

// TestHandleSaveWriteGateKillSwitchByteIdentical pins task 5.4: with
// MCPConfig.WriteHygieneEnabled=false (the default), the envelope has NO
// write_gate key at all — even when the underlying SaveResult's Decision is
// "update" via the PRE-EXISTING, unconditional topic_key-upsert step (which
// runs regardless of the write-hygiene flag). This is the scenario that
// actually distinguishes "gate off" from "decision happens to be save": a
// naive implementation that only suppressed write_gate for
// Decision==WriteGateDecisionSave would still leak a write_gate key here.
func TestHandleSaveWriteGateKillSwitchByteIdentical(t *testing.T) {
	s := newMCPTestStore(t) // WriteHygieneEnabled left at the store's own zero value (false)
	enrollProject(t, s, "wg-mcp-killswitch")
	cfg := MCPConfig{} // WriteHygieneEnabled left at zero value (false)

	_, firstBody := mustHandleSave(t, s, cfg, map[string]any{
		"title":     "Auth architecture",
		"content":   "Define boundaries for auth middleware",
		"type":      "architecture",
		"project":   "wg-mcp-killswitch",
		"topic_key": "architecture/auth-model",
	})
	if _, ok := firstBody["write_gate"]; ok {
		t.Fatalf("gate off must never surface write_gate on the first save, got %v", firstBody["write_gate"])
	}
	firstText := resultText(t, firstBody)
	if !strings.HasPrefix(firstText, "Memory saved:") {
		t.Fatalf("expected the default 'Memory saved' message, got %q", firstText)
	}

	// Same topic_key, different content: the pre-existing topic_key-upsert
	// step fires (Decision=WriteGateDecisionUpdate) REGARDLESS of the gate —
	// the envelope must still stay byte-for-byte pre-write-hygiene shaped.
	_, secondBody := mustHandleSave(t, s, cfg, map[string]any{
		"title":     "Auth architecture v2",
		"content":   "Define boundaries for auth middleware, revised",
		"type":      "architecture",
		"project":   "wg-mcp-killswitch",
		"topic_key": "architecture/auth-model",
	})
	if _, ok := secondBody["write_gate"]; ok {
		t.Fatalf("gate off must never surface write_gate even when the topic_key-upsert step fires, got %v", secondBody["write_gate"])
	}
	secondText := resultText(t, secondBody)
	if !strings.HasPrefix(secondText, "Memory saved:") {
		t.Fatalf("expected the default 'Memory saved' message even for a topic_key upsert with the gate off, got %q", secondText)
	}
	if strings.Contains(secondText, "instead of duplicating") || strings.Contains(secondText, "identical to existing") {
		t.Fatalf("gate off must never emit write-gate notice text, got %q", secondText)
	}
}
