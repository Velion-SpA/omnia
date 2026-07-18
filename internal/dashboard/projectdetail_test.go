package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/meta"
)

// newTestServerInstance builds a bare *Server (no httptest HTTP wrapper) for
// unit-level tests that need to call Server methods directly (e.g.
// buildProjectDetailData) rather than going through a real HTTP round trip.
// Mirrors newTestServerOnly's construction exactly, minus the mux/listener.
func newTestServerInstance(t *testing.T, fe *fakeEngram) *Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	return NewServer(Config{
		Port:      0,
		EngramURL: engServer.URL,
		// Empty temp dir → engramdb.Open fails fast → s.db stays nil → tests
		// exercise the FTS/curated fallback path, same as every other
		// handler test in this package.
		EngramDataDir: t.TempDir(),
	}, logger)
}

func TestHandleProjectDetail_RendersFullPage(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(500, "socrates"))
	fe.add(curatedObs(501, "socrates"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/project/socrates")
	if err != nil {
		t.Fatalf("GET /project/socrates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "socrates") {
		t.Error("expected project name 'socrates' in response")
	}
	if !strings.Contains(bodyStr, "feat: add feature 500") {
		t.Error("expected at least one memory card (ingested obs title) in response")
	}
	if !strings.Contains(bodyStr, "Memories") {
		t.Error("expected 'Memories' stat label in response")
	}
	if !strings.Contains(bodyStr, "Sources") {
		t.Error("expected 'Sources' stat label in response")
	}
	if !strings.Contains(bodyStr, "Last activity") {
		t.Error("expected 'Last activity' stat label in response")
	}
	if !strings.Contains(bodyStr, "/browse?project=socrates") {
		t.Error("expected a 'View all in Browse' link scoped to the project")
	}
	if !strings.Contains(bodyStr, `class="memory-card"`) {
		t.Error("expected the shared ui.MemoryCard grid to render")
	}
}

func TestHandleProjectDetail_UnknownProject_GracefulEmptyState(t *testing.T) {
	// No observations added at all — any project name will resolve to zero
	// results, exercising the "unknown/empty project" graceful path.
	fe := newFakeEngram()
	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/project/does-not-exist")
	if err != nil {
		t.Fatalf("GET /project/does-not-exist: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 (graceful empty state, not a 500/panic), got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "does-not-exist") {
		t.Error("expected the (unknown) project name to still render in the header")
	}
	if !strings.Contains(bodyStr, "No results") {
		t.Error("expected the shared empty-state message for zero memories")
	}
}

func TestHandleProjectDetail_URLEncodedName_Decodes(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(510, "my project"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/project/my%20project")
	if err != nil {
		t.Fatalf("GET /project/my%%20project: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "my project") {
		t.Error("expected the URL-decoded project name 'my project' in response")
	}
	if !strings.Contains(bodyStr, "feat: add feature 510") {
		t.Error("expected a memory card for the decoded project")
	}
}

func TestBuildProjectDetailData_EmptyName_GracefulEmptyState(t *testing.T) {
	fe := newFakeEngram()
	srv := newTestServerInstance(t, fe)

	data := srv.buildProjectDetailData(context.Background(), "")

	if data.Found {
		t.Error("expected Found=false for an empty project name")
	}
	if data.Stats.Total != 0 {
		t.Errorf("expected 0 memories for empty name, got %d", data.Stats.Total)
	}
	if len(data.Memories) != 0 {
		t.Errorf("expected 0 memory cards for empty name, got %d", len(data.Memories))
	}
}

// newTestServerWithProjects mirrors newTestServerOnly but configures
// cfg.Projects explicitly. handleOverview's project list comes from
// knownProjects(syncStatus, cfg), which — by design (see
// TestKnownProjects_EmptyConfigYieldsNoDefault) — has NO hard-coded
// default, so a test asserting on rendered project ROWS (not just page
// furniture) needs at least one configured project name.
func newTestServerWithProjects(t *testing.T, fe *fakeEngram, projects []string) *httptest.Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	srv := NewServer(Config{
		Port:          0,
		EngramURL:     engServer.URL,
		EngramDataDir: t.TempDir(),
		Projects:      projects,
	}, logger)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	dashServer := httptest.NewServer(mux)
	t.Cleanup(dashServer.Close)
	return dashServer
}

func TestHandleOverview_ProjectNameLinksToProjectDetail(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(520, "omnia"))

	dashServer := newTestServerWithProjects(t, fe, []string{"omnia"})

	resp, err := http.Get(dashServer.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, `href="/project/omnia"`) {
		t.Error("expected the Overview project row's name to link to /project/omnia")
	}
	if !strings.Contains(bodyStr, `href="/browse?project=omnia"`) {
		t.Error("expected the existing browse-filter link to still be present")
	}
}

func TestHandleBrowse_ActiveProjectChip_LinksToProjectDetail(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(530, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/browse?project=omnia")
	if err != nil {
		t.Fatalf("GET /browse?project=omnia: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, `href="/project/omnia"`) {
		t.Error("expected the active project filter chip's label to link to /project/omnia")
	}
	if !strings.Contains(bodyStr, `title="Remove filter"`) {
		t.Error("expected the existing remove-filter control to still be present")
	}
}

func TestDistinctSourceCount_CountsIngestedSourcesPlusCurated(t *testing.T) {
	views := []ObsView{
		{HasMeta: true, Meta: meta.Meta{Source: "github"}},
		{HasMeta: true, Meta: meta.Meta{Source: "github"}},
		{HasMeta: true, Meta: meta.Meta{Source: "discord"}},
		{HasMeta: false},
	}
	stats := computeProjectStats("proj", views)
	got := distinctSourceCount(stats)
	want := 3 // github + discord + one curated/manual bucket
	if got != want {
		t.Errorf("distinctSourceCount = %d, want %d", got, want)
	}
}

func TestDistinctSourceCount_ZeroWhenNoObservations(t *testing.T) {
	stats := computeProjectStats("proj", nil)
	if got := distinctSourceCount(stats); got != 0 {
		t.Errorf("distinctSourceCount = %d, want 0", got)
	}
}

func TestProjectDetailURL_EscapesSpecialChars(t *testing.T) {
	got := projectDetailURL("my project")
	want := "/project/my%20project"
	if got != want {
		t.Errorf("projectDetailURL(%q) = %q, want %q", "my project", got, want)
	}
}

func TestProjectDetailURL_EmptyNameReturnsEmpty(t *testing.T) {
	if got := projectDetailURL(""); got != "" {
		t.Errorf("projectDetailURL(\"\") = %q, want empty", got)
	}
}

// Ensures the new route doesn't panic on an httptest server wired exactly
// like the other handler tests in this package (belt-and-suspenders check
// that registerRoutes actually mounts GET /project/{name}).
func TestHandleProjectDetail_RouteRegistered(t *testing.T) {
	fe := newFakeEngram()
	dashServer := newTestServerOnly(t, fe)

	req, _ := http.NewRequest(http.MethodGet, dashServer.URL+"/project/anything", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /project/anything: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("expected GET /project/{name} to be a registered route, got 404")
	}
}
