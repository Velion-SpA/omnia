package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
	"gopkg.in/yaml.v3"
)

// v032ConfigSnapshot excludes v0.4 additions from the legacy compatibility contract.
type v032ConfigSnapshot struct {
	Engram               config.EngramConfig               `yaml:"engram"`
	Sources              config.SourcesConfig              `yaml:"sources"`
	BackfillDays         int                               `yaml:"backfill_days"`
	Routes               map[string]string                 `yaml:"routes"`
	Projects             []string                          `yaml:"projects"`
	ProjectHidden        []string                          `yaml:"project_hidden"`
	ProjectAliases       map[string]string                 `yaml:"project_aliases"`
	ProjectGroups        map[string][]string               `yaml:"project_groups"`
	Embeddings           config.EmbeddingsConfig           `yaml:"embeddings"`
	Recall               config.RecallConfig               `yaml:"recall"`
	StructuralForgetting config.StructuralForgettingConfig `yaml:"structural_forgetting"`
	Procedural           config.ProceduralConfig           `yaml:"procedural"`
	Injection            config.InjectionConfig            `yaml:"injection"`
	WriteHygiene         config.WriteHygieneConfig         `yaml:"write_hygiene"`
	TimeTravel           config.TimeTravelConfig           `yaml:"time_travel"`
	Review               config.ReviewConfig               `yaml:"review"`
}

func v032Snapshot(cfg config.Config) v032ConfigSnapshot {
	return v032ConfigSnapshot{
		Engram: cfg.Engram, Sources: cfg.Sources, BackfillDays: cfg.BackfillDays,
		Routes: cfg.Routes, Projects: cfg.Projects, ProjectHidden: cfg.ProjectHidden,
		ProjectAliases: cfg.ProjectAliases, ProjectGroups: cfg.ProjectGroups,
		Embeddings: cfg.Embeddings, Recall: cfg.Recall,
		StructuralForgetting: cfg.StructuralForgetting, Procedural: cfg.Procedural,
		Injection: cfg.Injection, WriteHygiene: cfg.WriteHygiene,
		TimeTravel: cfg.TimeTravel, Review: cfg.Review,
	}
}

func TestV04MemoryFrontier_DefaultsOffAndPreservesV032Fixture(t *testing.T) {
	zero := config.Config{}
	for name, enabled := range map[string]bool{
		"CodeGraph":     zero.CodeGraph.Enabled,
		"Enforcement":   zero.Enforcement.Enabled,
		"Consolidation": zero.Consolidation.Enabled,
		"Encryption":    zero.Encryption.Enabled,
		"Ranker":        zero.Ranker.Enabled,
		"Cartridge":     zero.Cartridge.Enabled,
		"VecIndex":      zero.VecIndex.Enabled,
	} {
		if enabled {
			t.Errorf("Config{}.%s.Enabled = true; want false", name)
		}
	}

	fixturePath := filepath.Join("testdata", "v0.3.2-config.yaml")
	cfg, err := config.Load(fixturePath)
	if err != nil {
		t.Fatalf("Load v0.3.2 fixture: %v", err)
	}
	for name, enabled := range map[string]bool{
		"CodeGraph":     cfg.CodeGraph.Enabled,
		"Enforcement":   cfg.Enforcement.Enabled,
		"Consolidation": cfg.Consolidation.Enabled,
		"Encryption":    cfg.Encryption.Enabled,
		"Ranker":        cfg.Ranker.Enabled,
		"Cartridge":     cfg.Cartridge.Enabled,
		"VecIndex":      cfg.VecIndex.Enabled,
	} {
		if enabled {
			t.Errorf("v0.3.2 fixture loaded with %s.Enabled = true; want false", name)
		}
	}
	got, err := yaml.Marshal(v032Snapshot(*cfg))
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "v0.3.2-config.golden.yaml"))
	if err != nil {
		t.Fatalf("read v0.3.2 golden fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("v0.3.2 compatibility fixture changed: want %q, got %q", want, got)
	}
}

func TestV04MemoryFrontier_OptInUsesParameterDefaults(t *testing.T) {
	path := writeTempConfig(t, ""+
		"code_graph: {enabled: true}\n"+
		"enforcement: {enabled: true}\n"+
		"consolidation: {enabled: true}\n"+
		"encryption: {enabled: true}\n"+
		"learned_ranker: {enabled: true}\n"+
		"cartridge: {enabled: true}\n"+
		"vector_index: {enabled: true}\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Enforcement.Enabled || cfg.Enforcement.Mode != "flag" {
		t.Errorf("Enforcement = %+v; want enabled with flag mode", cfg.Enforcement)
	}
	if !cfg.CodeGraph.Enabled || !cfg.Encryption.Enabled {
		t.Errorf("CodeGraph/Encryption = %+v/%+v; want explicit opt-in", cfg.CodeGraph, cfg.Encryption)
	}
	if !cfg.Consolidation.Enabled || cfg.Consolidation.MinScore != 0.5 || cfg.Consolidation.K != 8 || cfg.Consolidation.MinClusterSize != 3 || cfg.Consolidation.MaxClusterSize != 12 {
		t.Errorf("Consolidation = %+v; want enabled with documented defaults", cfg.Consolidation)
	}
	if !cfg.Ranker.Enabled || cfg.Ranker.MinTrainExamples != 50 {
		t.Errorf("Ranker = %+v; want enabled with min_train_examples=50", cfg.Ranker)
	}
	if !cfg.Cartridge.Enabled || cfg.Cartridge.TopMemories != 50 {
		t.Errorf("Cartridge = %+v; want enabled with top_memories=50", cfg.Cartridge)
	}
	if !cfg.VecIndex.Enabled || cfg.VecIndex.Quantization != "none" {
		t.Errorf("VecIndex = %+v; want enabled with quantization=none", cfg.VecIndex)
	}
}
