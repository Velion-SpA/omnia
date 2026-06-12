package main_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/core"
	github "github.com/velion/omnia/internal/source/github"
)

// captureSink captures items instead of writing to Engram.
type captureSink struct {
	items []core.Item
}

func (c *captureSink) Write(_ context.Context, item core.Item) error {
	c.items = append(c.items, item)
	return nil
}

func (c *captureSink) Health(_ context.Context) error { return nil }

// noopState satisfies core.StateStore without any persistence.
type noopState struct{}

func (n *noopState) GetCursor(source, key string) (string, bool) { return "", false }
func (n *noopState) SetCursor(source, key, value string) error   { return nil }
func (n *noopState) Flush() error                                 { return nil }

func TestE2EDryRunPipeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			issue := map[string]interface{}{
				"number":     1,
				"title":      "Initial issue",
				"state":      "open",
				"html_url":   "https://github.com/acme/service/issues/1",
				"body":       "First issue body.",
				"user":       map[string]string{"login": "alice"},
				"labels":     []interface{}{map[string]string{"name": "bug"}},
				"assignees":  []interface{}{},
				"created_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
				"updated_at": time.Now().Format(time.RFC3339),
			}
			json.NewEncoder(w).Encode([]interface{}{issue})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := &captureSink{}
	src := github.NewWithBaseURL([]string{"acme/service"}, "omnia", "", nil, srv.URL)

	pipeline := core.NewPipeline([]core.Source{src}, sink, &noopState{}, false, nil)
	if err := pipeline.Run(context.Background(), core.RunOptions{}); err != nil {
		t.Fatalf("pipeline run failed: %v", err)
	}

	if len(sink.items) == 0 {
		t.Fatal("expected at least one item written")
	}
	item := sink.items[0]
	if item.Type != "github-issue" {
		t.Errorf("type = %q, want github-issue", item.Type)
	}
	if !strings.Contains(item.Content, "Keywords:") {
		t.Errorf("missing Keywords section in content")
	}
	if !strings.Contains(item.TopicKey, "acme-service/issue-1") {
		t.Errorf("topic_key %q should contain acme-service/issue-1", item.TopicKey)
	}
}
