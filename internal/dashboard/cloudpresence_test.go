package dashboard

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/velion/omnia/internal/engramdb"
)

// fakeStructural satisfies StructuralReader and exposes CloudTargetKeys so it can
// stand in for *engramdb.DB in cloudsByProject. Only CloudTargetKeys carries test
// data; the rest return zero values.
type fakeStructural struct {
	targetKeys map[string]struct{}
}

func (f fakeStructural) List(context.Context, engramdb.Filter) ([]engramdb.Observation, error) {
	return nil, nil
}
func (f fakeStructural) ListByIDs(context.Context, []int) ([]engramdb.Observation, error) {
	return nil, nil
}
func (f fakeStructural) Projects(context.Context) ([]engramdb.ProjectCount, error) { return nil, nil }
func (f fakeStructural) ProjectsCanonical(context.Context, func(string) string) ([]engramdb.ProjectCount, error) {
	return nil, nil
}
func (f fakeStructural) Types(context.Context) ([]engramdb.TypeCount, error) { return nil, nil }
func (f fakeStructural) CloudTargetKeys(context.Context) (map[string]struct{}, error) {
	return f.targetKeys, nil
}

// fakeStructuralWithSync additionally satisfies syncStateReader, mirroring the
// real *engramdb.DB which always implements both capabilities together.
type fakeStructuralWithSync struct {
	fakeStructural
	states []engramdb.SyncTargetState
}

func (f fakeStructuralWithSync) SyncStates(context.Context) ([]engramdb.SyncTargetState, error) {
	return f.states, nil
}

func writeCloudJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write cloud.json: %v", err)
	}
}

func TestSplitCloudTargetKey(t *testing.T) {
	cases := []struct {
		in             string
		alias, project string
		ok             bool
	}{
		{"cloud:omnia", "cloud", "omnia", true},
		{"work:my-proj", "work", "my-proj", true},
		{"cloud", "", "", false},  // bare key, no project
		{"local", "", "", false},  // local tracking
		{"", "", "", false},       // empty
		{"cloud:", "", "", false}, // empty project
		{":proj", "", "", false},  // empty alias
	}
	for _, c := range cases {
		a, p, ok := splitCloudTargetKey(c.in)
		if a != c.alias || p != c.project || ok != c.ok {
			t.Errorf("splitCloudTargetKey(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, a, p, ok, c.alias, c.project, c.ok)
		}
	}
}

func TestLoadDashboardClouds(t *testing.T) {
	t.Run("absent file → empty", func(t *testing.T) {
		got := loadDashboardClouds(t.TempDir())
		if len(got.aliases) != 0 || got.defaultAlias != "" {
			t.Errorf("expected empty, got %+v", got)
		}
	})

	t.Run("v1 → single cloud alias", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"server_url":"https://x.test","token":"tok"}`)
		got := loadDashboardClouds(dir)
		if _, ok := got.aliases["cloud"]; !ok || got.defaultAlias != "cloud" {
			t.Errorf("v1 should map to alias 'cloud' default 'cloud', got %+v", got)
		}
	})

	t.Run("v2 named with explicit default", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"clouds":{"personal":{"server_url":"https://p.test"},"work":{"server_url":"https://w.test"}},"default":"personal"}`)
		got := loadDashboardClouds(dir)
		if _, ok := got.aliases["personal"]; !ok {
			t.Errorf("missing personal alias: %+v", got)
		}
		if _, ok := got.aliases["work"]; !ok {
			t.Errorf("missing work alias: %+v", got)
		}
		if got.defaultAlias != "personal" {
			t.Errorf("default = %q, want personal", got.defaultAlias)
		}
	})

	t.Run("v2 single → implicit default", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"clouds":{"work":{"server_url":"https://w.test"}}}`)
		got := loadDashboardClouds(dir)
		if got.defaultAlias != "work" {
			t.Errorf("single cloud should be implicit default, got %q", got.defaultAlias)
		}
	})
}

func TestDashboardCloudsDisplayName(t *testing.T) {
	c := dashboardClouds{
		aliases:      map[string]struct{}{"personal": {}, "work": {}},
		defaultAlias: "personal",
	}
	// Known alias → itself.
	if name, ok := c.displayName("work"); !ok || name != "work" {
		t.Errorf("work → (%q,%v), want (work,true)", name, ok)
	}
	// Legacy "cloud" prefix → default cloud.
	if name, ok := c.displayName("cloud"); !ok || name != "personal" {
		t.Errorf("cloud → (%q,%v), want (personal,true)", name, ok)
	}
	// Unknown/removed alias → skipped.
	if _, ok := c.displayName("ghost"); ok {
		t.Error("unknown alias should not resolve")
	}
}

func TestCloudsByProject(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	canonicalize := canonicalizerFunc(nil)

	t.Run("named multi-cloud with legacy default mapping", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"clouds":{"personal":{"server_url":"https://p.test"},"work":{"server_url":"https://w.test"}},"default":"personal"}`)
		s := &Server{
			cfg: Config{EngramDataDir: dir},
			db: fakeStructural{targetKeys: map[string]struct{}{
				"cloud:omnia":    {}, // legacy prefix → default cloud "personal"
				"work:velion":    {}, // explicit named cloud
				"personal:omnia": {}, // omnia also on personal explicitly
				"ghost:notes":    {}, // unknown cloud → skipped
				"local":          {}, // ignored (no project segment)
			}},
			groups: newGroupIndex(nil, logger),
			logger: logger,
		}
		got, show := s.cloudsByProject(context.Background(), canonicalize)
		if !show {
			t.Fatal("expected ShowClouds true")
		}
		// fakeStructural does not implement syncStateReader, so every target
		// CloudTargetKeys reports renders healthy — unchanged pre-OBL-12 behavior.
		want := map[string][]CloudPlacement{
			"omnia":  {{Name: "personal", Status: CloudPillHealthy}}, // cloud:omnia and personal:omnia both → personal (deduped)
			"velion": {{Name: "work", Status: CloudPillHealthy}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("degraded target with zero chunks is visible, not silently absent", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"clouds":{"personal":{"server_url":"https://p.test"},"work":{"server_url":"https://w.test"}},"default":"personal"}`)
		s := &Server{
			cfg: Config{EngramDataDir: dir},
			db: fakeStructuralWithSync{
				fakeStructural: fakeStructural{targetKeys: map[string]struct{}{
					"cloud:omnia": {}, // healthy: has replicated chunks
				}},
				states: []engramdb.SyncTargetState{
					{TargetKey: "cloud:omnia", Lifecycle: "healthy"},
					// work:velion never replicated a chunk (absent from
					// CloudTargetKeys) but IS degraded — must still show up.
					{TargetKey: "work:velion", Lifecycle: "degraded", ReasonCode: "auth_required", ReasonMessage: "token expired"},
					// personal:notes is mid-attempt — pending, amber.
					{TargetKey: "personal:notes", Lifecycle: "pending"},
				},
			},
			groups: newGroupIndex(nil, logger),
			logger: logger,
		}
		got, show := s.cloudsByProject(context.Background(), canonicalize)
		if !show {
			t.Fatal("expected ShowClouds true")
		}
		want := map[string][]CloudPlacement{
			"omnia":  {{Name: "personal", Status: CloudPillHealthy}},
			"velion": {{Name: "work", Status: CloudPillDegraded, ReasonCode: "auth_required"}},
			"notes":  {{Name: "personal", Status: CloudPillPending}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("no cloud.json → not resolvable", func(t *testing.T) {
		s := &Server{
			cfg:    Config{EngramDataDir: t.TempDir()},
			db:     fakeStructural{targetKeys: map[string]struct{}{"cloud:omnia": {}}},
			groups: newGroupIndex(nil, logger),
			logger: logger,
		}
		got, show := s.cloudsByProject(context.Background(), canonicalize)
		if show || got != nil {
			t.Errorf("without cloud.json expected (nil,false), got (%v,%v)", got, show)
		}
	})

	t.Run("structural backend without CloudTargetKeys → not resolvable", func(t *testing.T) {
		dir := t.TempDir()
		writeCloudJSON(t, dir, `{"clouds":{"work":{"server_url":"https://w.test"}}}`)
		s := &Server{
			cfg:    Config{EngramDataDir: dir},
			db:     noCloudStructural{},
			groups: newGroupIndex(nil, logger),
			logger: logger,
		}
		got, show := s.cloudsByProject(context.Background(), canonicalize)
		if show || got != nil {
			t.Errorf("cloud-incapable backend expected (nil,false), got (%v,%v)", got, show)
		}
	})
}

// noCloudStructural satisfies StructuralReader WITHOUT CloudTargetKeys, mirroring
// the cloud dashboard's structural reader.
type noCloudStructural struct{}

func (noCloudStructural) List(context.Context, engramdb.Filter) ([]engramdb.Observation, error) {
	return nil, nil
}
func (noCloudStructural) ListByIDs(context.Context, []int) ([]engramdb.Observation, error) {
	return nil, nil
}
func (noCloudStructural) Projects(context.Context) ([]engramdb.ProjectCount, error) { return nil, nil }
func (noCloudStructural) ProjectsCanonical(context.Context, func(string) string) ([]engramdb.ProjectCount, error) {
	return nil, nil
}
func (noCloudStructural) Types(context.Context) ([]engramdb.TypeCount, error) { return nil, nil }
