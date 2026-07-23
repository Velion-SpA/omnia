package embed

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/engramdb"
)

const engramSchema = `CREATE TABLE observations (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	sync_id        TEXT,
	session_id     TEXT,
	type           TEXT,
	title          TEXT NOT NULL DEFAULT '',
	content        TEXT,
	tool_name      TEXT,
	project        TEXT,
	scope          TEXT,
	topic_key      TEXT,
	revision_count INTEGER DEFAULT 0,
	created_at     TEXT,
	updated_at     TEXT,
	deleted_at     TEXT
)`

// metaOnlyContent is a valid omnia-meta block with no human body — embedInput
// must strip it to empty and Reconcile must skip the row.
const metaOnlyContent = "```omnia-meta\n" +
	"schema_version: 1\n" +
	"source: github\n" +
	"kind: issue\n" +
	"layer: ingested\n" +
	"project: p1\n" +
	"```\n"

type stubEmbedder struct{ calls int }

func (s *stubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	s.calls++
	return []float32{1, 0, 0}, nil // valid unit vector, dim 3
}

// callCountingEmbedder records every text it was asked to embed, so tests can
// assert embedDocument makes exactly one call regardless of input length (the
// server-side truncate:true + options.num_ctx:8192 in the /api/embed client
// now replace the old client-side shrinking-rune-budget retry loop).
type callCountingEmbedder struct {
	calls int
	texts []string
}

func (s *callCountingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	s.calls++
	s.texts = append(s.texts, text)
	return []float32{1, 0, 0}, nil
}

// TestEmbedDocument_SingleCallNoShrinkRetry guards the removal of the old
// embedBudgets/truncateRunes shrink-and-retry loop: a memory far larger than
// any of the former budgets (4000/2000/1000 runes) must embed in exactly ONE
// Embed call, with the full untruncated text passed through — truncation, if
// any, is now the server's job (truncate:true).
func TestEmbedDocument_SingleCallNoShrinkRetry(t *testing.T) {
	emb := &callCountingEmbedder{}
	longInput := strings.Repeat("x", 5000)

	vec, err := embedDocument(context.Background(), emb, longInput)
	if err != nil {
		t.Fatalf("embedDocument: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("vec dim: got %d, want 3", len(vec))
	}
	if emb.calls != 1 {
		t.Errorf("calls: got %d, want 1 (no client-side shrink retry)", emb.calls)
	}
	if len(emb.texts) == 1 && emb.texts[0] != longInput {
		t.Errorf("input: got truncated to %d runes, want the full %d-rune input passed through", len([]rune(emb.texts[0])), len([]rune(longInput)))
	}
}

// TestEmbedDocument_PropagatesEmbedError guards that a single Ollama failure
// surfaces directly (no retry loop to hide behind).
func TestEmbedDocument_PropagatesEmbedError(t *testing.T) {
	emb := &erroringEmbedder{}
	if _, err := embedDocument(context.Background(), emb, "some content"); err == nil {
		t.Error("expected the embed error to propagate, got nil")
	}
	if emb.calls != 1 {
		t.Errorf("calls: got %d, want 1 (no retry loop)", emb.calls)
	}
}

type erroringEmbedder struct{ calls int }

func (s *erroringEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	s.calls++
	return nil, fmt.Errorf("simulated ollama failure")
}

// flakyEmbedder errors only for the exact texts listed in failFor, so a
// migration test can simulate an Ollama outage partway through a re-embed
// run — some rows succeed, one fails — without aborting the whole run
// (Reconcile already tolerates per-row errors; see Stats.Errors).
type flakyEmbedder struct{ failFor map[string]bool }

func (f *flakyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.failFor[text] {
		return nil, fmt.Errorf("simulated ollama failure mid-migration")
	}
	return []float32{1, 0, 0}, nil
}

func execEngram(t *testing.T, path, query string, args ...any) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open rwc: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func newReconcileEnv(t *testing.T) (*engramdb.DB, *Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	raw, err := sql.Open("sqlite", "file:"+dbPath+"?mode=rwc")
	if err != nil {
		t.Fatalf("create engram.db: %v", err)
	}
	if _, err := raw.Exec(engramSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	insert := `INSERT INTO observations (sync_id, type, title, content, project, scope, updated_at, deleted_at)
	           VALUES (?, ?, ?, ?, ?, 'project', ?, ?)`
	rows := []struct {
		syncID, typ, title, content, project, updatedAt string
		deletedAt                                       any
	}{
		{"obs-a", "decision", "Alpha", "alpha content body", "p1", "2024-01-02 00:00:00", nil},
		{"obs-b", "decision", "Beta", "beta content body", "p1", "2024-01-03 00:00:00", nil},
		{"obs-c", "issue", "", metaOnlyContent, "p1", "2024-01-04 00:00:00", nil},       // meta-only → skipped
		{"", "decision", "NoSync", "missing sync id", "p1", "2024-01-05 00:00:00", nil}, // no sync_id → skipped
	}
	for _, r := range rows {
		if _, err := raw.Exec(insert, r.syncID, r.typ, r.title, r.content, r.project, r.updatedAt, r.deletedAt); err != nil {
			t.Fatalf("insert %s: %v", r.syncID, err)
		}
	}
	raw.Close()

	reader, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("engramdb.Open: %v", err)
	}
	t.Cleanup(func() { reader.Close() })

	store, err := OpenStore(filepath.Join(dir, "emb.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return reader, store, dbPath
}

func TestReconcile_Lifecycle(t *testing.T) {
	ctx := context.Background()
	reader, store, dbPath := newReconcileEnv(t)
	emb := &stubEmbedder{}

	// Phase 1: first run embeds the 2 embeddable rows; skips meta-only + no-sync_id.
	s, err := Reconcile(ctx, reader, store, emb, "m1", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if s.Embedded != 2 || s.Reused != 0 || s.Skipped != 2 || s.Pruned != 0 {
		t.Fatalf("run 1 stats: %+v, want embedded=2 reused=0 skipped=2 pruned=0", s)
	}
	if n, _ := store.Count(ctx); n != 2 {
		t.Fatalf("store count after run 1: got %d, want 2", n)
	}

	// Phase 2: idempotent — no changes, everything reused.
	s, err = Reconcile(ctx, reader, store, emb, "m1", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if s.Embedded != 0 || s.Reused != 2 {
		t.Fatalf("run 2 stats: %+v, want embedded=0 reused=2", s)
	}

	// Phase 3: edit obs-a content → only it re-embeds.
	execEngram(t, dbPath, `UPDATE observations SET content = ? WHERE sync_id = 'obs-a'`, "alpha content CHANGED")
	s, err = Reconcile(ctx, reader, store, emb, "m1", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	if s.Embedded != 1 || s.Reused != 1 {
		t.Fatalf("run 3 stats: %+v, want embedded=1 reused=1", s)
	}

	// Phase 4: soft-delete obs-b → pruned from the store.
	execEngram(t, dbPath, `UPDATE observations SET deleted_at = '2024-02-01 00:00:00' WHERE sync_id = 'obs-b'`)
	s, err = Reconcile(ctx, reader, store, emb, "m1", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 4: %v", err)
	}
	if s.Pruned != 1 {
		t.Fatalf("run 4 pruned: got %d, want 1", s.Pruned)
	}
	if n, _ := store.Count(ctx); n != 1 {
		t.Fatalf("store count after prune: got %d, want 1", n)
	}

	// Phase 5: model change re-embeds the remaining row.
	s, err = Reconcile(ctx, reader, store, emb, "m2", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 5: %v", err)
	}
	if s.Embedded != 1 {
		t.Fatalf("run 5 (model change): %+v, want embedded=1", s)
	}
}

func TestReconcile_ForceReembedsAll(t *testing.T) {
	ctx := context.Background()
	reader, store, _ := newReconcileEnv(t)
	emb := &stubEmbedder{}

	if _, err := Reconcile(ctx, reader, store, emb, "m1", 3, false, nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	s, err := Reconcile(ctx, reader, store, emb, "m1", 3, true, nil) // force
	if err != nil {
		t.Fatalf("reconcile force: %v", err)
	}
	if s.Embedded != 2 || s.Reused != 0 {
		t.Fatalf("force stats: %+v, want embedded=2 reused=0", s)
	}
}

// TestReconcile_DimChangeTriggersFullReembed is EMBM-6's migration lock: when
// the configured Model stays the same but the effective Dim changes (a
// Matryoshka dimension change, EMBM-4), the next Reconcile run must re-embed
// EVERY live row — none reused — because every stored row's Dim no longer
// matches the configured value.
func TestReconcile_DimChangeTriggersFullReembed(t *testing.T) {
	ctx := context.Background()
	reader, store, _ := newReconcileEnv(t)
	emb := &stubEmbedder{}

	// First run at dim 768 (e.g. EmbeddingGemma native).
	s, err := Reconcile(ctx, reader, store, emb, "embeddinggemma:300m", 768, false, nil)
	if err != nil {
		t.Fatalf("reconcile (dim 768): %v", err)
	}
	if s.Embedded != 2 || s.Reused != 0 {
		t.Fatalf("initial reconcile: got %+v, want embedded=2 reused=0", s)
	}

	// Same model, dim changes to 256 (Matryoshka truncation config change):
	// every live row must be re-embedded, none reused.
	s, err = Reconcile(ctx, reader, store, emb, "embeddinggemma:300m", 256, false, nil)
	if err != nil {
		t.Fatalf("reconcile (dim 256): %v", err)
	}
	if s.Reused != 0 {
		t.Errorf("dim-change reconcile: Reused got %d, want 0 (a dim change must force a full re-embed)", s.Reused)
	}
	if s.Embedded != 2 {
		t.Errorf("dim-change reconcile: Embedded got %d, want 2", s.Embedded)
	}
}

// TestReconcile_InterruptedMigrationIsResumable is EMBM-6's resumability
// lock: if a Reconcile run into a new model/dim is interrupted mid-run (one
// row's embed call fails, simulating an Ollama outage), the row that
// succeeded stays migrated at the new model/dim, and a subsequent Reconcile
// run reuses it while re-embedding ONLY the row still mismatched.
func TestReconcile_InterruptedMigrationIsResumable(t *testing.T) {
	ctx := context.Background()
	reader, store, _ := newReconcileEnv(t)
	stable := &stubEmbedder{}

	// Baseline at the old model/dim (both live rows embedded there).
	if _, err := Reconcile(ctx, reader, store, stable, "jina/jina-embeddings-v2-base-es", 768, false, nil); err != nil {
		t.Fatalf("baseline reconcile: %v", err)
	}

	// Migrate to a new model/dim, but the embedder fails for obs-b's exact
	// embed input, simulating an Ollama outage partway through the
	// migration — obs-a succeeds and lands on the new model/dim; obs-b does
	// not.
	flaky := &flakyEmbedder{failFor: map[string]bool{"Beta\n\nbeta content body": true}}
	s, err := Reconcile(ctx, reader, store, flaky, "embeddinggemma:300m", 256, false, nil)
	if err != nil {
		t.Fatalf("interrupted reconcile: %v", err)
	}
	if s.Embedded != 1 || s.Errors != 1 {
		t.Fatalf("interrupted migration stats: %+v, want embedded=1 errors=1", s)
	}

	// Resume with a stable embedder: obs-a is already migrated (reused,
	// its stored Model/Dim already match), only obs-b (still mismatched
	// because its embed failed above) re-embeds.
	s, err = Reconcile(ctx, reader, store, stable, "embeddinggemma:300m", 256, false, nil)
	if err != nil {
		t.Fatalf("resume reconcile: %v", err)
	}
	if s.Reused != 1 {
		t.Errorf("resume reconcile: Reused got %d, want 1 (obs-a already migrated)", s.Reused)
	}
	if s.Embedded != 1 {
		t.Errorf("resume reconcile: Embedded got %d, want 1 (only obs-b, still mismatched)", s.Embedded)
	}
}

// TestReconcile_EmptiedContentPrunesStaleEmbedding guards the W2-review fix:
// when a previously-embedded row's content becomes meta-only (no embeddable
// body), its stale vector must be pruned rather than lingering in the store.
func TestReconcile_EmptiedContentPrunesStaleEmbedding(t *testing.T) {
	ctx := context.Background()
	reader, store, dbPath := newReconcileEnv(t)
	emb := &stubEmbedder{}

	// First run embeds obs-a + obs-b (2 rows).
	if _, err := Reconcile(ctx, reader, store, emb, "m1", 3, false, nil); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if n, _ := store.Count(ctx); n != 2 {
		t.Fatalf("store count after run 1: got %d, want 2", n)
	}

	// obs-a's content is replaced with a meta-only block (no human body).
	execEngram(t, dbPath, `UPDATE observations SET title = '', content = ? WHERE sync_id = 'obs-a'`, metaOnlyContent)
	s, err := Reconcile(ctx, reader, store, emb, "m1", 3, false, nil)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if s.Pruned != 1 {
		t.Errorf("emptied-content row: pruned got %d, want 1", s.Pruned)
	}
	if n, _ := store.Count(ctx); n != 1 {
		t.Errorf("store count after emptied content: got %d, want 1 (obs-a pruned)", n)
	}
}
