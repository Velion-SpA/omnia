package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	github "github.com/Velion-SpA/omnia/internal/source/github"
)

// stubRouter satisfies projectRouter by returning a fixed project string.
type stubRouter struct {
	project string
}

func (r *stubRouter) ResolveGitHub(_ string) string { return r.project }

// stubState is a minimal StateStore for tests.
type stubState struct {
	cursors map[string]string
}

func newStubState(cursors map[string]string) *stubState {
	if cursors == nil {
		cursors = make(map[string]string)
	}
	return &stubState{cursors: cursors}
}

func (s *stubState) GetCursor(source, key string) (string, bool) {
	v, ok := s.cursors[source+":"+key]
	return v, ok
}

func (s *stubState) SetCursor(source, key, value string) error {
	s.cursors[source+":"+key] = value
	return nil
}

func (s *stubState) Flush() error { return nil }

func TestFetchRepoFromFixture(t *testing.T) {
	// Load fixtures.
	issueData, err := os.ReadFile("../../../testdata/github_issue.json")
	if err != nil {
		t.Fatal(err)
	}
	commentsData, err := os.ReadFile("../../../testdata/github_comments.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/issues/42/comments"):
			w.Header().Set("Content-Type", "application/json")
			w.Write(commentsData)
		case strings.Contains(r.URL.Path, "/issues"):
			// Return a list containing the single fixture issue.
			var single map[string]interface{}
			json.Unmarshal(issueData, &single)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{single})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := github.NewWithBaseURL([]string{"velion/api"}, &stubRouter{"omnia"}, "", nil, srv.URL)
	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one item")
	}
	item := items[0]
	if item.Type != "github-issue" {
		t.Errorf("type = %q, want github-issue", item.Type)
	}
	if !strings.Contains(item.Title, "#42") {
		t.Errorf("title %q should contain #42", item.Title)
	}
	if !strings.Contains(item.Content, "Keywords:") {
		t.Errorf("content should contain Keywords section")
	}
	if !strings.Contains(item.TopicKey, "velion-api/issue-42") {
		t.Errorf("topic_key %q should contain velion-api/issue-42", item.TopicKey)
	}
}

func TestCommitCollection(t *testing.T) {
	commits := []map[string]any{
		{
			"sha":      "abc123def456789",
			"html_url": "https://github.com/velion/api/commit/abc123def456789",
			"commit": map[string]any{
				"message": "feat: add thing\n\nlonger body here",
				"author":  map[string]any{"name": "Benja", "email": "b@x.com", "date": "2026-06-10T12:00:00Z"},
			},
			"author": map[string]any{"login": "arratiabenjamin"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits"):
			json.NewEncoder(w).Encode(commits)
		case strings.Contains(r.URL.Path, "/issues"):
			w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := github.NewWithBaseURL([]string{"velion/api"}, &stubRouter{"omnia"}, "", newStubState(nil), srv.URL)
	src.SetIncludeCommits(true)
	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	found := false
	for _, it := range items {
		if it.Type != "github-commit" {
			continue
		}
		found = true
		if !strings.Contains(it.Title, "abc123de") {
			t.Errorf("title %q should contain short sha", it.Title)
		}
		if !strings.Contains(it.TopicKey, "commit-abc123def456789") {
			t.Errorf("topic_key %q should contain commit sha", it.TopicKey)
		}
		if !strings.Contains(it.Content, "arratiabenjamin") {
			t.Errorf("content should mention author login")
		}
		if !strings.Contains(it.Content, "author: arratiabenjamin") {
			t.Errorf("meta block should carry author login")
		}
	}
	if !found {
		t.Fatal("expected a github-commit item")
	}
}

// TestCommitsDisabledByDefault verifies the commits endpoint is never hit unless
// IncludeCommits is enabled — protects the existing issues/PRs-only behaviour.
func TestCommitsDisabledByDefault(t *testing.T) {
	commitHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits"):
			commitHit = true
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := github.NewWithBaseURL([]string{"velion/api"}, &stubRouter{"omnia"}, "", newStubState(nil), srv.URL)
	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if commitHit {
		t.Error("commits endpoint must not be hit when IncludeCommits is false")
	}
}

func TestPRDetection(t *testing.T) {
	prData, err := os.ReadFile("../../../testdata/github_pr.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			var single map[string]interface{}
			json.Unmarshal(prData, &single)
			json.NewEncoder(w).Encode([]interface{}{single})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := github.NewWithBaseURL([]string{"velion/api"}, &stubRouter{"omnia"}, "", nil, srv.URL)
	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one item")
	}
	if items[0].Type != "github-pr" {
		t.Errorf("type = %q, want github-pr", items[0].Type)
	}
}

// TestCursorConsumption verifies that a stored cursor is used as the lower bound
// for the next run (C1). The stub server records the "since" query param; we
// confirm it is cursor-10m (overlap window to cover GitHub list-index propagation lag).
func TestCursorConsumption(t *testing.T) {
	storedCursor := "2024-06-01T00:00:00Z"
	cursorTime, _ := time.Parse(time.RFC3339, storedCursor)
	wantSince := cursorTime.Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var receivedSince string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			receivedSince = r.URL.Query().Get("since")
			// Return one issue updated at the cursor time so a new cursor is stored.
			issue := map[string]interface{}{
				"number":     7,
				"title":      "cursor test issue",
				"state":      "open",
				"html_url":   "https://github.com/acme/repo/issues/7",
				"body":       "body",
				"user":       map[string]string{"login": "alice"},
				"labels":     []interface{}{},
				"assignees":  []interface{}{},
				"created_at": storedCursor,
				"updated_at": storedCursor,
			}
			json.NewEncoder(w).Encode([]interface{}{issue})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newStubState(map[string]string{"github:acme/repo": storedCursor})
	src := github.NewWithBaseURL([]string{"acme/repo"}, &stubRouter{"omnia"}, "", st, srv.URL)

	_, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if receivedSince != wantSince {
		t.Errorf("server received since=%q, want cursor-10m %q", receivedSince, wantSince)
	}
}

