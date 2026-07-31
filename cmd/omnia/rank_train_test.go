package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/eval"
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

// TestRankerEvalDefaultUsesEmbeddedCorpus is finding #1's core regression
// test for rank-train's promotion gate: the real (non-injected) rankerEval
// must resolve its eval corpus via the binary-embedded corpus (empty
// CorpusPath sentinel — see eval.go/internal/eval.EmbeddedCorpus), NOT the
// old cwd-relative defaultEvalCorpusPath constant
// ("internal/eval/testdata/cases.json"), which only ever existed when the
// process's cwd happened to be a repo checkout root. Reproduced directly
// pre-fix via `cd /tmp && omnia rank-train`.
func TestRankerEvalDefaultUsesEmbeddedCorpus(t *testing.T) {
	oldRunEvalHarness := runEvalHarness
	var gotCorpusPath string
	sawCall := false
	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		gotCorpusPath = opts.CorpusPath
		sawCall = true
		return eval.RunSummary{}, nil
	}
	t.Cleanup(func() { runEvalHarness = oldRunEvalHarness })

	if err := rankerEval(context.Background()); err != nil {
		t.Fatalf("rankerEval: %v", err)
	}
	if !sawCall {
		t.Fatal("rankerEval did not call runEvalHarness")
	}
	if gotCorpusPath != "" {
		t.Errorf("rankerEval's default CorpusPath = %q, want \"\" (the embedded-corpus sentinel) — a non-empty repo-relative default breaks for any real installed binary run outside a checkout (finding #1)", gotCorpusPath)
	}
}

// TestCmdRankTrainUndersizedCorpusReportsSizeSpecifically is finding #1's
// clear-messaging requirement: when the promotion-gate eval fails because
// the corpus itself is undersized (spec EVAL-2's 50-case floor), rank-train
// must report that specifically and actionably — not the generic,
// opaque "evaluation regressed or failed" phrasing that conflates "corpus
// too small" with "the model got worse."
func TestCmdRankTrainUndersizedCorpusReportsSizeSpecifically(t *testing.T) {
	cfg := testConfig(t)
	path := writeRankerConfig(t, true, 4)
	seedRankerObservations(t, cfg, 6)
	oldEval := rankerEval
	rankerEval = func(ctx context.Context) error {
		return fmt.Errorf("load corpus: %w", &eval.CorpusSizeError{Source: "<embedded>", Count: 11})
	}
	t.Cleanup(func() { rankerEval = oldEval })
	withArgs(t, "omnia", "rank-train", "--config", path)
	stdout, stderr := captureOutput(t, func() { cmdRankTrain(cfg) })
	if strings.Contains(stdout, "promoted") {
		t.Fatalf("must not promote on an undersized corpus, got stdout=%q", stdout)
	}
	for _, want := range []string{"eval corpus has 11 cases", "need at least 50", "spec EVAL-2", "model trained but not promoted"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q (clear, specific corpus-size message)", stderr, want)
		}
	}
	if strings.Contains(stderr, "regressed or failed") {
		t.Fatalf("undersized-corpus error must use the clear, specific message — not the opaque 'evaluation regressed or failed' phrasing; got stderr=%q", stderr)
	}
}
