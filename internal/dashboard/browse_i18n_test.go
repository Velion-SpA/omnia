package dashboard

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestBrowse_RendersSpanishByDefault is the i18n Slice 2 counterpart of
// overview_i18n_test.go's TestOverview_RendersSpanishByDefault: the Browse
// page's own literals (not just the shared shell) render in Spanish with no
// lang cookie present, through the REAL entry point (Server.Handler(), which
// wraps i18n.Middleware).
func TestBrowse_RendersSpanishByDefault(t *testing.T) {
	fe := newFakeEngram() // no observations — exercises the empty-state path
	dashServer := newTestServerViaHandler(t, fe)

	resp, err := http.Get(dashServer.URL + "/browse")
	if err != nil {
		t.Fatalf("GET /browse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (page must not 500)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{
		"Explorar",       // page title/heading
		"Filtros",        // filter panel label
		"Buscar",         // search label/button
		"Sin resultados", // shared ui.MemoryFeed empty state (no obs match "project=" filter by default)
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected Spanish default Browse to contain %q, got body without it", want)
		}
	}
}

// TestBrowse_RendersEnglishWithCookie proves the same page switches fully to
// English when the lang=en cookie is present.
func TestBrowse_RendersEnglishWithCookie(t *testing.T) {
	fe := newFakeEngram() // no observations — exercises the empty-state path
	dashServer := newTestServerViaHandler(t, fe)

	req, _ := http.NewRequest(http.MethodGet, dashServer.URL+"/browse", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /browse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (page must not 500)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{
		"Browse",
		"Filters",
		"Search",
		"No results",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected English Browse (lang=en cookie) to contain %q, got body without it", want)
		}
	}
	if strings.Contains(bodyStr, "Filtros") {
		t.Error("English Browse should not contain Spanish 'Filtros'")
	}
}
