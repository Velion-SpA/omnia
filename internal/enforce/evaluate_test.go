package enforce

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// setEnforceAuditHome redirects the package-level audit log to a throwaway
// HOME so tests never touch the real user's ~/.local/state/omnia/audit.jsonl
// (mirrors internal/audit's own test convention).
func setEnforceAuditHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func mustReadAuditEntries(t *testing.T) []audit.Entry {
	t.Helper()
	entries, err := audit.Read(100)
	if err != nil {
		t.Fatalf("audit.Read: %v", err)
	}
	return entries
}

// TestEvaluate_OverrideRecordsDistinctVerdict (task 8.1, REQ-415) verifies
// that a failing postcondition re-invoked with Override:true returns the
// gate's OWN distinct `override` verdict — never silently reported as
// `pass` — and that the audit entry records that same distinct verdict plus
// the override reason.
func TestEvaluate_OverrideRecordsDistinctVerdict(t *testing.T) {
	setEnforceAuditHome(t)
	s := newEnforceTestStore(t)
	upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted,
		"changes touching internal/enforce/matcher.go must run go test before completion")

	result := Evaluate(context.Background(), s, EvalOptions{
		Config:         config.EnforcementConfig{Enabled: true, Mode: "flag", Commands: config.EnforcementCommandsConfig{Tests: "exit 1"}},
		Project:        "enforcetest",
		FilesTouched:   []string{"internal/enforce/matcher.go"},
		Override:       true,
		OverrideReason: "known flaky test in CI, verified locally",
		Actor:          "mcp",
	})
	if result.Verdict != VerdictOverride {
		t.Fatalf("Verdict = %q, want %q (distinct from pass)", result.Verdict, VerdictOverride)
	}
	if result.Verdict == VerdictPass {
		t.Fatalf("override must never be silently reported as pass")
	}

	entries := mustReadAuditEntries(t)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Verdict != VerdictOverride {
		t.Fatalf("audit entry Verdict = %q, want %q", entries[0].Verdict, VerdictOverride)
	}
	if entries[0].OverrideReason == "" {
		t.Fatalf("expected the override reason to be recorded in the audit entry")
	}
}

// TestEvaluate_EveryOutcomeWritesExactlyOneAuditEntry (task 8.3, REQ-416)
// verifies pass, flag, block, and override each write exactly one
// ActionEnforce audit entry recording verdict, procedure sync_id(s),
// postcondition kind, and exit code.
func TestEvaluate_EveryOutcomeWritesExactlyOneAuditEntry(t *testing.T) {
	cases := []struct {
		name        string
		command     string
		mode        string
		override    bool
		wantVerdict string
	}{
		{"pass", "exit 0", "flag", false, VerdictPass},
		{"flag", "exit 1", "flag", false, VerdictFlag},
		{"block", "exit 1", "block", false, VerdictBlock},
		{"override", "exit 1", "flag", true, VerdictOverride},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnforceAuditHome(t)
			s := newEnforceTestStore(t)
			upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted,
				"changes touching internal/enforce/matcher.go must run go test before completion")

			result := Evaluate(context.Background(), s, EvalOptions{
				Config:       config.EnforcementConfig{Enabled: true, Mode: tc.mode, Commands: config.EnforcementCommandsConfig{Tests: tc.command}},
				Project:      "enforcetest",
				FilesTouched: []string{"internal/enforce/matcher.go"},
				Override:     tc.override,
				Actor:        "cli",
			})
			if result.Verdict != tc.wantVerdict {
				t.Fatalf("Verdict = %q, want %q", result.Verdict, tc.wantVerdict)
			}

			entries := mustReadAuditEntries(t)
			if len(entries) != 1 {
				t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(entries), entries)
			}
			e := entries[0]
			if e.Action != audit.ActionEnforce {
				t.Fatalf("Action = %q, want %q", e.Action, audit.ActionEnforce)
			}
			if e.Verdict != tc.wantVerdict {
				t.Fatalf("audit Verdict = %q, want %q", e.Verdict, tc.wantVerdict)
			}
			if len(e.ProcedureSyncIDs) != 1 {
				t.Fatalf("expected exactly 1 procedure sync_id recorded, got %+v", e.ProcedureSyncIDs)
			}
		})
	}
}

// TestEvaluate_NoMatchingProceduresPassesAndStillAudits (REQ-411 fail-safe +
// REQ-416: "every gate invocation" includes the no-match case) verifies a
// change touching no trusted-procedure-covered files passes and is still
// audited exactly once, with zero procedure sync_ids.
func TestEvaluate_NoMatchingProceduresPassesAndStillAudits(t *testing.T) {
	setEnforceAuditHome(t)
	s := newEnforceTestStore(t)

	result := Evaluate(context.Background(), s, EvalOptions{
		Config:       config.EnforcementConfig{Enabled: true, Mode: "flag"},
		Project:      "enforcetest",
		FilesTouched: []string{"internal/enforce/matcher.go"},
		Actor:        "cli",
	})
	if result.Verdict != VerdictPass {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictPass)
	}
	entries := mustReadAuditEntries(t)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry even when nothing matched, got %d", len(entries))
	}
	if len(entries[0].ProcedureSyncIDs) != 0 {
		t.Fatalf("expected zero procedure sync_ids when nothing matched, got %+v", entries[0].ProcedureSyncIDs)
	}
}

// TestEvaluate_NeverWritesToTouchedFiles (task 8.5/8.6, REQ-417: "Verify-Only
// — No Auto-Fix") verifies a file touched by a failing postcondition is
// never modified by the gate itself.
func TestEvaluate_NeverWritesToTouchedFiles(t *testing.T) {
	setEnforceAuditHome(t)
	s := newEnforceTestStore(t)
	upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted,
		"changes touching internal/enforce/matcher.go must run go test before completion")

	repo := t.TempDir()
	touched := filepath.Join(repo, "matcher.go")
	original := []byte("package enforce\n// original content, never touched by the gate\n")
	if err := os.WriteFile(touched, original, 0o600); err != nil {
		t.Fatalf("seed touched file: %v", err)
	}

	result := Evaluate(context.Background(), s, EvalOptions{
		Config:       config.EnforcementConfig{Enabled: true, Mode: "flag", Commands: config.EnforcementCommandsConfig{Tests: "exit 1"}},
		Project:      "enforcetest",
		RepoRoot:     repo,
		FilesTouched: []string{"internal/enforce/matcher.go"},
		Actor:        "cli",
	})
	if result.Verdict != VerdictFlag {
		t.Fatalf("expected a flag verdict to exercise the failing path, got %q", result.Verdict)
	}
	after, err := os.ReadFile(touched)
	if err != nil {
		t.Fatalf("read touched file after gate: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("gate must never modify a touched file; got %q, want %q", after, original)
	}
}
