package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTimeTravelConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		enabled bool
		cap     int
	}{
		{name: "absent defaults off", yaml: "{}\n"},
		{name: "explicit settings", yaml: "time_travel:\n  enabled: true\n  max_revisions_per_memory: 2\n", enabled: true, cap: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.TimeTravel.Enabled != tt.enabled || cfg.TimeTravel.MaxRevisionsPerMemory != tt.cap {
				t.Fatalf("TimeTravel = %+v, want enabled=%v cap=%d", cfg.TimeTravel, tt.enabled, tt.cap)
			}
		})
	}
}
