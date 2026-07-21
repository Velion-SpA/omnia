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

// fakeProjectLinksStore wraps fakeTeamsStore (teams/profiles/projects +
// admin-dashboard capabilities) and adds the Slice 5a sub-project linking
// capability under test here — mirroring fakeProjectAdminStatsStore's wrapper
// pattern in project_admin_stats_test.go. Its SetProjectParent replicates the
// SAME 2-level validation as cloudstore.SetProjectParent so the handler's
// error-mapping can be exercised without a real Postgres store.
type fakeProjectLinksStore struct {
	*fakeTeamsStore
	parents map[string]string // child -> parent
}

func newFakeProjectLinksStore() *fakeProjectLinksStore {
	return &fakeProjectLinksStore{fakeTeamsStore: newFakeTeamsStore(), parents: map[string]string{}}
}

func (s *fakeProjectLinksStore) SetProjectParent(_ context.Context, child, parent string) error {
	child = strings.TrimSpace(child)
	parent = strings.TrimSpace(parent)
	if child == "" || parent == "" {
		return cloudstore.ErrProjectLinkSelf // unreachable in these tests; keeps signature honest
	}
	if child == parent {
		return cloudstore.ErrProjectLinkSelf
	}
	if _, parentIsChild := s.parents[parent]; parentIsChild {
		return cloudstore.ErrProjectLinkParentIsChild
	}
	for _, p := range s.parents {
		if p == child {
			return cloudstore.ErrProjectLinkChildIsParent
		}
	}
	s.parents[child] = parent
	if s.projectMeta[child] == nil {
		s.projectMeta[child] = &cloudstore.ProjectMeta{Project: child, Kind: "work"}
	}
	return nil
}

func (s *fakeProjectLinksStore) ClearProjectParent(_ context.Context, child string) error {
	delete(s.parents, child)
	return nil
}

func (s *fakeProjectLinksStore) ListProjectParents(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.parents))
	for k, v := range s.parents {
		out[k] = v
	}
	return out, nil
}

// Compile-time assertion: the fake must satisfy the new capability seam.
var _ projectLinksAdminStore = (*fakeProjectLinksStore)(nil)

func newProjectLinksTestServer(t *testing.T) (*CloudServer, *fakeProjectLinksStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeProjectLinksStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

func formRequest(method, url string, cookie *http.Cookie, body string) *http.Request {
	req := cookieRequest(method, url, cookie, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// ─── POST /admin/projects/{project}/parent ───────────────────────────────────

func TestHandleAdminSetProjectParent_OperatorGate(t *testing.T) {
	srv, _, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly-marketing/parent", accountCookie(t, authSvc, "1", "alice"), "parent=workly"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator POST parent: expected 403, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/projects/workly-marketing/parent", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("unauthenticated POST parent: expected non-200, got %d", rec.Code)
	}
}

func TestHandleAdminSetProjectParent_Success(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly-marketing/parent", operatorCookie(t, authSvc), "parent=workly"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.parents["workly-marketing"] != "workly" {
		t.Fatalf("expected workly-marketing linked to workly, got %+v", store.parents)
	}
}

func TestHandleAdminSetProjectParent_MissingParentIsBadRequest(t *testing.T) {
	srv, _, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly-marketing/parent", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing parent, got %d", rec.Code)
	}
}

func TestHandleAdminSetProjectParent_RejectsSelfLink(t *testing.T) {
	srv, _, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly/parent", operatorCookie(t, authSvc), "parent=workly"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (friendly error, not 500) for self-link, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "itself") {
		t.Fatalf("expected a friendly self-link message, got: %s", rec.Body.String())
	}
}

func TestHandleAdminSetProjectParent_RejectsParentIsChild(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.parents["workly-videos"] = "workly" // workly-videos is already a child

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly-marketing-videos/parent", operatorCookie(t, authSvc), "parent=workly-videos"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (friendly error, not 500) for parent-is-child, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminSetProjectParent_RejectsChildIsParent(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.parents["workly-marketing"] = "workly" // workly is already a parent

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/workly/parent", operatorCookie(t, authSvc), "parent=velion"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (friendly error, not 500) for child-is-parent, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── POST /admin/projects/{project}/parent/clear ─────────────────────────────

func TestHandleAdminClearProjectParent_OperatorGate(t *testing.T) {
	srv, _, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/workly-marketing/parent/clear", accountCookie(t, authSvc, "1", "alice"), ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator POST parent/clear: expected 403, got %d", rec.Code)
	}
}

func TestHandleAdminClearProjectParent_Success(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.parents["workly-marketing"] = "workly"

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/workly-marketing/parent/clear", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if _, stillLinked := store.parents["workly-marketing"]; stillLinked {
		t.Fatal("expected workly-marketing unlinked after clear")
	}
}

func TestHandleAdminClearProjectParent_IdempotentOnNeverLinked(t *testing.T) {
	srv, _, authSvc := newProjectLinksTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/never-linked/parent/clear", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (idempotent no-op), got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── Projects page: badges, sub-count, suggestion banner ────────────────────

// TestHandleAdminProjectsPage_RendersParentBadgeAndSubCount verifies the
// parent's childstrip (Admin projects redesign, issue #93): the child no
// longer renders as its own top-level card with a "sub-proyecto de {parent}"
// footer badge (that info now lives on the CHILD's own detail page instead,
// via ParentProject/ParentProjectURL — see project_detail_admin_test.go) —
// on the LIST page, the parent's card shows a childstrip naming the child
// and its own count.
func TestHandleAdminProjectsPage_RendersParentBadgeAndSubCount(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}
	store.parents["workly-marketing"] = "workly"

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	// i18n Slice 3: Spanish default ("1 sub-project" → "1 sub-proyecto").
	body := rec.Body.String()
	if !strings.Contains(body, `class="projcard parent"`) {
		t.Fatalf("expected workly to render as a parent card, got: %s", body)
	}
	if !strings.Contains(body, "1 sub-proyecto") {
		t.Fatalf("expected the childstrip header to show the sub-project count, got: %s", body)
	}
	if !strings.Contains(body, `class="crow"`) || !strings.Contains(body, "workly-marketing") {
		t.Fatalf("expected the child to render as a named .crow row in the childstrip, got: %s", body)
	}
}

func TestHandleAdminProjectsPage_RendersSuggestionBanner(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}
	// No existing link yet — SuggestProjectParents should surface one.

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	// i18n Slice 3: Spanish default ("Suggested: link" → "Sugerido: enlazar").
	body := rec.Body.String()
	if !strings.Contains(body, "Sugerido") {
		t.Fatalf("expected a suggestion banner, got: %s", body)
	}
	if !strings.Contains(body, "workly-marketing") || !strings.Contains(body, "workly") {
		t.Fatalf("expected the suggestion to name both projects, got: %s", body)
	}
}

// TestHandleAdminProjectsPage_DegradesGracefullyWithoutLinksCapability proves
// the Projects page still renders (no panic, no 5xx) when the underlying
// store does not implement projectLinksAdminStore — mirrors how SyncEnabled
// and the Slice 4b stats already default gracefully when their capability is
// absent.
func TestHandleAdminProjectsPage_DegradesGracefullyWithoutLinksCapability(t *testing.T) {
	store := newFakeTeamsStore() // NOT wrapped — no linking capability at all.
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
		t.Fatalf("expected 200 even without linking capability, got %d body=%q", rec.Code, rec.Body.String())
	}

	// The routes must not even be registered when the store lacks the
	// capability — hitting them falls through to the dashboard's own
	// catch-all mount (never a panic, never a 2xx that pretends to link).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, formRequest(http.MethodPost, "/admin/projects/proj1/parent", operatorCookie(t, authSvc), "parent=other"))
	if rec.Code == http.StatusOK {
		t.Fatalf("expected a non-200 when the store lacks linking capability, got %d", rec.Code)
	}
}
