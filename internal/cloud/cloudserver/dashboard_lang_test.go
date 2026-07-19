package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDashboardLangSwitch_PublicNoAuth proves GET /lang/{lang} is reachable
// on the cloud mount WITHOUT an authenticated dashboard session — the same
// public-path allowlist treatment as /login and /static — so the header
// toggle also works from the (unauthenticated) login page.
func TestDashboardLangSwitch_PublicNoAuth(t *testing.T) {
	srv := New(&fakeStore{}, fakeAuth{err: errUnauthorized}, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lang/en?next=/browse", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other for public /lang switch, got %d body=%q", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/browse" {
		t.Fatalf("Location = %q, want %q", loc, "/browse")
	}
	var langCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "lang" {
			langCookie = c
		}
	}
	if langCookie == nil || langCookie.Value != "en" {
		t.Fatalf("expected lang=en cookie, got %+v", rec.Result().Cookies())
	}
}

// TestDashboardLangSwitch_DoesNotLeakIntoAuthedRoutes is a narrow guard: the
// /lang/ bypass in the gate must not accidentally start matching non-/lang/
// paths (e.g. via an overly broad prefix check) and skip auth for them.
func TestDashboardLangSwitch_DoesNotLeakIntoAuthedRoutes(t *testing.T) {
	srv := New(&fakeStore{}, fakeAuth{err: errUnauthorized}, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/langfoo", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected /langfoo to still require auth (redirect to login), got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/login") {
		t.Fatalf("expected redirect to /login for /langfoo, got %q", loc)
	}
}

var errUnauthorized = &authError{"unauthorized"}

type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

// TestDashboardAuthedRequest_CarriesLangContext proves i18n.Middleware's ctx
// values (lang, path) reach the AUTHENTICATED dashboard render path too — not
// just the /lang bypass — since dashboardGate's default case sets its own
// ctx values (WithScope/WithAdminNav/WithUserIdentity) BEFORE calling
// dashHandler.ServeHTTP, and Middleware must layer lang on top without
// clobbering them. Uses insecure no-auth mode (s.auth == nil) so any request
// is authorized, isolating the assertion to the i18n wiring itself.
func TestDashboardAuthedRequest_CarriesLangContext(t *testing.T) {
	srv := New(&fakeStore{}, nil, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for authorized (insecure no-auth mode) request, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<html lang="en"`) {
		t.Errorf("expected <html lang=\"en\"> on the authenticated dashboard render with lang=en cookie, got body without it")
	}
}
