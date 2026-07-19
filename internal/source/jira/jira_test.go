package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/source/atlassian"
	jira "github.com/velion/omnia/internal/source/jira"
)

// stubRouter satisfies jira's projectRouter interface by returning a fixed project string.
type stubRouter struct {
	project string
}

func (r *stubRouter) ResolveJira(_ string) string { return r.project }

// stubState is a minimal core.StateStore for tests.
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

// jqlBody captures the subset of the POST /rest/api/3/search/jql request body
// tests need to inspect.
type jqlBody struct {
	JQL           string   `json:"jql"`
	NextPageToken string   `json:"nextPageToken"`
	MaxResults    int      `json:"maxResults"`
	Fields        []string `json:"fields"`
}

func TestFetchIssueFromFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"issues": [{
				"key": "ENG-42",
				"fields": {
					"summary": "Fix the thing",
					"status": {"name": "In Progress"},
					"assignee": {"displayName": "Alice"},
					"description": {"type": "doc", "content": [
						{"type": "paragraph", "content": [{"type": "text", "text": "Root cause is X."}]}
					]},
					"comment": {"comments": [{
						"body": {"type": "doc", "content": [
							{"type": "paragraph", "content": [{"type": "text", "text": "Looks good."}]}
						]},
						"author": {"displayName": "Bob"},
						"created": "2024-06-01T10:00:00.000+0000"
					}]},
					"created": "2024-05-01T09:00:00.000+0000",
					"updated": "2024-06-01T10:00:00.000+0000"
				}
			}],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if item.Type != "jira-issue" {
		t.Errorf("type = %q, want jira-issue", item.Type)
	}
	if !strings.Contains(item.Title, "ENG-42") {
		t.Errorf("title %q should contain ENG-42", item.Title)
	}
	if item.TopicKey != "jira/eng/issue-eng-42" {
		t.Errorf("topic_key = %q, want jira/eng/issue-eng-42", item.TopicKey)
	}
	if !strings.Contains(item.Content, "Root cause is X.") {
		t.Errorf("content should contain the converted ADF description, got: %s", item.Content)
	}
	if !strings.Contains(item.Content, "Looks good.") {
		t.Errorf("content should contain the converted ADF comment body")
	}
	if !strings.Contains(item.Content, "Bob") {
		t.Errorf("content should mention the comment author")
	}
	if !strings.Contains(item.Content, "Alice") {
		t.Errorf("content should mention the assignee")
	}
	if !strings.Contains(item.Content, "source: jira") {
		t.Errorf("meta block should carry source: jira")
	}
	if !strings.Contains(item.Content, "kind: issue") {
		t.Errorf("meta block should carry kind: issue")
	}
}

func TestFirstRunNoCursorFetchesAllIssuesForProject(t *testing.T) {
	var gotBody jqlBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, newStubState(nil))

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if strings.Contains(gotBody.JQL, "updated >=") {
		t.Errorf("first-run JQL %q should not filter by updated (fetch all issues)", gotBody.JQL)
	}
	if !strings.Contains(gotBody.JQL, `project = "ENG"`) {
		t.Errorf("JQL %q should scope to the configured project key", gotBody.JQL)
	}
}

func TestCursorConsumption(t *testing.T) {
	storedCursor := "2024-06-01T00:00:00Z"
	cursorTime, _ := time.Parse(time.RFC3339, storedCursor)
	wantSince := cursorTime.Add(-5 * time.Minute).UTC().Format("2006-01-02 15:04")

	var gotBody jqlBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	st := newStubState(map[string]string{"jira:ENG": storedCursor})
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, st)

	if _, err := src.Fetch(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if !strings.Contains(gotBody.JQL, wantSince) {
		t.Errorf("JQL %q should contain cursor-5m overlap %q", gotBody.JQL, wantSince)
	}
}

func TestPaginationAcrossNextPageToken(t *testing.T) {
	var page2TokenReceived string
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body jqlBody
		json.NewDecoder(r.Body).Decode(&body)
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			w.Write([]byte(`{
				"issues": [{"key": "ENG-1", "fields": {"summary": "one", "status": {"name": "Open"}, "created": "2024-01-01T00:00:00.000+0000", "updated": "2024-01-01T00:00:00.000+0000"}}],
				"nextPageToken": "page2token",
				"isLast": false
			}`))
			return
		}
		page2TokenReceived = body.NextPageToken
		w.Write([]byte(`{
			"issues": [{"key": "ENG-2", "fields": {"summary": "two", "status": {"name": "Open"}, "created": "2024-01-02T00:00:00.000+0000", "updated": "2024-01-02T00:00:00.000+0000"}}],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items across 2 pages, got %d", len(items))
	}
	if page2TokenReceived != "page2token" {
		t.Errorf("page 2 request nextPageToken = %q, want page2token", page2TokenReceived)
	}
	if callCount != 2 {
		t.Errorf("expected 2 requests (2 pages), got %d", callCount)
	}
}

func TestCursorAdvancesToMaxUpdatedAfterFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"issues": [
				{"key": "ENG-1", "fields": {"summary": "one", "status": {"name": "Open"}, "created": "2024-01-01T00:00:00.000+0000", "updated": "2024-01-01T00:00:00.000+0000"}},
				{"key": "ENG-2", "fields": {"summary": "two", "status": {"name": "Open"}, "created": "2024-01-05T00:00:00.000+0000", "updated": "2024-01-05T00:00:00.000+0000"}}
			],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, st)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	got, ok := st.GetCursor("jira", "ENG")
	if !ok {
		t.Fatal("expected a cursor to be stored for project ENG")
	}
	want := "2024-01-05T00:00:00Z"
	if got != want {
		t.Errorf("cursor = %q, want max updated %q", got, want)
	}
}

// TestMultipleProjectKeysFetchedWithIndependentCursors verifies that each
// configured project key gets its own JQL project filter and its own cursor
// key, so unrelated projects never share (or clobber) each other's cursor.
func TestMultipleProjectKeysFetchedWithIndependentCursors(t *testing.T) {
	var seenProjects []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body jqlBody
		json.NewDecoder(r.Body).Decode(&body)
		seenProjects = append(seenProjects, body.JQL)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(body.JQL, `"ENG"`) {
			w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"e","status":{"name":"Open"},"created":"2024-01-01T00:00:00.000+0000","updated":"2024-01-01T00:00:00.000+0000"}}],"isLast":true}`))
			return
		}
		w.Write([]byte(`{"issues":[{"key":"OPS-1","fields":{"summary":"o","status":{"name":"Open"},"created":"2024-02-01T00:00:00.000+0000","updated":"2024-02-01T00:00:00.000+0000"}}],"isLast":true}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG", "OPS"}, &stubRouter{"omnia"}, st)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items (1 per project), got %d", len(items))
	}
	if len(seenProjects) != 2 {
		t.Fatalf("expected 2 requests (1 per project), got %d", len(seenProjects))
	}

	engCursor, ok := st.GetCursor("jira", "ENG")
	if !ok || engCursor != "2024-01-01T00:00:00Z" {
		t.Errorf("ENG cursor = %q, ok=%v, want 2024-01-01T00:00:00Z", engCursor, ok)
	}
	opsCursor, ok := st.GetCursor("jira", "OPS")
	if !ok || opsCursor != "2024-02-01T00:00:00Z" {
		t.Errorf("OPS cursor = %q, ok=%v, want 2024-02-01T00:00:00Z", opsCursor, ok)
	}
}

// TestAuthFailureSkipsSourceLoudlyAndDoesNotAdvanceCursor verifies that a
// 401/403 from the shared Atlassian client surfaces as an error wrapping
// atlassian.ErrAuthFailed (so the pipeline logs it loudly and moves on to
// other sources) and that no cursor is stored for the affected project.
func TestAuthFailureSkipsSourceLoudlyAndDoesNotAdvanceCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "bad-token")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, st)

	_, err := src.FetchAll(context.Background())
	if err == nil {
		t.Fatal("expected an error for 401 response")
	}
	if !errors.Is(err, atlassian.ErrAuthFailed) {
		t.Errorf("err = %v, want wrapping atlassian.ErrAuthFailed", err)
	}
	if _, ok := st.GetCursor("jira", "ENG"); ok {
		t.Error("cursor must not be stored when auth fails")
	}
}

// TestRateLimitRetryHandledBySharedClient verifies that a 429 from the
// search endpoint is retried (via the shared atlassian.Client's bounded
// retry) rather than failing the whole source.
func TestRateLimitRetryHandledBySharedClient(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then one success)", attempts)
	}
}

func TestName(t *testing.T) {
	src := jira.New(atlassian.New("https://x.atlassian.net", "u", "t"), nil, &stubRouter{}, nil)
	if src.Name() != "jira" {
		t.Errorf("Name() = %q, want jira", src.Name())
	}
}

// TestTimestampWithColonOffsetParsesViaRFC3339Fallback verifies the jiraTime
// parser also accepts a strict RFC3339 timestamp (colon in the offset, e.g.
// "...Z" or "...+00:00") even though Jira Cloud normally sends the no-colon
// form ("...+0000") — a defensive fallback in case a future API response
// varies.
func TestTimestampWithColonOffsetParsesViaRFC3339Fallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"issues": [{"key": "ENG-7", "fields": {"summary": "colon offset", "status": {"name": "Open"}, "created": "2024-03-01T00:00:00Z", "updated": "2024-03-02T00:00:00Z"}}],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, st)

	if _, err := src.FetchAll(context.Background()); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	got, ok := st.GetCursor("jira", "ENG")
	if !ok || got != "2024-03-02T00:00:00Z" {
		t.Errorf("cursor = %q, ok=%v, want 2024-03-02T00:00:00Z", got, ok)
	}
}

// TestMalformedTimestampDegradesInsteadOfAbortingProject is the regression
// test for MUST-FIX 2 (adversarial review): a single issue with an
// unparsable created/updated timestamp must NOT abort the whole page decode
// (the old behavior: the custom jiraTime.UnmarshalJSON returned an error,
// which aborted json.Unmarshal for the entire searchResponse, which aborted
// fetchProject/Fetch for this project AND every remaining configured
// project key — and since the cursor is never advanced on error, the same
// bad record would re-fail every future run forever, permanently blocking
// ingestion). The fix degrades the malformed timestamp to the zero value
// instead: the issue is still ingested (its other fields are fine), and the
// cursor still advances to the max of the VALID timestamps in the batch.
func TestMalformedTimestampDegradesInsteadOfAbortingProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"issues": [
				{"key": "ENG-1", "fields": {"summary": "good one", "status": {"name": "Open"}, "created": "2024-01-01T00:00:00.000+0000", "updated": "2024-01-01T00:00:00.000+0000"}},
				{"key": "ENG-2", "fields": {"summary": "bad timestamp", "status": {"name": "Open"}, "created": "not-a-date", "updated": "not-a-date"}},
				{"key": "ENG-3", "fields": {"summary": "another good one", "status": {"name": "Open"}, "created": "2024-01-03T00:00:00.000+0000", "updated": "2024-01-03T00:00:00.000+0000"}}
			],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	st := newStubState(nil)
	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, st)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch must not hard-fail because one issue has a malformed timestamp: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected all 3 issues to be ingested (malformed timestamp degrades, issue is not skipped), got %d", len(items))
	}
	if !strings.Contains(items[1].Title, "bad timestamp") {
		t.Errorf("the issue with the malformed timestamp should still be ingested, got title %q", items[1].Title)
	}
	if strings.Contains(items[1].Content, "- Created:") || strings.Contains(items[1].Content, "- Updated:") {
		t.Errorf("the malformed-timestamp issue's content should omit Created/Updated lines (zero-value), got: %s", items[1].Content)
	}

	// The cursor must advance to the max of the VALID timestamps (the
	// degraded zero-value one never wins the max comparison), so future
	// runs make forward progress instead of getting stuck re-fetching the
	// same bad record forever.
	got, ok := st.GetCursor("jira", "ENG")
	if !ok || got != "2024-01-03T00:00:00Z" {
		t.Errorf("cursor = %q, ok=%v, want 2024-01-03T00:00:00Z (max of the valid timestamps)", got, ok)
	}
}

// TestPaginationStopsOnRepeatedNextPageToken is a regression test for
// MUST-FIX 1 (adversarial review, part A): the visited-token guard. A
// server that keeps answering isLast:false with the SAME nextPageToken
// forever must not be paginated forever — fetchProject must detect the
// repeated token and stop.
func TestPaginationStopsOnRepeatedNextPageToken(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issues":[{"key":"ENG-%d","fields":{"summary":"x","status":{"name":"Open"},"created":"2024-01-01T00:00:00.000+0000","updated":"2024-01-01T00:00:00.000+0000"}}],"nextPageToken":"same-token","isLast":false}`, requests)
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

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
		t.Fatal("Fetch did not terminate — a repeated nextPageToken must stop pagination, not loop forever")
	}

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want exactly 2 (page 1 fetched, page 2 fetched, then the repeated token on page 2's response is detected and pagination stops)", requests)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items (one per page actually fetched), got %d", len(items))
	}
}

// TestPaginationCapPreventsUnboundedLoopWithCyclingTokens is a regression
// test for MUST-FIX 1 (adversarial review, part B): the hard page cap. A
// server that keeps answering isLast:false with a NEW, never-repeating
// token every time (so the visited-token guard alone would never fire)
// must still be bounded by a hard page cap — fetchProject must terminate
// instead of hanging / OOMing (collect.go runs under context.Background()
// with no deadline, so nothing else would stop it).
func TestPaginationCapPreventsUnboundedLoopWithCyclingTokens(t *testing.T) {
	// Must match jira.go's private maxPagesPerProject constant (mirrors how
	// client_test.go's TestGetJSON_RateLimitRetryCapTerminates hardcodes
	// wantMaxRequests = 5 against atlassian.Client's private maxRetries).
	const wantMaxPages = 50

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issues":[{"key":"ENG-%d","fields":{"summary":"x","status":{"name":"Open"},"created":"2024-01-01T00:00:00.000+0000","updated":"2024-01-01T00:00:00.000+0000"}}],"nextPageToken":"token-%d","isLast":false}`, requests, requests)
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = src.FetchAll(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Fetch did not terminate — an endless isLast:false stream with ever-changing tokens must still hit a hard page cap")
	}

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if requests != wantMaxPages {
		t.Errorf("requests = %d, want exactly %d (the hard page cap)", requests, wantMaxPages)
	}
}

// TestNoDescriptionOrCommentsDoesNotPanic guards the case where an issue has
// no description and no comments (both entirely absent from the response).
func TestNoDescriptionOrCommentsDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"issues": [{"key": "ENG-9", "fields": {"summary": "bare issue", "status": {"name": "Open"}, "created": "2024-01-01T00:00:00.000+0000", "updated": "2024-01-01T00:00:00.000+0000"}}],
			"isLast": true
		}`))
	}))
	defer srv.Close()

	client := atlassian.New(srv.URL, "user@example.com", "tok")
	src := jira.New(client, []string{"ENG"}, &stubRouter{"omnia"}, nil)

	items, err := src.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(items[0].Title, "bare issue") {
		t.Errorf("title = %q, want it to contain the summary", items[0].Title)
	}
}
