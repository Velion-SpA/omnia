package enforce

import (
	"context"
	"fmt"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// defaultAuditActor is used when a caller does not identify itself.
const defaultAuditActor = "omnia"

// EvalOptions bundles one full mem_enforce/omnia enforce invocation's input
// (design.md Capability 2 Interfaces: `{repo_root, files_touched[], project,
// override?, override_reason?}`) plus the operator's EnforcementConfig.
type EvalOptions struct {
	Config       config.EnforcementConfig
	Project      string
	RepoRoot     string
	FilesTouched []string

	// Override/OverrideReason are the explicit escape hatch (REQ-415).
	Override       bool
	OverrideReason string

	// Actor identifies the calling surface for audit provenance ("mcp" |
	// "cli"), mirroring audit.Entry.Actor's own "provisional identity" doc.
	Actor string
}

// Evaluate is the SINGLE function mem_enforce (internal/mcp) and `omnia
// enforce` (cmd/omnia) both call (REQ-418: an identical pass/flag/block/
// override contract from both surfaces, task 8.9). It matches trusted
// procedures (MatchTrustedProcedures), runs their postconditions and
// derives a verdict (Decide), and records exactly one audit entry per
// invocation (REQ-416) — covering the "no matching trusted procedures"
// fail-safe path too, so a gate call that legitimately found nothing to
// enforce is still distinguishable in the audit trail from a gate that
// never ran.
func Evaluate(ctx context.Context, src ProcedureSource, opts EvalOptions) Result {
	procedures, err := MatchTrustedProcedures(src, opts.Project, opts.FilesTouched, 0)
	if err != nil {
		// A lookup failure must never escalate to a block (fail-safe by
		// design, design.md: "cannot scope → pass with note") — pass, but
		// surface WHY via Note so this is never indistinguishable from a
		// genuine "nothing matched" pass. appendAuditEntry carries the note
		// into the audit trail too, so a procedure store that starts
		// failing (DB corruption, disk I/O error) doesn't silently look
		// like a healthy, empty gate forever.
		result := Result{
			Verdict:     VerdictPass,
			Violations:  []Violation{},
			Overridable: false,
			Note:        fmt.Sprintf("procedure lookup failed, returning unscoped pass: %v", err),
		}
		appendAuditEntry(opts, result, nil)
		return result
	}

	result := Decide(ctx, procedures, DecideOptions{
		Config:   opts.Config,
		RepoRoot: opts.RepoRoot,
		Override: opts.Override,
	})
	appendAuditEntry(opts, result, procedures)
	return result
}

// appendAuditEntry records exactly one audit.Entry per gate decision
// (REQ-416, ADR-7), including which procedure(s) were evaluated — even on a
// `pass` with zero matched procedures.
func appendAuditEntry(opts EvalOptions, result Result, procedures []store.Procedure) {
	syncIDs := make([]string, 0, len(procedures))
	for _, p := range procedures {
		syncIDs = append(syncIDs, p.SyncID)
	}

	// A representative postcondition kind/exit code from the first
	// violation, when any — the audit entry's ProcedureSyncIDs already
	// carries the full evaluated set for anyone needing every detail.
	var kind string
	var exitCode int
	if len(result.Violations) > 0 {
		kind = result.Violations[0].Kind
		exitCode = result.Violations[0].ExitCode
	}

	actor := opts.Actor
	if actor == "" {
		actor = defaultAuditActor
	}

	audit.Append(audit.Entry{
		Ts:                audit.Now(),
		Actor:             actor,
		Action:            audit.ActionEnforce,
		Project:           opts.Project,
		Summary:           "memory enforcement gate decision",
		Result:            "ok",
		Verdict:           result.Verdict,
		ProcedureSyncIDs:  syncIDs,
		PostconditionKind: kind,
		ExitCode:          exitCode,
		OverrideReason:    opts.OverrideReason,
		Note:              result.Note,
	})
}
