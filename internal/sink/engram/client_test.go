package engram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Velion-SpA/omnia/internal/core"
	engram "github.com/Velion-SpA/omnia/internal/sink/engram"
)

func TestClientWrite(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/observations":
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 42, "status": "saved"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := engram.New(srv.URL)
	ctx := context.Background()

	item := core.Item{
		Type:      "github-issue",
		Title:     "Test issue (#1)",
		Content:   "Some content here",
		Project:   "omnia-test",
		TopicKey:  "github/test/issue-1",
		FetchedAt: time.Now(),
	}

	if err := c.Write(ctx, item); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if gotBody["type"] != "github-issue" {
		t.Errorf("type = %v, want github-issue", gotBody["type"])
	}
	if gotBody["topic_key"] != "github/test/issue-1" {
		t.Errorf("topic_key = %v, want github/test/issue-1", gotBody["topic_key"])
	}
	if gotBody["project"] != "omnia-test" {
		t.Errorf("project = %v, want omnia-test", gotBody["project"])
	}
}

func TestClientHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer srv.Close()

	c := engram.New(srv.URL)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
}
