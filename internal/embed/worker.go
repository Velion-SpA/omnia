package embed

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/velion/omnia/internal/meta"
)

// defaultWorkerQueueSize bounds how many pending auto-embed jobs the worker
// buffers before it starts dropping. Dropped jobs are recovered by the
// periodic Reconcile backstop, so a full queue degrades gracefully rather than
// blocking a save.
const defaultWorkerQueueSize = 256

// Job is a single auto-embed request enqueued after a memory is saved. It
// carries plain fields (not a store or engramdb Observation) on purpose, so
// either save path — the MCP handler or the HTTP server — can build one
// without this package taking a dependency on theirs.
type Job struct {
	SyncID    string
	ObsID     int
	Project   string
	Type      string
	TopicKey  string
	Title     string
	Content   string
	UpdatedAt string
}

// Worker embeds saved memories out-of-band so the save path never blocks on
// the (network) embedding call. Enqueue is non-blocking: when the bounded
// queue is full the job is dropped rather than blocking the caller, and the
// periodic Reconcile backstop re-embeds anything dropped or failed. A failing
// embedder is isolated — logged and skipped, never crashing the worker or the
// caller.
type Worker struct {
	store    *Store
	emb      Embedder
	model    string
	dim      int
	queue    chan Job
	logger   *slog.Logger
	wg       sync.WaitGroup
	onUpsert UpsertHook
}

// UpsertHook is invoked after a successful local vector upsert, with the row
// that was just persisted. It exists so a composition root (e.g. cmd/omnia)
// can record a sync mutation for cloud propagation (human-like-memory PR5
// slice 2, cloud semantic parity) without this package importing
// internal/store or internal/sync — internal/embed only depends on the
// Ollama-backed Embedder and its own leaf Store (architecture-guardrails:
// keep plugin/adaptor layers thin; do not hide cross-system coupling inside
// this leaf package). A nil hook (the default) disables the behavior.
type UpsertHook func(row Row)

// SetUpsertHook installs hook, called after every successful local upsert
// performed by this worker's process loop. Safe to call before or after
// Start; a nil hook restores the no-op default.
func (w *Worker) SetUpsertHook(hook UpsertHook) { w.onUpsert = hook }

// Store returns the underlying embeddings store this worker embeds into
// (memory-provenance foundation, omnia-provenance-foundation). It exists so a
// caller that already holds a *Worker (e.g. internal/mcp's MCPConfig.AutoEmbed)
// can fan a physical purge out to the SAME store a save fanned into via
// Enqueue, without opening a second connection to the embeddings file or this
// package exposing broader write access. Mirrors enqueueAutoEmbed's existing
// cfg.AutoEmbed nil-check convention: callers must guard against a nil
// Worker themselves.
func (w *Worker) Store() *Store { return w.store }

// NewWorker builds an auto-embed worker. A queueSize <= 0 uses the default.
func NewWorker(store *Store, emb Embedder, model string, dim, queueSize int, logger *slog.Logger) *Worker {
	if queueSize <= 0 {
		queueSize = defaultWorkerQueueSize
	}
	return &Worker{
		store:  store,
		emb:    emb,
		model:  model,
		dim:    dim,
		queue:  make(chan Job, queueSize),
		logger: logger,
	}
}

// Start launches the background goroutine. It runs until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.run(ctx)
}

// Stop blocks until the background goroutine has exited. Call it after
// cancelling the context passed to Start.
func (w *Worker) Stop() { w.wg.Wait() }

func (w *Worker) run(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-w.queue:
			w.process(ctx, job)
		}
	}
}

// Enqueue submits a job without blocking. It returns false when the job is not
// embeddable (missing sync_id) or the queue is full (the job is dropped;
// Reconcile recovers it later). It never blocks the caller — keeping the save
// path fast is the whole point.
func (w *Worker) Enqueue(job Job) bool {
	if job.SyncID == "" {
		return false
	}
	select {
	case w.queue <- job:
		return true
	default:
		if w.logger != nil {
			w.logger.Debug("auto-embed queue full; dropping (reconcile backstop will recover)", "sync_id", job.SyncID)
		}
		return false
	}
}

// process embeds one job and upserts it. Mirrors Reconcile's per-row logic. A
// failed embed or upsert is logged and swallowed so one bad row never stops
// the worker.
func (w *Worker) process(ctx context.Context, job Job) {
	input := jobEmbedInput(job)
	if strings.TrimSpace(input) == "" {
		return
	}
	vec, err := embedDocument(ctx, w.emb, input)
	if err != nil {
		if w.logger != nil {
			w.logger.Warn("auto-embed failed; reconcile backstop will retry", "sync_id", job.SyncID, "err", err)
		}
		return
	}
	row := Row{
		SyncID:      job.SyncID,
		ObsID:       job.ObsID,
		Project:     job.Project,
		Type:        job.Type,
		TopicKey:    job.TopicKey,
		Title:       job.Title,
		UpdatedAt:   job.UpdatedAt,
		ContentHash: hashInput(input),
		Model:       w.model,
		Dim:         w.dim,
		Vector:      vec,
		EmbeddedAt:  nowUTC(),
	}
	if err := w.store.Upsert(ctx, row); err != nil {
		if w.logger != nil {
			w.logger.Warn("auto-embed upsert failed", "sync_id", job.SyncID, "err", err)
		}
		return
	}
	if w.onUpsert != nil {
		w.onUpsert(row)
	}
}

// jobEmbedInput mirrors embedInput (title + meta-stripped content) for a Job,
// so an auto-embedded row clusters identically to a Reconcile-embedded one.
func jobEmbedInput(job Job) string {
	body := strings.TrimSpace(meta.Strip(job.Content))
	title := strings.TrimSpace(job.Title)
	switch {
	case title == "":
		return body
	case body == "":
		return title
	default:
		return title + "\n\n" + body
	}
}
