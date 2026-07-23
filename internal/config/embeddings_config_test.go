package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/datadir"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestEmbeddings_DefaultsDisabled(t *testing.T) {
	// A config with no embeddings section must default to disabled, with the
	// standard local Ollama defaults filled in.
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embeddings.Enabled {
		t.Error("Embeddings.Enabled: got true, want false by default")
	}
	if cfg.Embeddings.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL default: got %q", cfg.Embeddings.BaseURL)
	}
	// jina-embeddings-v2-base-es replaces nomic-embed-text as the default: a
	// purpose-built ES<->EN shared-space model with 768-dim (same as nomic, so
	// no vector-store schema change) and real 8192-token context. See engram
	// obs #1401 (supersedes design.md D3's original nomic pick).
	if cfg.Embeddings.Model != "jina/jina-embeddings-v2-base-es" {
		t.Errorf("Model default: got %q, want jina/jina-embeddings-v2-base-es", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.Dim != 768 {
		t.Errorf("Dim default: got %d, want 768", cfg.Embeddings.Dim)
	}
	// DBPath gets NO eager default from Load/applyDefaults (see #82): the
	// right default depends on the active data directory, which Load does
	// not know. It must stay empty here; callers resolve the effective path
	// via config.ResolveEmbeddingsDBPath(cfg.Embeddings.DBPath, activeDataDir).
	if cfg.Embeddings.DBPath != "" {
		t.Errorf("DBPath default: got %q, want empty (resolution is deferred to ResolveEmbeddingsDBPath)", cfg.Embeddings.DBPath)
	}
}

func TestEmbeddings_ParsesEnabled(t *testing.T) {
	path := writeTempConfig(t, "embeddings:\n  enabled: true\n  model: mxbai-embed-large\n  dim: 1024\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Embeddings.Enabled {
		t.Error("Embeddings.Enabled: got false, want true")
	}
	if cfg.Embeddings.Model != "mxbai-embed-large" {
		t.Errorf("Model: got %q", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.Dim != 1024 {
		t.Errorf("Dim: got %d, want 1024", cfg.Embeddings.Dim)
	}
}

// TestResolveEmbeddingsDBPath_ExplicitAlwaysWins locks the escape hatch: an
// operator-set db_path must be returned unchanged regardless of the active
// data directory, exactly as it behaved before per-data-dir scoping existed.
func TestResolveEmbeddingsDBPath_ExplicitAlwaysWins(t *testing.T) {
	got := config.ResolveEmbeddingsDBPath("/explicit/path/embeddings.db", "/some/alt/data/dir")
	if got != "/explicit/path/embeddings.db" {
		t.Errorf("got %q, want the explicit path unchanged", got)
	}
}

// TestResolveEmbeddingsDBPath_CanonicalDataDirUsesLegacyGlobalPath proves
// backward compatibility: the canonical (no-override) data directory must
// still resolve to the historic global default, so upgrading to the #82 fix
// never relocates or invalidates an existing install's embeddings.
func TestResolveEmbeddingsDBPath_CanonicalDataDirUsesLegacyGlobalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OMNIA_DATA_DIR", "")
	t.Setenv("ENGRAM_DATA_DIR", "")

	canonical := datadir.Resolve("")
	got := config.ResolveEmbeddingsDBPath("", canonical)
	want := filepath.Join(home, ".local", "share", "omnia", "embeddings.db")
	if got != want {
		t.Errorf("got %q, want %q (legacy global default, unchanged for existing installs)", got, want)
	}
}

// TestResolveEmbeddingsDBPath_AlternateDataDirGetsScopedStore is the
// regression test for #82: `omnia embed` run with an alternate
// OMNIA_DATA_DIR must resolve to a vector store file SCOPED to that data
// dir, never to the shared legacy global path a real/primary instance
// would be using. That's what makes the reconcile prune step incapable of
// deleting the primary instance's embeddings — the two runs never open the
// same file.
func TestResolveEmbeddingsDBPath_AlternateDataDirGetsScopedStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ENGRAM_DATA_DIR", "")

	altDataDir := filepath.Join(t.TempDir(), "alt-instance")
	// Reproduce the REAL #82 footgun: OMNIA_DATA_DIR is set process-wide to the
	// alternate dir — exactly what an `omnia embed` run against an alt data dir
	// creates. Resolution must NOT be fooled into returning the shared legacy
	// path. (The earlier regression test cleared OMNIA_DATA_DIR, so it never
	// reproduced the bug and passed even against the broken implementation.)
	t.Setenv("OMNIA_DATA_DIR", altDataDir)

	got := config.ResolveEmbeddingsDBPath("", altDataDir)
	want := filepath.Join(altDataDir, "embeddings.db")
	if got != want {
		t.Errorf("got %q, want %q (scoped to the alt data dir)", got, want)
	}

	legacy := filepath.Join(home, ".local", "share", "omnia", "embeddings.db")
	if got == legacy {
		t.Fatalf("alt data dir resolved to the shared legacy embeddings path %q — this is exactly the #82 bug (an alternate-data-dir `omnia embed` run pruning the primary instance's vectors)", legacy)
	}
}

// TestValidateEmbeddings_RejectsTruncationForNonMRLModel locks EMBM-3's core
// guard: jina-embeddings-v2-base-es has no Matryoshka (MRL) training, so a
// configured Dim below its native 768 output must be rejected rather than
// silently truncated.
func TestValidateEmbeddings_RejectsTruncationForNonMRLModel(t *testing.T) {
	err := config.ValidateEmbeddings(config.EmbeddingsConfig{
		Model: "jina/jina-embeddings-v2-base-es",
		Dim:   256,
	})
	if err == nil {
		t.Fatal("ValidateEmbeddings: expected an error truncating a non-MRL model (jina) to 256, got nil")
	}
}

// TestValidateEmbeddings_AcceptsTruncationForMRLModel is the accept-path
// counterpart: embeddinggemma:300m IS Matryoshka-trained, so the identical
// shape (Dim below native output) must be accepted.
func TestValidateEmbeddings_AcceptsTruncationForMRLModel(t *testing.T) {
	err := config.ValidateEmbeddings(config.EmbeddingsConfig{
		Model: "embeddinggemma:300m",
		Dim:   256,
	})
	if err != nil {
		t.Errorf("ValidateEmbeddings: expected no error truncating an MRL-capable model (embeddinggemma:300m) to 256, got %v", err)
	}
}

// TestValidateEmbeddings_RejectsDimExceedingNativeDim is the cheap-fix RED
// test: a configured Dim GREATER than a non-MRL model's native output
// (jina's 768) must be rejected at config-load time, not surface only as a
// generic runtime dim-mismatch error deep inside embed.Client.Embed.
func TestValidateEmbeddings_RejectsDimExceedingNativeDim(t *testing.T) {
	err := config.ValidateEmbeddings(config.EmbeddingsConfig{
		Model: "jina/jina-embeddings-v2-base-es",
		Dim:   1024,
	})
	if err == nil {
		t.Fatal("ValidateEmbeddings: expected an error for dim 1024 exceeding jina's native 768, got nil")
	}
}

// TestValidateEmbeddings_RejectsDimExceedingNativeDim_EvenForMRLModel proves
// the exceeds-native-dim guard applies regardless of MRL capability:
// Matryoshka training only ever supports truncating a model's native
// output, never expanding it, so embeddinggemma:300m (MRL-capable, native
// 768) must still reject a Dim of 1024.
func TestValidateEmbeddings_RejectsDimExceedingNativeDim_EvenForMRLModel(t *testing.T) {
	err := config.ValidateEmbeddings(config.EmbeddingsConfig{
		Model: "embeddinggemma:300m",
		Dim:   1024,
	})
	if err == nil {
		t.Fatal("ValidateEmbeddings: expected an error for dim 1024 exceeding embeddinggemma:300m's native 768 (MRL never expands), got nil")
	}
}

// TestValidateEmbeddings_UnknownModelSkipsGuard proves EMBM-1's selectable-
// without-code-changes guarantee: a model name absent from the capability
// registry must not be blocked by the MRL guard — Client.Embed's own
// dim-mismatch check remains the safety net for that model at embed time.
func TestValidateEmbeddings_UnknownModelSkipsGuard(t *testing.T) {
	err := config.ValidateEmbeddings(config.EmbeddingsConfig{
		Model: "some-unregistered-model",
		Dim:   64,
	})
	if err != nil {
		t.Errorf("ValidateEmbeddings: expected no error for an unregistered model (guard skipped), got %v", err)
	}
}

// TestResolveEmbeddingsDBPath_HomeDefaultViaEnvStaysLegacy proves the fix does
// NOT over-scope: when OMNIA_DATA_DIR is explicitly set to the home-default
// data dir (the same instance a bare run would use), embeddings still resolve
// to the legacy global path — no needless relocation for existing installs.
func TestResolveEmbeddingsDBPath_HomeDefaultViaEnvStaysLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ENGRAM_DATA_DIR", "")

	homeDefault := datadir.HomeDefault()
	t.Setenv("OMNIA_DATA_DIR", homeDefault)

	got := config.ResolveEmbeddingsDBPath("", homeDefault)
	want := filepath.Join(home, ".local", "share", "omnia", "embeddings.db")
	if got != want {
		t.Errorf("got %q, want %q (home-default dir must stay on the legacy global path even when set via OMNIA_DATA_DIR)", got, want)
	}
}
