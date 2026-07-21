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

// ─── Fake store: rollup + sync control + memories + audit ───────────────────
//
// fakeProjectDetailStore combines every capability
// handleAdminProjectDetailPage optionally uses: fakeProjectRollupStore
// (stats + links, Slice 4b/5a/5b) plus sync-control (OBL-04), memories
// (ListRecentObservations — the Memorias tab's data source), and the audit
// log (the Actividad tab). No single existing fake covers all four.
type fakeProjectDetailStore struct {
	*fakeProjectRollupStore
	pausedReason map[string]string
	observations map[string][]cloudstore.DashboardObservationRow
	auditEntries []cloudstore.DashboardAuditRow
}

func newFakeProjectDetailStore() *fakeProjectDetailStore {
	return &fakeProjectDetailStore{
		fakeProjectRollupStore: newFakeProjectRollupStore(),
		pausedReason:           map[string]string{},
		observations:           map[string][]cloudstore.DashboardObservationRow{},
	}
}

func (s *fakeProjectDetailStore) SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error {
	if s.syncEnabledMap == nil {
		s.syncEnabledMap = map[string]bool{}
	}
	s.syncEnabledMap[project] = enabled
	if enabled {
		delete(s.pausedReason, project)
	} else {
		s.pausedReason[project] = reason
	}
	return nil
}

func (s *fakeProjectDetailStore) GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error) {
	enabled, ok := s.syncEnabledMap[project]
	if !ok {
		return nil, nil
	}
	ctrl := &cloudstore.ProjectSyncControl{Project: project, SyncEnabled: enabled}
	if r, ok := s.pausedReason[project]; ok && r != "" {
		rr := r
		ctrl.PausedReason = &rr
	}
	return ctrl, nil
}

func (s *fakeProjectDetailStore) ListProjectSyncControlsMap(context.Context) (map[string]cloudstore.ProjectSyncControl, error) {
	out := make(map[string]cloudstore.ProjectSyncControl, len(s.syncEnabledMap))
	for project, enabled := range s.syncEnabledMap {
		ctrl := cloudstore.ProjectSyncControl{Project: project, SyncEnabled: enabled}
		if r, ok := s.pausedReason[project]; ok && r != "" {
			rr := r
			ctrl.PausedReason = &rr
		}
		out[project] = ctrl
	}
	return out, nil
}

func (s *fakeProjectDetailStore) ListRecentObservations(project, _ string, limit int) ([]cloudstore.DashboardObservationRow, error) {
	rows := s.observations[project]
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *fakeProjectDetailStore) ListAuditEntriesPaginated(_ context.Context, filter cloudstore.AuditFilter, limit, offset int) ([]cloudstore.DashboardAuditRow, int, error) {
	var rows []cloudstore.DashboardAuditRow
	for _, e := range s.auditEntries {
		if filter.Project != "" && e.Project != filter.Project {
			continue
		}
		rows = append(rows, e)
	}
	total := len(rows)
	if offset > total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total || limit <= 0 {
		end = total
	}
	return rows[offset:end], total, nil
}

var _ projectMemoriesAdminStore = (*fakeProjectDetailStore)(nil)
var _ auditLogStore = (*fakeProjectDetailStore)(nil)

func newProjectDetailTestServer(t *testing.T) (*CloudServer, *fakeProjectDetailStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeProjectDetailStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestAdminProjectDetailPage_NotFoundForUnknownProject(t *testing.T) {
	srv, _, authSvc := newProjectDetailTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/ghost", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAdminProjectDetailPage_ForbiddenForNonOperator(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/solo", accountCookie(t, authSvc, "9", "eve")))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-operator, got %d", rec.Code)
	}
}

func TestAdminProjectDetailPage_ParentShowsSubProjectsSectionAndRolledUpStats(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work", DisplayName: "Workly Marketing"}
	store.parents["workly-marketing"] = "workly"
	store.chunkStats["workly"] = cloudstore.ProjectChunkStats{MemoryCount: 10, SourceCount: 2}
	store.chunkStats["workly-marketing"] = cloudstore.ProjectChunkStats{MemoryCount: 55, SourceCount: 6}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/workly", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="subsection"`) {
		t.Fatalf("expected a prominent Sub-proyectos section for a parent, got: %s", body)
	}
	if !strings.Contains(body, "Workly Marketing") || !strings.Contains(body, "/admin/projects/workly-marketing") {
		t.Fatalf("expected a sub-project tile linking to the child's own detail page, got: %s", body)
	}
	// Rolled up: 10+55 memories, 2+6 sources.
	if !strings.Contains(body, ">65<") || !strings.Contains(body, ">8<") {
		t.Fatalf("expected rolled-up stat strip (65 memories, 8 sources), got: %s", body)
	}
}

func TestAdminProjectDetailPage_ChildShowsParentBackLink(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}
	store.parents["workly-marketing"] = "workly"

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/workly-marketing", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="pdetail-parentlink"`) || !strings.Contains(body, "workly") {
		t.Fatalf("expected a parent back-link, got: %s", body)
	}
	if strings.Contains(body, `class="subsection"`) {
		t.Fatalf("a child must never render its own Sub-proyectos section, got: %s", body)
	}
}

func TestAdminProjectDetailPage_MemoriesTabListsNewestFirstWithTypeAndTitle(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}
	store.observations["solo"] = []cloudstore.DashboardObservationRow{
		{Project: "solo", Type: "bugfix", Title: "Fixed the thing", CreatedAt: "2026-07-01 10:00:00"},
		{Project: "solo", Type: "decision", Title: "Chose the approach", CreatedAt: "2026-06-01 10:00:00"},
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/solo", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Fixed the thing", "Chose the approach", "bugfix", "decision", `class="mrow"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Memorias tab missing %q, body=%q", want, body)
		}
	}
}

func TestAdminProjectDetailPage_MemoriesUnavailableWhenStoreLacksCapability(t *testing.T) {
	// A plain fakeProjectRollupStore does NOT implement ListRecentObservations.
	store := newFakeProjectRollupStore()
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/solo", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no soporta el listado de memorias individuales") {
		t.Fatalf("expected the Memorias-unavailable note, got: %s", rec.Body.String())
	}
}

func TestAdminProjectDetailPage_AccessTabRendersFullMemberList(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	store.grant("1", "solo", 15, "owner")
	store.access["solo"] = []cloudstore.ProjectAccessRow{{AccountID: "1", Perms: 15, Source: "override"}}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/solo", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") || !strings.Contains(body, `class="arow"`) {
		t.Fatalf("expected the Acceso tab to list alice, got: %s", body)
	}
}

func TestAdminProjectDetailPage_ActivityTabShowsAuditTimelineScopedToProject(t *testing.T) {
	srv, store, authSvc := newProjectDetailTestServer(t)
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}
	store.auditEntries = []cloudstore.DashboardAuditRow{
		{Project: "solo", Action: cloudstore.AuditActionProjectPause, Contributor: "operator", OccurredAt: "2026-07-01T10:00:00Z"},
		{Project: "other", Action: cloudstore.AuditActionProjectPause, Contributor: "operator", OccurredAt: "2026-07-01T10:00:00Z"},
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects/solo", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, cloudstore.AuditActionProjectPause) {
		t.Fatalf("expected the Actividad tab to show the project's own audit entry, got: %s", body)
	}
	if strings.Count(body, cloudstore.AuditActionProjectPause) != 1 {
		t.Fatalf("expected exactly ONE audit entry (scoped to 'solo', excluding 'other'), got: %s", body)
	}
}
