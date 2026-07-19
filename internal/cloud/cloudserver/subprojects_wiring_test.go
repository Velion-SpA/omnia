package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeSubProjectLinksMountStore embeds fakeMembershipStore (ChunkStore +
// RBAC) and adds the clouddash.CloudStore surface (ListProjects/
// ListRecentObservations, so CloudServer.dashboardSource wires a real
// clouddash.Source instead of falling back to emptyCloudStore) PLUS the
// Slice 5a project-links capability (ListProjectParents) under test here —
// mirroring fakeTenantScopeStore's wrapper pattern in
// dashboard_tenant_scope_test.go.
type fakeSubProjectLinksMountStore struct {
	*fakeMembershipStore
	projects []cloudstore.DashboardProjectRow
	rows     []cloudstore.DashboardObservationRow
	parents  map[string]string
}

func (f *fakeSubProjectLinksMountStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	return f.projects, nil
}

func (f *fakeSubProjectLinksMountStore) ListRecentObservations(string, string, int) ([]cloudstore.DashboardObservationRow, error) {
	return f.rows, nil
}

func (f *fakeSubProjectLinksMountStore) ListProjectParents(context.Context) (map[string]string, error) {
	return f.parents, nil
}

func newSubProjectMountTestServer(t *testing.T, store *fakeSubProjectLinksMountStore) (*CloudServer, *cloudauth.Service) {
	t.Helper()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, authSvc
}

// TestMountDashboard_ProjectLinksStore_RendersSubProjectsSection proves the
// Slice 5b wiring end to end: a store that supports ListProjectParents
// (Slice 5a) makes the mounted (shared internal/dashboard) project-detail
// page render the "Sub-projects" section for a parent project, through the
// REAL cloud mount (dashboard_mount.go's mountDashboard), not just the
// clouddash-level unit tests.
func TestMountDashboard_ProjectLinksStore_RendersSubProjectsSection(t *testing.T) {
	store := &fakeSubProjectLinksMountStore{
		fakeMembershipStore: newFakeMembershipStore(),
		projects: []cloudstore.DashboardProjectRow{
			{Project: "workly", Observations: 1},
		},
		rows: []cloudstore.DashboardObservationRow{
			{Project: "workly", SessionID: "s1", SyncID: "w1", Type: "decision", Title: "Workly memory", Content: "body", CreatedAt: "2026-01-01 00:00:00"},
		},
		parents: map[string]string{"workly-marketing": "workly", "workly-videos": "workly"},
	}
	srv, authSvc := newSubProjectMountTestServer(t, store)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/project/workly", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Spanish is the default language for the shared dashboard (i18n Slice 2
	// — internal/dashboard/projectdetail.templ's projectDetail.subProjects
	// key), which this mount reuses verbatim.
	if !strings.Contains(body, "Sub-proyectos") {
		t.Fatalf("expected the Sub-projects section for a parent project, got: %s", body)
	}
	if !strings.Contains(body, `href="/project/workly-marketing"`) || !strings.Contains(body, `href="/project/workly-videos"`) {
		t.Fatalf("expected links to both children, got: %s", body)
	}
}

// TestMountDashboard_StoreWithoutProjectLinks_DegradesGracefully proves a
// store that does NOT implement ListProjectParents still mounts and renders
// the project-detail page — no panic, no 5xx, and neither Slice 5b section —
// exactly like the local (no-links) dashboard.
func TestMountDashboard_StoreWithoutProjectLinks_DegradesGracefully(t *testing.T) {
	baseStore := newFakeMembershipStore() // no ListProjects/ListProjectParents at all
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(baseStore, authSvc, 0, WithDashboardAdminToken("admin-token"))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/project/anything", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (no panic without project-links capability), got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Sub-proyectos") {
		t.Error("expected no Sub-projects section without a project-links-capable store")
	}
	if strings.Contains(body, "project-parent-link") {
		t.Error("expected no parent breadcrumb without a project-links-capable store")
	}
}
