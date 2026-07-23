package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606) ────────────
//
// ProcedureInducer turns a trajectory (a bugfix's content + its recorded
// outcome, formatted by the caller into prompt) into a verifiable,
// parameterized InducedProcedure — via the SAME internal/llm shell-out
// boundary AgentRunner.Compare already uses (design decision: "do NOT touch
// the locked AgentRunner.Compare"; this is a NEW, separate interface).
//
// worked→playbook / did_not_work→anti_playbook is decided by the CALLER from
// the source outcome, NEVER by the model (design decision #2). If the
// model's raw JSON output includes a non-empty "polarity" field anyway,
// Induce rejects it via ErrModelSetPolarity — this file only extracts a
// program from a trajectory, it never decides polarity itself.

// ─── InducedProcedure ─────────────────────────────────────────────────────────

// InducedStep is one ordered, slot-templated step of an induced procedure's
// program (ASI: programmatic > prose, +11.3%) — mirrors store.ProcedureStep's
// shape so callers can pass this straight into store.UpsertProcedure without
// re-mapping fields.
type InducedStep struct {
	Order    int      `json:"order"`
	Template string   `json:"template"`
	Slots    []string `json:"slots,omitempty"`
}

// InducedProcedure is the parsed output of a single ProcedureInducer.Induce
// call. It deliberately has NO Polarity field — see this file's package doc
// comment.
type InducedProcedure struct {
	Trigger           string
	Steps             []InducedStep
	ExpectedOutcome   string
	PostconditionKind string
	PostconditionExpr string
	Model             string
	DurationMS        int64
}

// ProcedureInducer is the abstraction over an external LLM CLI that extracts
// a verifiable, parameterized procedure from a trajectory. Concrete runner
// implementations (ClaudeRunner, OpenCodeRunner) implement Induce via the
// same shell-out pattern as their Compare method.
type ProcedureInducer interface {
	// Induce sends prompt to the underlying LLM CLI and returns a structured
	// InducedProcedure. On error the returned InducedProcedure is zero-value.
	Induce(ctx context.Context, prompt string) (InducedProcedure, error)
}

// ErrModelSetPolarity is returned when the agent's raw JSON output sets a
// non-empty "polarity" field — polarity is derived by the caller from the
// source outcome (worked→playbook, did_not_work→anti_playbook), never by the
// model (design decision #2).
var ErrModelSetPolarity = errors.New("agent output must not set polarity — polarity is derived by the caller from the source outcome, never the model")

// inducedProcedureJSON is the JSON shape the LLM is prompted to return.
// Polarity is accepted here ONLY so a model that (incorrectly) sets it can be
// detected and rejected — see ErrModelSetPolarity.
type inducedProcedureJSON struct {
	Trigger           string           `json:"trigger"`
	Steps             []inducedStepRaw `json:"steps"`
	ExpectedOutcome   string           `json:"expected_outcome"`
	PostconditionKind string           `json:"postcondition_kind"`
	PostconditionExpr string           `json:"postcondition_expr"`
	Polarity          string           `json:"polarity,omitempty"`
}

type inducedStepRaw struct {
	Order    int      `json:"order"`
	Template string   `json:"template"`
	Slots    []string `json:"slots,omitempty"`
}

// parseInducedProcedureJSON decodes and validates the inner JSON payload
// shared by ClaudeRunner.Induce / OpenCodeRunner.Induce.
func parseInducedProcedureJSON(inner string) (InducedProcedure, error) {
	var ij inducedProcedureJSON
	if err := json.Unmarshal([]byte(inner), &ij); err != nil {
		return InducedProcedure{}, fmt.Errorf("%w: induced procedure: %v", ErrInvalidJSON, err)
	}
	if strings.TrimSpace(ij.Polarity) != "" {
		return InducedProcedure{}, ErrModelSetPolarity
	}
	if strings.TrimSpace(ij.Trigger) == "" {
		return InducedProcedure{}, fmt.Errorf("%w: induced procedure: trigger is required", ErrInvalidJSON)
	}

	steps := make([]InducedStep, 0, len(ij.Steps))
	for _, st := range ij.Steps {
		steps = append(steps, InducedStep{Order: st.Order, Template: st.Template, Slots: st.Slots})
	}

	return InducedProcedure{
		Trigger:           ij.Trigger,
		Steps:             steps,
		ExpectedOutcome:   ij.ExpectedOutcome,
		PostconditionKind: ij.PostconditionKind,
		PostconditionExpr: ij.PostconditionExpr,
	}, nil
}

// ─── ClaudeRunner.Induce ──────────────────────────────────────────────────────

// Induce sends prompt to the Claude CLI and returns a structured
// InducedProcedure. Reuses the exact same envelope shell-out
// (`claude -p --output-format json --model haiku --max-turns 1`) and fence
// stripping ClaudeRunner.Compare uses — only the inner JSON schema differs
// (an induced procedure program, not a Verdict).
func (r *ClaudeRunner) Induce(ctx context.Context, prompt string) (InducedProcedure, error) {
	args := []string{"-p", "--output-format", "json", "--model", "haiku", "--max-turns", "1"}
	raw, err := r.runCLI(ctx, "claude", args, prompt)
	if err != nil {
		return InducedProcedure{}, err
	}

	var env claudeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return InducedProcedure{}, fmt.Errorf("%w: outer envelope: %v", ErrInvalidJSON, err)
	}

	inner := strings.TrimSpace(env.Result)
	if m := fenceRE.FindStringSubmatch(inner); len(m) == 2 {
		inner = strings.TrimSpace(m[1])
	}

	induced, err := parseInducedProcedureJSON(inner)
	if err != nil {
		return InducedProcedure{}, err
	}
	induced.DurationMS = env.DurationMS
	if induced.Model == "" {
		for k := range env.ModelUsage {
			induced.Model = k
			break
		}
	}
	return induced, nil
}

var _ ProcedureInducer = (*ClaudeRunner)(nil)

// ─── OpenCodeRunner.Induce ────────────────────────────────────────────────────

// Induce sends prompt to the OpenCode CLI and returns a structured
// InducedProcedure. Reuses the exact same NDJSON shell-out
// (`opencode run --format json --pure`) and last-text-event-wins scan
// OpenCodeRunner.Compare uses — only the inner JSON schema differs.
func (r *OpenCodeRunner) Induce(ctx context.Context, prompt string) (InducedProcedure, error) {
	args := []string{"run", "--format", "json", "--pure"}
	raw, err := r.runCLI(ctx, "opencode", args, prompt)
	if err != nil {
		return InducedProcedure{}, err
	}

	inner, model, durationMS, err := scanOpenCodeLastText(raw)
	if err != nil {
		return InducedProcedure{}, err
	}

	induced, err := parseInducedProcedureJSON(inner)
	if err != nil {
		return InducedProcedure{}, err
	}
	if induced.Model == "" {
		induced.Model = model
	}
	induced.DurationMS = durationMS
	return induced, nil
}

var _ ProcedureInducer = (*OpenCodeRunner)(nil)

// scanOpenCodeLastText scans OpenCode's NDJSON event stream and returns the
// last "text" event's payload, the model reported by "step_finish" metadata
// (if any), and the step_start→step_finish duration in milliseconds.
//
// This duplicates parseOpenCodeNDJSON's scan loop (opencode.go) rather than
// calling it directly: that function additionally unmarshals-and-validates
// the text payload against Verdict's locked relation vocabulary, which an
// induced-procedure payload (a different JSON schema) would never satisfy.
// Keeping the scan loop itself as the only shared shape (and duplicating the
// small NDJSON walk, not the shell-out or the process boundary) is what lets
// OpenCodeRunner.Compare's Verdict contract stay completely untouched, per
// design decision "do NOT touch the locked AgentRunner.Compare".
func scanOpenCodeLastText(raw []byte) (text string, model string, durationMS int64, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))

	var (
		lastTextContent string
		foundText       bool
		stepStartTime   time.Time
		stepFinishTime  time.Time
	)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev openCodeEvent
		if jsonErr := json.Unmarshal(line, &ev); jsonErr != nil {
			continue
		}

		switch ev.Type {
		case "step_start":
			if ev.Timestamp != "" {
				if t, parseErr := time.Parse(time.RFC3339, ev.Timestamp); parseErr == nil {
					stepStartTime = t
				}
			}
		case "text":
			if ev.Part != nil && ev.Part.Text != "" {
				lastTextContent = ev.Part.Text
				foundText = true
			}
		case "step_finish":
			if ev.Timestamp != "" {
				if t, parseErr := time.Parse(time.RFC3339, ev.Timestamp); parseErr == nil {
					stepFinishTime = t
				}
			}
			if len(ev.Metadata) > 0 {
				var meta openCodeStepFinishMeta
				if metaErr := json.Unmarshal(ev.Metadata, &meta); metaErr == nil && meta.Model != "" {
					model = meta.Model
				}
			}
		}
	}

	if !foundText {
		return "", "", 0, fmt.Errorf("opencode: no text event found in NDJSON stream")
	}
	if !stepStartTime.IsZero() && !stepFinishTime.IsZero() && stepFinishTime.After(stepStartTime) {
		durationMS = stepFinishTime.Sub(stepStartTime).Milliseconds()
	}
	return lastTextContent, model, durationMS, nil
}

// ─── Deterministic fallback (degraded, no LLM) ───────────────────────────────

// DeterministicInduce builds a minimal, degraded InducedProcedure directly
// from the source trigger + trajectory text, with NO LLM call: a single
// step whose template is the trimmed trajectory verbatim. It cannot
// generalize a trajectory into real parameterized slots (design decision:
// "free but cannot generalize... kept as degraded fallback") — it exists so
// induction never has to be all-or-nothing when no CLI runner is configured
// or Induce errors.
//
// trigger and trajectory are both trimmed; an empty trajectory yields a
// procedure with zero steps (still a valid, if useless, InducedProcedure —
// callers decide whether an empty-steps procedure is worth persisting).
func DeterministicInduce(trigger, trajectory string) InducedProcedure {
	trimmedTrajectory := strings.TrimSpace(trajectory)
	var steps []InducedStep
	if trimmedTrajectory != "" {
		steps = []InducedStep{{Order: 1, Template: trimmedTrajectory}}
	}
	return InducedProcedure{
		Trigger:           strings.TrimSpace(trigger),
		Steps:             steps,
		PostconditionKind: "custom",
	}
}

// ─── Prompt construction (PR2: online + offline induction callers) ──────────

// BuildInducePrompt renders the canonical prompt sent to
// ProcedureInducer.Induce for a given trigger + trajectory pair. Shared by
// BOTH the online caller (internal/mcp's mem_save wiring, PR2 Phase 4) and
// the offline caller (`omnia procedure-induct`, PR2 Phase 6) so the two
// induction paths never drift out of sync on prompt wording — mirrors
// BuildPrompt's shared role for AgentRunner.Compare. Deliberately instructs
// the model to omit "polarity" (see ErrModelSetPolarity's doc: polarity is
// assigned by the caller, never the model).
func BuildInducePrompt(trigger, trajectory string) string {
	return fmt.Sprintf(`You are extracting a reusable, verifiable procedure from a single past trajectory.

Trigger (the situation this procedure applies to):
%s

Trajectory (what was actually done, and its result):
%s

Respond with a single JSON object only (no prose, no markdown fences), with this exact shape:
{
  "trigger": "short description of when this procedure applies",
  "steps": [{"order": 1, "template": "step description", "slots": ["param1"]}],
  "expected_outcome": "what should happen if the steps are followed",
  "postcondition_kind": "tests_pass" | "lint_clean" | "build_green" | "custom",
  "postcondition_expr": "optional machine-checkable expression when postcondition_kind is custom"
}

Do NOT include a "polarity" field in your response — polarity is assigned by the caller, never by you.`, trigger, trajectory)
}

// SafeInduce wraps a ProcedureInducer call so a missing runner or an Induce
// error never propagates to the caller: it falls back to
// DeterministicInduce(trigger, trajectory) instead of returning an error.
// This is the graceful-degradation seam later online/offline induction
// wiring (mem_save's async induction, `omnia procedure-induct`) calls so
// "LLM unavailable" never fails the caller's own operation — callers still
// decide, from the returned InducedProcedure's Steps, whether the result is
// substantive enough to persist via store.UpsertProcedure.
func SafeInduce(ctx context.Context, inducer ProcedureInducer, trigger, trajectory, prompt string) InducedProcedure {
	if inducer == nil {
		return DeterministicInduce(trigger, trajectory)
	}
	induced, err := inducer.Induce(ctx, prompt)
	if err != nil {
		return DeterministicInduce(trigger, trajectory)
	}
	return induced
}
