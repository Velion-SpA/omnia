package ranker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrainSeparatesLabeledExamplesAndRoundTripsVersionedModel(t *testing.T) {
	examples := []Example{
		{Features: Features{LexicalRRF: .9, SemanticCosine: .9, Recency: .9, Importance: 1, OutcomeHistory: 1}, Label: 1},
		{Features: Features{LexicalRRF: .8, SemanticCosine: .8, Recency: .8, Importance: 1, OutcomeHistory: 1}, Label: 1},
		{Features: Features{LexicalRRF: .1, SemanticCosine: .1, Recency: .1, Importance: .3, OutcomeHistory: 0}, Label: 0},
		{Features: Features{LexicalRRF: .2, SemanticCosine: .2, Recency: .2, Importance: .3, OutcomeHistory: 0}, Label: 0},
	}
	model, err := Train(examples, TrainOptions{Iterations: 1000, LearningRate: .4, L2: .01, TrainedAt: "2026-07-30T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if model.Predict(examples[0].Features) <= model.Predict(examples[2].Features) {
		t.Fatal("trained model did not separate positive from negative examples")
	}
	dir := t.TempDir()
	if err := Promote(dir, model); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCurrent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != model.Version {
		t.Fatalf("version = %q, want %q", loaded.Version, model.Version)
	}
	if _, err := filepath.Abs(CurrentPath(dir)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCurrentRejectsOlderFeatureSchemaAndCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCurrent(dir, "not-json"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCurrent(dir); err == nil {
		t.Fatal("corrupt model loaded")
	}
	if err := WriteCurrent(dir, "old"); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":"old","feature_schema":"old-schema","weights":[0,0,0,0,0,0]}`)
	if err := os.WriteFile(filepath.Join(dir, "model-old.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCurrent(dir); err == nil {
		t.Fatal("stale feature schema loaded")
	}
}
