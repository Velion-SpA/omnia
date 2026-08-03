package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
)

// ─── #224 [RED]: `enforce` and `blame` accept --config and then ignore it ───
//
// Both commands resolved their capability block from config.DefaultPath()
// regardless of the flag. The flag parsed without error, so nothing signalled
// that the operator's chosen file was never read — the command simply obeyed a
// config it was not pointed at. Every sibling v0.4 command (consolidate,
// rank-train, cartridge) honors the flag; these two are the outliers.

// writeConfigFile writes a config.yaml carrying one capability toggle and
// returns its path.
func writeConfigFile(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// defaultPathSaysDisabled points config.DefaultPath() at a throwaway HOME whose
// config.yaml disables everything, so a test that observes "enabled" can only
// have read the file it explicitly passed.
func defaultPathSaysDisabled(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfigFile(t, filepath.Join(home, ".config", "omnia"), "code_graph:\n  enabled: false\nenforcement:\n  enabled: false\n")
}

func TestLoadCodeGraphConfig_ReadsTheGivenPathNotDefaultPath(t *testing.T) {
	defaultPathSaysDisabled(t)
	chosen := writeConfigFile(t, t.TempDir(), "code_graph:\n  enabled: true\n")

	if !loadCodeGraphConfig(chosen) {
		t.Fatal("loadCodeGraphConfig must read the path it is given, not config.DefaultPath()")
	}
}

func TestLoadEnforcementConfig_ReadsTheGivenPathNotDefaultPath(t *testing.T) {
	defaultPathSaysDisabled(t)
	chosen := writeConfigFile(t, t.TempDir(), "enforcement:\n  enabled: true\n")

	if _, enabled := loadEnforcementConfig(chosen); !enabled {
		t.Fatal("loadEnforcementConfig must read the path it is given, not config.DefaultPath()")
	}
}

// A missing file at the chosen path still degrades to disabled rather than
// exiting — the established convention for optional, default-OFF capabilities
// (unchanged by this fix, asserted so it cannot regress).
func TestLoadConfig_MissingChosenPathStillDegradesToDisabled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	if loadCodeGraphConfig(missing) {
		t.Fatal("a missing --config path must degrade to disabled, not enabled")
	}
	if _, enabled := loadEnforcementConfig(missing); enabled {
		t.Fatal("a missing --config path must degrade to disabled, not enabled")
	}
}

// The wiring half: the command must resolve --config out of its own args and
// hand it to the loader. Both flag spellings are exercised because
// globalConfigPath accepts `--config PATH` and `--config=PATH` alike.
func TestCmdBlame_PassesConfigFlagToLoader(t *testing.T) {
	cases := map[string][]string{
		"space form":  {"omnia", "blame", "svc.go:1", "--config", "/tmp/chosen-blame.yaml"},
		"equals form": {"omnia", "blame", "svc.go:1", "--config=/tmp/chosen-blame.yaml"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			var got string
			old := loadCodeGraphConfig
			loadCodeGraphConfig = func(path string) bool { got = path; return false }
			t.Cleanup(func() { loadCodeGraphConfig = old })

			withArgs(t, args...)
			captureOutput(t, func() { cmdBlame(cfg) })

			if got != "/tmp/chosen-blame.yaml" {
				t.Fatalf("blame must forward --config to the loader: got %q", got)
			}
		})
	}
}

func TestCmdEnforce_PassesConfigFlagToLoader(t *testing.T) {
	cases := map[string][]string{
		"space form":  {"omnia", "enforce", "--files", "svc.go", "--config", "/tmp/chosen-enforce.yaml"},
		"equals form": {"omnia", "enforce", "--files", "svc.go", "--config=/tmp/chosen-enforce.yaml"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			var got string
			old := loadEnforcementConfig
			loadEnforcementConfig = func(path string) (config.EnforcementConfig, bool) {
				got = path
				return config.EnforcementConfig{}, false
			}
			t.Cleanup(func() { loadEnforcementConfig = old })

			withArgs(t, args...)
			captureOutput(t, func() { cmdEnforce(cfg) })

			if got != "/tmp/chosen-enforce.yaml" {
				t.Fatalf("enforce must forward --config to the loader: got %q", got)
			}
		})
	}
}

// No flag at all keeps the previous behaviour: config.DefaultPath().
func TestCmdBlame_WithoutConfigFlagUsesDefaultPath(t *testing.T) {
	cfg := testConfig(t)
	var got string
	old := loadCodeGraphConfig
	loadCodeGraphConfig = func(path string) bool { got = path; return false }
	t.Cleanup(func() { loadCodeGraphConfig = old })

	withArgs(t, "omnia", "blame", "svc.go:1")
	captureOutput(t, func() { cmdBlame(cfg) })

	if got != config.DefaultPath() {
		t.Fatalf("without --config, blame must fall back to DefaultPath(): got %q", got)
	}
}
