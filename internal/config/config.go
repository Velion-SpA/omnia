package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Engram       EngramConfig      `yaml:"engram"`
	Sources      SourcesConfig     `yaml:"sources"`
	BackfillDays int               `yaml:"backfill_days"`
	Routes       map[string]string `yaml:"routes"`
	// Projects is an explicit list of Engram project names to show in the
	// dashboard. The dashboard merges this list with "omnia" and the projects
	// derived from the routes map — whichever yields more projects wins.
	Projects []string `yaml:"projects"`
	// ProjectHidden lists canonical project names to hide from the dashboard entirely.
	// Values are matched after canonicalization (alias lookup + lowercase+trim).
	ProjectHidden []string `yaml:"project_hidden"`
	// ProjectAliases maps raw project variant names to their canonical name.
	// Used for non-case-fold merges (e.g., "NUDGE Sistema" -> "nudge").
	// Leave empty; add entries only when needed.
	ProjectAliases map[string]string `yaml:"project_aliases"`
	// ProjectGroups defines the two-level parent→children hierarchy.
	// Key = parent canonical; value = []child canonicals.
	// Children are hidden from the top-level overview and aggregated into the parent.
	// A child must not also be a parent; self-referential entries are ignored.
	ProjectGroups map[string][]string `yaml:"project_groups"`
}

type EngramConfig struct {
	BaseURL        string `yaml:"base_url"`
	DefaultProject string `yaml:"default_project"`
}

type SourcesConfig struct {
	Discord DiscordConfig `yaml:"discord"`
	GitHub  GitHubConfig  `yaml:"github"`
}

type DiscordConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Token    string          `yaml:"token"`
	Channels []ChannelConfig `yaml:"channels"`
	Project  string          `yaml:"project"`
}

type ChannelConfig struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Guild string `yaml:"guild"`
}

type GitHubConfig struct {
	Enabled bool     `yaml:"enabled"`
	Token   string   `yaml:"token"`
	Repos   []string `yaml:"repos"`
	Project string   `yaml:"project"`
}

// Router resolves the Engram project for a given ingestion origin.
// Resolution order: explicit routes map → default derivation → fallback.
type Router struct {
	routes         map[string]string // from config.Routes
	defaultProject string            // config.Engram.DefaultProject (last-resort fallback only)
}

// NewRouter creates a Router from the config routes map and fallback default project.
func NewRouter(routes map[string]string, defaultProject string) *Router {
	r := &Router{
		routes:         make(map[string]string),
		defaultProject: normalizeProject(defaultProject),
	}
	for k, v := range routes {
		r.routes[k] = normalizeProject(v)
	}
	return r
}

// ResolveGitHub returns the project for a GitHub repo (format: "owner/repo").
// Key tried: "github/{owner}/{repo}" → falls back to repo name (without owner) → defaultProject.
func (r *Router) ResolveGitHub(ownerRepo string) string {
	key := "github/" + ownerRepo
	if p, ok := r.routes[key]; ok {
		return p
	}
	// Default: repo name without owner.
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) == 2 && parts[1] != "" {
		return normalizeProject(parts[1])
	}
	return r.defaultProject
}

// ResolveDiscord returns the project for a Discord channel (format: channel ID as string).
// Key tried: "discord/{channelID}" → falls back to guild slug → defaultProject.
func (r *Router) ResolveDiscord(channelID string, guildSlug string) string {
	key := "discord/" + channelID
	if p, ok := r.routes[key]; ok {
		return p
	}
	if guildSlug != "" {
		return normalizeProject(guildSlug)
	}
	return r.defaultProject
}

func normalizeProject(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// DefaultPath returns the default config file path.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "omnia", "config.yaml")
}

// Load reads and parses the config file at the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Engram.BaseURL == "" {
		cfg.Engram.BaseURL = "http://127.0.0.1:7437"
	}
	if cfg.Engram.DefaultProject == "" {
		cfg.Engram.DefaultProject = "omnia"
	}
	if cfg.BackfillDays == 0 {
		cfg.BackfillDays = 30
	}
	if cfg.Sources.Discord.Project == "" {
		cfg.Sources.Discord.Project = cfg.Engram.DefaultProject
	}
	if cfg.Sources.GitHub.Project == "" {
		cfg.Sources.GitHub.Project = cfg.Engram.DefaultProject
	}
}
