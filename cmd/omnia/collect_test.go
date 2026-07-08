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

	"github.com/Velion-SpA/omnia/internal/core"
	github "github.com/Velion-SpA/omnia/internal/source/github"
)

// stubRouter satisfies the github source's projectRouter interface.
type stubRouter struct {
	project string
}

func (r *stubRouter) ResolveGitHub(_ string) string { return r.project }

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
	src := github.NewWithBaseURL([]string{"acme/service"}, &stubRouter{"omnia"}, "", nil, srv.URL)

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

// TestPipelineCursorRoundTrip is an integration-level test that drives the PIPELINE
// (not the source directly) twice with a stub HTTP server and a capture sink.
//
// Run 1: no cursor → server returns N items → pipeline stores cursors and writes items.
// Run 2: cursor stored → server asserts it received the cursor value as the "since"
//
//	query param → server returns 0 items → pipeline writes 0 items.
//
// This catches the regression where pipeline.runSource always passed now-backfillDays
// to Fetch instead of zero, making the cursor-reading branch in each source unreachable.
func TestPipelineCursorRoundTrip(t *testing.T) {
	const repo = "acme/pipes"
	issuedAt := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	issuedAtStr := issuedAt.UTC().Format(time.RFC3339)

	// run1SinceReceived is what the server saw during run 1.
	// run2SinceReceived is what the server saw during run 2; it must equal the cursor.
	var run1SinceReceived, run2SinceReceived string
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			w.Write([]byte("[]"))
		case strings.Contains(r.URL.Path, "/issues"):
			since := r.URL.Query().Get("since")
			callCount++
			if callCount == 1 {
				run1SinceReceived = since
				// Return one issue updated at issuedAt so a cursor is stored.
				issue := map[string]interface{}{
					"number":     55,
					"title":      "roundtrip issue",
					"state":      "open",
					"html_url":   "https://github.com/acme/pipes/issues/55",
					"body":       "body",
					"user":       map[string]string{"login": "eve"},
					"labels":     []interface{}{},
					"assignees":  []interface{}{},
					"created_at": issuedAtStr,
					"updated_at": issuedAtStr,
				}
				json.NewEncoder(w).Encode([]interface{}{issue})
			} else {
				run2SinceReceived = since
				// No new items after the cursor.
				json.NewEncoder(w).Encode([]interface{}{})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newTrackingState()
	src := github.NewWithBaseURL([]string{repo}, &stubRouter{"omnia"}, "", st, srv.URL)
	sink := &captureSink{}
	pipeline := core.NewPipeline([]core.Source{src}, sink, st, false, 30, nil)

	// Run 1 — no cursor, should backfill.
	if err := pipeline.Run(context.Background(), core.RunOptions{}); err != nil {
		t.Fatalf("run 1 failed: %v", err)
	}
	if len(sink.items) != 1 {
		t.Fatalf("run 1: want 1 item, got %d", len(sink.items))
	}

	// Cursor must have been stored after run 1.
	storedCursor, ok := st.GetCursor("github", repo)
	if !ok {
		t.Fatal("run 1: expected cursor to be stored in state")
	}
	if storedCursor != issuedAtStr {
		t.Errorf("run 1: stored cursor = %q, want %q", storedCursor, issuedAtStr)
	}

	// Run 1's since should be a backfill timestamp (not zero, not cursor).
	// We only assert it is non-empty and is a valid RFC3339 time.
	if _, err := time.Parse(time.RFC3339, run1SinceReceived); err != nil {
		t.Errorf("run 1: server received invalid since=%q: %v", run1SinceReceived, err)
	}

	// Run 2 — cursor already stored; pipeline must pass zero to Fetch so the source
	// reads the cursor. The source applies a 10-minute overlap window (cursor-10m) to
	// cover GitHub list-index propagation lag. The stub returns 0 items for run 2.
	// Re-fetching boundary items is safe because the sink upserts by topic_key.
	sink.items = nil
	st.flushed = false
	if err := pipeline.Run(context.Background(), core.RunOptions{}); err != nil {
		t.Fatalf("run 2 failed: %v", err)
	}

	// The source subtracts 10m from the cursor (overlap window to cover index lag).
	cursorTime, _ := time.Parse(time.RFC3339, storedCursor)
	wantRun2Since := cursorTime.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if run2SinceReceived != wantRun2Since {
		t.Errorf("run 2: server received since=%q, want cursor-10m %q", run2SinceReceived, wantRun2Since)
	}
	if len(sink.items) != 0 {
		t.Errorf("run 2: want 0 items (stub returned none), got %d", len(sink.items))
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
	src := github.NewWithBaseURL([]string{"acme/svc"}, &stubRouter{"omnia"}, "", st, srv.URL)
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
