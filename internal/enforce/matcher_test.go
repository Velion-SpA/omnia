package enforce

import (
	"testing"

	"github.com/velion/omnia/internal/store"
)

func newEnforceTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func upsertEnforceTestProcedure(t *testing.T, s *store.Store, state, trigger string) store.Procedure {
	t.Helper()
	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:  "enforcetest",
		Polarity: store.ProcedurePolarityAntiPlaybook,
		Trigger:  trigger,
		Steps: []store.ProcedureStep{
			{Order: 1, Template: "run tests before declaring the change done"},
		},
		ExpectedOutcome:   "tests pass",
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.8,
		State:             state,
		SourceObsSyncIDs:  []string{"obs-fixture"},
	})
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
	p, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	return *p
}

// TestMatchTrustedProcedures_OnlyTrustedSelected (task 7.1, REQ-411) verifies
// that when a trusted procedure and matching candidate/retired procedures all
// share a trigger overlapping the touched files, only the trusted one is
// selected — candidate/retired procedures must never gate a completion.
func TestMatchTrustedProcedures_OnlyTrustedSelected(t *testing.T) {
	s := newEnforceTestStore(t)

	trusted := upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted,
		"changes touching internal/enforce/matcher.go must run go test before completion")
	upsertEnforceTestProcedure(t, s, store.ProcedureStateCandidate,
		"changes touching internal/enforce/matcher.go must run go test before completion")
	upsertEnforceTestProcedure(t, s, store.ProcedureStateRetired,
		"changes touching internal/enforce/matcher.go must run go test before completion")

	matched, err := MatchTrustedProcedures(s, "enforcetest", []string{"internal/enforce/matcher.go"}, 0)
	if err != nil {
		t.Fatalf("MatchTrustedProcedures: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected exactly 1 matched trusted procedure, got %d: %+v", len(matched), matched)
	}
	if matched[0].SyncID != trusted.SyncID {
		t.Fatalf("expected trusted procedure %q to match, got %q", trusted.SyncID, matched[0].SyncID)
	}
	if matched[0].State != store.ProcedureStateTrusted {
		t.Fatalf("matched procedure state = %q, want trusted", matched[0].State)
	}
}

// TestMatchTrustedProcedures_NoTouchedFilesNeverMatches (fail-safe: an empty
// change description has nothing to scope a trigger match against) verifies
// no procedures are ever matched when no files are supplied.
func TestMatchTrustedProcedures_NoTouchedFilesNeverMatches(t *testing.T) {
	s := newEnforceTestStore(t)
	upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted, "any trigger at all")

	matched, err := MatchTrustedProcedures(s, "enforcetest", nil, 0)
	if err != nil {
		t.Fatalf("MatchTrustedProcedures: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected no matches with no touched files, got %d", len(matched))
	}
}

// TestMatchTrustedProcedures_DifferentProjectNeverMatches verifies project
// scoping: a trusted procedure belonging to another project must never leak
// into this project's gate decision.
func TestMatchTrustedProcedures_DifferentProjectNeverMatches(t *testing.T) {
	s := newEnforceTestStore(t)
	upsertEnforceTestProcedure(t, s, store.ProcedureStateTrusted,
		"changes touching internal/enforce/matcher.go must run go test before completion")

	matched, err := MatchTrustedProcedures(s, "some-other-project", []string{"internal/enforce/matcher.go"}, 0)
	if err != nil {
		t.Fatalf("MatchTrustedProcedures: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected no matches for a different project, got %d", len(matched))
	}
}
