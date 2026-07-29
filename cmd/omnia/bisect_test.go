package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func bisectFixture(t *testing.T, enabled bool, count int) (store.Config, []int64) {
	cfg := testConfig(t)
	cfg.TimeTravelEnabled = enabled
	s, err := store.New(cfg)
	requireBisect(t, err == nil, "create store")
	defer s.Close()
	requireBisect(t, s.CreateSession("bisect", "omnia", t.TempDir()) == nil, "create session")
	if enabled {
		_, _ = s.DB().Exec(`UPDATE time_travel_metadata SET started_at='2026-01-01T00:00:00Z' WHERE id=1`)
	}
	ids := make([]int64, count)
	for i := range count {
		ids[i], err = s.AddObservation(store.AddObservationParams{
			SessionID: "bisect", Type: "decision", Title: "decision " + string(rune('A'+i)),
			Content: "summary line\n" + strings.Repeat("private detail ", 20), Project: "omnia", Scope: "project",
		})
		requireBisect(t, err == nil, "add observation")
		_, _ = s.DB().Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`,
			"2026-01-0"+string(rune('2'+i))+"T00:00:00Z", "2026-01-0"+string(rune('2'+i))+"T00:00:00Z", ids[i])
	}
	return cfg, ids
}
func TestBisectBoundsMidpointMarksResumeAndReset(t *testing.T) {
	cfg, ids := bisectFixture(t, true, 4)
	out := mustBisect(t, cfg, "start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-06T00:00:00Z")
	requireBisect(t, strings.Contains(out, "Bisecting: 4 left to test") && strings.Contains(out, "decision B") &&
		!strings.Contains(out, "private detail"), "unsafe or incorrect midpoint")
	statePath := filepath.Join(cfg.DataDir, "bisect-state.json")
	info, err := os.Stat(statePath)
	requireBisect(t, err == nil && info.Mode().Perm() == 0o600, "state is not private")
	state, err := readBisectState(statePath)
	requireBisect(t, err == nil && state.Generation != "", "state generation is empty")
	requireBisect(t, mustBisect(t, cfg, "status") == out, "session did not resume")
	out = mustBisect(t, cfg, "good")
	requireBisect(t, strings.Contains(out, "decision C"), "good mark chose wrong midpoint")
	out = mustBisect(t, cfg, "bad")
	requireBisect(t, strings.Contains(out, "Implicated revision") && strings.Contains(out, "decision C"), "bad mark did not converge")
	s, _ := store.New(cfg)
	defer s.Close()
	var count, mutated int
	_ = s.DB().QueryRow(`SELECT COUNT(*), COALESCE(SUM(title != 'decision ' || char(64 + id) OR content != ?),0) FROM observations`,
		strings.TrimSpace("summary line\n"+strings.Repeat("private detail ", 20))).Scan(&count, &mutated)
	requireBisect(t, count == len(ids) && mutated == 0, "bisect mutated live data")
	_ = mustBisect(t, cfg, "reset")
	_, err = os.Stat(statePath)
	requireBisect(t, os.IsNotExist(err), "state survives reset")
}
func TestBisectEdgesValidationAndDeterminism(t *testing.T) {
	cfg, _ := bisectFixture(t, true, 1)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing good", []string{"start", "--bad", "2026-01-04T00:00:00Z"}, "good bound is required"},
		{"missing bad", []string{"start", "--good", "2026-01-01T00:00:00Z"}, "bad bound is required"},
		{"reversed", []string{"start", "--good", "2026-01-04T00:00:00Z", "--bad", "2026-01-02T00:00:00Z"}, "good bound must precede bad bound"},
		{"before boundary", []string{"start", "--good", "2025-12-01T00:00:00Z", "--bad", "2026-01-04T00:00:00Z"}, "history unavailable before"},
		{"single", []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-03T00:00:00Z"}, "Implicated revision"},
		{"none", []string{"start", "--good", "2026-01-03T00:00:00Z", "--bad", "2026-01-04T00:00:00Z"}, "no revisions in range"},
		{"extra status arg", []string{"status", "ignored"}, "does not accept arguments"},
		{"help", []string{"--help"}, "start --good"},
	}
	requireBisect(t, !shouldCheckForUpdates([]string{"bisect", "status"}), "bisect attempted update check")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _ = runBisect(cfg, []string{"reset"})
			out, err := runBisect(cfg, tc.args)
			if !strings.Contains(out+fmt.Sprint(err), tc.want) {
				t.Fatalf("output=%q err=%v want %q", out, err, tc.want)
			}
		})
	}
	cfg2, ids := bisectFixture(t, true, 4)
	first, _ := runBisect(cfg2, []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", fmt.Sprint(ids[3])})
	_, _ = runBisect(cfg2, []string{"reset"})
	second, _ := runBisect(cfg2, []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", fmt.Sprint(ids[3])})
	requireBisect(t, first == second, "non-deterministic midpoint")
	for range 3 {
		second, _ = runBisect(cfg2, []string{"good"})
	}
	requireBisect(t, second == "no revisions remain", "all-good did not terminate")
	_ = mustBisect(t, cfg2, "reset")
	_ = mustBisect(t, cfg2, "start", "--good", "2026-01-01T12:00:00Z", "--bad", fmt.Sprint(ids[3]))
	_ = mustBisect(t, cfg2, "bad")
	requireBisect(t, strings.Contains(mustBisect(t, cfg2, "bad"), "decision A"), "all-bad chose wrong event")
}

func TestBisectRejectsUnavailableBadBoundsWithoutLeakingPreEnableContent(t *testing.T) {
	const secret = "PRE-ENABLE-SECRET-MUST-NOT-LEAK"
	tests := []struct {
		name    string
		bad     func([]int64) string
		wantErr bool
	}{
		{"timestamp before nanosecond boundary", func([]int64) string { return "2026-01-02T03:04:05Z" }, true},
		{"pre-enable ID after boundary instant", func(ids []int64) string { return fmt.Sprint(ids[1]) }, true},
		{"post-enable fractional ID before boundary", func(ids []int64) string { return fmt.Sprint(ids[4]) }, true},
		{"valid post-enable bounds", func(ids []int64) string { return fmt.Sprint(ids[3]) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.TimeTravelEnabled = true
			s, err := store.New(cfg)
			requireBisect(t, err == nil, "create store")
			requireBisect(t, s.CreateSession("boundary", "omnia", t.TempDir()) == nil, "create session")
			titles := []string{secret + "-1", secret + "-2", "post-enable good", "post-enable bad", secret + "-BACKDATED"}
			ids := make([]int64, len(titles))
			for i, title := range titles {
				ids[i], err = s.AddObservation(store.AddObservationParams{
					SessionID: "boundary", Type: "decision", Title: title, Content: title,
					Project: "omnia", Scope: "project",
				})
				requireBisect(t, err == nil, "add observation")
			}
			timestamps := []string{
				"2026-01-02T03:04:05.800000000Z",
				"2026-01-02T03:04:05.900000000Z",
				"2026-01-02 03:04:05",
				"2026-01-02T03:04:05.950000000Z",
				"2026-01-02T03:04:05.100000000Z",
			}
			for i, id := range ids {
				_, err = s.DB().Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`, timestamps[i], timestamps[i], id)
				requireBisect(t, err == nil, "set observation timestamp")
			}
			_, err = s.DB().Exec(
				`UPDATE time_travel_metadata SET started_at='2026-01-02T03:04:05.123456789Z', initial_max_observation_id=? WHERE id=1`,
				ids[1],
			)
			requireBisect(t, err == nil, "set recording boundary")
			requireBisect(t, s.Close() == nil, "close store")

			out, err := runBisect(cfg, []string{"start", "--good", fmt.Sprint(ids[2]), "--bad", tt.bad(ids)})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "history unavailable before") {
					t.Fatalf("output=%q err=%v, want unavailable-bound error", out, err)
				}
			} else if err != nil || !strings.Contains(out, "post-enable bad") {
				t.Fatalf("output=%q err=%v, want post-enable candidate", out, err)
			}
			if strings.Contains(out+fmt.Sprint(err), secret) {
				t.Fatalf("pre-enable content leaked: output=%q err=%v", out, err)
			}
		})
	}
}

func TestBisectRejectsDisabledCorruptAndStaleStateAndSkipsTombstone(t *testing.T) {
	disabled, _ := bisectFixture(t, false, 1)
	_, err := runBisect(disabled, []string{"start", "--good", "2026-01-01", "--bad", "now"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "disabled"), "disabled mode accepted bisect")
	cfg, ids := bisectFixture(t, true, 3)
	statePath := filepath.Join(cfg.DataDir, "bisect-state.json")
	requireBisect(t, os.WriteFile(statePath, []byte("{broken"), 0o600) == nil, "write corrupt fixture")
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "corrupt"), "corrupt state accepted")
	_ = mustBisect(t, cfg, "reset")
	_, _ = runBisect(cfg, []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z"})
	s, _ := store.New(cfg)
	_, _ = s.DB().Exec(`DELETE FROM observations WHERE id=?`, ids[1])
	_ = s.Close()
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "stale"), "stale state accepted")
	tombCfg, tombIDs := bisectFixture(t, true, 3)
	_, _ = runBisect(tombCfg, []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z"})
	s, _ = store.New(tombCfg)
	requireBisect(t, s.DeleteObservation(tombIDs[1], true) == nil, "hard delete candidate")
	_ = s.Close()
	out, err := runBisect(tombCfg, []string{"status"})
	requireBisect(t, err == nil && strings.Contains(out, "unavailable (tombstoned)") &&
		!strings.Contains(out, "decision B"), "tombstoned content surfaced")
}

func TestBisectStateSecurityValidationAndCrashSafety(t *testing.T) {
	cfg, _ := bisectFixture(t, true, 3)
	_ = mustBisect(t, cfg, "start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z")
	path := filepath.Join(cfg.DataDir, "bisect-state.json")
	oldModeCheck := bisectPrivateModeRequired
	defer func() { bisectPrivateModeRequired = oldModeCheck }()
	bisectPrivateModeRequired = func() bool { return true }
	requireBisect(t, os.Chmod(path, 0o644) == nil, "chmod fixture")
	_, err := runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "0600"), "permissive state accepted")
	bisectPrivateModeRequired = func() bool { return false }
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err == nil, "Windows-compatible mode rejected")
	bisectPrivateModeRequired = oldModeCheck
	_ = mustBisect(t, cfg, "reset")
	_ = mustBisect(t, cfg, "start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z")
	data, _ := os.ReadFile(path)
	var state bisectState
	_ = json.Unmarshal(data, &state)
	state.Candidates[0], state.Candidates[1] = state.Candidates[1], state.Candidates[0]
	data, _ = json.Marshal(state)
	_ = os.WriteFile(path, data, 0o600)
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "ordered"), "unordered state accepted")
	state.Candidates[0], state.Candidates[1] = state.Candidates[1], state.Candidates[0]
	syncID := state.Candidates[2].SyncID
	state.Candidates[2].SyncID = "stale"
	data, _ = json.Marshal(state)
	_ = os.WriteFile(path, data, 0o600)
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "stale"), "non-current stale candidate accepted")
	state.Candidates[2].SyncID = syncID
	state.Lo = -1
	data, _ = json.Marshal(state)
	_ = os.WriteFile(path, data, 0o600)
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "invariants"), "invalid bounds accepted")
	state.Lo = 0
	_ = os.Remove(path)
	target := path + ".target"
	_ = os.WriteFile(target, []byte("{}"), 0o600)
	if err := os.Symlink(target, path); err != nil && runtime.GOOS == "windows" {
		t.Skip("symlink privilege unavailable")
	}
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "symlink"), "symlink state accepted")
	_ = mustBisect(t, cfg, "reset")
	requireBisect(t, os.Rename(target, path) == nil, "reset followed state symlink")
	original, _ := os.ReadFile(path)
	oldReplace := replaceBisectFile
	replaceBisectFile = func(string, string) error { return errors.New("write failed") }
	defer func() { replaceBisectFile = oldReplace }()
	err = writeBisectState(path, &bisectState{Version: bisectStateVersion, Generation: "generation", Candidates: state.Candidates, Hi: len(state.Candidates) - 1})
	after, _ := os.ReadFile(path)
	requireBisect(t, err != nil && string(after) == string(original), "failed atomic write replaced prior state")
	oldRemove := removeBisectFile
	removeBisectFile = func(string) error { return errors.New("remove failed") }
	defer func() { removeBisectFile = oldRemove }()
	_, err = runBisect(cfg, []string{"reset"})
	_, statErr := os.Stat(path)
	requireBisect(t, err != nil && strings.Contains(err.Error(), "remove failed") && statErr == nil, "reset ignored remove failure")
	removeBisectFile = oldRemove
	_ = os.Remove(path)
	requireBisect(t, os.Mkdir(path, 0o700) == nil, "create nonregular state")
	_ = mustBisect(t, cfg, "reset")
}

func TestBisectProcessLockRejectsConcurrentMarks(t *testing.T) {
	cfg, _ := bisectFixture(t, true, 3)
	_ = mustBisect(t, cfg, "start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z")
	path := filepath.Join(cfg.DataDir, "bisect-state.json")
	before, _ := readBisectState(path)
	oldHook := beforeBisectMutationLock
	defer func() { beforeBisectMutationLock = oldHook }()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	beforeBisectMutationLock = func() { ready <- struct{}{}; <-release }
	results := make(chan error, 2)
	for _, command := range []string{"good", "bad"} {
		command := command
		go func() { _, err := runBisect(cfg, []string{command}); results <- err }()
	}
	<-ready
	<-ready
	close(release)
	first, second := <-results, <-results
	oneStale := first == nil && second != nil && strings.Contains(second.Error(), "stale") ||
		second == nil && first != nil && strings.Contains(first.Error(), "stale")
	requireBisect(t, oneStale, "concurrent marks judged different candidates")
	after, err := readBisectState(path)
	movedOnce := after.Lo == 2 && after.Hi == 2 || after.Lo == 0 && after.Hi == 1
	requireBisect(t, err == nil && before.Generation == after.Generation && movedOnce, "winning mark was advanced twice")
}

func TestBisectMarkRejectsResetStartABA(t *testing.T) {
	cfg, _ := bisectFixture(t, true, 3)
	oldGeneration := newBisectGeneration
	defer func() { newBisectGeneration = oldGeneration }()
	generation := 0
	newBisectGeneration = func() (string, error) { generation++; return fmt.Sprint(generation), nil }
	startArgs := []string{"start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z"}
	_, _ = runBisect(cfg, startArgs)
	path := filepath.Join(cfg.DataDir, "bisect-state.json")
	before, _ := readBisectState(path)
	oldHook := beforeBisectMutationLock
	defer func() { beforeBisectMutationLock = oldHook }()
	ready, release, result := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
	beforeBisectMutationLock = func() { ready <- struct{}{}; <-release }
	go func() { _, err := runBisect(cfg, []string{"good"}); result <- err }()
	<-ready
	_ = mustBisect(t, cfg, "reset")
	_, _ = runBisect(cfg, startArgs)
	restarted, _ := readBisectState(path)
	close(release)
	err := <-result
	after, _ := readBisectState(path)
	requireBisect(t, before.Generation != restarted.Generation && err != nil && strings.Contains(err.Error(), "stale"), "reset/start ABA accepted stale mark")
	requireBisect(t, after.Generation == restarted.Generation && after.Lo == 0 && after.Hi == 2, "stale mark mutated restarted session")
}

func TestBisectAbsentDataDirectory(t *testing.T) {
	cfg := testConfig(t)
	cfg.DataDir = filepath.Join(cfg.DataDir, "missing")
	cfg.TimeTravelEnabled = true
	requireBisect(t, mustBisect(t, cfg, "reset") == "Bisect state reset.", "absent reset failed")
	_, err := runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "data directory"), "absent status error unclear")
	out, err := runBisect(cfg, []string{"start", "--good", "now", "--bad", "2099-01-01T00:00:00Z"})
	info, statErr := os.Stat(cfg.DataDir)
	private := statErr == nil && (!bisectPrivateModeRequired() || info.Mode().Perm() == 0o700)
	requireBisect(t, err == nil && out == "no revisions in range" && statErr == nil && private, "start did not securely create absent data directory")
}
func mustBisect(t *testing.T, cfg store.Config, args ...string) string {
	t.Helper()
	out, err := runBisect(cfg, args)
	requireBisect(t, err == nil, fmt.Sprint(err))
	return out
}
func requireBisect(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Fatal(message)
	}
}
