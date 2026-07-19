package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeProjectAdminStatsStore wraps fakeTeamsStore (teams/profiles/projects +
// admin-dashboard capabilities, from teams_admin_test.go) and adds the Slice
// 4b stats/reverse-access capability under test here. Kept as a separate
// wrapper type (rather than adding fields to fakeTeamsStore itself) so the
// existing teams_admin_test.go tests — which construct fakeTeamsStore
// directly and must keep passing unmodified — are never at risk.
type fakeProjectAdminStatsStore struct {
	*fakeTeamsStore
	chunkStats map[string]cloudstore.ProjectChunkStats
	access     map[string][]cloudstore.ProjectAccessRow
}

func newFakeProjectAdminStatsStore() *fakeProjectAdminStatsStore {
	return &fakeProjectAdminStatsStore{
		fakeTeamsStore: newFakeTeamsStore(),
		chunkStats:     map[string]cloudstore.ProjectChunkStats{},
		access:         map[string][]cloudstore.ProjectAccessRow{},
	}
}

func (s *fakeProjectAdminStatsStore) ListProjectChunkStats(context.Context) (map[string]cloudstore.ProjectChunkStats, error) {
	return s.chunkStats, nil
}

func (s *fakeProjectAdminStatsStore) ListAccountAccessForProject(_ context.Context, project string) ([]cloudstore.ProjectAccessRow, error) {
	return s.access[project], nil
}

// Compile-time assertion: the fake must satisfy the new capability seam.
var _ projectAdminStatsStore = (*fakeProjectAdminStatsStore)(nil)

func newProjectAdminStatsTestServer(t *testing.T) (*CloudServer, *fakeProjectAdminStatsStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeProjectAdminStatsStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

// ─── Access fragment: GET /admin/projects/{project}/access ──────────────────

func TestHandleAdminProjectAccessFragment_OperatorGate(t *testing.T) {
	srv, _, authSvc := newProjectAdminStatsTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/projects/proj1/access", accountCookie(t, authSvc, "1", "alice"), ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator GET access fragment: expected 403, got %d", rec.Code)
	}

	// No session at all → still rejected (never a bare 200 for an
	// unauthenticated request — requireOperator runs BEFORE any data load).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/projects/proj1/access", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("unauthenticated GET access fragment: expected non-200, got %d", rec.Code)
	}
}

func TestHandleAdminProjectAccessFragment_RendersAccessRows(t *testing.T) {
	srv, store, authSvc := newProjectAdminStatsTestServer(t)

	// Seed usernames (adminDashboardStore.ListUsers, via the embedded fake) so
	// the fragment resolves account_id → username exactly like
	// handleAdminTeamDetailPage already does for team members.
	store.users = append(store.users, cloudstore.AdminUser{ID: "1", Username: "alice"})
	store.users = append(store.users, cloudstore.AdminUser{ID: "2", Username: "bob"})

	store.access["proj1"] = []cloudstore.ProjectAccessRow{
		{AccountID: "1", Perms: 15, Source: "override"}, // Full · Override
		{AccountID: "2", Perms: 1, Source: "team"},      // Read · Team
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/projects/proj1/access", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET access fragment: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") || !strings.Contains(body, "bob") {
		t.Fatalf("fragment missing usernames: %s", body)
	}
	// i18n Slice 3: Spanish default ("Full"→"Total", "Read"→"Lectura",
	// "Team"→"Equipo"; "Override" is the same word in both languages).
	if !strings.Contains(body, "Total") || !strings.Contains(body, "Lectura") {
		t.Fatalf("fragment missing effective-perm labels: %s", body)
	}
	if !strings.Contains(body, "Override") || !strings.Contains(body, "Equipo") {
		t.Fatalf("fragment missing source badges: %s", body)
	}
}

func TestHandleAdminProjectAccessFragment_EmptyProjectShowsNoAccounts(t *testing.T) {
	srv, _, authSvc := newProjectAdminStatsTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/projects/empty-proj/access", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a project with no access rows, got %d", rec.Code)
	}
	// i18n Slice 3: Spanish default empty-state copy.
	if !strings.Contains(rec.Body.String(), "Ninguna cuenta tiene acceso") {
		t.Fatalf("expected an empty-state message, got: %s", rec.Body.String())
	}
}

// ─── Projects page: stats wired onto adminProjectRow ─────────────────────────

func TestHandleAdminProjectsPage_RendersCardStats(t *testing.T) {
	srv, store, authSvc := newProjectAdminStatsTestServer(t)

	store.projectMeta["proj1"] = &cloudstore.ProjectMeta{Project: "proj1", Kind: "work", DisplayName: "Proj One"}
	lastActivity := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store.chunkStats["proj1"] = cloudstore.ProjectChunkStats{
		Project:      "proj1",
		MemoryCount:  42,
		SourceCount:  3,
		LastActivity: lastActivity,
	}
	store.access["proj1"] = []cloudstore.ProjectAccessRow{
		{AccountID: "1", Perms: 1, Source: "team"},
		{AccountID: "2", Perms: 15, Source: "override"},
	}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc), "")
	req.Header.Set("Accept", "text/html")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/projects (HTML): expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "42") {
		t.Fatalf("page missing memory count (42): %s", body)
	}
	if !strings.Contains(body, ">3<") && !strings.Contains(body, "3</div>") {
		t.Fatalf("page missing source count (3): %s", body)
	}
	// Both access rows carry Read → access count should render as 2.
	if !strings.Contains(body, "2</div>") && !strings.Contains(body, ">2<") {
		t.Fatalf("page missing access count (2): %s", body)
	}
}

// TestHandleAdminProjectsPage_DegradesGracefullyWithoutStatsCapability proves
// the Projects page still renders (no panic, no 5xx) when the underlying
// store does not implement projectAdminStatsStore — mirrors how SyncEnabled
// already defaults gracefully when projectSyncControlAdminStore is absent.
func TestHandleAdminProjectsPage_DegradesGracefullyWithoutStatsCapability(t *testing.T) {
	store := newFakeTeamsStore() // NOT wrapped — no stats capability at all.
	store.projectMeta["proj1"] = &cloudstore.ProjectMeta{Project: "proj1", Kind: "personal"}
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even without stats capability, got %d body=%q", rec.Code, rec.Body.String())
	}
}
