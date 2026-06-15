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

func TestHandleDelete_SoftAudited(t *testing.T) {
	fe := newFakeEngram()
	var deletedURL string
	fe.mux.HandleFunc("/observations/55", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedURL = r.URL.String()
			delete(fe.observations, 55)
			w.WriteHeader(http.StatusOK)
			return
		}
		// GET
		o, ok := fe.observations[55]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(o)
	})
	fe.add(sampleObs(55, "omnia"))

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	dashServer := newTestServerOnly(t, fe)
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/obs/55", dashServer.URL), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if strings.Contains(deletedURL, "hard=true") {
		t.Errorf("soft delete should NOT include hard=true, got URL: %s", deletedURL)
	}
}

func TestHandleDelete_HardAudited(t *testing.T) {
	fe := newFakeEngram()
	var deletedURL string
	fe.mux.HandleFunc("/observations/56", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedURL = r.URL.String()
			delete(fe.observations, 56)
			w.WriteHeader(http.StatusOK)
			return
		}
		o, ok := fe.observations[56]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(o)
	})
	fe.add(sampleObs(56, "omnia"))

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	dashServer := newTestServerOnly(t, fe)
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/obs/56?hard=true", dashServer.URL), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE hard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(deletedURL, "hard=true") {
		t.Errorf("hard delete should include hard=true, got URL: %s", deletedURL)
	}
}

func TestActorResolution(t *testing.T) {
	fe := newFakeEngram()
	engServer := fe.server()
	t.Cleanup(engServer.Close)
	logger := newSlogLogger()

	srv := NewServer(Config{
		Port:      0,
		EngramURL: engServer.URL,
		Actor:     "config-actor",
	}, logger)

	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("X-Omnia-Actor", "header-actor")
	got := srv.resolveActor(req)
	if got != "header-actor" {
		t.Errorf("expected header-actor, got %q", got)
	}

	req2, _ := http.NewRequest("GET", "/", nil)
	got2 := srv.resolveActor(req2)
	if got2 != "config-actor" {
		t.Errorf("expected config-actor, got %q", got2)
	}

	srv3 := NewServer(Config{Port: 0, EngramURL: engServer.URL, Actor: ""}, logger)
	origUser := os.Getenv("USER")
	os.Setenv("USER", "env-user")
	defer os.Setenv("USER", origUser)
	req3, _ := http.NewRequest("GET", "/", nil)
	got3 := srv3.resolveActor(req3)
	if got3 != "env-user" {
		t.Errorf("expected env-user, got %q", got3)
	}
}

func TestActivityPage_Renders(t *testing.T) {
	fe := newFakeEngram()
	dashServer := newTestServerOnly(t, fe)

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	resp, err := http.Get(dashServer.URL + "/activity")
	if err != nil {
		t.Fatalf("GET /activity: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Activity") {
		t.Error("expected 'Activity' in activity page")
	}
}

// --- Type filter and category tests ---

func TestHandleBrowse_TypeFilter_OnlyMatchingTypeReturned(t *testing.T) {
	fe := newFakeEngram()

	arch := sampleObs(100, "omnia")
	arch.Type = "architecture"
	fe.add(arch)

	dec := sampleObs(101, "omnia")
	dec.Type = "decision"
	fe.add(dec)

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/browse?project=omnia&type=architecture")
	if err != nil {
		t.Fatalf("GET /browse?type=architecture: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "feat: add feature 100") {
		t.Error("expected architecture obs in results when type=architecture")
	}
	if strings.Contains(bodyStr, "feat: add feature 101") {
		t.Error("decision obs should be filtered out when type=architecture")
	}
}

func TestHandleBrowse_TypeFilter_CombinedWithProject(t *testing.T) {
	fe := newFakeEngram()

	o1 := sampleObs(200, "omnia")
	o1.Type = "bugfix"
	fe.add(o1)

	o2 := sampleObs(201, "omnia")
	o2.Type = "bugfix"
	fe.add(o2)

	o3 := sampleObs(202, "omnia")
	o3.Type = "pattern"
	fe.add(o3)

	dashServer := newTestServerOnly(t, fe)

	resp, err := http.Get(dashServer.URL + "/browse?project=omnia&type=bugfix")
	if err != nil {
		t.Fatalf("GET /browse?project=omnia&type=bugfix: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "feat: add feature 200") {
		t.Error("expected obs 200 in results")
	}
	if !strings.Contains(bodyStr, "feat: add feature 201") {
		t.Error("expected obs 201 in results")
	}
	if strings.Contains(bodyStr, "feat: add feature 202") {
		t.Error("pattern obs should be excluded when type=bugfix")
	}
}

func TestDistinctTypes_SortedAndDeduped(t *testing.T) {
	views := []ObsView{
		{Obs: Observation{Type: "decision"}},
		{Obs: Observation{Type: "architecture"}},
		{Obs: Observation{Type: "decision"}}, // duplicate
		{Obs: Observation{Type: ""}},          // empty — must be excluded
		{Obs: Observation{Type: "bugfix"}},
	}
	got := distinctTypes(views)
	want := []string{"architecture", "bugfix", "decision"}
	if len(got) != len(want) {
		t.Fatalf("distinctTypes: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("index %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestDistinctTypes_EmptyViews(t *testing.T) {
	got := distinctTypes(nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice for nil views, got %v", got)
	}
}

func TestComputeProjectStats_ByType(t *testing.T) {
	views := []ObsView{
		{Obs: Observation{Type: "architecture"}, HasMeta: false},
		{Obs: Observation{Type: "architecture"}, HasMeta: false},
		{Obs: Observation{Type: "decision"}, HasMeta: false},
		{Obs: Observation{Type: ""}, HasMeta: false}, // empty type — not counted
	}
	stats := computeProjectStats("omnia", views)
	if stats.ByType["architecture"] != 2 {
		t.Errorf("expected ByType[architecture]=2, got %d", stats.ByType["architecture"])
	}
	if stats.ByType["decision"] != 1 {
		t.Errorf("expected ByType[decision]=1, got %d", stats.ByType["decision"])
	}
	if _, ok := stats.ByType[""]; ok {
		t.Error("empty type should not appear in ByType map")
	}
}

func TestSortedTypeCounts_ByCountDescThenName(t *testing.T) {
	m := map[string]int{
		"bugfix":       1,
		"architecture": 3,
		"decision":     3,
		"pattern":      2,
	}
	got := sortedTypeCounts(m)
	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(got))
	}
	// Tied at 3: architecture < decision alphabetically → architecture first
	if got[0].Name != "architecture" || got[0].Count != 3 {
		t.Errorf("got[0] = %+v, want {architecture 3}", got[0])
	}
	if got[1].Name != "decision" || got[1].Count != 3 {
		t.Errorf("got[1] = %+v, want {decision 3}", got[1])
	}
	if got[2].Name != "pattern" || got[2].Count != 2 {
		t.Errorf("got[2] = %+v, want {pattern 2}", got[2])
	}
	if got[3].Name != "bugfix" || got[3].Count != 1 {
		t.Errorf("got[3] = %+v, want {bugfix 1}", got[3])
	}
}

func TestSortedTypeCounts_EmptyMap(t *testing.T) {
	got := sortedTypeCounts(map[string]int{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

// --- knownProjects tests ---

func TestKnownProjects_AlwaysIncludesOmnia(t *testing.T) {
	got := knownProjects(SyncStatus{}, Config{})
	if len(got) == 0 {
		t.Fatal("expected at least one project")
	}
	found := false
	for _, p := range got {
		if p == "omnia" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'omnia' in result, got %v", got)
	}
}

func TestKnownProjects_UnionWithConfigProjects(t *testing.T) {
	cfg := Config{
		Projects: []string{"workly", "trackly", "homelab"},
	}
	got := knownProjects(SyncStatus{}, cfg)

	want := map[string]bool{
		"omnia":   true,
		"workly":  true,
		"trackly": true,
		"homelab": true,
	}
	for _, p := range got {
		delete(want, p)
	}
	if len(want) > 0 {
		t.Errorf("missing projects in result: %v (got: %v)", want, got)
	}
}

func TestKnownProjects_UnionWithRouteTargets(t *testing.T) {
	cfg := Config{
		Routes: map[string]string{
			"github/arratiabenjamin/saluvita":  "saluvita",
			"github/arratiabenjamin/velion-web": "velion-web",
		},
	}
	got := knownProjects(SyncStatus{}, cfg)

	want := map[string]bool{
		"omnia":     true,
		"saluvita":  true,
		"velion-web": true,
	}
	for _, p := range got {
		delete(want, p)
	}
	if len(want) > 0 {
		t.Errorf("missing route-derived projects: %v (got: %v)", want, got)
	}
}

func TestKnownProjects_DeduplicatesAndSorts(t *testing.T) {
	cfg := Config{
		Projects: []string{"omnia", "workly", "omnia", "trackly"},
		Routes: map[string]string{
			"github/x/workly": "workly", // duplicate of Projects entry
		},
	}
	got := knownProjects(SyncStatus{}, cfg)

	// No duplicates.
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	for p, count := range seen {
		if count > 1 {
			t.Errorf("project %q appears %d times, want 1", p, count)
		}
	}

	// Sorted.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("result not sorted at index %d: %v", i, got)
		}
	}
}

func TestKnownProjects_DropsEmptyStrings(t *testing.T) {
	cfg := Config{
		Projects: []string{"", "workly", "  ", ""},
		Routes: map[string]string{
			"github/x/y": "",
		},
	}
	got := knownProjects(SyncStatus{}, cfg)
	for _, p := range got {
		if p == "" || strings.TrimSpace(p) == "" {
			t.Errorf("empty/blank project in result: %q", p)
		}
	}
}

func TestKnownProjects_EmptyConfigYieldsOmnia(t *testing.T) {
	got := knownProjects(SyncStatus{}, Config{})
	if len(got) != 1 || got[0] != "omnia" {
		t.Errorf("empty config: expected [omnia], got %v", got)
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
		// Point to an empty temp dir so engramdb.Open fails fast and falls back
		// to the fake Engram HTTP server — tests must not touch the real DB.
		EngramDataDir: t.TempDir(),
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
