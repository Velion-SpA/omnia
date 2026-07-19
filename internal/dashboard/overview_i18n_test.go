package dashboard

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestOverview_RendersSpanishByDefault is the end-to-end proof requested by
// the i18n Slice 1 design: the Overview page's shell + own literals render
// in Spanish with no lang cookie present.
func TestOverview_RendersSpanishByDefault(t *testing.T) {
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

	for _, want := range []string{
		"Resumen",               // nav label + page title
		"Memorias Totales",      // tile-label
		"Proyectos",             // tile-label / panel-title
		"Última Sincronización", // tile-label
		"Bases de Conocimiento", // eyebrow
		"ordenado por cantidad", // panel-badge
		"Desglose",              // eyebrow
		"Tipos de Memoria",      // panel-title
		"Ingesta Reciente",      // eyebrow
		"Feed en Vivo",          // panel-title
		"En vivo",               // live-badge
		"Conectado",             // eyebrow
		"Fuentes",               // panel-title
		"PRs · commits · revisiones",
		"resúmenes · hilos · menciones",
		"sesiones · fixes · decisiones",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected Spanish default Overview to contain %q, got body without it", want)
		}
	}
}

// TestOverview_RendersEnglishWithCookie proves the same page switches fully
// to English when the lang=en cookie is present.
func TestOverview_RendersEnglishWithCookie(t *testing.T) {
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

	for _, want := range []string{
		"Overview",
		"Total Memories",
		"Projects",
		"Last Sync",
		"Knowledge Bases",
		"sorted by count",
		"Breakdown",
		"Memory Types",
		"Recent Ingest",
		"Live Feed",
		"Live",
		"Connected",
		"Sources",
		"PRs · commits · reviews",
		"digests · threads · mentions",
		"sessions · fixes · decisions",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected English Overview (lang=en cookie) to contain %q, got body without it", want)
		}
	}
	if strings.Contains(bodyStr, "Memorias Totales") {
		t.Error("English Overview should not contain Spanish 'Memorias Totales'")
	}
}

// TestOverview_EmptyProjectsMessage_Translated covers the split-literal case
// (prefix + <code>omnia sync</code> + suffix) around the empty-state message.
func TestOverview_EmptyProjectsMessage_Translated(t *testing.T) {
	fe := newFakeEngram() // no observations -> empty projects list
	dashServer := newTestServerViaHandler(t, fe)

	resp, err := http.Get(dashServer.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "No se encontraron proyectos.") || !strings.Contains(bodyStr, "<code>omnia sync</code>") || !strings.Contains(bodyStr, "primero.") {
		t.Errorf("expected Spanish empty-projects message with embedded <code>omnia sync</code>, got:\n%s", bodyStr)
	}
}
