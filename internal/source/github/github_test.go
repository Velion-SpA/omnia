package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	github "github.com/velion/omnia/internal/source/github"
)

func TestFetchRepoFromFixture(t *testing.T) {
	// Load fixtures.
	issueData, err := os.ReadFile("../../testdata/github_issue.json")
	if err != nil {
		t.Fatal(err)
	}
	commentsData, err := os.ReadFile("../../testdata/github_comments.json")
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

	src := github.NewWithBaseURL([]string{"velion/api"}, "omnia", "", nil, srv.URL)
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

func TestPRDetection(t *testing.T) {
	prData, err := os.ReadFile("../../testdata/github_pr.json")
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

	src := github.NewWithBaseURL([]string{"velion/api"}, "omnia", "", nil, srv.URL)
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
