package cloud

import "testing"

func TestConfigFromEnvCloudHost(t *testing.T) {
	t.Run("default bind host stays loopback", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_HOST", "")
		cfg := ConfigFromEnv()
		if cfg.BindHost != "127.0.0.1" {
			t.Fatalf("expected default bind host 127.0.0.1, got %q", cfg.BindHost)
		}
	})

	t.Run("env overrides bind host", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_HOST", "0.0.0.0")
		cfg := ConfigFromEnv()
		if cfg.BindHost != "0.0.0.0" {
			t.Fatalf("expected bind host override 0.0.0.0, got %q", cfg.BindHost)
		}
	})
}

func TestConfigFromEnvAllowedProjects(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a, proj-b,proj-a")
	cfg := ConfigFromEnv()
	if len(cfg.AllowedProjects) != 2 {
		t.Fatalf("expected deduplicated allowlist, got %v", cfg.AllowedProjects)
	}
	if cfg.AllowedProjects[0] != "proj-a" || cfg.AllowedProjects[1] != "proj-b" {
		t.Fatalf("unexpected allowlist order/values: %v", cfg.AllowedProjects)
	}
}

func TestConfigFromEnvMaxPushBodyBytes(t *testing.T) {
	t.Run("default is 8 MiB", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", "")
		cfg := ConfigFromEnv()
		if cfg.MaxPushBodyBytes != DefaultMaxPushBodyBytes {
			t.Fatalf("expected default max push bytes %d, got %d", DefaultMaxPushBodyBytes, cfg.MaxPushBodyBytes)
		}
	})

	t.Run("env overrides with positive integer", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", "10485760")
		cfg := ConfigFromEnv()
		if cfg.MaxPushBodyBytes != 10485760 {
			t.Fatalf("expected max push bytes override 10485760, got %d", cfg.MaxPushBodyBytes)
		}
	})

	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Run("invalid value keeps default "+value, func(t *testing.T) {
			t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", value)
			cfg := ConfigFromEnv()
			if cfg.MaxPushBodyBytes != DefaultMaxPushBodyBytes {
				t.Fatalf("expected default max push bytes for %q, got %d", value, cfg.MaxPushBodyBytes)
			}
		})
	}
}

func TestIsDefaultJWTSecret(t *testing.T) {
	t.Run("default secret returns true", func(t *testing.T) {
		if !IsDefaultJWTSecret(DefaultJWTSecret) {
			t.Fatal("expected default jwt secret to be recognized")
		}
	})

	t.Run("custom secret returns false", func(t *testing.T) {
		if IsDefaultJWTSecret("custom-super-secret-value-1234567890") {
			t.Fatal("expected custom jwt secret to be treated as non-default")
		}
	})
}

// TestConfigFromEnvCloudSemantic covers the cloud_semantic.enabled feature
// flag (design D5, human-like-memory PR5 slice 3) and its optional query
// embedder settings. Disabled by default: off keeps CloudSemanticEnabled
// false and the cloud dashboard's Semantic() unavailable, exactly as before
// this slice (D7 rollback guarantee, cloud side).
func TestConfigFromEnvCloudSemantic(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv("OMNIA_CLOUD_SEMANTIC_ENABLED", "")
		cfg := ConfigFromEnv()
		if cfg.CloudSemanticEnabled {
			t.Fatal("expected cloud semantic to default to disabled")
		}
	})

	t.Run("env enables cloud semantic", func(t *testing.T) {
		t.Setenv("OMNIA_CLOUD_SEMANTIC_ENABLED", "true")
		cfg := ConfigFromEnv()
		if !cfg.CloudSemanticEnabled {
			t.Fatal("expected OMNIA_CLOUD_SEMANTIC_ENABLED=true to enable cloud semantic")
		}
	})

	t.Run("legacy ENGRAM_ prefix also enables cloud semantic", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_SEMANTIC_ENABLED", "1")
		cfg := ConfigFromEnv()
		if !cfg.CloudSemanticEnabled {
			t.Fatal("expected legacy ENGRAM_CLOUD_SEMANTIC_ENABLED=1 to enable cloud semantic")
		}
	})

	t.Run("default query embedder settings mirror the local jina default", func(t *testing.T) {
		t.Setenv("OMNIA_CLOUD_SEMANTIC_EMBED_MODEL", "")
		t.Setenv("OMNIA_CLOUD_SEMANTIC_EMBED_DIM", "")
		cfg := ConfigFromEnv()
		if cfg.CloudSemanticEmbedModel != "jina/jina-embeddings-v2-base-es" {
			t.Fatalf("expected default cloud semantic embed model, got %q", cfg.CloudSemanticEmbedModel)
		}
		if cfg.CloudSemanticEmbedDim != 768 {
			t.Fatalf("expected default cloud semantic embed dim 768, got %d", cfg.CloudSemanticEmbedDim)
		}
		if cfg.CloudSemanticEmbedBaseURL != "" {
			t.Fatalf("expected no default query embedder base URL (no Ollama in the cloud by default), got %q", cfg.CloudSemanticEmbedBaseURL)
		}
	})

	t.Run("env overrides query embedder settings", func(t *testing.T) {
		t.Setenv("OMNIA_CLOUD_SEMANTIC_EMBED_BASE_URL", "http://homelab-ollama:11434")
		t.Setenv("OMNIA_CLOUD_SEMANTIC_EMBED_MODEL", "bge-m3")
		t.Setenv("OMNIA_CLOUD_SEMANTIC_EMBED_DIM", "1024")
		cfg := ConfigFromEnv()
		if cfg.CloudSemanticEmbedBaseURL != "http://homelab-ollama:11434" {
			t.Fatalf("expected overridden base URL, got %q", cfg.CloudSemanticEmbedBaseURL)
		}
		if cfg.CloudSemanticEmbedModel != "bge-m3" {
			t.Fatalf("expected overridden model, got %q", cfg.CloudSemanticEmbedModel)
		}
		if cfg.CloudSemanticEmbedDim != 1024 {
			t.Fatalf("expected overridden dim 1024, got %d", cfg.CloudSemanticEmbedDim)
		}
	})
}

func TestIsDefaultTokenPepper(t *testing.T) {
	for _, weak := range []string{"", "   ", DefaultTokenPepper} {
		if !IsDefaultTokenPepper(weak) {
			t.Fatalf("expected %q to be treated as a default/weak pepper", weak)
		}
	}
	if IsDefaultTokenPepper("a-strong-unique-pepper-value") {
		t.Fatal("expected a custom pepper to be treated as non-default")
	}
}
