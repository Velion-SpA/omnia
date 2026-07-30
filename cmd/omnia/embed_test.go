package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// embedCLIDataDir seeds a real (empty) omnia.db under a temp data dir and
// points OMNIA_DATA_DIR at it, so cmdEmbed's engramdb.Open("") call — which
// resolves the data dir via env, not a passed-in store.Config — succeeds.
func embedCLIDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OMNIA_DATA_DIR", dir)

	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = dir
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return dir
}

func writeEmbedCLIConfig(t *testing.T, dataDir string, extraYAML string) string {
	t.Helper()
	path := filepath.Join(dataDir, "config.yaml")
	if err := os.WriteFile(path, []byte(extraYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestCmdEmbed_ReindexRequiresVecIndexEnabled (task 2.19, design capability
// 7): `omnia embed --reindex` with vector_index.enabled absent/false must
// report a clear "capability disabled" message and never invoke the
// maintenance API — matching the anti-pattern-avoidance convention already
// used by `omnia blame`/`omnia consolidate`/`omnia rank-train` (a missing or
// disabled capability degrades gracefully, never a hard failure).
func TestCmdEmbed_ReindexRequiresVecIndexEnabled(t *testing.T) {
	dir := embedCLIDataDir(t)
	cfgPath := writeEmbedCLIConfig(t, dir, ""+
		"embeddings:\n"+
		"  enabled: true\n"+
		"  base_url: http://127.0.0.1:11434\n"+
		"  model: jina/jina-embeddings-v2-base-es\n"+
		"  dim: 768\n")

	stdout, stderr := captureOutput(t, func() {
		cmdEmbed([]string{"--config", cfgPath, "--reindex"})
	})
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected a capability-disabled message for --reindex without vector_index.enabled, got stdout=%q", stdout)
	}
}

// TestCmdEmbed_MissingConfigFileDegradesGracefully proves cmdEmbed's
// config.Load call follows this codebase's established anti-pattern fix
// (config.Load error must never be a bare fatal()): a missing config.yaml
// (the common fresh-install case) must degrade to "capability disabled,"
// never a hard process exit. This was a real pre-existing gap found and
// fixed while wiring `--reindex` — matching the same fix already applied to
// `omnia blame`/`omnia consolidate`/`omnia rank-train`.
func TestCmdEmbed_MissingConfigFileDegradesGracefully(t *testing.T) {
	dir := embedCLIDataDir(t)
	missing := filepath.Join(dir, "does-not-exist.yaml")

	// Guard against a regression back to fatal(): if cmdEmbed ever calls
	// exitFunc again for this path, this stub turns it into a panic that
	// captureOutputAndRecover reports, instead of silently exiting the test
	// binary via the real os.Exit.
	stubExitWithPanic(t)

	stdout, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdEmbed([]string{"--config", missing})
	})
	if recovered != nil {
		t.Fatalf("cmdEmbed with a missing config file must not panic/fatal-exit: recovered=%v stdout=%q stderr=%q", recovered, stdout, stderr)
	}
	// The degrade message goes through the slog logger (stderr), exactly
	// like the pre-existing `!cfg.Embeddings.Enabled` branch just below it —
	// only a hard fatal()/exitFunc call would be the regression this test
	// guards against.
	if !strings.Contains(stderr, "embeddings disabled") {
		t.Fatalf("expected a graceful degrade message on stderr, got stdout=%q stderr=%q", stdout, stderr)
	}
}
