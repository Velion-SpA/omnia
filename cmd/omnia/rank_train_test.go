package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func writeRankerConfig(t *testing.T, enabled bool, minExamples int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "learned_ranker:\n  enabled: " + boolYAML(enabled) + "\n"
	if minExamples > 0 {
		body += "  min_train_examples: " + itoa(minExamples) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolYAML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestCmdRankTrainDisabledIsNoop(t *testing.T) {
	cfg := testConfig(t)
	path := writeRankerConfig(t, false, 0)
	withArgs(t, "omnia", "rank-train", "--config", path)
	_, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if !strings.Contains(stderr, "disabled") {
		t.Fatalf("expected disabled message, got stderr=%q", stderr)
	}
}

func TestCmdRankTrainMissingConfigFileDegradesToDisabled(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	withArgs(t, "omnia", "rank-train", "--config", path)
	_, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if !strings.Contains(stderr, "disabled") {
		t.Fatalf("expected disabled message (missing config file must degrade, not fatal), got stderr=%q", stderr)
	}
}

func seedRankerObservations(t *testing.T, cfg store.Config, n int) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession("rank-train-session", "rank-train-project", "/tmp/rank-train"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < n; i++ {
		outcome := "worked"
		if i%2 == 1 {
			outcome = "did_not_work"
		}
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "rank-train-session", Type: "bugfix", Title: fmt.Sprintf("fix %d", i),
			Content: fmt.Sprintf("distinct content body for training row %d", i),
			Project: "rank-train-project", Scope: "project", Outcome: outcome,
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}
}

func TestCmdRankTrainInsufficientExamplesReportsCount(t *testing.T) {
	cfg := testConfig(t)
	path := writeRankerConfig(t, true, 10)
	seedRankerObservations(t, cfg, 2)
	withArgs(t, "omnia", "rank-train", "--config", path)
	_, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if !strings.Contains(stderr, "insufficient training examples") {
		t.Fatalf("expected insufficient-examples message, got stderr=%q", stderr)
	}
}

func TestCmdRankTrainPromotesOnSuccessfulEval(t *testing.T) {
	cfg := testConfig(t)
	path := writeRankerConfig(t, true, 4)
	seedRankerObservations(t, cfg, 6)
	oldEval := rankerEval
	rankerEval = func(ctx context.Context) error { return nil }
	t.Cleanup(func() { rankerEval = oldEval })
	withArgs(t, "omnia", "rank-train", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if !strings.Contains(stdout, "promoted") || stderr != "" {
		t.Fatalf("expected promotion, got stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdRankTrainDoesNotPromoteOnEvalRegression(t *testing.T) {
	cfg := testConfig(t)
	path := writeRankerConfig(t, true, 4)
	seedRankerObservations(t, cfg, 6)
	oldEval := rankerEval
	rankerEval = func(ctx context.Context) error { return context.DeadlineExceeded }
	t.Cleanup(func() { rankerEval = oldEval })
	withArgs(t, "omnia", "rank-train", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if strings.Contains(stdout, "promoted") || !strings.Contains(stderr, "not promoted") {
		t.Fatalf("expected refused promotion, got stdout=%q stderr=%q", stdout, stderr)
	}
}
