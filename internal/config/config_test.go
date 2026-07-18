package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/projectname"
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

// TestNormalizeProjectAgreesWithProjectnameLeaf pins the H6 audit fix:
// config.normalizeProject must delegate to the shared internal/projectname
// leaf package so it can never diverge from the store/project copies again.
// Before the fix, normalizeProject did NOT collapse repeated separators
// (unlike store.NormalizeProject) — this now intentionally changes to match.
func TestNormalizeProjectAgreesWithProjectnameLeaf(t *testing.T) {
	inputs := []string{
		"engram",
		"Engram",
		"  engram  ",
		"my--project",
		"my__project",
		"",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			got := normalizeProject(in)
			want := projectname.Normalize(in)
			if got != want {
				t.Errorf("normalizeProject(%q) = %q, want %q (from projectname.Normalize)", in, got, want)
			}
		})
	}
}

func TestRouterResolveJira_Default(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveJira("ENG")
	if got != "eng" {
		t.Errorf("ResolveJira default: got %q, want %q", got, "eng")
	}
}

func TestRouterResolveJira_Override(t *testing.T) {
	routes := map[string]string{
		"jira/ENG": "engineering",
	}
	r := NewRouter(routes, "omnia")
	got := r.ResolveJira("ENG")
	if got != "engineering" {
		t.Errorf("ResolveJira override: got %q, want %q", got, "engineering")
	}
}

func TestRouterResolveJira_EmptyProjectKeyFallsBackToDefault(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveJira("")
	if got != "omnia" {
		t.Errorf("ResolveJira empty key: got %q, want %q", got, "omnia")
	}
}

func TestRouterResolveConfluence_Default(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveConfluence("DOCS")
	if got != "docs" {
		t.Errorf("ResolveConfluence default: got %q, want %q", got, "docs")
	}
}

func TestRouterResolveConfluence_Override(t *testing.T) {
	routes := map[string]string{
		"confluence/DOCS": "documentation",
	}
	r := NewRouter(routes, "omnia")
	got := r.ResolveConfluence("DOCS")
	if got != "documentation" {
		t.Errorf("ResolveConfluence override: got %q, want %q", got, "documentation")
	}
}

func TestRouterResolveConfluence_EmptySpaceKeyFallsBackToDefault(t *testing.T) {
	r := NewRouter(nil, "omnia")
	got := r.ResolveConfluence("")
	if got != "omnia" {
		t.Errorf("ResolveConfluence empty key: got %q, want %q", got, "omnia")
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
