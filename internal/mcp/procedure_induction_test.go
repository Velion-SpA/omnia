package mcp

import (
	"context"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/store"
)

// ─── Phase 4/5 — Online Candidate Induction + SSGM Reuse Attribution ────────

// fakeProcedureInducer is a test double for llm.ProcedureInducer.
type fakeProcedureInducer struct {
	result llm.InducedProcedure
	err    error
	called bool
}

func (f *fakeProcedureInducer) Induce(_ context.Context, _ string) (llm.InducedProcedure, error) {
	f.called = true
	if f.err != nil {
		return llm.InducedProcedure{}, f.err
	}
	return f.result, nil
}

func goldenInducedProcedure() llm.InducedProcedure {
	return llm.InducedProcedure{
		Trigger:           "slice index out of range",
		Steps:             []llm.InducedStep{{Order: 1, Template: "add a length guard before indexing"}},
		ExpectedOutcome:   "handler no longer panics",
		PostconditionKind: store.PostconditionTestsPass,
		Model:             "fake-model",
	}
}

// waitForProceduralHook blocks until the async procedural-memory goroutine
// (runProceduralHooksForObservation, review fix: induction/reuse-feedback
// now run in a fire-and-forget goroutine so a hung LLM CLI can't wedge
// handleSave/handleUpdate) signals completion via ProceduralWiring's
// test-only afterHook seam. Fails the test after a generous bound so a
// genuinely stuck hook fails fast instead of hanging CI.
func waitForProceduralHook(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the async procedural-memory hook to complete")
	}
}

// slowProcedureInducer is a test double whose Induce call blocks until
// either release is closed or ctx is done — used to prove the online
// induction hook is bounded/non-blocking instead of running SafeInduce
// synchronously on handleSave's own goroutine.
type slowProcedureInducer struct {
	release chan struct{}
}

func (f *slowProcedureInducer) Induce(ctx context.Context, _ string) (llm.InducedProcedure, error) {
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	return llm.InducedProcedure{}, ctx.Err()
}

// TestHandleSave_OutcomeWorkedInducesCandidatePlaybook (task 4.1) verifies
// that a bugfix save with outcome=worked, with cfg.Procedural configured
// with a fake inducer, creates a candidate playbook procedure linked via
// source_obs_sync_ids.
func TestHandleSave_OutcomeWorkedInducesCandidatePlaybook(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	inducer := &fakeProcedureInducer{result: goldenInducedProcedure()}
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: inducer}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed slice index panic",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)
	if !inducer.called {
		t.Fatal("expected the configured Inducer to be called")
	}

	body := callResultJSON(t, res)
	savedSyncID, _ := body["sync_id"].(string)
	if savedSyncID == "" {
		t.Fatal("expected envelope sync_id")
	}

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected 1 induced procedure; got %d", len(procedures))
	}
	p := procedures[0]
	if p.Polarity != store.ProcedurePolarityPlaybook {
		t.Errorf("Polarity = %q; want %q", p.Polarity, store.ProcedurePolarityPlaybook)
	}
	if p.State != store.ProcedureStateCandidate {
		t.Errorf("State = %q; want %q", p.State, store.ProcedureStateCandidate)
	}
	if len(p.SourceObsSyncIDs) != 1 || p.SourceObsSyncIDs[0] != savedSyncID {
		t.Errorf("SourceObsSyncIDs = %v; want [%q]", p.SourceObsSyncIDs, savedSyncID)
	}
}

// TestHandleSave_OutcomeDidNotWorkInducesAntiPlaybook triangulates the
// polarity mapping with the OTHER outcome value.
func TestHandleSave_OutcomeDidNotWorkInducesAntiPlaybook(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	inducer := &fakeProcedureInducer{result: goldenInducedProcedure()}
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: inducer}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Attempted retry loop",
		"content": "Tried a bare retry loop; it made the flake worse.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "did_not_work",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 || procedures[0].Polarity != store.ProcedurePolarityAntiPlaybook {
		t.Fatalf("expected 1 anti_playbook procedure; got %+v", procedures)
	}
}

// TestHandleSave_NoProceduralConfigured_NoInductionNoError (task 4.4)
// verifies that with cfg.Procedural unset (the default, procedural.enabled
// =false), an outcome=worked save succeeds with no procedure induced and no
// error — the total no-op contract. cfg.Procedural is nil here, so
// runProceduralHooksForObservation never spawns a goroutine — no async wait
// needed.
func TestHandleSave_NoProceduralConfigured_NoInductionNoError(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed slice index panic",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 0 {
		t.Fatalf("expected no induced procedure when Procedural is nil; got %d", len(procedures))
	}
}

// TestHandleSave_ProceduralEnabledButNoRunnerConfigured_NoErrorDegradesGracefully
// covers the "Procedural wiring exists but Inducer is nil" branch —
// SafeInduce degrades to DeterministicInduce rather than erroring.
func TestHandleSave_ProceduralEnabledButNoRunnerConfigured_NoErrorDegradesGracefully(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: nil}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed slice index panic",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)
	// DeterministicInduce produces a single verbatim-content step — a
	// procedure IS still induced (degraded quality, not degraded to nothing).
	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected 1 degraded-fallback procedure; got %d", len(procedures))
	}
}

// TestHandleSave_SlowInducerDoesNotBlockSave (Blocking 1) verifies that
// runProceduralHooksForObservation's online induction call runs in a
// bounded, fire-and-forget goroutine: a deliberately slow/blocking Inducer
// must NOT delay handleSave's response. Prior to this fix, SafeInduce ran
// synchronously inside handleSave/handleUpdate — which run on writeQueue's
// single worker goroutine — so a hung LLM CLI would wedge every write tool
// for the whole session.
func TestHandleSave_SlowInducerDoesNotBlockSave(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	release := make(chan struct{})
	inducer := &slowProcedureInducer{release: release}
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{
		Inducer:     inducer,
		HookTimeout: 2 * time.Second, // safety net only; release is closed explicitly below
	}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	start := time.Now()
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed slice index panic",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
	}}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("handleSave took %s with a blocking Inducer configured; the induction hook must be async/non-blocking", elapsed)
	}

	// Release the blocked Inducer and wait for the bounded goroutine to
	// finish before the test's temp store closes, so it never touches a
	// closed DB after this test returns.
	close(release)
	waitForProceduralHook(t, done)
}

// ─── Phase 5: SSGM applied_procedure reuse feedback ─────────────────────────

// TestHandleSave_AppliedProcedureWorkedConfirmsReuse (task 5.1) verifies an
// explicit applied_procedure ref + outcome=worked invokes ConfirmReuse and
// updates its counters.
func TestHandleSave_AppliedProcedureWorkedConfirmsReuse(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "slice index out of range",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "add a length guard"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	})
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":             "Reused the slice guard playbook",
		"content":           "Applied the stored slice guard steps; it worked again.",
		"type":              "bugfix",
		"project":           "engram",
		"outcome":           "worked",
		"applied_procedure": syncID,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.ReuseConfirmed != 1 {
		t.Errorf("ReuseConfirmed = %d; want 1", got.ReuseConfirmed)
	}
}

// TestHandleSave_AppliedProcedureDidNotWorkContradicts triangulates with the
// OTHER outcome value.
func TestHandleSave_AppliedProcedureDidNotWorkContradicts(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "slice index out of range",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "add a length guard"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	})
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	_, err = h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":             "Reused the slice guard playbook, but it failed this time",
		"content":           "Applied the stored slice guard steps; the bug still happened.",
		"type":              "bugfix",
		"project":           "engram",
		"outcome":           "did_not_work",
		"applied_procedure": syncID,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	waitForProceduralHook(t, done)

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.ContradictedCount != 1 {
		t.Errorf("ContradictedCount = %d; want 1", got.ContradictedCount)
	}
}

// TestHandleSave_NoAppliedProcedureNoSessionMatch_SafeNoOp (task 5.3)
// verifies that with no applied_procedure and no session-match candidate,
// the save succeeds unchanged and no governance call happens.
func TestHandleSave_NoAppliedProcedureNoSessionMatch_SafeNoOp(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	// A procedure exists, but was never linked to this session's observations.
	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "totally unrelated trigger text",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "step"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	})
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed something else entirely",
		"content": "No relation to the stored procedure's trigger at all.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.ReuseConfirmed != 0 {
		t.Errorf("ReuseConfirmed = %d; want 0 (no session match, no applied_procedure)", got.ReuseConfirmed)
	}
}

// TestHandleUpdate_AppliedProcedureConfirmsReuse verifies mem_update also
// wires applied_procedure feedback (spec: "mem_save/mem_update MUST accept
// optional applied_procedure").
func TestHandleUpdate_AppliedProcedureConfirmsReuse(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-proc-update", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-proc-update",
		Type:      "bugfix",
		Title:     "Fixed slice index panic",
		Content:   "Guarded the slice access with a length check before indexing.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "slice index out of range",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "add a length guard"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	})
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{}}
	cfg.Procedural.afterHook = func() { close(done) }
	res, err := handleUpdate(s, cfg)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":                float64(id),
		"outcome":           "worked",
		"applied_procedure": syncID,
	}}})
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.ReuseConfirmed != 1 {
		t.Errorf("ReuseConfirmed = %d; want 1", got.ReuseConfirmed)
	}
}

// ─── Blocking review fix #2: self-crediting reuse-feedback ──────────────────

// TestHandleSave_NewlyInducedProcedureNotSelfCredited (Blocking 2) verifies
// that a brand-new candidate procedure induced from THIS save's own
// observation is never self-credited by that same save's reuse-feedback
// fallback: induction (Phase 4) runs BEFORE the reuse-feedback fallback
// (Phase 5), so without guards the fallback would FTS-match the row it just
// created (it's in this session's observations) and call ConfirmReuse on it.
func TestHandleSave_NewlyInducedProcedureNotSelfCredited(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	inducer := &fakeProcedureInducer{result: goldenInducedProcedure()} // Trigger: "slice index out of range"
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: inducer}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		// Title deliberately matches the induced procedure's own Trigger
		// text word-for-word so the FTS5 fallback match in
		// resolveSessionProcedureMatch would hit this SAME just-created
		// procedure if the self-exclusion guard were missing.
		"title":   "slice index out of range",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
		// Deliberately NO applied_procedure — this exercises the fallback
		// session-match path the self-credit bug hit.
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected exactly 1 induced procedure; got %d", len(procedures))
	}
	p := procedures[0]
	if p.ReuseConfirmed != 0 {
		t.Errorf("ReuseConfirmed = %d; want 0 (must not self-credit the procedure induced from THIS same save)", p.ReuseConfirmed)
	}
	if p.State != store.ProcedureStateCandidate {
		t.Errorf("State = %q; want %q (a self-match must never promote it)", p.State, store.ProcedureStateCandidate)
	}
}

// TestHandleSave_DedupRefreshedTrustedProcedureNotSelfCredited (Blocking 2 +
// nit) proves the currentObsSyncID exclusion guard specifically: the
// trusted-state filter alone does NOT protect a procedure that the dedup
// fix (nit) refreshes in place, since a dedup UPDATE merges the CURRENT
// observation into an already-TRUSTED procedure's source_obs_sync_ids. Only
// the self-exclusion guard stops the same save's reuse-feedback fallback
// from then "confirming reuse" of the procedure it just refreshed.
func TestHandleSave_DedupRefreshedTrustedProcedureNotSelfCredited(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Pre-seed an ALREADY-TRUSTED playbook procedure, as if promoted by
	// prior genuine reuses.
	trustedSyncID, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "slice index out of range",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "add a length guard"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.8,
		State:             store.ProcedureStateTrusted,
		ReuseConfirmed:    3,
		SourceObsSyncIDs:  []string{"obs-prior-sync-id"},
	})
	if err != nil {
		t.Fatalf("UpsertProcedure (seed trusted): %v", err)
	}

	inducer := &fakeProcedureInducer{result: llm.InducedProcedure{
		Trigger:           "slice index out of range", // same trigger text -> findDedupProcedure hits the seeded row
		Steps:             []llm.InducedStep{{Order: 1, Template: "add a length guard"}},
		PostconditionKind: store.PostconditionTestsPass,
		Model:             "fake-model",
	}}
	done := make(chan struct{})
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: inducer}}
	cfg.Procedural.afterHook = func() { close(done) }
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "slice index out of range",
		"content": "Guarded the slice access with a length check before indexing, again.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "worked",
		// Deliberately NO applied_procedure — exercises the fallback
		// session-match path the self-credit bug hit.
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}
	waitForProceduralHook(t, done)

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected the dedup path to UPDATE the seeded procedure, not mint a new one; got %d", len(procedures))
	}
	p := procedures[0]
	if p.SyncID != trustedSyncID {
		t.Fatalf("SyncID = %q; want the seeded trusted procedure's sync_id %q (dedup should update it in place)", p.SyncID, trustedSyncID)
	}
	if p.ReuseConfirmed != 3 {
		t.Errorf("ReuseConfirmed = %d; want unchanged 3 (this save must not self-credit a reuse of the procedure it just refreshed)", p.ReuseConfirmed)
	}
}

// ─── Related nit: online induction dedup ────────────────────────────────────

// TestHandleSave_RecurringBugfixDedupsInsteadOfMintingDuplicate (nit fix)
// verifies that two saves recording the same recurring bugfix outcome
// UPDATE the same procedure row (merging source_obs_sync_ids) instead of
// minting an unbounded new candidate row on every save.
func TestHandleSave_RecurringBugfixDedupsInsteadOfMintingDuplicate(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	// Trigger deliberately matches the observation title word-for-word: this
	// test's dedupKey falls back to obs.Title (no error_signature
	// supplied), and findDedupProcedure's FTS5 lookup needs full token
	// overlap with the prior save's stored Trigger to find it.
	inducer := &fakeProcedureInducer{result: llm.InducedProcedure{
		Trigger:           "slice index out of range",
		Steps:             []llm.InducedStep{{Order: 1, Template: "add a length guard before indexing"}},
		PostconditionKind: store.PostconditionTestsPass,
		Model:             "fake-model",
	}}
	cfg := MCPConfig{Procedural: &ProceduralWiring{Inducer: inducer}}
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	// Same title (the dedupKey fallback) both times, but DIFFERENT content:
	// AddObservation's own exact-duplicate detection hashes content, so
	// identical content would collapse both calls into ONE observation row
	// (duplicate_count++) regardless of this fix — varying content keeps
	// these two genuinely distinct source observations, which is what this
	// test needs to prove the dedup MERGES source_obs_sync_ids rather than
	// each save simply reusing the same single observation.
	contents := []string{
		"Guarded the slice access with a length check before indexing.",
		"Guarded the slice access with a length check before indexing (recurrence #2).",
	}

	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		cfg.Procedural.afterHook = func() { close(done) }
		res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "slice index out of range",
			"content": contents[i],
			"type":    "bugfix",
			"project": "engram",
			"outcome": "worked",
		}}})
		if err != nil {
			t.Fatalf("handler error (save #%d): %v", i+1, err)
		}
		if res.IsError {
			t.Fatalf("unexpected save error (save #%d): %s", i+1, callResultText(t, res))
		}
		waitForProceduralHook(t, done)
	}

	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected the second save to UPDATE the same procedure row (dedup), not mint a new one; got %d procedures", len(procedures))
	}
	if len(procedures[0].SourceObsSyncIDs) != 2 {
		t.Errorf("SourceObsSyncIDs = %v; want 2 entries (both saves' observations merged)", procedures[0].SourceObsSyncIDs)
	}
}
