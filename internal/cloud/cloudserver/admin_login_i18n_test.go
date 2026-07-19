package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/ui/i18n"
)

// langCookieRequest is cookieRequest plus an i18n.CookieName cookie, so a test
// can exercise the ES|EN switch on top of the operator/account session cookie
// every admin route already requires.
func langCookieRequest(method, url string, sessionCookie *http.Cookie, lang string) *http.Request {
	req := cookieRequest(method, url, sessionCookie, "")
	req.AddCookie(&http.Cookie{Name: i18n.CookieName, Value: lang})
	return req
}

// TestAdminUsersPage_RendersSpanishByDefault verifies the Users admin page
// (i18n Slice 3 — the last untranslated page before this slice) renders
// Spanish copy when no lang cookie is present, per the locked design
// (obs #1480): Spanish is the default locale everywhere, including the
// cloud Admin section, which used to be exempt because its handlers never
// passed through i18n.Middleware (see CloudServer.Handler's doc comment).
func TestAdminUsersPage_RendersSpanishByDefault(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@x.io"}}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Usuarios", "Nuevo usuario", `lang="es"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Spanish-default Users page to contain %q, body=%q", want, body)
		}
	}
}

// TestAdminUsersPage_RendersEnglishWithCookie verifies the SAME page switches
// to English when the caller carries the lang=en cookie — proving the i18n
// Slice 3 fix (wrapping CloudServer.Handler in i18n.Middleware) actually
// makes the Admin section's language cookie effective, not just its default.
func TestAdminUsersPage_RendersEnglishWithCookie(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@x.io"}}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, langCookieRequest(http.MethodGet, "/admin", operatorCookie(t, authSvc), "en"))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Users", "New user", `lang="en"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected English Users page (lang=en cookie) to contain %q, body=%q", want, body)
		}
	}
}

// TestAdminUsersPage_OperatorGatingUnchanged confirms the i18n rework never
// touched authorization: a non-operator account session is still 403,
// regardless of language cookie.
func TestAdminUsersPage_OperatorGatingUnchanged(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, langCookieRequest(http.MethodGet, "/admin", accountCookie(t, authSvc, "1", "alice"), "en"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator GET /admin: expected 403, got %d", rec.Code)
	}
}

// TestDashboardLoginPage_RendersSpanishByDefault verifies the cloud login
// page (raw-HTML, not templ — renderDashboardLoginPage in
// dashboard_mount.go) renders Spanish copy by default. This page is public
// (pre-authentication) and its handler used to be unreachable from
// i18n.Middleware entirely (registered directly on s.mux, bypassing both the
// shared dashboard's own middleware wrap AND — before this slice's fix —
// CloudServer.Handler's), so this test also guards the Handler()-level fix.
func TestDashboardLoginPage_RendersSpanishByDefault(t *testing.T) {
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	srv := New(&fakeStore{}, authSvc, 0)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Iniciar sesión", `lang="es"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Spanish-default login page to contain %q, body=%q", want, body)
		}
	}
}

// TestDashboardLoginPage_RendersEnglishWithCookie verifies the login page
// switches to English with the lang=en cookie.
func TestDashboardLoginPage_RendersEnglishWithCookie(t *testing.T) {
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	srv := New(&fakeStore{}, authSvc, 0)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: i18n.CookieName, Value: "en"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Sign In", `lang="en"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected English login page (lang=en cookie) to contain %q, body=%q", want, body)
		}
	}
}

// TestAdminProjectsPage_SuggestionBannerRendersSpanish verifies the Slice 5a
// suggestion banner's interpolated copy ("Suggested: link %s under %s")
// renders its Spanish translation ("Sugerido: enlazar ... bajo ...") by
// default, per the task's explicit example.
func TestAdminProjectsPage_SuggestionBannerRendersSpanish(t *testing.T) {
	srv, store, authSvc := newProjectLinksTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sugerido: enlazar") || !strings.Contains(body, "bajo") {
		t.Fatalf("expected Spanish suggestion banner copy, got: %s", body)
	}
}
