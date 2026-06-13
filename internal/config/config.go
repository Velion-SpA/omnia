package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Engram       EngramConfig  `yaml:"engram"`
	Sources      SourcesConfig `yaml:"sources"`
	BackfillDays int           `yaml:"backfill_days"`
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
