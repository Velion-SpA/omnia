package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// autoEmbedSignalEmbedder signals the embedded text so a test can prove the
// enqueued job was processed by the running worker without a sleep.
type autoEmbedSignalEmbedder struct {
	ch chan string
}

func (e autoEmbedSignalEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.ch <- text
	return []float32{1, 0, 0}, nil
}

// TestEnqueueAutoEmbed_NilWorkerIsNoop pins the disabled/default path: with no
// worker configured, enqueueAutoEmbed must be a safe no-op — the save path
// stays exactly today's, with no embedding side effect and no panic.
func TestEnqueueAutoEmbed_NilWorkerIsNoop(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-ae", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-ae", Type: "manual", Title: "t", Content: "c",
		Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	enqueueAutoEmbed(MCPConfig{}, s, id) // AutoEmbed nil — must not panic.
}

// TestEnqueueAutoEmbed_EnqueuesSavedMemory proves that when a worker IS
// configured, saving a memory enqueues it for out-of-band embedding, and the
// worker embeds the saved title+content. This is the auto-embed-on-save wiring
// (human-like-memory PR4): a new memory becomes semantically searchable
// without a manual `omnia embed` run.
func TestEnqueueAutoEmbed_EnqueuesSavedMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := newMCPTestStore(t)
	if err := s.CreateSession("s-ae", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-ae", Type: "decision", Title: "Postgres index choice",
		Content: "we picked a partial index", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	embStore, err := embed.OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("open embed store: %v", err)
	}
	defer embStore.Close()
	sig := make(chan string, 1)
	worker := embed.NewWorker(embStore, autoEmbedSignalEmbedder{ch: sig}, "m", 3, 8, nil)
	worker.Start(ctx)

	enqueueAutoEmbed(MCPConfig{AutoEmbed: worker}, s, id)

	select {
	case text := <-sig:
		if !strings.Contains(text, "Postgres index choice") {
			t.Errorf("embedded text should include the saved title, got %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auto-embed did not process the saved memory within 5s")
	}

	cancel()
	worker.Stop()
}
