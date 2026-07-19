package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

func TestAtlassian_DefaultsProjectFallsBackToEngramDefault(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  default_project: myproj\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sources.Atlassian.Jira.Project != "myproj" {
		t.Errorf("Jira.Project default: got %q, want %q", cfg.Sources.Atlassian.Jira.Project, "myproj")
	}
	if cfg.Sources.Atlassian.Confluence.Project != "myproj" {
		t.Errorf("Confluence.Project default: got %q, want %q", cfg.Sources.Atlassian.Confluence.Project, "myproj")
	}
}

func TestAtlassian_ParsesFromYAML(t *testing.T) {
	yaml := `
engram:
  default_project: omnia
sources:
  atlassian:
    site_url: https://acme.atlassian.net
    email: bot@acme.com
    token: secret-token
    jira:
      enabled: true
      project_keys:
        - ENG
        - OPS
      project: engineering
    confluence:
      enabled: true
      space_keys:
        - DOCS
      project: docs
`
	path := writeTempConfig(t, yaml)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a := cfg.Sources.Atlassian
	if a.SiteURL != "https://acme.atlassian.net" {
		t.Errorf("SiteURL: got %q", a.SiteURL)
	}
	if a.Email != "bot@acme.com" {
		t.Errorf("Email: got %q", a.Email)
	}
	if a.Token != "secret-token" {
		t.Errorf("Token: got %q", a.Token)
	}
	if !a.Jira.Enabled {
		t.Error("Jira.Enabled: got false, want true")
	}
	if len(a.Jira.ProjectKeys) != 2 || a.Jira.ProjectKeys[0] != "ENG" || a.Jira.ProjectKeys[1] != "OPS" {
		t.Errorf("Jira.ProjectKeys: got %v", a.Jira.ProjectKeys)
	}
	if a.Jira.Project != "engineering" {
		t.Errorf("Jira.Project: got %q", a.Jira.Project)
	}
	if !a.Confluence.Enabled {
		t.Error("Confluence.Enabled: got false, want true")
	}
	if len(a.Confluence.SpaceKeys) != 1 || a.Confluence.SpaceKeys[0] != "DOCS" {
		t.Errorf("Confluence.SpaceKeys: got %v", a.Confluence.SpaceKeys)
	}
	if a.Confluence.Project != "docs" {
		t.Errorf("Confluence.Project: got %q", a.Confluence.Project)
	}
}

func TestAtlassian_DisabledByDefault(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  default_project: omnia\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sources.Atlassian.Jira.Enabled {
		t.Error("Jira.Enabled default: got true, want false")
	}
	if cfg.Sources.Atlassian.Confluence.Enabled {
		t.Error("Confluence.Enabled default: got true, want false")
	}
}
