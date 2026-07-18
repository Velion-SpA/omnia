package confluence_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Velion-SpA/omnia/internal/core"
	"github.com/Velion-SpA/omnia/internal/source/atlassian"
	confluence "github.com/Velion-SpA/omnia/internal/source/confluence"
)

// stubRouter satisfies confluence's projectRouter interface by returning a
// fixed project string.
type stubRouter struct {
	project string
}

func (r *stubRouter) ResolveConfluence(_ string) string { return r.project }

// stubState is a minimal core.StateStore for tests (mirrors jira_test.go).
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

// spaceHandlerFixture returns an http.HandlerFunc serving:
//   - GET /wiki/api/v2/spaces?keys=DOCS -> resolves to spaceID "999"
//   - GET /wiki/api/v2/spaces/999/pages -> pagesHandler(w, r)
//
// This mirrors the two-call sequence the adapter must make: resolve the
// numeric space id from the configured space key, then page through
// /wiki/api/v2/spaces/{id}/pages.
func spaceHandlerFixture(t *testing.T, spaceKey, spaceID string, pagesHandler http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/wiki/api/v2/spaces":
			if got := r.URL.Query().Get("keys"); got != spaceKey {
				t.Errorf("spaces lookup keys=%q, want %q", got, spaceKey)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"results":[{"id":%q,"key":%q,"name":"Docs Space"}]}`, spaceID, spaceKey)
		case r.URL.Path == "/wiki/api/v2/spaces/"+spaceID+"/pages":
			pagesHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

func TestFetchPageFromFixture(t *testing.T) {
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"results": [{
				"id": "111",
				"title": "Getting Started",
				"createdAt": "2024-01-01T00:00:00.000Z",
				"version": {"number": 3, "createdAt": "2024-06-01T10:00:00.000Z"},
				"body": {"storage": {"value": "<p>Root cause is X.</p>"}},
				"_links": {"webui": "/spaces/DOCS/pages/111/Getting+Started"}
			}]
		}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if item.Type != "confluence-page" {
		t.Errorf("type = %q, want confluence-page", item.Type)
	}
	if !strings.Contains(item.Title, "Getting Started") {
		t.Errorf("title %q should contain the page title", item.Title)
	}
	if item.TopicKey != "confluence/docs/page-111" {
		t.Errorf("topic_key = %q, want confluence/docs/page-111", item.TopicKey)
	}
	if !strings.Contains(item.Content, "Root cause is X.") {
		t.Errorf("content should contain the converted storage HTML body, got: %s", item.Content)
	}
	if !strings.Contains(item.Content, "source: confluence") {
		t.Errorf("meta block should carry source: confluence")
	}
	if !strings.Contains(item.Content, "kind: page") {
		t.Errorf("meta block should carry kind: page")
	}
	if !strings.Contains(item.Content, srv.URL+"/wiki/spaces/DOCS/pages/111/Getting+Started") {
		t.Errorf("content should contain the full page URL, got: %s", item.Content)
	}
}

func TestFirstRunNoCursorFetchesAllPages(t *testing.T) {
	var requests int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"id":"1","title":"Old page","createdAt":"2020-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2020-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>old</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/Old"}}
		]}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, newStubState(nil))

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("first run with no cursor should fetch ALL pages (even very old ones), got %d items", len(items))
	}
}

func TestCursorConsumptionSkipsOlderPages(t *testing.T) {
	storedCursor := "2024-06-01T00:00:00Z"

	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Newest-first order (matches the real server contract now that the
		// adapter requests sort=-modified-date and relies on it for the
		// early-stop-on-cursor optimization): the New page comes BEFORE the
		// Old page. Reaching Old page (createdAt <= cursor) correctly stops
		// the sweep — anything after it in a newest-first list is also old.
		w.Write([]byte(`{"results":[
			{"id":"2","title":"New page","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":2,"createdAt":"2024-07-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>new</p>"}},"_links":{"webui":"/spaces/DOCS/pages/2/New"}},
			{"id":"1","title":"Old page","createdAt":"2020-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>old</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/Old"}}
		]}`))
	}))
	defer srv.Close()

	st := newStubState(map[string]string{"confluence:DOCS": storedCursor})
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (only the page newer than the cursor), got %d", len(items))
	}
	if !strings.Contains(items[0].Title, "New page") {
		t.Errorf("expected the surviving item to be the newer page, got title %q", items[0].Title)
	}
}

func TestPaginationAcrossNextLink(t *testing.T) {
	var callCount int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			fmt.Fprintf(w, `{
				"results": [{"id":"1","title":"Page one","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>one</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/One"}}],
				"_links": {"next": "/wiki/api/v2/spaces/999/pages?cursor=abc&limit=100&body-format=storage"}
			}`)
			return
		}
		fmt.Fprintf(w, `{
			"results": [{"id":"2","title":"Page two","createdAt":"2024-01-02T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-02T00:00:00.000Z"},"body":{"storage":{"value":"<p>two</p>"}},"_links":{"webui":"/spaces/DOCS/pages/2/Two"}}]
		}`)
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items across 2 pages, got %d", len(items))
	}
	// 1 spaces lookup + 2 page requests.
	if callCount != 2 {
		t.Errorf("expected 2 page requests, got %d", callCount)
	}
}

func TestCursorAdvancesToMaxVersionCreatedAtAfterFetch(t *testing.T) {
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"id":"1","title":"one","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>one</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/one"}},
			{"id":"2","title":"two","createdAt":"2024-01-05T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-05T00:00:00.000Z"},"body":{"storage":{"value":"<p>two</p>"}},"_links":{"webui":"/spaces/DOCS/pages/2/two"}}
		]}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	got, ok := st.GetCursor("confluence", "DOCS")
	if !ok {
		t.Fatal("expected a cursor to be stored for space DOCS")
	}
	want := "2024-01-05T00:00:00Z"
	if got != want {
		t.Errorf("cursor = %q, want max version.createdAt %q", got, want)
	}
}

// TestMultipleSpaceKeysFetchedWithIndependentCursors verifies each configured
// space key resolves its own space id and gets its own cursor key, so
// unrelated spaces never share (or clobber) each other's cursor.
func TestMultipleSpaceKeysFetchedWithIndependentCursors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/wiki/api/v2/spaces" && r.URL.Query().Get("keys") == "DOCS":
			w.Write([]byte(`{"results":[{"id":"100","key":"DOCS"}]}`))
		case r.URL.Path == "/wiki/api/v2/spaces" && r.URL.Query().Get("keys") == "ENG":
			w.Write([]byte(`{"results":[{"id":"200","key":"ENG"}]}`))
		case r.URL.Path == "/wiki/api/v2/spaces/100/pages":
			w.Write([]byte(`{"results":[{"id":"1","title":"docs page","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>d</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/d"}}]}`))
		case r.URL.Path == "/wiki/api/v2/spaces/200/pages":
			w.Write([]byte(`{"results":[{"id":"2","title":"eng page","createdAt":"2024-02-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-02-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>e</p>"}},"_links":{"webui":"/spaces/ENG/pages/2/e"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS", "ENG"}, &stubRouter{"omnia"}, st)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items (1 per space), got %d", len(items))
	}

	docsCursor, ok := st.GetCursor("confluence", "DOCS")
	if !ok || docsCursor != "2024-01-01T00:00:00Z" {
		t.Errorf("DOCS cursor = %q, ok=%v, want 2024-01-01T00:00:00Z", docsCursor, ok)
	}
	engCursor, ok := st.GetCursor("confluence", "ENG")
	if !ok || engCursor != "2024-02-01T00:00:00Z" {
		t.Errorf("ENG cursor = %q, ok=%v, want 2024-02-01T00:00:00Z", engCursor, ok)
	}
}

// TestAuthFailureSkipsSourceLoudlyAndDoesNotAdvanceCursor verifies that a
// 401/403 from the shared Atlassian client (even during space-id resolution)
// surfaces as an error wrapping atlassian.ErrAuthFailed and that no cursor is
// stored for the affected space.
func TestAuthFailureSkipsSourceLoudlyAndDoesNotAdvanceCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "bad-token")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	_, err := src.FetchAll(context.Background())
	if err == nil {
		t.Fatal("expected an error for 401 response")
	}
	if !errors.Is(err, atlassian.ErrAuthFailed) {
		t.Errorf("err = %v, want wrapping atlassian.ErrAuthFailed", err)
	}
	if _, ok := st.GetCursor("confluence", "DOCS"); ok {
		t.Error("cursor must not be stored when auth fails")
	}
}

// TestRateLimitRetryHandledBySharedClient verifies that a 429 from the pages
// endpoint is retried (via the shared atlassian.Client's bounded retry)
// rather than failing the whole source.
func TestRateLimitRetryHandledBySharedClient(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then one success)", attempts)
	}
}

func TestName(t *testing.T) {
	src := confluence.New(atlassian.New("https://x.atlassian.net", "u", "t"), nil, &stubRouter{}, nil)
	if src.Name() != "confluence" {
		t.Errorf("Name() = %q, want confluence", src.Name())
	}
}

// TestMalformedTimestampDegradesInsteadOfAbortingSpace mirrors jira's
// regression test: a single page with an unparsable version.createdAt must
// NOT abort the whole page decode / space fetch. It degrades to the zero
// value (logged), the page is still ingested, and the cursor still advances
// to the max of the VALID timestamps in the batch.
func TestMalformedTimestampDegradesInsteadOfAbortingSpace(t *testing.T) {
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"id":"1","title":"good one","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>a</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/a"}},
			{"id":"2","title":"bad timestamp","createdAt":"2024-01-02T00:00:00.000Z","version":{"number":1,"createdAt":"not-a-date"},"body":{"storage":{"value":"<p>b</p>"}},"_links":{"webui":"/spaces/DOCS/pages/2/b"}},
			{"id":"3","title":"another good one","createdAt":"2024-01-03T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-03T00:00:00.000Z"},"body":{"storage":{"value":"<p>c</p>"}},"_links":{"webui":"/spaces/DOCS/pages/3/c"}}
		]}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch must not hard-fail because one page has a malformed timestamp: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected all 3 pages to be ingested (malformed timestamp degrades, page is not skipped), got %d", len(items))
	}
	if !strings.Contains(items[1].Title, "bad timestamp") {
		t.Errorf("the page with the malformed timestamp should still be ingested, got title %q", items[1].Title)
	}
	if strings.Contains(items[1].Content, "- Last Modified:") {
		t.Errorf("the malformed-timestamp page's content should omit the Last Modified line (zero-value), got: %s", items[1].Content)
	}

	got, ok := st.GetCursor("confluence", "DOCS")
	if !ok || got != "2024-01-03T00:00:00Z" {
		t.Errorf("cursor = %q, ok=%v, want 2024-01-03T00:00:00Z (max of the valid timestamps)", got, ok)
	}
}

// TestPaginationStopsOnRepeatedNextLink is a regression test for the
// visited-link guard: a server that keeps answering with the SAME
// `_links.next` value forever must not be paginated forever.
func TestPaginationStopsOnRepeatedNextLink(t *testing.T) {
	var requests int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"id":"%d","title":"x","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/%d/x"}}],"_links":{"next":"/wiki/api/v2/spaces/999/pages?cursor=same-token"}}`, requests, requests)
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	done := make(chan struct{})
	var items []core.Item
	var err error
	go func() {
		items, err = src.FetchAll(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Fetch did not terminate — a repeated _links.next must stop pagination, not loop forever")
	}

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want exactly 2 (page 1 fetched, page 2 fetched, then the repeated next link on page 2's response is detected and pagination stops)", requests)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items (one per page actually fetched), got %d", len(items))
	}
}

// TestPaginationCapPreventsUnboundedLoopWithCyclingNextLinks is a regression
// test for the hard page cap: a server that keeps answering with a NEW,
// never-repeating `_links.next` value every time (so the visited-link guard
// alone would never fire) must still be bounded by a hard page cap.
func TestPaginationCapPreventsUnboundedLoopWithCyclingNextLinks(t *testing.T) {
	// Must match confluence.go's private maxPagesPerSpace constant (mirrors
	// jira_test.go's TestPaginationCapPreventsUnboundedLoopWithCyclingTokens).
	const wantMaxPages = 50

	var requests int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"id":"%d","title":"x","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/%d/x"}}],"_links":{"next":"/wiki/api/v2/spaces/999/pages?cursor=token-%d"}}`, requests, requests, requests)
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = src.FetchAll(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Fetch did not terminate — an endless stream of never-repeating next links must still hit a hard page cap")
	}

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if requests != wantMaxPages {
		t.Errorf("requests = %d, want exactly %d (the hard page cap)", requests, wantMaxPages)
	}
}

// TestEmptyResultsPageStopsPaginationDefensively guards against a
// misbehaving server returning an empty results page (with or without a
// next link) — pagination must stop rather than spin on empty pages.
func TestEmptyResultsPageStopsPaginationDefensively(t *testing.T) {
	var requests int
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[],"_links":{"next":"/wiki/api/v2/spaces/999/pages?cursor=should-not-be-followed"}}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 (empty results page must stop pagination defensively)", requests)
	}
}

// TestSpaceKeyNotFoundReturnsError verifies that an empty spaces-lookup
// result (the configured space key doesn't exist / isn't accessible with
// these credentials) surfaces as an error rather than a panic or a silent
// empty fetch, and that no cursor is stored.
func TestSpaceKeyNotFoundReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"MISSING"}, &stubRouter{"omnia"}, st)

	_, err := src.FetchAll(context.Background())
	if err == nil {
		t.Fatal("expected an error when the space key resolves to zero spaces")
	}
	if _, ok := st.GetCursor("confluence", "MISSING"); ok {
		t.Error("cursor must not be stored when space id resolution fails")
	}
}

// TestSpaceKeyResolutionFallsBackToFirstResultWhenKeyDoesNotEchoExactly
// covers the defensive fallback branch in resolveSpaceID: if the spaces
// endpoint returns a non-empty result set but no entry's Key exactly matches
// (case-insensitively) the configured key, the first result's id is used
// rather than hard-failing.
func TestSpaceKeyResolutionFallsBackToFirstResultWhenKeyDoesNotEchoExactly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/wiki/api/v2/spaces":
			// Key deliberately does NOT match "DOCS" exactly.
			w.Write([]byte(`{"results":[{"id":"777","key":"UNEXPECTED"}]}`))
		case r.URL.Path == "/wiki/api/v2/spaces/777/pages":
			w.Write([]byte(`{"results":[{"id":"1","title":"fallback page","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/x"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected the fallback space id (777) to still be used to fetch pages, got %d items", len(items))
	}
}

// TestLargePageBodyIsChunkedAcrossMultipleItems verifies that a page body
// exceeding the chunk budget is split into multiple Items with "-partN"
// topic_key suffixes and a context header on continuation chunks (mirrors
// internal/source/jira's chunking behavior for oversized issue content).
func TestLargePageBodyIsChunkedAcrossMultipleItems(t *testing.T) {
	// Build a storage-HTML body comfortably larger than maxChunkRunes
	// (45000) once converted to text, using many paragraphs so storage.ToText
	// has plenty of block boundaries to work with.
	var body strings.Builder
	line := strings.Repeat("word ", 200) // ~1000 runes per paragraph
	for i := 0; i < 60; i++ {
		body.WriteString("<p>")
		body.WriteString(line)
		body.WriteString("</p>")
	}

	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"id":"1","title":"huge page","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":%q}},"_links":{"webui":"/spaces/DOCS/pages/1/huge"}}]}`, body.String())
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected the oversized body to be chunked into 2+ items, got %d", len(items))
	}
	if !strings.HasSuffix(items[0].TopicKey, "-part1") {
		t.Errorf("first chunk topic_key = %q, want a -part1 suffix", items[0].TopicKey)
	}
	if !strings.Contains(items[1].Content, "huge page") {
		t.Errorf("continuation chunk should carry a context header mentioning the page title, got: %s", items[1].Content[:min(200, len(items[1].Content))])
	}
	if !strings.Contains(items[0].Title, "part 1/") {
		t.Errorf("chunked item title should carry a (part N/Total) suffix, got %q", items[0].Title)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestMalformedTimestampOnSubsequentRunStillIngestsPage is a regression test:
// a page whose version.createdAt is malformed/unparsable must still be
// ingested even on a run where a cursor ALREADY EXISTS from a previous run.
// The client-side "skip pages not newer than cursor" filter compares a
// degraded (zero-value) timestamp against the stored cursor, and a zero time
// is never After() any non-zero cursor — so without an explicit bypass, a
// malformed-timestamp page would be silently and PERMANENTLY excluded from
// every future run (indistinguishable from "genuinely old"), a poison pill
// that mirrors jira's MUST-FIX 2 but manifests as a silent drop instead of a
// hard abort.
func TestMalformedTimestampOnSubsequentRunStillIngestsPage(t *testing.T) {
	storedCursor := "2024-06-01T00:00:00Z"

	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"id":"1","title":"bad timestamp on a later run","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":2,"createdAt":"not-a-date"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/x"}}
		]}`))
	}))
	defer srv.Close()

	st := newStubState(map[string]string{"confluence:DOCS": storedCursor})
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("a page with a malformed timestamp must still be ingested even when a cursor already exists (must not be silently dropped as if it were 'older than cursor'), got %d items", len(items))
	}
}

// TestPagesRequestSortsNewestModifiedFirst locks in that the pages-list
// request asks the server to sort by descending modified-date. This is
// load-bearing, not cosmetic: the early-stop-on-cursor optimization (see
// TestSweepResumesAcrossCappedRunsAndCommitsCursorOnceComplete) is only safe
// if "already-seen" content is contiguous from the front of the list, which
// requires a stable newest-edited-first order — an unsorted/id-ordered list
// could put a just-edited old page anywhere, and stopping early would
// silently miss it.
func TestPagesRequestSortsNewestModifiedFirst(t *testing.T) {
	var gotSort string
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		gotSort = r.URL.Query().Get("sort")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if gotSort != "-modified-date" {
		t.Errorf("sort query param = %q, want -modified-date (newest-edited-first)", gotSort)
	}
}

// TestSweepResumesAcrossCappedRunsAndCommitsCursorOnceComplete is the
// regression test for the BLOCKING bug (adversarial review MUST-FIX 1):
// Confluence v2's pages-list endpoint has no server-side "since" filter, so
// restarting a capped sweep from page 1 every run permanently lost whatever
// lay beyond the per-run page cap for any space larger than
// maxPagesPerSpace*limit pages — the "the next run continues from there" log
// was FALSE. This drives a space with totalPages = maxPagesPerSpace + 3
// pages (sorted newest-first, i.e. page 1 is the newest/highest-timestamp
// page), forcing run 1 to hit the cap partway through, and verifies:
//   - run 1 ingests exactly maxPagesPerSpace items and does NOT commit the
//     real incremental cursor (that would prematurely tell a later
//     steady-state run "everything above this is already ingested" and mask
//     the still-unswept tail);
//   - run 1 persists a resume link;
//   - run 2 resumes pagination from EXACTLY that persisted link (proven by
//     the server asserting the exact cursor token on its first request, not
//     a fresh page-1 request), ingests the remaining pages, reaches the
//     natural end of pagination, and THEN commits the real cursor (the true
//     sweep max, from page 1) and clears the resume link.
//
// Together, run 1 + run 2 ingest ALL totalPages pages across the sweep —
// nothing is silently dropped, and no single run does more than
// maxPagesPerSpace requests of work.
func TestSweepResumesAcrossCappedRunsAndCommitsCursorOnceComplete(t *testing.T) {
	const wantMaxPages = 50 // must match confluence.go's private maxPagesPerSpace
	const totalPages = wantMaxPages + 3

	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	// page n (1-indexed) is the (n-1)th most recent page: page 1 is newest.
	pageTime := func(n int) time.Time { return base.Add(-time.Duration(n-1) * 24 * time.Hour) }

	var requests int
	var run2FirstRequestCursor string
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")

		n := 1
		if cur := r.URL.Query().Get("cursor"); cur != "" {
			fmt.Sscanf(cur, "page-%d", &n)
			n++ // the cursor token names the LAST page already served
			if requests == wantMaxPages+1 {
				run2FirstRequestCursor = cur
			}
		}

		if n > totalPages {
			w.Write([]byte(`{"results":[]}`))
			return
		}

		nextLink := ""
		if n < totalPages {
			nextLink = fmt.Sprintf(`,"_links":{"next":"/wiki/api/v2/spaces/999/pages?cursor=page-%d"}`, n)
		}
		fmt.Fprintf(w, `{"results":[{"id":"%d","title":"page %d","version":{"number":1,"createdAt":%q},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/%d/x"}}]%s}`,
			n, n, pageTime(n).Format(time.RFC3339), n, nextLink)
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, st)

	// --- Run 1: capped partway through the sweep. ---
	items1, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("run 1: Fetch failed: %v", err)
	}
	if len(items1) != wantMaxPages {
		t.Fatalf("run 1: want %d items (capped), got %d", wantMaxPages, len(items1))
	}
	if _, ok := st.GetCursor("confluence", "DOCS"); ok {
		t.Fatal("run 1: the incremental cursor must NOT be committed while the sweep is still capped/incomplete")
	}
	resumeLink, ok := st.GetCursor("confluence", "DOCS#resume-link")
	if !ok || resumeLink == "" {
		t.Fatal("run 1: expected a resume link to be persisted after hitting the page cap")
	}

	// --- Run 2: must resume from exactly where run 1 left off. ---
	items2, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("run 2: Fetch failed: %v", err)
	}
	if run2FirstRequestCursor != fmt.Sprintf("page-%d", wantMaxPages) {
		t.Fatalf("run 2's first request carried cursor=%q, want cursor=page-%d (must resume from run 1's exact stopping point, not restart at page 1)", run2FirstRequestCursor, wantMaxPages)
	}
	if len(items2) != totalPages-wantMaxPages {
		t.Fatalf("run 2: want %d remaining items, got %d", totalPages-wantMaxPages, len(items2))
	}
	// No page should ever be ingested twice across the sweep.
	seen := make(map[string]bool)
	for _, it := range items1 {
		seen[it.TopicKey] = true
	}
	for _, it := range items2 {
		if seen[it.TopicKey] {
			t.Errorf("run 2 re-ingested a page already ingested in run 1: %s", it.TopicKey)
		}
	}

	gotCursor, ok := st.GetCursor("confluence", "DOCS")
	if !ok {
		t.Fatal("run 2: expected the incremental cursor to be committed once the sweep completes")
	}
	wantCursor := pageTime(1).UTC().Format(time.RFC3339) // the sweep's true max, from page 1
	if gotCursor != wantCursor {
		t.Errorf("run 2: cursor = %q, want the sweep's true max %q", gotCursor, wantCursor)
	}
	if link, ok := st.GetCursor("confluence", "DOCS#resume-link"); ok && link != "" {
		t.Errorf("run 2: resume link must be cleared once the sweep completes, got %q", link)
	}
}

// TestSpaceKeyResolutionFallbackWithMultipleNonMatchingResultsLogsWarning is
// the regression test for MUST-FIX 2: resolveSpaceID's defensive fallback
// (used when no result's Key exactly matches the configured key) must log a
// warning rather than silently picking Results[0] — a silent fallback could
// mean ingesting the WRONG space with no operator visibility. Covers the
// TRUE multiple-results case: two spaces returned, neither an exact match.
func TestSpaceKeyResolutionFallbackWithMultipleNonMatchingResultsLogsWarning(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/wiki/api/v2/spaces":
			// Two results, NEITHER an exact (case-insensitive) match for "DOCS".
			w.Write([]byte(`{"results":[{"id":"111","key":"DOCS-ARCHIVE"},{"id":"222","key":"DOCS-OLD"}]}`))
		case r.URL.Path == "/wiki/api/v2/spaces/111/pages":
			w.Write([]byte(`{"results":[{"id":"1","title":"fallback page","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/DOCS/pages/1/x"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected the fallback (first) result to still be used to fetch pages, got %d items", len(items))
	}
	logged := buf.String()
	if !strings.Contains(logged, "DOCS") || !strings.Contains(logged, "111") {
		t.Errorf("expected a warning logged naming the requested key and the chosen fallback id when no exact space-key match was found, got log output: %q", logged)
	}
}

// TestSpaceKeyWithSpecialCharactersRoundTripsThroughURLEscaping is the
// regression test for MUST-FIX 3: proves url.QueryEscape on the space key in
// resolveSpaceID's query string actually round-trips correctly end-to-end
// (server-side decoding reconstructs the exact original key) for a key
// containing URL-unsafe characters (space, "&", "/") that would otherwise be
// misinterpreted as query-string structure (a stray "&" splitting the query
// into unrelated parameters, a raw space being invalid in a URL, etc).
func TestSpaceKeyWithSpecialCharactersRoundTripsThroughURLEscaping(t *testing.T) {
	const weirdKey = "DOCS & MORE/2024"
	var gotKeys string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/wiki/api/v2/spaces":
			gotKeys = r.URL.Query().Get("keys")
			fmt.Fprintf(w, `{"results":[{"id":"555","key":%q}]}`, weirdKey)
		case r.URL.Path == "/wiki/api/v2/spaces/555/pages":
			w.Write([]byte(`{"results":[{"id":"1","title":"weird space page","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":"<p>x</p>"}},"_links":{"webui":"/spaces/x/pages/1/x"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{weirdKey}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if gotKeys != weirdKey {
		t.Errorf("server decoded keys=%q, want the exact original space key %q (URL-escaping round trip failed)", gotKeys, weirdKey)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

// TestChunkedContinuationHeaderSanitizesCommentBreakoutSequenceInTitle is the
// regression test for the SHOULD-FIX: a page title is interpolated raw into
// the "<!-- TITLE | DATE -->" HTML-comment context header used on
// continuation chunks. A title containing a literal "-->" would prematurely
// close that comment, letting attacker-controlled page content break out
// into the surrounding markdown/HTML rendering context.
func TestChunkedContinuationHeaderSanitizesCommentBreakoutSequenceInTitle(t *testing.T) {
	maliciousTitle := `Evil Page --> <script>alert(1)</script>`
	var body strings.Builder
	line := strings.Repeat("word ", 200) // ~1000 runes per paragraph
	for i := 0; i < 60; i++ {
		body.WriteString("<p>")
		body.WriteString(line)
		body.WriteString("</p>")
	}

	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"id":"1","title":%q,"version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"body":{"storage":{"value":%q}},"_links":{"webui":"/spaces/DOCS/pages/1/evil"}}]}`, maliciousTitle, body.String())
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected the oversized body to be chunked into 2+ items, got %d", len(items))
	}
	continuation := items[1].Content
	if strings.Count(continuation, "-->") > 1 {
		t.Errorf("continuation chunk content contains an unescaped '-->' from the page title, which could break out of the context header's HTML comment: %s", continuation[:min(300, len(continuation))])
	}
}

// TestNoBodyDoesNotPanic guards the case where a page has no body/storage
// value at all (absent from the response).
func TestNoBodyDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(spaceHandlerFixture(t, "DOCS", "999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":"1","title":"bare page","createdAt":"2024-01-01T00:00:00.000Z","version":{"number":1,"createdAt":"2024-01-01T00:00:00.000Z"},"_links":{"webui":"/spaces/DOCS/pages/1/bare"}}]}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := confluence.New(client, []string{"DOCS"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(items[0].Title, "bare page") {
		t.Errorf("title = %q, want it to contain the page title", items[0].Title)
	}
}
