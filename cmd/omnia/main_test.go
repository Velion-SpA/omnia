package main_test

import (
	"context"
	"encoding/json"
	"errors"
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

// trackingState records whether Flush was called and delegates GetCursor/SetCursor to an inner map.
type trackingState struct {
	cursors   map[string]string
	flushed   bool
}

func newTrackingState() *trackingState {
	return &trackingState{cursors: make(map[string]string)}
}

func (s *trackingState) GetCursor(source, key string) (string, bool) {
	v, ok := s.cursors[source+":"+key]
	return v, ok
}

func (s *trackingState) SetCursor(source, key, value string) error {
	s.cursors[source+":"+key] = value
	return nil
}

func (s *trackingState) Flush() error {
	s.flushed = true
	return nil
}

// failSink always returns an error on Write, simulating a persistent sink failure.
type failSink struct{}

func (f *failSink) Write(_ context.Context, _ core.Item) error {
	return errors.New("sink unavailable")
}

func (f *failSink) Health(_ context.Context) error { return nil }

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

	pipeline := core.NewPipeline([]core.Source{src}, sink, &noopState{}, false, 30, nil)
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

// TestFailedSinkWriteBlocksCursorFlush verifies that when a sink write fails
// (even after the built-in retry), the state Flush is not called for that source,
// keeping the cursor at its previous value so the next run re-fetches the window (C2).
func TestFailedSinkWriteBlocksCursorFlush(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			issue := map[string]interface{}{
				"number":     2,
				"title":      "Some issue",
				"state":      "open",
				"html_url":   "https://github.com/acme/svc/issues/2",
				"body":       "body",
				"user":       map[string]string{"login": "bob"},
				"labels":     []interface{}{},
				"assignees":  []interface{}{},
				"created_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
				"updated_at": time.Now().Format(time.RFC3339),
			}
			json.NewEncoder(w).Encode([]interface{}{issue})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newTrackingState()
	src := github.NewWithBaseURL([]string{"acme/svc"}, "omnia", "", st, srv.URL)
	sink := &failSink{}

	pipeline := core.NewPipeline([]core.Source{src}, sink, st, false, 30, nil)
	err := pipeline.Run(context.Background(), core.RunOptions{})
	if err == nil {
		t.Fatal("expected non-nil error when sink writes fail")
	}
	if st.flushed {
		t.Error("state Flush must not be called when sink writes fail for a source")
	}
}
