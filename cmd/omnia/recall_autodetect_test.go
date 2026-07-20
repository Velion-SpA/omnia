package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
)

// TestOllamaReachable_ReachableServerReturnsTrue proves a live server
// answering 200 on /api/tags is reported reachable.
func TestOllamaReachable_ReachableServerReturnsTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !ollamaReachable(srv.URL, 2*time.Second) {
		t.Error("ollamaReachable: got false, want true for a live 200-OK server")
	}
}

// TestOllamaReachable_NonOKStatusReturnsFalse proves a server that answers
// but with a non-2xx status (e.g. Ollama not fully started, or something
// else entirely listening on that port) is treated as unreachable.
func TestOllamaReachable_NonOKStatusReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if ollamaReachable(srv.URL, 2*time.Second) {
		t.Error("ollamaReachable: got true, want false for a 500 response")
	}
}

// TestOllamaReachable_ClosedServerReturnsFalse proves a closed/refused
// connection (the common "Ollama isn't running" case) is treated as
// unreachable rather than panicking or hanging past the timeout.
func TestOllamaReachable_ClosedServerReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // now nothing is listening on this address

	if ollamaReachable(url, 2*time.Second) {
		t.Error("ollamaReachable: got true, want false for a closed server")
	}
}

// TestOllamaReachable_EmptyBaseURLReturnsFalse guards the degenerate input
// (e.g. a zero-value EmbeddingsConfig) without constructing an invalid
// request.
func TestOllamaReachable_EmptyBaseURLReturnsFalse(t *testing.T) {
	if ollamaReachable("", 2*time.Second) {
		t.Error("ollamaReachable: got true, want false for an empty base URL")
	}
}

// TestMaybeAutoDetectRecall_ExplicitChoiceNeverOverridden proves the auto-
// probe never runs (and never mutates Recall.Enabled) when the operator
// already expressed an explicit opinion — including an explicit `false` —
// even if Ollama is actually reachable and embeddings are enabled. This is
// the D7-adjacent rollback guarantee for issue #83: the auto-probe only
// ever decides a DEFAULT, never overrides a choice.
func TestMaybeAutoDetectRecall_ExplicitChoiceNeverOverridden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		RecallEnabledExplicit: true,
		Recall:                config.RecallConfig{Enabled: false},
		Embeddings:            config.EmbeddingsConfig{Enabled: true, BaseURL: srv.URL},
	}
	maybeAutoDetectRecall(cfg)
	if cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got true, want false — an explicit choice must never be overridden by the probe")
	}
}

// TestMaybeAutoDetectRecall_EmbeddingsDisabledSkipsProbe proves the default,
// no-embeddings install never runs a network probe or changes
// Recall.Enabled — byte-for-byte the pre-#83 silent default.
func TestMaybeAutoDetectRecall_EmbeddingsDisabledSkipsProbe(t *testing.T) {
	cfg := &config.Config{
		RecallEnabledExplicit: false,
		Recall:                config.RecallConfig{Enabled: false},
		Embeddings:            config.EmbeddingsConfig{Enabled: false, BaseURL: "http://127.0.0.1:1"},
	}
	maybeAutoDetectRecall(cfg)
	if cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got true, want false — embeddings are disabled, the probe must not run at all")
	}
}

// TestMaybeAutoDetectRecall_UnsetAndReachable_EnablesRecall is the success
// path issue #83 asks for: recall.enabled never mentioned, embeddings
// enabled, Ollama answers -> recall gets auto-enabled.
func TestMaybeAutoDetectRecall_UnsetAndReachable_EnablesRecall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		RecallEnabledExplicit: false,
		Recall:                config.RecallConfig{Enabled: false},
		Embeddings:            config.EmbeddingsConfig{Enabled: true, BaseURL: srv.URL},
	}
	maybeAutoDetectRecall(cfg)
	if !cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got false, want true — recall.enabled was unset, embeddings are on, and Ollama answered")
	}
}

// TestMaybeAutoDetectRecall_UnsetAndUnreachable_LeavesDisabled is the
// documented failure path: recall.enabled never mentioned, embeddings
// enabled, but Ollama is NOT reachable -> stays FTS-only (Recall.Enabled
// stays false), never fatal.
func TestMaybeAutoDetectRecall_UnsetAndUnreachable_LeavesDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()

	cfg := &config.Config{
		RecallEnabledExplicit: false,
		Recall:                config.RecallConfig{Enabled: false},
		Embeddings:            config.EmbeddingsConfig{Enabled: true, BaseURL: url},
	}
	maybeAutoDetectRecall(cfg)
	if cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got true, want false — Ollama is not reachable, recall must stay off")
	}
}
