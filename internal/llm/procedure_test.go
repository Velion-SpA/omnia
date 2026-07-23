package llm

// Note: this test file lives in package llm (not llm_test), mirroring
// claude_test.go, so it can inject fakeCLI directly into ClaudeRunner/
// OpenCodeRunner without spawning real processes.

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// ─── Compile-time interface checks ────────────────────────────────────────────

var (
	_ ProcedureInducer = (*ClaudeRunner)(nil)
	_ ProcedureInducer = (*OpenCodeRunner)(nil)
)

// ─── ClaudeRunner.Induce ──────────────────────────────────────────────────────

// TestClaudeRunner_Induce_GoldenJSON (task 3.1) verifies Induce parses a
// valid JSON program via a fake CLI into a fully-populated InducedProcedure.
func TestClaudeRunner_Induce_GoldenJSON(t *testing.T) {
	innerJSON := `{"trigger":"nil pointer dereference in handler","steps":[{"order":1,"template":"check {{var}} before use","slots":["var"]},{"order":2,"template":"add guard clause"}],"expected_outcome":"handler no longer panics","postcondition_kind":"tests_pass","postcondition_expr":"go test ./..."}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{"input_tokens":300,"output_tokens":50}},"duration_ms":900}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	got, err := r.Induce(context.Background(), "induce a procedure from this trajectory")
	if err != nil {
		t.Fatalf("Induce: unexpected error: %v", err)
	}
	if got.Trigger != "nil pointer dereference in handler" {
		t.Errorf("Trigger = %q; unexpected", got.Trigger)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("Steps: want 2; got %d", len(got.Steps))
	}
	if got.Steps[0].Template != "check {{var}} before use" || len(got.Steps[0].Slots) != 1 || got.Steps[0].Slots[0] != "var" {
		t.Errorf("Steps[0] unexpected: %+v", got.Steps[0])
	}
	if got.ExpectedOutcome != "handler no longer panics" {
		t.Errorf("ExpectedOutcome = %q; unexpected", got.ExpectedOutcome)
	}
	if got.PostconditionKind != "tests_pass" {
		t.Errorf("PostconditionKind = %q; want %q", got.PostconditionKind, "tests_pass")
	}
	if got.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q; want %q", got.Model, "claude-haiku-4-5")
	}
	if got.DurationMS != 900 {
		t.Errorf("DurationMS = %d; want 900", got.DurationMS)
	}
}

// TestClaudeRunner_Induce_MalformedInnerJSON (task 3.3) verifies a malformed
// inner JSON payload returns ErrInvalidJSON.
func TestClaudeRunner_Induce_MalformedInnerJSON(t *testing.T) {
	envelope := `{"type":"result","result":"not valid json","total_cost_usd":0,"modelUsage":{},"duration_ms":0}`

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	_, err := r.Induce(context.Background(), "induce")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

// TestClaudeRunner_Induce_RejectsModelSetPolarity (task 3.3) verifies that
// when the model's raw JSON output sets a non-empty "polarity" field,
// Induce rejects it via ErrModelSetPolarity — polarity is derived by the
// caller from the source outcome, never by the model (design decision #2).
func TestClaudeRunner_Induce_RejectsModelSetPolarity(t *testing.T) {
	innerJSON := `{"trigger":"some bug","steps":[{"order":1,"template":"do the thing"}],"postcondition_kind":"custom","polarity":"playbook"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{}},"duration_ms":100}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	_, err := r.Induce(context.Background(), "induce")
	if !errors.Is(err, ErrModelSetPolarity) {
		t.Errorf("expected ErrModelSetPolarity; got %v", err)
	}
}

// TestClaudeRunner_Induce_RejectsEmptyTrigger triangulates the validation
// path with a DIFFERENT malformed shape: valid JSON, but an empty trigger.
func TestClaudeRunner_Induce_RejectsEmptyTrigger(t *testing.T) {
	innerJSON := `{"trigger":"","steps":[{"order":1,"template":"do the thing"}],"postcondition_kind":"custom"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{}},"duration_ms":100}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	_, err := r.Induce(context.Background(), "induce")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON for empty trigger; got %v", err)
	}
}

// TestClaudeRunner_Induce_CLIError verifies runCLI errors propagate unwrapped
// (same contract as Compare's own CLIError test).
func TestClaudeRunner_Induce_CLIError(t *testing.T) {
	cliErr := errors.New("exec failed")
	r := &ClaudeRunner{runCLI: fakeCLI(nil, cliErr)}
	_, err := r.Induce(context.Background(), "induce")
	if !errors.Is(err, cliErr) {
		t.Errorf("expected wrapped cliErr; got %v", err)
	}
}

// ─── OpenCodeRunner.Induce ────────────────────────────────────────────────────

// TestOpenCodeRunner_Induce_GoldenNDJSON verifies Induce parses a valid
// induced-procedure JSON program from an NDJSON "text" event.
func TestOpenCodeRunner_Induce_GoldenNDJSON(t *testing.T) {
	innerJSON := `{"trigger":"flaky retry loop","steps":[{"order":1,"template":"add exponential backoff"}],"postcondition_kind":"build_green"}`
	ndjson := "" +
		`{"type":"step_start","timestamp":"2026-01-01T00:00:00Z"}` + "\n" +
		fmt.Sprintf(`{"type":"text","part":{"text":%q}}`, innerJSON) + "\n" +
		`{"type":"step_finish","timestamp":"2026-01-01T00:00:01Z","metadata":{"model":"opencode-model-x"}}` + "\n"

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	got, err := r.Induce(context.Background(), "induce")
	if err != nil {
		t.Fatalf("Induce: unexpected error: %v", err)
	}
	if got.Trigger != "flaky retry loop" {
		t.Errorf("Trigger = %q; unexpected", got.Trigger)
	}
	if len(got.Steps) != 1 || got.Steps[0].Template != "add exponential backoff" {
		t.Errorf("Steps unexpected: %+v", got.Steps)
	}
	if got.Model != "opencode-model-x" {
		t.Errorf("Model = %q; want %q", got.Model, "opencode-model-x")
	}
	if got.DurationMS != 1000 {
		t.Errorf("DurationMS = %d; want 1000", got.DurationMS)
	}
}

// TestOpenCodeRunner_Induce_NoTextEvent triangulates with a stream that never
// emits a "text" event.
func TestOpenCodeRunner_Induce_NoTextEvent(t *testing.T) {
	ndjson := `{"type":"step_start","timestamp":"2026-01-01T00:00:00Z"}` + "\n"

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Induce(context.Background(), "induce")
	if err == nil {
		t.Fatal("Induce: expected error when no text event is present, got nil")
	}
}

// TestOpenCodeRunner_Induce_RejectsModelSetPolarity mirrors the Claude
// equivalent for the OpenCode transport.
func TestOpenCodeRunner_Induce_RejectsModelSetPolarity(t *testing.T) {
	innerJSON := `{"trigger":"some bug","steps":[{"order":1,"template":"do the thing"}],"polarity":"anti_playbook"}`
	ndjson := fmt.Sprintf(`{"type":"text","part":{"text":%q}}`, innerJSON) + "\n"

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Induce(context.Background(), "induce")
	if !errors.Is(err, ErrModelSetPolarity) {
		t.Errorf("expected ErrModelSetPolarity; got %v", err)
	}
}

// ─── Deterministic fallback (task 3.4) ────────────────────────────────────────

// TestDeterministicInduce_NonEmptyTrajectory verifies the degraded fallback
// produces a single verbatim-template step from a non-empty trajectory.
func TestDeterministicInduce_NonEmptyTrajectory(t *testing.T) {
	got := DeterministicInduce("  connection timeout  ", "  retried with backoff and it worked  ")
	if got.Trigger != "connection timeout" {
		t.Errorf("Trigger = %q; want trimmed %q", got.Trigger, "connection timeout")
	}
	if len(got.Steps) != 1 {
		t.Fatalf("Steps: want 1; got %d", len(got.Steps))
	}
	if got.Steps[0].Template != "retried with backoff and it worked" {
		t.Errorf("Steps[0].Template = %q; want trimmed trajectory", got.Steps[0].Template)
	}
	if got.PostconditionKind != "custom" {
		t.Errorf("PostconditionKind = %q; want %q", got.PostconditionKind, "custom")
	}
}

// TestDeterministicInduce_EmptyTrajectory triangulates with a DIFFERENT
// input: an empty trajectory yields zero steps, not a bogus placeholder step.
func TestDeterministicInduce_EmptyTrajectory(t *testing.T) {
	got := DeterministicInduce("some trigger", "   ")
	if len(got.Steps) != 0 {
		t.Errorf("Steps: want 0 for empty trajectory; got %d", len(got.Steps))
	}
}

// ─── SafeInduce (graceful degradation) ───────────────────────────────────────

// TestSafeInduce_NilInducerFallsBack verifies a nil ProcedureInducer never
// errors the caller — it degrades straight to DeterministicInduce.
func TestSafeInduce_NilInducerFallsBack(t *testing.T) {
	got := SafeInduce(context.Background(), nil, "trigger", "trajectory text", "prompt")
	if got.Trigger != "trigger" {
		t.Errorf("Trigger = %q; want %q (deterministic fallback)", got.Trigger, "trigger")
	}
	if len(got.Steps) != 1 || got.Steps[0].Template != "trajectory text" {
		t.Errorf("Steps unexpected: %+v", got.Steps)
	}
}

// TestSafeInduce_ErrorFallsBack triangulates with a DIFFERENT setup: a
// configured inducer that errors must also degrade to the deterministic
// fallback rather than propagating the error.
func TestSafeInduce_ErrorFallsBack(t *testing.T) {
	r := &ClaudeRunner{runCLI: fakeCLI(nil, errors.New("cli unavailable"))}
	got := SafeInduce(context.Background(), r, "trigger", "trajectory text", "prompt")
	if got.Trigger != "trigger" {
		t.Errorf("Trigger = %q; want %q (deterministic fallback on error)", got.Trigger, "trigger")
	}
}

// TestSafeInduce_SuccessReturnsInducedResult triangulates the third branch:
// a working inducer's real result is returned as-is, not the fallback.
func TestSafeInduce_SuccessReturnsInducedResult(t *testing.T) {
	innerJSON := `{"trigger":"real induced trigger","steps":[{"order":1,"template":"real step"}],"postcondition_kind":"lint_clean"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{}},"duration_ms":50}`,
		innerJSON,
	)
	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}

	got := SafeInduce(context.Background(), r, "ignored trigger", "ignored trajectory", "prompt")
	if got.Trigger != "real induced trigger" {
		t.Errorf("Trigger = %q; want the REAL induced trigger, not the fallback", got.Trigger)
	}
}
