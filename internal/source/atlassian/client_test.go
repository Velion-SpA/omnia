package atlassian_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/source/atlassian"
)

func TestGetJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"42","title":"hello"}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	next, err := c.GetJSON(context.Background(), "/rest/api/3/issue/42", &out)
	if err != nil {
		t.Fatalf("GetJSON failed: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want empty (no pagination link in response)", next)
	}
	if out.ID != "42" || out.Title != "hello" {
		t.Errorf("decoded out = %+v, want id=42 title=hello", out)
	}
}

func TestGetJSON_BasicAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "secret-token")
	var out map[string]any
	if _, err := c.GetJSON(context.Background(), "/anything", &out); err != nil {
		t.Fatalf("GetJSON failed: %v", err)
	}

	wantCreds := base64.StdEncoding.EncodeToString([]byte("user@example.com:secret-token"))
	want := "Basic " + wantCreds
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestGetJSON_Pagination(t *testing.T) {
	var hitPage2 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Check the query string FIRST: both requests share the same path
		// ("/wiki/api/v2/pages"), only the cursor query param distinguishes
		// page 1 from page 2.
		if r.URL.RawQuery == "cursor=abc" {
			hitPage2 = true
			w.Write([]byte(`{"results":[{"id":"2"}],"_links":{}}`))
			return
		}
		if r.URL.Path == "/wiki/api/v2/pages" {
			w.Write([]byte(`{"results":[{"id":"1"}],"_links":{"next":"/wiki/api/v2/pages?cursor=abc"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var page1 struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	next, err := c.GetJSON(context.Background(), "/wiki/api/v2/pages", &page1)
	if err != nil {
		t.Fatalf("GetJSON page1 failed: %v", err)
	}
	if next != "/wiki/api/v2/pages?cursor=abc" {
		t.Fatalf("next = %q, want /wiki/api/v2/pages?cursor=abc", next)
	}

	var page2 struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	next2, err := c.GetJSON(context.Background(), next, &page2)
	if err != nil {
		t.Fatalf("GetJSON page2 failed: %v", err)
	}
	if next2 != "" {
		t.Errorf("next2 = %q, want empty (last page)", next2)
	}
	if !hitPage2 {
		t.Fatal("expected page2 endpoint to be hit with cursor=abc")
	}
	if len(page2.Results) != 1 || page2.Results[0].ID != "2" {
		t.Errorf("page2.Results = %+v, want [{id:2}]", page2.Results)
	}
}

func TestGetJSON_Unauthorized401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "bad-token")
	var out map[string]any
	_, err := c.GetJSON(context.Background(), "/rest/api/3/myself", &out)
	if err == nil {
		t.Fatal("expected an error for 401 response")
	}
	if !errors.Is(err, atlassian.ErrAuthFailed) {
		t.Errorf("err = %v, want wrapping atlassian.ErrAuthFailed", err)
	}
}

func TestGetJSON_Forbidden403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out map[string]any
	_, err := c.GetJSON(context.Background(), "/rest/api/3/myself", &out)
	if err == nil {
		t.Fatal("expected an error for 403 response")
	}
	if !errors.Is(err, atlassian.ErrAuthFailed) {
		t.Errorf("err = %v, want wrapping atlassian.ErrAuthFailed", err)
	}
}

func TestGetJSON_RateLimitRetriesAfterRetryAfterHeader(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out struct {
		OK bool `json:"ok"`
	}
	start := time.Now()
	_, err := c.GetJSON(context.Background(), "/rest/api/3/search", &out)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetJSON failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then one success)", attempts)
	}
	if !out.OK {
		t.Error("expected ok=true after retry")
	}
	if elapsed > 5*time.Second {
		t.Errorf("retry took %v, want a fast retry given Retry-After: 0", elapsed)
	}
}

func TestGetJSON_ContextCancelledDuringRetryWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var out map[string]any
	_, err := c.GetJSON(ctx, "/rest/api/3/search", &out)
	if err == nil {
		t.Fatal("expected context deadline error")
	}
	if !strings.Contains(err.Error(), "context") && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a context-cancellation related error", err)
	}
}

func TestGetJSON_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out map[string]any
	_, err := c.GetJSON(context.Background(), "/rest/api/3/search", &out)
	if err == nil {
		t.Fatal("expected an error for 500 response")
	}
}

// TestGetJSON_RateLimitRetryCapTerminates guards against an unbounded retry
// loop: a server that ALWAYS answers 429 with "Retry-After: 0" must not be
// retried forever (or busy-loop). GetJSON must give up after a small, fixed
// number of attempts and return an error, and must not exceed that request
// count against the server.
func TestGetJSON_RateLimitRetryCapTerminates(t *testing.T) {
	const wantMaxRequests = 5
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out map[string]any

	done := make(chan struct{})
	var err error
	go func() {
		_, err = c.GetJSON(context.Background(), "/rest/api/3/search", &out)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("GetJSON did not terminate — unbounded retry loop against a server that always 429s")
	}

	if err == nil {
		t.Fatal("expected an error after exceeding the retry cap")
	}
	if requests > wantMaxRequests {
		t.Errorf("requests = %d, want at most %d (retry cap must be enforced)", requests, wantMaxRequests)
	}
	if requests == 0 {
		t.Error("expected at least one request to have been made")
	}
}

// TestGetJSON_ResponseBodyExceedsSizeCapErrors guards against a
// huge/hostile response body exhausting memory: GetJSON must cap how much
// of the body it reads and error out instead of buffering everything.
//
// The oversized body is otherwise WELL-FORMED JSON (a single huge string
// field), not garbage — this matters: garbage bytes would already fail to
// unmarshal today for an unrelated reason (invalid JSON), which would make
// this test pass even without a real size cap. A valid-but-huge payload
// only errors once a cap is actually enforced.
// --- PostJSON ---
//
// Jira's modern Cloud search endpoint (POST /rest/api/3/search/jql) requires a
// JSON request body (the JQL query + nextPageToken), which GetJSON cannot send
// (GET only). PostJSON mirrors GetJSON's auth/retry/body-cap behavior for a
// POST request with a JSON body, so the jira adapter can reuse the same
// shared transport rather than duplicating the retry loop.

func TestPostJSON_SendsBodyAndDecodesResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[{"key":"ENG-1"}],"isLast":true}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	body := map[string]any{"jql": `project = "ENG"`, "maxResults": 100}
	var out struct {
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
		IsLast bool `json:"isLast"`
	}
	if _, err := c.PostJSON(context.Background(), "/rest/api/3/search/jql", body, &out); err != nil {
		t.Fatalf("PostJSON failed: %v", err)
	}
	if gotBody["jql"] != `project = "ENG"` {
		t.Errorf("server received jql = %v, want %q", gotBody["jql"], `project = "ENG"`)
	}
	if len(out.Issues) != 1 || out.Issues[0].Key != "ENG-1" {
		t.Errorf("decoded out.Issues = %+v, want one issue ENG-1", out.Issues)
	}
	if !out.IsLast {
		t.Error("expected IsLast = true")
	}
}

func TestPostJSON_SetsJSONContentType(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out map[string]any
	if _, err := c.PostJSON(context.Background(), "/anything", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatalf("PostJSON failed: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
}

func TestPostJSON_BasicAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "secret-token")
	var out map[string]any
	if _, err := c.PostJSON(context.Background(), "/anything", map[string]string{}, &out); err != nil {
		t.Fatalf("PostJSON failed: %v", err)
	}

	wantCreds := base64.StdEncoding.EncodeToString([]byte("user@example.com:secret-token"))
	want := "Basic " + wantCreds
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestPostJSON_Unauthorized401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "bad-token")
	var out map[string]any
	_, err := c.PostJSON(context.Background(), "/rest/api/3/search/jql", map[string]string{}, &out)
	if err == nil {
		t.Fatal("expected an error for 401 response")
	}
	if !errors.Is(err, atlassian.ErrAuthFailed) {
		t.Errorf("err = %v, want wrapping atlassian.ErrAuthFailed", err)
	}
}

func TestPostJSON_RateLimitRetriesAfterRetryAfterHeader(t *testing.T) {
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

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out struct {
		Issues []any `json:"issues"`
	}
	start := time.Now()
	_, err := c.PostJSON(context.Background(), "/rest/api/3/search/jql", map[string]string{"jql": "x"}, &out)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("PostJSON failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then one success)", attempts)
	}
	if elapsed > 5*time.Second {
		t.Errorf("retry took %v, want a fast retry given Retry-After: 0", elapsed)
	}
}

func TestGetJSON_ResponseBodyExceedsSizeCapErrors(t *testing.T) {
	const overCapBytes = 10*1024*1024 + 1024 // 1 KiB over a 10 MiB cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":"`))
		padding := make([]byte, overCapBytes)
		for i := range padding {
			padding[i] = 'a'
		}
		w.Write(padding)
		w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	c := atlassian.New(srv.URL, "user@example.com", "tok")
	var out struct {
		Data string `json:"data"`
	}
	_, err := c.GetJSON(context.Background(), "/rest/api/3/huge", &out)
	if err == nil {
		t.Fatal("expected an error for a response body over the size cap (unbounded reads must be rejected)")
	}
}
