package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServerWithResolverViaHandler mirrors newTestServerWithResolver
// (subprojects_test.go) but builds through Server.Handler() instead of a
// bare mux — required for i18n Slice 2's cookie-driven render tests, since
// newTestServerWithResolver bypasses i18n.Middleware entirely (same
// distinction handler_i18n_test.go's newTestServerViaHandler documents for
// the no-resolver case).
func newTestServerWithResolverViaHandler(t *testing.T, fe *fakeEngram, resolver SubProjectResolver) *httptest.Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	srv := NewServer(Config{
		Port:          0,
		EngramURL:     engServer.URL,
		EngramDataDir: t.TempDir(),
	}, logger, WithSubProjectResolver(resolver))

	dashServer := httptest.NewServer(srv.Handler())
	t.Cleanup(dashServer.Close)
	return dashServer
}

// TestProjectDetail_RendersSpanishByDefault is the i18n Slice 2 counterpart
// of overview_i18n_test.go's TestOverview_RendersSpanishByDefault, exercised
// through the REAL entry point (Server.Handler(), which wraps
// i18n.Middleware) with a group-parent project so the "Sub-projects" /
// "View all in Browse" strings called out by the task both render.
func TestProjectDetail_RendersSpanishByDefault(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(1, "workly"))
	resolver := &fakeSubProjectResolver{
		children: map[string][]string{"workly": {"workly-marketing"}},
	}
	dashServer := newTestServerWithResolverViaHandler(t, fe, resolver)

	resp, err := http.Get(dashServer.URL + "/project/workly")
	if err != nil {
		t.Fatalf("GET /project/workly: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (page must not 500)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{
		"Memorias",             // stat label
		"Fuentes",              // stat label (reuses overview.sources)
		"Última actividad",     // stat label
		"Sub-proyectos",        // projectDetail.subProjects
		"Ver todo en Explorar", // projectDetail.viewAllBrowse
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected Spanish default project detail to contain %q, got body without it", want)
		}
	}
}

// TestProjectDetail_RendersEnglishWithCookie proves the same page switches
// fully to English when the lang=en cookie is present.
func TestProjectDetail_RendersEnglishWithCookie(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(1, "workly"))
	resolver := &fakeSubProjectResolver{
		children: map[string][]string{"workly": {"workly-marketing"}},
	}
	dashServer := newTestServerWithResolverViaHandler(t, fe, resolver)

	req, _ := http.NewRequest(http.MethodGet, dashServer.URL+"/project/workly", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /project/workly: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (page must not 500)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{
		"Memories",
		"Sources",
		"Last activity",
		"Sub-projects",
		"View all in Browse",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected English project detail (lang=en cookie) to contain %q, got body without it", want)
		}
	}
	if strings.Contains(bodyStr, "Sub-proyectos") {
		t.Error("English project detail should not contain Spanish 'Sub-proyectos'")
	}
}
