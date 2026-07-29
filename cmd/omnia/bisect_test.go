package main

import (
	"fmt"
	"os"
	"path/filepath"
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
func TestBisectRejectsDisabledCorruptAndStaleStateAndSkipsTombstone(t *testing.T) {
	disabled, _ := bisectFixture(t, false, 1)
	_, err := runBisect(disabled, []string{"start", "--good", "2026-01-01", "--bad", "now"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "disabled"), "disabled mode accepted bisect")
	cfg, ids := bisectFixture(t, true, 3)
	statePath := filepath.Join(cfg.DataDir, "bisect-state.json")
	requireBisect(t, os.WriteFile(statePath, []byte("{broken"), 0o600) == nil, "write corrupt fixture")
	_, err = runBisect(cfg, []string{"status"})
	requireBisect(t, err != nil && strings.Contains(err.Error(), "corrupt"), "corrupt state accepted")
	_, _ = runBisect(cfg, []string{"reset"})
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

func TestBisectBoundsAndSanitizesOutput(t *testing.T) {
	cfg, ids := bisectFixture(t, true, 3)
	s, _ := store.New(cfg)
	_, _ = s.DB().Exec(`UPDATE observations SET title=?, type=?, content=? WHERE id=?`,
		strings.Repeat("T", 200)+"\x1b[31m\nleak", strings.Repeat("Y", 80), strings.Repeat("secret ", 100), ids[1])
	_ = s.Close()
	out := mustBisect(t, cfg, "start", "--good", "2026-01-01T12:00:00Z", "--bad", "2026-01-05T00:00:00Z")
	requireBisect(t, !strings.ContainsAny(out, "\x1b\r") && len([]rune(out)) < 320 && strings.Count(out, "\n") == 1,
		"bisect output is unbounded or unsafe")
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
