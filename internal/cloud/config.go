package cloud

import (
	"strconv"
	"strings"

	"github.com/velion/omnia/internal/envx"
)

type Config struct {
	DSN              string
	JWTSecret        string
	CORSOrigins      []string
	MaxPool          int
	Port             int
	BindHost         string
	AdminToken       string
	AllowedProjects  []string
	MaxPushBodyBytes int64

	// CloudSemanticEnabled gates cloud semantic parity (design D5,
	// human-like-memory PR5 slice 3) on the mounted dashboard's
	// clouddash.Source. Disabled by default: off reproduces the cloud
	// dashboard's original substring-only search byte-for-byte (D7 rollback
	// guarantee, cloud side) — zero migration risk, since cloud_embeddings
	// is additive-only.
	CloudSemanticEnabled bool
	// CloudSemanticEmbedBaseURL is the OPTIONAL Ollama endpoint used to embed
	// interactive dashboard search queries server-side. Empty (the default)
	// disables query embedding cleanly — cloud_embeddings rows synced in
	// from devices that DO run Ollama (design D5's compute-locally-and-sync
	// flow) are still searchable, but the dashboard's search box degrades to
	// its substring/lexical leg. Set this only when an Ollama instance is
	// actually reachable from the cloud host (e.g. same homelab LAN).
	CloudSemanticEmbedBaseURL string
	// CloudSemanticEmbedModel/Dim mirror internal/config.EmbeddingsConfig's
	// defaults (duplicated, not imported, so this package stays a plain
	// env-only config leaf — same convention EmbeddingsConfig's own doc
	// comment establishes).
	CloudSemanticEmbedModel string
	CloudSemanticEmbedDim   int
}

const DefaultJWTSecret = "engram-dev-jwt-secret-for-local-smoke-1234"
const DefaultMaxPushBodyBytes int64 = 8 * 1024 * 1024

// DefaultTokenPepper is the development-only sentinel for the managed-token
// pepper (OBL-01). Booting with managed tokens enabled and this value (or an
// empty pepper) is refused, mirroring the JWT-secret gate.
const DefaultTokenPepper = "engram-dev-token-pepper-for-local-smoke"

func DefaultConfig() Config {
	return Config{
		DSN:              "postgres://engram:engram_dev@localhost:5433/engram_cloud?sslmode=disable",
		JWTSecret:        DefaultJWTSecret,
		CORSOrigins:      []string{"*"},
		MaxPool:          10,
		Port:             8080,
		BindHost:         "127.0.0.1",
		MaxPushBodyBytes: DefaultMaxPushBodyBytes,
		// Cloud semantic parity is disabled by default (D7 rollback
		// guarantee); model/dim mirror the local jina default so an operator
		// only needs to set the base URL + enabled flag to light it up.
		CloudSemanticEnabled:    false,
		CloudSemanticEmbedModel: "jina/jina-embeddings-v2-base-es",
		CloudSemanticEmbedDim:   768,
	}
}

func IsDefaultJWTSecret(secret string) bool {
	return strings.TrimSpace(secret) == DefaultJWTSecret
}

// IsDefaultTokenPepper reports whether the managed-token pepper is empty or the
// development sentinel — either of which is refused at boot when managed tokens
// are enabled.
func IsDefaultTokenPepper(pepper string) bool {
	p := strings.TrimSpace(pepper)
	return p == "" || p == DefaultTokenPepper
}

func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	if v := strings.TrimSpace(envx.Get("OMNIA_DATABASE_URL")); v != "" {
		cfg.DSN = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_JWT_SECRET")); v != "" {
		cfg.JWTSecret = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_ADMIN")); v != "" {
		cfg.AdminToken = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Port = n
		}
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_HOST")); v != "" {
		cfg.BindHost = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_MAX_PUSH_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.MaxPushBodyBytes = n
		}
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_ALLOWED_PROJECTS")); v != "" {
		parts := strings.Split(v, ",")
		projects := make([]string, 0, len(parts))
		seen := make(map[string]struct{})
		for _, part := range parts {
			project := strings.TrimSpace(part)
			if project == "" {
				continue
			}
			if _, ok := seen[project]; ok {
				continue
			}
			seen[project] = struct{}{}
			projects = append(projects, project)
		}
		cfg.AllowedProjects = projects
	}
	if v, ok := envx.Lookup("OMNIA_CLOUD_SEMANTIC_ENABLED"); ok {
		cfg.CloudSemanticEnabled = parseEnvBool(v)
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_SEMANTIC_EMBED_BASE_URL")); v != "" {
		cfg.CloudSemanticEmbedBaseURL = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_SEMANTIC_EMBED_MODEL")); v != "" {
		cfg.CloudSemanticEmbedModel = v
	}
	if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_SEMANTIC_EMBED_DIM")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CloudSemanticEmbedDim = n
		}
	}
	return cfg
}

// parseEnvBool mirrors cmd/omnia's envBool exactly (same accepted truthy
// values: 1/true/yes/on), duplicated here rather than imported since
// cmd/omnia is package main and cannot be imported by internal/cloud.
func parseEnvBool(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
