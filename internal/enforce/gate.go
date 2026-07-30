package enforce

import (
	"context"
	"strings"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// Verdict values (REQ-413): the gate MUST return exactly one of these per
// invocation. VerdictOverride (REQ-415, PR8) is its own distinct outcome —
// never silently reported as VerdictPass.
const (
	VerdictPass     = "pass"
	VerdictFlag     = "flag"
	VerdictBlock    = "block"
	VerdictOverride = "override"
)

// Outcome values for one evaluated procedure's postcondition — informational
// detail behind a Violation entry, not part of the public verdict contract.
const (
	outcomePassed      = "passed"
	outcomeFailed      = "failed"
	outcomeSkipped     = "skipped"
	outcomeRunnerError = "runner_error"
	outcomeTimeout     = "timeout"
)

// Violation describes one trusted procedure's postcondition evaluation that
// did not cleanly pass: a real failure, a skip (command not configured /
// custom not allowed), a runner error, or a timeout. Surfaced back to the
// caller (mem_enforce/omnia enforce) for diagnosis.
type Violation struct {
	ProcedureSyncID string `json:"procedure_sync_id"`
	Kind            string `json:"kind"`
	Outcome         string `json:"outcome"`
	Command         string `json:"command,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	OutputPreview   string `json:"output_preview,omitempty"`
	Note            string `json:"note,omitempty"`
}

// Result is the gate's pass/flag/block(/override) verdict contract
// (REQ-413), returned by both Decide (PR7, no audit/override) and Evaluate
// (PR8, the full audited + overridable entry point).
type Result struct {
	Verdict     string      `json:"verdict"`
	Violations  []Violation `json:"violations"`
	Overridable bool        `json:"overridable"`
	// Note carries fail-safe diagnostic context for a `pass` verdict that
	// was NOT driven by a normal postcondition evaluation — currently only
	// set by Evaluate (evaluate.go) when a procedure-lookup error forces an
	// unscoped pass (design.md: "cannot scope → pass with note"). Empty for
	// every ordinary pass/flag/block/override outcome derived from Decide.
	Note string `json:"note,omitempty"`
}

// DecideOptions bundles the configuration one Decide call needs.
type DecideOptions struct {
	Config   config.EnforcementConfig
	RepoRoot string
	// Override is the explicit escape hatch (REQ-415): when true and at
	// least one postcondition did not cleanly pass, the verdict is
	// VerdictOverride — its own distinct outcome, never silently VerdictPass.
	// It has no effect when every postcondition already passed (nothing to
	// override).
	Override bool
}

// Decide runs the postcondition for every already-matched trusted procedure
// (see MatchTrustedProcedures) and derives the pass/flag/block/override
// verdict (REQ-412/413/414/415). It is a PURE decision function: no LLM
// call anywhere (REQ-412), and no audit logging — that is layered on top by
// Evaluate (evaluate.go), the single function mem_enforce/omnia enforce
// both call.
func Decide(ctx context.Context, procedures []store.Procedure, opts DecideOptions) Result {
	// Initialized as []Violation{}, not left as a nil slice: evaluate.go's
	// error path already constructs []Violation{} for its "no violations"
	// pass, so both paths in this package serialize the same JSON shape
	// ("violations":[]) instead of one producing null — omnia enforce's
	// JSON output is consumed by hooks/CI (REQ-418) where that
	// null-vs-empty-array difference matters.
	violations := []Violation{}
	for _, p := range procedures {
		v := evaluatePostcondition(ctx, opts.Config, opts.RepoRoot, p)
		if v.Outcome != outcomePassed {
			violations = append(violations, v)
		}
	}
	return decideVerdict(opts.Config, opts.Override, violations)
}

// decideVerdict derives pass/flag/block/override from a set of non-passing
// evaluation outcomes (REQ-413/414/415). A `skipped` outcome (command not
// configured, or custom postconditions not allowed) NEVER escalates the
// verdict — fail-safe by design (design.md: "a wrong block is worse than a
// missed catch"). A `runner_error`/`timeout` outcome forces `flag` even
// under block mode, since a broken/slow verification environment is never
// grounds to hard-block a caller's workflow. An explicit override only ever
// applies when there is something to override (blocking == true); it never
// turns a clean pass into a spurious "override" record.
func decideVerdict(cfg config.EnforcementConfig, override bool, violations []Violation) Result {
	blocking := false
	forceFlag := false
	for _, v := range violations {
		switch v.Outcome {
		case outcomeFailed:
			blocking = true
		case outcomeRunnerError, outcomeTimeout:
			blocking = true
			forceFlag = true
		}
	}
	if !blocking {
		return Result{Verdict: VerdictPass, Violations: violations, Overridable: false}
	}
	if override {
		return Result{Verdict: VerdictOverride, Violations: violations, Overridable: false}
	}
	if cfg.Mode == "block" && !forceFlag {
		return Result{Verdict: VerdictBlock, Violations: violations, Overridable: true}
	}
	return Result{Verdict: VerdictFlag, Violations: violations, Overridable: true}
}

// evaluatePostcondition mechanically evaluates one trusted procedure's
// postcondition (REQ-412: tests_pass/lint_clean/build_green run the
// configured command; custom evaluates postcondition_expr — no LLM call
// anywhere on this path).
func evaluatePostcondition(ctx context.Context, cfg config.EnforcementConfig, repoRoot string, p store.Procedure) Violation {
	command, note, ok := resolveCommand(cfg, p)
	if !ok {
		return Violation{ProcedureSyncID: p.SyncID, Kind: p.PostconditionKind, Outcome: outcomeSkipped, Note: note}
	}

	res := RunCommand(ctx, repoRoot, command, cfg.TimeoutSeconds)
	switch {
	case res.TimedOut:
		return Violation{ProcedureSyncID: p.SyncID, Kind: p.PostconditionKind, Command: command, Outcome: outcomeTimeout, Note: "postcondition command timed out"}
	case res.Err != nil:
		return Violation{ProcedureSyncID: p.SyncID, Kind: p.PostconditionKind, Command: command, Outcome: outcomeRunnerError, Note: res.Err.Error()}
	case res.ExitCode == 0:
		return Violation{ProcedureSyncID: p.SyncID, Kind: p.PostconditionKind, Command: command, Outcome: outcomePassed}
	default:
		return Violation{ProcedureSyncID: p.SyncID, Kind: p.PostconditionKind, Command: command, Outcome: outcomeFailed, ExitCode: res.ExitCode, OutputPreview: res.Output}
	}
}

// resolveCommand resolves the shell command to run for p's
// postcondition_kind, per operator config (ADR-6). ok=false — a SKIP, never
// a block (fail-safe by design) — when: the kind's command is not
// configured (tests_pass/lint_clean/build_green), or the procedure is
// custom and either AllowCustomCommands is false (ADR-6: "procedure-supplied
// strings are less trusted") or it carries no postcondition_expr.
func resolveCommand(cfg config.EnforcementConfig, p store.Procedure) (command, note string, ok bool) {
	switch p.PostconditionKind {
	case store.PostconditionTestsPass:
		if strings.TrimSpace(cfg.Commands.Tests) == "" {
			return "", "no tests command configured for postcondition_kind=tests_pass", false
		}
		return cfg.Commands.Tests, "", true
	case store.PostconditionLintClean:
		if strings.TrimSpace(cfg.Commands.Lint) == "" {
			return "", "no lint command configured for postcondition_kind=lint_clean", false
		}
		return cfg.Commands.Lint, "", true
	case store.PostconditionBuildGreen:
		if strings.TrimSpace(cfg.Commands.Build) == "" {
			return "", "no build command configured for postcondition_kind=build_green", false
		}
		return cfg.Commands.Build, "", true
	case store.PostconditionCustom:
		if !cfg.AllowCustomCommands {
			return "", "custom postconditions disabled (enforcement.allow_custom_commands=false)", false
		}
		if strings.TrimSpace(p.PostconditionExpr) == "" {
			return "", "no postcondition_expr configured for postcondition_kind=custom", false
		}
		return p.PostconditionExpr, "", true
	default:
		return "", "unrecognized postcondition_kind", false
	}
}
