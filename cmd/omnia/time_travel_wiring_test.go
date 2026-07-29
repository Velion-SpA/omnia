package main

import (
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func TestApplyTimeTravelConfig(t *testing.T) {
	cfg := store.Config{}
	app := config.Config{TimeTravel: config.TimeTravelConfig{Enabled: true, MaxRevisionsPerMemory: 7}}
	applyTimeTravelConfig(&cfg, &app)
	if !cfg.TimeTravelEnabled || cfg.HistoryRevisionCap != 7 {
		t.Fatalf("store config = enabled:%v cap:%d, want true/7", cfg.TimeTravelEnabled, cfg.HistoryRevisionCap)
	}
}
