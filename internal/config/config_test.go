package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRouterResolveGitHub_Default(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveGitHub("arratiabenjamin/saluvita")
	if got != "saluvita" {
		t.Errorf("ResolveGitHub default: got %q, want %q", got, "saluvita")
	}
}

func TestRouterResolveGitHub_Override(t *testing.T) {
	routes := map[string]string{
		"github/arratiabenjamin/saluvita": "myproject",
	}
	r := NewRouter(routes, "omnia")
	got := r.ResolveGitHub("arratiabenjamin/saluvita")
	if got != "myproject" {
		t.Errorf("ResolveGitHub override: got %q, want %q", got, "myproject")
	}
}

func TestRouterResolveGitHub_Normalization(t *testing.T) {
	routes := map[string]string{
		"github/arratiabenjamin/saluvita": "  MyProject  ",
	}
	r := NewRouter(routes, "omnia")
	got := r.ResolveGitHub("arratiabenjamin/saluvita")
	if got != "myproject" {
		t.Errorf("ResolveGitHub normalization: got %q, want %q", got, "myproject")
	}
}

func TestRouterResolveDiscord_Default(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveDiscord("123456", "velion-server")
	if got != "velion-server" {
		t.Errorf("ResolveDiscord default: got %q, want %q", got, "velion-server")
	}
}

func TestRouterResolveDiscord_Override(t *testing.T) {
	routes := map[string]string{
		"discord/123456": "saluvita",
	}
	r := NewRouter(routes, "omnia")
	got := r.ResolveDiscord("123456", "velion-server")
	if got != "saluvita" {
		t.Errorf("ResolveDiscord override: got %q, want %q", got, "saluvita")
	}
}

func TestRouterNormalizesDefaultProject(t *testing.T) {
	r := NewRouter(nil, "  OMNIA  ")
	// When no route and no guild slug, falls back to defaultProject.
	got := r.ResolveDiscord("99999", "")
	if got != "omnia" {
		t.Errorf("Router normalizes defaultProject: got %q, want %q", got, "omnia")
	}
}

func TestConfigRoutesYAMLParse(t *testing.T) {
	yaml := `
engram:
  base_url: http://127.0.0.1:7437
  default_project: omnia
routes:
  github/owner/repo: myproject
  discord/123456: saluvita
sources:
  github:
    enabled: true
    repos:
      - owner/repo
backfill_days: 30
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got := cfg.Routes["github/owner/repo"]; got != "myproject" {
		t.Errorf("Routes[github/owner/repo] = %q, want %q", got, "myproject")
	}
	if got := cfg.Routes["discord/123456"]; got != "saluvita" {
		t.Errorf("Routes[discord/123456] = %q, want %q", got, "saluvita")
	}
}
