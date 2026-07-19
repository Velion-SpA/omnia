package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSubProjectResolver is a minimal in-memory dashboard.SubProjectResolver
// (Command Center v2, Slice 5b) for testing the project-detail page's
// Sub-projects section + parent breadcrumb WITHOUT depending on
// internal/cloud/clouddash — internal/dashboard must stay layer-independent,
// so its own tests exercise the seam with a local fake, exactly like every
// other optional-capability seam in this package (e.g. StructuralReader,
// SemanticIndex fakes elsewhere in this test package).
type fakeSubProjectResolver struct {
	children map[string][]string // parent -> children
	parents  map[string]string   // child -> parent
}

func (f *fakeSubProjectResolver) ChildrenOf(_ context.Context, project string) []string {
	return f.children[project]
}

func (f *fakeSubProjectResolver) ParentOf(_ context.Context, project string) string {
	return f.parents[project]
}

var _ SubProjectResolver = (*fakeSubProjectResolver)(nil)

// newTestServerWithResolver mirrors newTestServerOnly but wires a
// SubProjectResolver via the new functional option.
func newTestServerWithResolver(t *testing.T, fe *fakeEngram, resolver SubProjectResolver) *httptest.Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	srv := NewServer(Config{
		Port:          0,
		EngramURL:     engServer.URL,
		EngramDataDir: t.TempDir(),
	}, logger, WithSubProjectResolver(resolver))

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	dashServer := httptest.NewServer(mux)
	t.Cleanup(dashServer.Close)
	return dashServer
}

// ─── Unit-level: buildProjectDetailData ──────────────────────────────────────

func TestBuildProjectDetailData_ParentProject_PopulatesChildren(t *testing.T) {
	fe := newFakeEngram()
	resolver := &fakeSubProjectResolver{
		children: map[string][]string{"workly": {"workly-marketing", "workly-videos"}},
	}
	srv := newTestServerInstance(t, fe, WithSubProjectResolver(resolver))

	data := srv.buildProjectDetailData(context.Background(), "workly")

	if len(data.Children) != 2 {
		t.Fatalf("expected 2 children, got %d (%+v)", len(data.Children), data.Children)
	}
	if data.Children[0].Name != "workly-marketing" || data.Children[0].URL != "/project/workly-marketing" {
		t.Errorf("unexpected child[0]: %+v", data.Children[0])
	}
	if data.ParentProject != "" {
		t.Errorf("expected no parent breadcrumb for a parent project, got %q", data.ParentProject)
	}
}

func TestBuildProjectDetailData_ChildProject_PopulatesParentBreadcrumb(t *testing.T) {
	fe := newFakeEngram()
	resolver := &fakeSubProjectResolver{
		parents: map[string]string{"workly-marketing": "workly"},
	}
	srv := newTestServerInstance(t, fe, WithSubProjectResolver(resolver))

	data := srv.buildProjectDetailData(context.Background(), "workly-marketing")

	if data.ParentProject != "workly" {
		t.Errorf("expected ParentProject=workly, got %q", data.ParentProject)
	}
	if data.ParentProjectURL != "/project/workly" {
		t.Errorf("expected ParentProjectURL=/project/workly, got %q", data.ParentProjectURL)
	}
	if len(data.Children) != 0 {
		t.Errorf("expected no children for a child project, got %+v", data.Children)
	}
}

func TestBuildProjectDetailData_NilResolver_NoChildrenNoParent(t *testing.T) {
	fe := newFakeEngram()
	srv := newTestServerInstance(t, fe) // no resolver

	data := srv.buildProjectDetailData(context.Background(), "anything")

	if len(data.Children) != 0 {
		t.Errorf("expected no children with a nil resolver, got %+v", data.Children)
	}
	if data.ParentProject != "" {
		t.Errorf("expected no parent with a nil resolver, got %q", data.ParentProject)
	}
}

// ─── HTTP-level: rendered page ───────────────────────────────────────────────

func TestHandleProjectDetail_ParentProject_RendersChildrenSection(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(600, "workly"))
	resolver := &fakeSubProjectResolver{
		children: map[string][]string{"workly": {"workly-marketing", "workly-videos"}},
	}
	dashServer := newTestServerWithResolver(t, fe, resolver)

	resp, err := http.Get(dashServer.URL + "/project/workly")
	if err != nil {
		t.Fatalf("GET /project/workly: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Spanish is the default language (see projectDetail.subProjects).
	if !strings.Contains(bodyStr, "Sub-proyectos") {
		t.Error("expected a Sub-projects section for a parent project")
	}
	if !strings.Contains(bodyStr, `href="/project/workly-marketing"`) {
		t.Error("expected a link to the workly-marketing child")
	}
	if !strings.Contains(bodyStr, `href="/project/workly-videos"`) {
		t.Error("expected a link to the workly-videos child")
	}
	if strings.Contains(bodyStr, "project-parent-link") {
		t.Error("a parent project must not render its own parent breadcrumb")
	}
}

func TestHandleProjectDetail_ChildProject_RendersParentBreadcrumb(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(601, "workly-marketing"))
	resolver := &fakeSubProjectResolver{
		parents: map[string]string{"workly-marketing": "workly"},
	}
	dashServer := newTestServerWithResolver(t, fe, resolver)

	resp, err := http.Get(dashServer.URL + "/project/workly-marketing")
	if err != nil {
		t.Fatalf("GET /project/workly-marketing: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "project-parent-link") {
		t.Error("expected a parent breadcrumb link for a child project")
	}
	if !strings.Contains(bodyStr, `href="/project/workly"`) {
		t.Error("expected the breadcrumb to link to the parent's detail page")
	}
	if strings.Contains(bodyStr, "Sub-proyectos") {
		t.Error("a child project (no linked children) must not render a Sub-projects section")
	}
}

func TestHandleProjectDetail_NilResolver_RendersNeitherSectionNorPanics(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(602, "socrates"))
	dashServer := newTestServerOnly(t, fe) // no resolver wired at all

	resp, err := http.Get(dashServer.URL + "/project/socrates")
	if err != nil {
		t.Fatalf("GET /project/socrates: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (no panic without a resolver), got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "Sub-proyectos") {
		t.Error("expected no Sub-projects section when no resolver is configured")
	}
	if strings.Contains(bodyStr, "project-parent-link") {
		t.Error("expected no parent breadcrumb when no resolver is configured")
	}
}
