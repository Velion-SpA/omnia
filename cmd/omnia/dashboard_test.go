package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildDashboardConfig_VecIndexEnabledThreadsFromConfigFile (task 2.17,
// design capability 7): cmdDashboard → dashboard.Config forwards
// vector_index.enabled from config.yaml into VecIndexEnabled, so
// newLocalDataSource's embed.OpenStore call downstream receives it.
func TestBuildDashboardConfig_VecIndexEnabledThreadsFromConfigFile(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("vector_index:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := buildDashboardConfig(cfgPath, "", dir, "", 7800, "", logger)
	if !got.VecIndexEnabled {
		t.Error("buildDashboardConfig: VecIndexEnabled must be true when config.yaml sets vector_index.enabled: true")
	}
}

// TestBuildDashboardConfig_VecIndexDisabledByDefault proves the disabled/
// absent path stays exactly as before v0.4 — no config file, no flag, no
// VecIndexEnabled.
func TestBuildDashboardConfig_VecIndexDisabledByDefault(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	got := buildDashboardConfig(filepath.Join(t.TempDir(), "missing-config.yaml"), "", t.TempDir(), "", 7800, "", logger)
	if got.VecIndexEnabled {
		t.Error("buildDashboardConfig: VecIndexEnabled must default to false when no config file is present")
	}
}
