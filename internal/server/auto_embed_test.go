package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/embed"
)

// serverAutoEmbedSignalEmbedder signals the embedded text so a test can prove
// the enqueued job was processed by the running worker without a time.Sleep
// (mirrors internal/mcp/auto_embed_test.go's autoEmbedSignalEmbedder).
type serverAutoEmbedSignalEmbedder struct {
	ch chan string
}

func (e serverAutoEmbedSignalEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.ch <- text
	return []float32{1, 0, 0}, nil
}

// TestHandleAddObservation_AutoEmbedsWhenWorkerConfigured proves the ENABLED
// HTTP path (human-like-memory PR4 review fix — closing the test-coverage
// asymmetry between internal/mcp's enqueueAutoEmbed tests and the HTTP save
// path): POST /observations, with a worker wired via SetAutoEmbed, enqueues
// and embeds the just-saved memory out-of-band.
func TestHandleAddObservation_AutoEmbedsWhenWorkerConfigured(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)

	ctx, cancel := context.WithCancel(context.Background())

	embStore, err := embed.OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("open embed store: %v", err)
	}
	defer embStore.Close()

	sig := make(chan string, 1)
	worker := embed.NewWorker(embStore, serverAutoEmbedSignalEmbedder{ch: sig}, "m", 3, 8, nil)
	worker.Start(ctx)
	// A single t.Cleanup (not two separate defers) guarantees cancel() runs
	// before Stop() regardless of an early t.Fatal: Stop() blocks on the
	// worker's WaitGroup, which only resolves once ctx is cancelled.
	t.Cleanup(func() {
		cancel()
		worker.Stop()
	})
	srv.SetAutoEmbed(worker)

	h := srv.Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-auto-embed","project":"engram","directory":"/work/engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d body=%s", createRec.Code, createRec.Body.String())
	}

	obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"s-auto-embed","type":"decision","title":"Postgres index choice","content":"we picked a partial index","project":"engram"}`))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("expected observation create 201, got %d body=%s", obsRec.Code, obsRec.Body.String())
	}

	select {
	case text := <-sig:
		if !strings.Contains(text, "Postgres index choice") {
			t.Errorf("embedded text should include the saved title, got %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auto-embed did not process the saved memory within 5s")
	}
}

// TestHandleAddObservation_NoAutoEmbedWorkerStillSaves pins the disabled
// (default) path explicitly: with no SetAutoEmbed call, POST /observations
// still succeeds — no worker, no enqueue, the save path stays byte-for-byte
// today's.
func TestHandleAddObservation_NoAutoEmbedWorkerStillSaves(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-no-auto-embed","project":"engram","directory":"/work/engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d body=%s", createRec.Code, createRec.Body.String())
	}

	obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"s-no-auto-embed","type":"note","title":"t","content":"c","project":"engram"}`))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("expected observation create 201, got %d body=%s", obsRec.Code, obsRec.Body.String())
	}
}
