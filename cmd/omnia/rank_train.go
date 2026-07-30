package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

// rankerEval is injectable so the training promotion gate is independently testable.
var rankerEval = func(ctx context.Context) error {
	_, err := runEvalHarness(ctx, evalRunOptions{CorpusPath: defaultEvalCorpusPath, ABPairsPath: defaultEvalABPairsPath, ConfigPath: config.DefaultPath(), Runs: 1})
	return err
}

func cmdRankTrain(cfg store.Config) {
	fs := flag.NewFlagSet("rank-train", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to config file")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return
	}
	appCfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
		return
	}
	if !appCfg.Ranker.Enabled {
		fmt.Fprintln(os.Stderr, "learned ranker is disabled")
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	rows, err := s.ListRankerTrainingRows()
	if err != nil {
		fatal(err)
		return
	}
	examples := make([]ranker.Example, 0, len(rows))
	now := time.Now().UTC()
	for _, row := range rows {
		updated := parseRankerUpdatedAt(row.UpdatedAt)
		candidate := ranker.Candidate{UpdatedAt: updated, Type: row.Type, Outcome: row.Outcome, Judgment: row.Judgment}
		label, ok := ranker.Label(candidate)
		if !ok {
			continue
		}
		examples = append(examples, ranker.Example{Features: ranker.BuildFeatures(candidate, appCfg.Recall.Ranking, now), Label: label})
	}
	if len(examples) < appCfg.Ranker.MinTrainExamples {
		fmt.Fprintf(os.Stderr, "ranker: insufficient training examples: got %d, need %d\n", len(examples), appCfg.Ranker.MinTrainExamples)
		return
	}
	model, err := ranker.Train(examples, ranker.TrainOptions{})
	if err != nil {
		fatal(err)
		return
	}
	dir := appCfg.Ranker.ModelDir
	if dir == "" {
		dir = filepath.Join(cfg.DataDir, "ranker")
	}
	if err := ranker.SaveCandidate(dir, model); err != nil {
		fatal(err)
		return
	}
	// Promotion is deliberately after candidate persistence and the full eval pass.
	if err := rankerEval(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ranker: evaluation regressed or failed; model not promoted: %v\n", err)
		return
	}
	if err := ranker.Promote(dir, model); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("ranker model %s promoted\n", model.Version)
}

func parseRankerUpdatedAt(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
