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
	// Embeddings configures Omnia's own local semantic-search index ("capa propia").
	// Disabled by default so the production sync job is unaffected until opted in.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
}

type EngramConfig struct {
	BaseURL        string `yaml:"base_url"`
	DefaultProject string `yaml:"default_project"`
}

// EmbeddingsConfig configures the local embeddings layer. When Enabled is false,
// `omnia embed` is a no-op and the dashboard serves keyword (FTS) search only.
// Omnia keeps its OWN writable vector store at DBPath; engram.db stays read-only.
type EmbeddingsConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"` // Ollama base URL, e.g. http://localhost:11434
	Model   string `yaml:"model"`    // e.g. nomic-embed-text
	Dim     int    `yaml:"dim"`      // embedding dimension, e.g. 768 for nomic-embed-text
	DBPath  string `yaml:"db_path"`  // path to Omnia's own embeddings SQLite file
}

type SourcesConfig struct {
	Discord   DiscordConfig   `yaml:"discord"`
	GitHub    GitHubConfig    `yaml:"github"`
	Atlassian AtlassianConfig `yaml:"atlassian"`
}

// AtlassianConfig holds ONE shared Atlassian Cloud site + Basic-auth
// credential pair (email + API token), used by both the Jira and Confluence
// adapters (design decision: single shared Cloud token/site, not one token
// per adapter). Jira/Confluence keep their own Enabled flag, project/space
// keys, and Engram Project so each source can be toggled and routed
// independently even though auth is shared.
//
// Phase 2 (this skeleton) only declares the config shape and its defaults.
// Router.ResolveJira/ResolveConfluence and collect.go wiring land with the
// Jira/Confluence adapters themselves (later phases).
type AtlassianConfig struct {
	SiteURL    string           `yaml:"site_url"`
	Email      string           `yaml:"email"`
	Token      string           `yaml:"token"`
	Jira       JiraConfig       `yaml:"jira"`
	Confluence ConfluenceConfig `yaml:"confluence"`
}

type JiraConfig struct {
	Enabled bool `yaml:"enabled"`
	// ProjectKeys lists the Jira project keys to ingest (e.g. "ENG", "OPS").
	ProjectKeys []string `yaml:"project_keys"`
	// Project is the Engram project this source's items route to by default.
	Project string `yaml:"project"`
}

type ConfluenceConfig struct {
	Enabled bool `yaml:"enabled"`
	// SpaceKeys lists the Confluence space keys to ingest.
	SpaceKeys []string `yaml:"space_keys"`
	// Project is the Engram project this source's items route to by default.
	Project string `yaml:"project"`
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
	// IncludeCommits also ingests commit history (sha, message, author login +
	// git name/email, date, url) as `github-commit` observations, in addition to
	// issues/PRs. Off by default: the first run backfills up to MaxCommitsPerRepo
	// per repo over the backfill window, which can be a lot of observations.
	IncludeCommits bool `yaml:"include_commits"`
	// MaxCommitsPerRepo caps commits fetched per repo per run (0 → default 300).
	MaxCommitsPerRepo int `yaml:"max_commits_per_repo"`
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
	if cfg.Sources.Atlassian.Jira.Project == "" {
		cfg.Sources.Atlassian.Jira.Project = cfg.Engram.DefaultProject
	}
	if cfg.Sources.Atlassian.Confluence.Project == "" {
		cfg.Sources.Atlassian.Confluence.Project = cfg.Engram.DefaultProject
	}
	if cfg.Embeddings.BaseURL == "" {
		cfg.Embeddings.BaseURL = "http://localhost:11434"
	}
	if cfg.Embeddings.Model == "" {
		cfg.Embeddings.Model = "nomic-embed-text"
	}
	if cfg.Embeddings.Dim == 0 {
		cfg.Embeddings.Dim = 768
	}
	if cfg.Embeddings.DBPath == "" {
		home, _ := os.UserHomeDir()
		cfg.Embeddings.DBPath = filepath.Join(home, ".local", "share", "omnia", "embeddings.db")
	}
}
