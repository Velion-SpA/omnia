package diagnostic

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/store"
)

func newDiagnosticTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	// A NOT-yet-existing subdirectory of t.TempDir() — t.TempDir() itself is
	// 0755 on this toolchain (its group/world-execute bits are not something
	// store.New's MkdirAll can retroactively tighten for an ALREADY-existing
	// path). Using a fresh child dir lets New()'s owner-only MkdirAll
	// (memory-provenance foundation, omnia-provenance-foundation) actually
	// apply, so this shared fixture reflects a real fresh-install store and
	// StoreExposureCheck's permission finding doesn't spuriously fire across
	// every other test that reuses this helper.
	cfg.DataDir = filepath.Join(t.TempDir(), "store")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteLockContentionBranches(t *testing.T) {
	s := newDiagnosticTestStore(t)
	tests := []struct {
		name       string
		snapshot   store.SQLiteLockSnapshot
		probeErr   error
		wantStatus string
		wantReason string
	}{
		{
			name:       "healthy snapshot is ok",
			snapshot:   store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 0, CheckpointLog: 2, CheckpointedFrames: 2},
			wantStatus: StatusOK,
			wantReason: CheckSQLiteLockContention + "_ok",
		},
		{
			name:       "checkpoint busy is warning",
			snapshot:   store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 3, CheckpointLog: 7, CheckpointedFrames: 4},
			wantStatus: StatusWarning,
			wantReason: "sqlite_lock_contention_detected",
		},
		{
			name:       "probe failure is error",
			probeErr:   errors.New("probe unavailable"),
			wantStatus: StatusError,
			wantReason: "sqlite_lock_probe_failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := NewRunner().RunOne(context.Background(), Scope{
				Store:   s,
				Project: "engram",
				ReadSQLiteLockSnapshot: func(context.Context) (store.SQLiteLockSnapshot, error) {
					return tc.snapshot, tc.probeErr
				},
			}, CheckSQLiteLockContention)
			if err != nil {
				t.Fatalf("RunOne: %v", err)
			}
			if report.Status != tc.wantStatus || report.Checks[0].ReasonCode != tc.wantReason {
				t.Fatalf("status=%s reason=%s report=%+v", report.Status, report.Checks[0].ReasonCode, report)
			}
		})
	}
}

func TestRegistryLookupAndOrdering(t *testing.T) {
	codes := RegisteredCodes()
	// Alphabetical: manual_... < session_... < sqlite_... < store_... < sync_...
	want := []string{CheckManualSessionNameProjectMismatch, CheckSessionProjectDirectoryMismatch, CheckSQLiteLockContention, CheckStoreExposure, CheckSyncMutationRequiredFields}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Fatalf("RegisteredCodes = %v, want %v", codes, want)
	}
	if _, err := DefaultRegistry().Lookup("not_real"); err == nil {
		t.Fatal("expected invalid check error")
	}
}

func TestRunnerRollsUpBlockedFindings(t *testing.T) {
	s := newDiagnosticTestStore(t)
	runner := NewRunnerWithRegistry(NewRegistry(fakeBlockedCheck{}))
	report, err := runner.RunOne(context.Background(), Scope{Store: s, Project: "engram", Now: time.Now()}, "fake_blocked")
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusBlocked || report.Summary.Blocked != 1 {
		t.Fatalf("status=%s summary=%+v", report.Status, report.Summary)
	}
	if got := report.Checks[0].Findings[0].ReasonCode; got != "fake_blocked_reason" {
		t.Fatalf("reason_code=%q", got)
	}
}

type fakeBlockedCheck struct{}

func (fakeBlockedCheck) Code() string { return "fake_blocked" }
func (fakeBlockedCheck) Run(context.Context, Scope) (CheckResult, error) {
	return resultFromFindings("fake_blocked", map[string]any{"evaluated": true}, []Finding{{CheckID: "fake_blocked", Severity: SeverityBlocking, ReasonCode: "fake_blocked_reason", Message: "blocked", Why: "test", Evidence: mustJSON(map[string]any{"ok": false}), SafeNextStep: "none"}}), nil
}

func TestSessionProjectDirectoryMismatchFinding(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("s1", "api", "/work/web"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store:   s,
		Project: "api",
		DetectProject: func(dir string) (DetectedProject, bool) {
			if dir == "/work/web" {
				return DetectedProject{Project: "web", Source: "test", Path: dir}, true
			}
			return DetectedProject{}, false
		},
	}, CheckSessionProjectDirectoryMismatch)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusWarning || len(report.Checks[0].Findings) != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestRunnerRunAllHealthyEvaluatesEveryMVPCheck(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	report, err := NewRunner().RunAll(context.Background(), Scope{
		Store:   s,
		Project: "engram",
		ReadSQLiteLockSnapshot: func(context.Context) (store.SQLiteLockSnapshot, error) {
			return store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	// 5 checks since omnia-provenance-foundation added StoreExposureCheck;
	// the test store's fresh, owner-only temp dir (outside any cloud-sync
	// folder) evaluates OK for it, so every check stays OK end-to-end.
	if report.Status != StatusOK || report.Summary.OK != 5 || len(report.Checks) != 5 {
		t.Fatalf("report=%+v", report)
	}
	for _, check := range report.Checks {
		if check.Result != StatusOK || len(check.Evidence) == 0 {
			t.Fatalf("expected ok check with evidence, got %+v", check)
		}
	}
}
