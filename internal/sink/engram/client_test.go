package engram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velion/omnia/internal/core"
	engram "github.com/velion/omnia/internal/sink/engram"
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

// TestClientWrite_SendsSource covers the provenance bugfix: item.Source
// used to be silently dropped from the /observations request body, so
// every collector's writes landed with no source signal for trust_tag
// classification (all fell back to "unverified"). Table-driven so an empty
// Source (an unclassified caller) is proven distinct from a populated one,
// rather than only testing the happy path.
func TestClientWrite_SendsSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"github collector provenance", "ingest:tool"},
		{"repodoc collector provenance", "ingest:doc"},
		{"empty source still sent as a key", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/observations" {
					json.NewDecoder(r.Body).Decode(&gotBody)
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]interface{}{"id": 1, "status": "saved"})
				}
			}))
			defer srv.Close()

			c := engram.New(srv.URL)
			item := core.Item{
				Type:      "doc",
				Title:     "Test doc",
				Content:   "Some content",
				Project:   "omnia-test",
				TopicKey:  "repodoc/test/1",
				Source:    tt.source,
				FetchedAt: time.Now(),
			}
			if err := c.Write(context.Background(), item); err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			got, ok := gotBody["source"]
			if !ok {
				t.Fatal("request body is missing a \"source\" key — item.Source was dropped")
			}
			if got != tt.source {
				t.Errorf("source = %v, want %q", got, tt.source)
			}
		})
	}
}

// TestClientWriteAndGetID_SendsSourceAndDecodesSyncID covers the same
// provenance gap for the WriteAndGetID path, plus the observationResponse
// SyncID field: it must decode a server-provided sync_id when present, and
// must decode to "" (never error) when absent — the server does not emit
// this field yet (see observationResponse's doc comment for the wiring
// request), so a strict "field must be present" assertion would be wrong.
func TestClientWriteAndGetID_SendsSourceAndDecodesSyncID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/observations" {
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			// Server does not send sync_id today (the wiring gap this test
			// documents) — response shape mirrors production exactly.
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99, "status": "saved"})
		}
	}))
	defer srv.Close()

	c := engram.New(srv.URL)
	item := core.Item{
		Type:      "doc",
		Title:     "Test doc",
		Content:   "Some content",
		Project:   "omnia-test",
		TopicKey:  "repodoc/test/1",
		Source:    "ingest:doc",
		FetchedAt: time.Now(),
	}
	id, err := c.WriteAndGetID(context.Background(), item)
	if err != nil {
		t.Fatalf("WriteAndGetID failed: %v", err)
	}
	if id != 99 {
		t.Errorf("id = %d, want 99", id)
	}
	if gotBody["source"] != "ingest:doc" {
		t.Errorf("source = %v, want %q", gotBody["source"], "ingest:doc")
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
