package cloud

import (
	"strconv"
	"strings"

	"github.com/Velion-SpA/omnia/internal/envx"
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
	return cfg
}
