package server

// answer_test.go — GET /answer wiring tests (P3, docs/conversational-
// retrieval-plan.md "Answer-shaped context endpoint"). Mirrors server_test.go's
// own handleSearch test shape (TestHandleSearchUsesInjectedSearchFunc et al.)
// but for AnswerFunc/SetAnswer — this package only tests the HTTP plumbing
// (query param parsing, JSON shape, the no-fallback 503), never the
// assembly/confidence LOGIC itself (that lives in internal/mcp/answer_test.go,
// which this package deliberately does not import — see AnswerFunc's own doc
// comment for why).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleAnswerReturns503WhenSetAnswerNeverCalled: unlike GET /search,
// GET /answer has no meaningful FTS5-only fallback — its entire contract is
// a calibrated confidence signal, so an unwired server must fail loudly
// rather than silently claim a confidence it never computed.
func TestHandleAnswerReturns503WhenSetAnswerNeverCalled(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/answer?q=hello", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when SetAnswer was never called, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAnswerRequiresQuery mirrors handleSearch's own "q parameter is
// required" 400.
func TestHandleAnswerRequiresQuery(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()
	srv.SetAnswer(func(ctx context.Context, query string, req AnswerRequest) (AnswerResponse, error) {
		t.Fatal("AnswerFunc must not be called when q is missing")
		return AnswerResponse{}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/answer", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing q, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAnswerUsesInjectedAnswerFunc proves GET /answer routes through
// whatever AnswerFunc cmd/omnia wired via SetAnswer, forwards every query
// param into AnswerRequest, and returns the AnswerFunc's response verbatim
// as JSON — including that sources are cited by sync_id only.
func TestHandleAnswerUsesInjectedAnswerFunc(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var gotQuery string
	var gotReq AnswerRequest
	sentinel := AnswerResponse{
		Context:    "assembled answer text",
		Confidence: "high",
		Intent:     "identity",
		Sources:    []AnswerSource{{SyncID: "obs-abc123", Title: "README", Type: "doc", UpdatedAt: "2026-08-01"}},
		Degraded:   false,
	}
	srv.SetAnswer(func(ctx context.Context, query string, req AnswerRequest) (AnswerResponse, error) {
		gotQuery = query
		gotReq = req
		return sentinel, nil
	})

	httpReq := httptest.NewRequest(http.MethodGet, "/answer?q=what+is+omnia&project=omnia&scope=project&limit=5&max_chars=800", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotQuery != "what is omnia" {
		t.Fatalf("expected injected AnswerFunc to receive query %q, got %q", "what is omnia", gotQuery)
	}
	if gotReq.Project != "omnia" || gotReq.Scope != "project" || gotReq.Limit != 5 {
		t.Fatalf("expected query params forwarded into AnswerRequest.SearchOptions, got %+v", gotReq)
	}
	if gotReq.MaxChars != 800 {
		t.Fatalf("expected max_chars=800 forwarded into AnswerRequest.MaxChars, got %d", gotReq.MaxChars)
	}

	var got AnswerResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode answer response: %v", err)
	}
	if got.Context != sentinel.Context || got.Confidence != sentinel.Confidence || got.Intent != sentinel.Intent || got.Degraded != sentinel.Degraded {
		t.Fatalf("expected the injected AnswerFunc's own response returned verbatim, got %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0].SyncID != "obs-abc123" {
		t.Fatalf("expected sources cited by sync_id, got %+v", got.Sources)
	}
}

// TestHandleAnswerDefaultsMaxCharsToZeroWhenOmitted proves an absent
// max_chars query param forwards as MaxChars 0 — it is the AnswerFunc
// implementation's own job (cmd/omnia's buildHTTPAnswerFunc, via
// config.AnswerConfig.MaxChars) to apply the 1400-char default, not
// handleAnswer's.
func TestHandleAnswerDefaultsMaxCharsToZeroWhenOmitted(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var gotReq AnswerRequest
	srv.SetAnswer(func(ctx context.Context, query string, req AnswerRequest) (AnswerResponse, error) {
		gotReq = req
		return AnswerResponse{Sources: []AnswerSource{}}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/answer?q=hello", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotReq.MaxChars != 0 {
		t.Fatalf("expected MaxChars 0 when max_chars= is absent, got %d", gotReq.MaxChars)
	}
}

// TestHandleAnswerSurfacesInjectedAnswerFuncError mirrors
// TestHandleSearchSurfacesInjectedSearchFuncError.
func TestHandleAnswerSurfacesInjectedAnswerFuncError(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	srv.SetAnswer(func(ctx context.Context, query string, req AnswerRequest) (AnswerResponse, error) {
		return AnswerResponse{}, errors.New("forced answer func error")
	})

	req := httptest.NewRequest(http.MethodGet, "/answer?q=hello", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
