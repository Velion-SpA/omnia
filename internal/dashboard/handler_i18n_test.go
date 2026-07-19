package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServerViaHandler builds a test server through Server.Handler(),
// unlike newTestServerOnly (which registers routes on a bare mux, bypassing
// any wrapping Handler() applies). Used specifically to prove the i18n
// Middleware wrap point and the /lang/{lang} route are wired into the real
// entry point both the local `omnia dashboard` command (Start) and the cloud
// mount (dashboard.NewServerWithDataSource(...).Handler()) use.
func newTestServerViaHandler(t *testing.T, fe *fakeEngram) *httptest.Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	srv := NewServer(Config{
		Port:          0,
		EngramURL:     engServer.URL,
		EngramDataDir: t.TempDir(),
	}, logger)

	dashServer := httptest.NewServer(srv.Handler())
	t.Cleanup(dashServer.Close)
	return dashServer
}

// TestHandler_LangSwitchRoute_SetsCookieAndRedirects proves GET /lang/{lang}
// is registered on the real Handler() entry point (both mounts share it) and
// behaves per the locked design: set cookie, 303 to a sanitized next.
func TestHandler_LangSwitchRoute_SetsCookieAndRedirects(t *testing.T) {
	fe := newFakeEngram()
	dashServer := newTestServerViaHandler(t, fe)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(dashServer.URL + "/lang/en?next=/browse")
	if err != nil {
		t.Fatalf("GET /lang/en: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/browse" {
		t.Errorf("Location = %q, want %q", loc, "/browse")
	}
	var langCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "lang" {
			langCookie = c
		}
	}
	if langCookie == nil || langCookie.Value != "en" {
		t.Fatalf("expected lang=en cookie, got %+v", resp.Cookies())
	}
}

// TestHandler_MiddlewareDefaultsOverviewToSpanish proves the local dashboard
// mux (via Handler(), which Start() also uses) carries the Spanish default
// through to the rendered Overview page when no lang cookie is present.
func TestHandler_MiddlewareDefaultsOverviewToSpanish(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(1, "omnia"))
	dashServer := newTestServerViaHandler(t, fe)

	resp, err := http.Get(dashServer.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Resumen") {
		t.Errorf("expected Spanish nav label 'Resumen' by default, got body without it")
	}
	if !strings.Contains(bodyStr, `<html lang="es"`) {
		t.Errorf("expected <html lang=\"es\"> by default")
	}
}

// TestHandler_MiddlewareHonorsLangCookie_English proves a lang=en cookie
// flows through Middleware -> ctx -> the rendered page.
func TestHandler_MiddlewareHonorsLangCookie_English(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(1, "omnia"))
	dashServer := newTestServerViaHandler(t, fe)

	req, _ := http.NewRequest(http.MethodGet, dashServer.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, `<html lang="en"`) {
		t.Errorf("expected <html lang=\"en\"> with lang=en cookie, got body without it")
	}
}
