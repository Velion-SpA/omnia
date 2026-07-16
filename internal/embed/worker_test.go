package embed

import (
	"context"
	"errors"
	"testing"
	"time"
)

// workerStubEmbedder returns a fixed vector (or error) synchronously. Used for
// the process()-level tests that run in the test goroutine.
type workerStubEmbedder struct {
	vec []float32
	err error
}

func (e *workerStubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.vec, e.err
}

// workerSignalEmbedder sends the embedded text on ch, so a test can prove the
// running worker goroutine dequeued and embedded a job without a sleep.
type workerSignalEmbedder struct {
	ch  chan string
	vec []float32
}

func (e *workerSignalEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.ch <- text
	return e.vec, nil
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func sampleJob() Job {
	return Job{
		SyncID:    "obs-abc",
		ObsID:     7,
		Project:   "velion",
		Type:      "decision",
		TopicKey:  "velion/x",
		Title:     "Title",
		Content:   "some content",
		UpdatedAt: "2026-07-16 10:00:00",
	}
}

func TestWorker_ProcessEmbedsAndUpserts(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{vec: []float32{1, 0, 0}}, "jina/jina-embeddings-v2-base-es", 3, 0, nil)

	w.process(ctx, sampleJob())

	stored, err := store.Stored(ctx)
	if err != nil {
		t.Fatalf("Stored: %v", err)
	}
	meta, ok := stored["obs-abc"]
	if !ok {
		t.Fatalf("expected sync_id obs-abc to be embedded, got %v", stored)
	}
	if meta.Model != "jina/jina-embeddings-v2-base-es" || meta.Dim != 3 {
		t.Errorf("stored meta: got model=%q dim=%d, want jina/... dim=3", meta.Model, meta.Dim)
	}
	if meta.ContentHash == "" {
		t.Error("expected a non-empty content hash")
	}
}

// TestWorker_ProcessInvokesUpsertHookOnSuccess (RED, human-like-memory PR5
// slice 2) asserts that a successful embed+upsert calls the installed
// UpsertHook exactly once with the row that was just persisted. This is the
// seam a composition root uses to record a sync mutation without this
// package importing internal/store or internal/sync (architecture-guardrails:
// internal/embed stays a leaf package).
func TestWorker_ProcessInvokesUpsertHookOnSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{vec: []float32{1, 0, 0}}, "jina/jina-embeddings-v2-base-es", 3, 0, nil)

	var gotRows []Row
	w.SetUpsertHook(func(row Row) { gotRows = append(gotRows, row) })

	w.process(ctx, sampleJob())

	if len(gotRows) != 1 {
		t.Fatalf("expected UpsertHook to be called exactly once; got %d calls", len(gotRows))
	}
	if gotRows[0].SyncID != "obs-abc" {
		t.Errorf("hook row SyncID: want %q, got %q", "obs-abc", gotRows[0].SyncID)
	}
	if gotRows[0].Model != "jina/jina-embeddings-v2-base-es" || gotRows[0].Dim != 3 {
		t.Errorf("hook row model/dim: got model=%q dim=%d", gotRows[0].Model, gotRows[0].Dim)
	}
}

// TestWorker_ProcessDoesNotInvokeHookOnEmbedFailure (RED, triangulation)
// asserts the hook is NOT called when the embed call fails (nothing was
// upserted, so nothing should be reported as upserted).
func TestWorker_ProcessDoesNotInvokeHookOnEmbedFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{err: errors.New("ollama down")}, "m", 3, 0, nil)

	called := false
	w.SetUpsertHook(func(row Row) { called = true })

	w.process(ctx, sampleJob())

	if called {
		t.Fatal("expected UpsertHook NOT to be called when embed failed")
	}
}

// TestWorker_ProcessWithNilHookDoesNotPanic (RED, triangulation) asserts the
// default (no hook installed) path is safe — the common case for every
// existing caller/test that never calls SetUpsertHook.
func TestWorker_ProcessWithNilHookDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{vec: []float32{1, 0, 0}}, "m", 3, 0, nil)

	w.process(ctx, sampleJob()) // must not panic with no hook installed
}

func TestWorker_ProcessEmbedErrorIsolated(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{err: errors.New("ollama down")}, "m", 3, 0, nil)

	// Must not panic and must not upsert anything on embed failure.
	w.process(ctx, sampleJob())

	stored, err := store.Stored(ctx)
	if err != nil {
		t.Fatalf("Stored: %v", err)
	}
	if _, ok := stored["obs-abc"]; ok {
		t.Fatal("embed failed, so nothing should have been upserted")
	}
}

func TestWorker_ProcessEmptyContentSkipped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	w := NewWorker(store, &workerStubEmbedder{vec: []float32{1, 0, 0}}, "m", 3, 0, nil)

	job := sampleJob()
	job.Title = ""
	job.Content = "   "
	w.process(ctx, job)

	stored, err := store.Stored(ctx)
	if err != nil {
		t.Fatalf("Stored: %v", err)
	}
	if _, ok := stored["obs-abc"]; ok {
		t.Fatal("empty content should not be embedded")
	}
}

func TestWorker_EnqueueEmptySyncIDRejected(t *testing.T) {
	w := NewWorker(newTestStore(t), &workerStubEmbedder{vec: []float32{1, 0, 0}}, "m", 3, 8, nil)
	job := sampleJob()
	job.SyncID = ""
	if w.Enqueue(job) {
		t.Fatal("Enqueue with empty sync_id should return false")
	}
}

func TestWorker_EnqueueNonBlockingWhenFull(t *testing.T) {
	// A queue of size 1, worker NOT started, so nothing drains it. The first
	// enqueue fills the buffer; the second must be dropped (false) rather than
	// blocking the caller.
	w := NewWorker(newTestStore(t), &workerStubEmbedder{vec: []float32{1, 0, 0}}, "m", 3, 1, nil)

	if !w.Enqueue(sampleJob()) {
		t.Fatal("first Enqueue should succeed")
	}
	second := sampleJob()
	second.SyncID = "obs-def"
	if w.Enqueue(second) {
		t.Fatal("second Enqueue on a full queue should be dropped (false), not block")
	}
}

func TestWorker_StartProcessesEnqueuedJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan string, 1)
	w := NewWorker(newTestStore(t), &workerSignalEmbedder{ch: sig, vec: []float32{1, 0, 0}}, "m", 3, 8, nil)
	w.Start(ctx)

	if !w.Enqueue(sampleJob()) {
		t.Fatal("Enqueue should succeed")
	}

	select {
	case text := <-sig:
		if text == "" {
			t.Error("worker embedded empty text")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not process the enqueued job within 5s")
	}

	cancel()
	w.Stop()
}

func TestWorker_StartStopNoLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := NewWorker(newTestStore(t), &workerStubEmbedder{vec: []float32{1, 0, 0}}, "m", 3, 8, nil)
	w.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after ctx cancel — goroutine leak or deadlock")
	}
}
