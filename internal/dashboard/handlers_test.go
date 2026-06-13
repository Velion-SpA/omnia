package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func newSlogLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- Test helpers ---

// fakeEngram builds a test server that mimics the Engram HTTP API.
type fakeEngram struct {
	observations map[int]Observation
	mux          *http.ServeMux
}

func newFakeEngram() *fakeEngram {
	fe := &fakeEngram{
		observations: map[int]Observation{},
		mux:          http.NewServeMux(),
	}
	fe.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})
	fe.mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, `{"error":"q parameter is required"}`, http.StatusBadRequest)
			return
		}
		var results []Observation
		for _, o := range fe.observations {
			results = append(results, o)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results) //nolint:errcheck
	})
	fe.mux.HandleFunc("/observations/", func(w http.ResponseWriter, r *http.Request) {
		// Extract ID from path: /observations/{id}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/observations/"), "?")
		idStr := parts[0]
		var id int
		fmt.Sscanf(idStr, "%d", &id)

		switch r.Method {
		case http.MethodGet:
			o, ok := fe.observations[id]
			if !ok {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(o) //nolint:errcheck

		case http.MethodPatch:
			o, ok := fe.observations[id]
			if !ok {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			if v, ok := body["title"]; ok {
				o.Title = v
			}
			if v, ok := body["content"]; ok {
				o.Content = v
			}
			if v, ok := body["type"]; ok {
				o.Type = v
			}
			fe.observations[id] = o
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			delete(fe.observations, id)
			w.WriteHeader(http.StatusOK)
		}
	})
	return fe
}

func (fe *fakeEngram) add(o Observation) {
	fe.observations[o.ID] = o
}

func (fe *fakeEngram) server() *httptest.Server {
	return httptest.NewServer(fe.mux)
}

// sampleObs builds a test observation with an ingested omnia-meta block.
func sampleObs(id int, project string) Observation {
	content := fmt.Sprintf("## PR #%d\n\nThis is a pull request.\n\n```omnia-meta\nschema_version: 1\nsource: github\nkind: pull_request\nlayer: ingested\nproject: %s\nrepo: owner/repo\nstatus: merged\nauthor: alice\nparticipants: [\"alice\",\"bob\"]\nurl: https://github.com/owner/repo/pull/%d\n```\n", id, project, id)
	return Observation{
		ID:        id,
		Type:      "architecture",
		Title:     fmt.Sprintf("feat: add feature %d", id),
		Content:   content,
		Project:   project,
		UpdatedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
		CreatedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
	}
}

// curatedObs builds a test observation WITHOUT an omnia-meta block.
func curatedObs(id int, project string) Observation {
	return Observation{
		ID:        id,
		Type:      "decision",
		Title:     fmt.Sprintf("Decision: use approach %d", id),
		Content:   "We decided to use approach X because of Y.",
		Project:   project,
		UpdatedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
		CreatedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
	}
}

// --- Tests ---

func TestHandleOverview_Renders(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(1, "omnia"))
	fe.add(curatedObs(2, "omnia"))

	dashServer, _ := newTestServerSimple(t, fe)

	resp, err := http.Get(dashServer.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Omnia") {
		t.Error("expected 'Omnia' in overview response")
	}
	if !strings.Contains(bodyStr, "Overview") {
		t.Error("expected 'Overview' in response")
	}
}

func TestHandleBrowse_ListsObservations(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(10, "omnia"))
	fe.add(curatedObs(11, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/browse?project=omnia")
	if err != nil {
		t.Fatalf("GET /browse: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "feat: add feature 10") {
		t.Error("expected ingested obs title in browse response")
	}
}

func TestHandleDetail_RendersMetaPanel(t *testing.T) {
	fe := newFakeEngram()
	o := sampleObs(42, "omnia")
	fe.add(o)

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(fmt.Sprintf("%s/detail/42", dashServer.URL))
	if err != nil {
		t.Fatalf("GET /detail/42: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Meta panel fields should be present.
	if !strings.Contains(bodyStr, "github") {
		t.Error("expected source 'github' in meta panel")
	}
	if !strings.Contains(bodyStr, "owner/repo") {
		t.Error("expected repo in meta panel")
	}
	if !strings.Contains(bodyStr, "alice") {
		t.Error("expected author in meta panel")
	}
	if !strings.Contains(bodyStr, "ingested") {
		t.Error("expected 'ingested' badge")
	}
	// The raw omnia-meta block should NOT appear in the rendered content.
	if strings.Contains(bodyStr, "schema_version: 1") {
		t.Error("raw meta block should be stripped from content display")
	}
}

func TestHandleDetail_CuratedObsNometa(t *testing.T) {
	fe := newFakeEngram()
	o := curatedObs(99, "omnia")
	fe.add(o)

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(fmt.Sprintf("%s/detail/99", dashServer.URL))
	if err != nil {
		t.Fatalf("GET /detail/99: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "curated") {
		t.Error("expected 'curated' badge for observation without meta block")
	}
}

func TestHandleEditForm_Renders(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(5, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(fmt.Sprintf("%s/api/obs/5/edit-form", dashServer.URL))
	if err != nil {
		t.Fatalf("GET edit-form: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "feat: add feature 5") {
		t.Error("expected title in edit form")
	}
}

func TestHandlePatch_UpdatesObservation(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(7, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	form := url.Values{}
	form.Set("title", "updated title")
	form.Set("type", "decision")
	form.Set("content", "updated content")

	req, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/obs/7", dashServer.URL), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify the fake engram recorded the change.
	updated := fe.observations[7]
	if updated.Title != "updated title" {
		t.Errorf("expected title 'updated title', got %q", updated.Title)
	}
}

func TestHandleDelete_SoftAndHard(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(20, "omnia"))
	fe.add(sampleObs(21, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	// Soft delete.
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/obs/20?hard=false", dashServer.URL), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE soft: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("soft delete: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Soft deleted") {
		t.Error("expected 'Soft deleted' in response")
	}

	// Hard delete.
	req2, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/obs/21?hard=true", dashServer.URL), nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("DELETE hard: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("hard delete: expected 200, got %d", resp2.StatusCode)
	}
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), "Permanently deleted") {
		t.Error("expected 'Permanently deleted' in response")
	}

	// Both should be removed from fake engram.
	if _, ok := fe.observations[20]; ok {
		t.Error("soft-deleted obs should be removed from fake (no distinction in test double)")
	}
	if _, ok := fe.observations[21]; ok {
		t.Error("hard-deleted obs should be removed from fake")
	}
}

func TestHandleDeleteConfirm_RequiresExplicitConfirm(t *testing.T) {
	fe := newFakeEngram()
	fe.add(sampleObs(30, "omnia"))

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(fmt.Sprintf("%s/api/obs/30/delete-confirm", dashServer.URL))
	if err != nil {
		t.Fatalf("GET delete-confirm: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	// Confirm step should show both soft and hard delete buttons.
	if !strings.Contains(bodyStr, "Soft delete") {
		t.Error("expected 'Soft delete' option in confirm step")
	}
	if !strings.Contains(bodyStr, "Hard delete") {
		t.Error("expected 'Hard delete' option in confirm step")
	}
}

func TestMetaMapping_IngestedVsCurated(t *testing.T) {
	ingested := sampleObs(1, "omnia")
	curated := curatedObs(2, "omnia")

	vi := enrichObs(ingested)
	vc := enrichObs(curated)

	if !vi.HasMeta {
		t.Error("sampleObs should have a valid meta block")
	}
	if vi.Meta.Source != "github" {
		t.Errorf("expected source 'github', got %q", vi.Meta.Source)
	}
	if vi.Meta.Kind != "pull_request" {
		t.Errorf("expected kind 'pull_request', got %q", vi.Meta.Kind)
	}
	if vi.Meta.Author != "alice" {
		t.Errorf("expected author 'alice', got %q", vi.Meta.Author)
	}
	if len(vi.Meta.Participants) != 2 {
		t.Errorf("expected 2 participants, got %d", len(vi.Meta.Participants))
	}

	if vc.HasMeta {
		t.Error("curatedObs should NOT have a valid meta block")
	}
}

func TestSyncStatus_GraceWhenMissing(t *testing.T) {
	// loadSyncStatus should not panic when files are absent.
	// It degrades gracefully.
	status := loadSyncStatus()
	// Just ensure it returns (no panic), Missing may or may not be set.
	_ = status
}

func TestStripMetaBlock(t *testing.T) {
	content := "## Title\n\nBody content here.\n\n```omnia-meta\nschema_version: 1\nsource: github\nkind: issue\n```\n"
	result := stripMetaBlock(content)
	if strings.Contains(result, "schema_version") {
		t.Error("stripMetaBlock should remove the omnia-meta block")
	}
	if !strings.Contains(result, "Body content here") {
		t.Error("stripMetaBlock should preserve the human-readable body")
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		ts   string
		want string
	}{
		{"", "unknown"},
		{"bad-format", "bad-format"},
	}
	for _, c := range cases {
		got := formatAge(c.ts)
		if got != c.want {
			t.Errorf("formatAge(%q) = %q, want %q", c.ts, got, c.want)
		}
	}

	// Recent timestamp (UTC) should return "just now".
	recent := time.Now().UTC().Add(-5 * time.Second).Format("2006-01-02 15:04:05")
	got := formatAge(recent)
	if got != "just now" {
		t.Errorf("formatAge(recent) = %q, want 'just now'", got)
	}

	// Old timestamp should return a date string (not "just now" or "unknown").
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	got = formatAge(old)
	if got == "just now" || got == "unknown" {
		t.Errorf("formatAge(old) = %q, expected a date string", got)
	}
}

// --- Constructor helpers for tests ---

func newTestServerOnly(t *testing.T, fe *fakeEngram) *httptest.Server {
	t.Helper()
	engServer := fe.server()
	t.Cleanup(engServer.Close)

	logger := newSlogLogger()
	srv := NewServer(Config{
		Port:      0,
		EngramURL: engServer.URL,
	}, logger)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	dashServer := httptest.NewServer(mux)
	t.Cleanup(dashServer.Close)
	return dashServer
}

func newTestServerSimple(t *testing.T, fe *fakeEngram) (*httptest.Server, *fakeEngram) {
	t.Helper()
	return newTestServerOnly(t, fe), fe
}
