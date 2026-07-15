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

// lenLimitedEmbedder mimics Ollama rejecting an over-long prompt (HTTP 500),
// so embedDocument's shrink-and-retry path can be exercised.
type lenLimitedEmbedder struct {
	limit int
	calls int
}

func (s *lenLimitedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	s.calls++
	if len([]rune(text)) > s.limit {
		return nil, fmt.Errorf("simulated ollama status 500 (input too long)")
	}
	return []float32{1, 0, 0}, nil
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 3); got != "hél" {
		t.Errorf("truncateRunes rune-safe: got %q, want %q", got, "hél")
	}
	if got := truncateRunes("ab", 5); got != "ab" {
		t.Errorf("truncateRunes shorter-than-n: got %q, want %q", got, "ab")
	}
}

func TestEmbedDocument_ShrinksOnFailure(t *testing.T) {
	// limit 1500 → budgets 4000 and 2000 fail, 1000 succeeds (3 calls).
	emb := &lenLimitedEmbedder{limit: 1500}
	vec, err := embedDocument(context.Background(), emb, strings.Repeat("x", 5000))
	if err != nil {
		t.Fatalf("embedDocument: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("vec dim: got %d, want 3", len(vec))
	}
	if emb.calls != 3 {
		t.Errorf("calls: got %d, want 3 (4000→2000→1000)", emb.calls)
	}
}

func TestEmbedDocument_AllBudgetsFail(t *testing.T) {
	emb := &lenLimitedEmbedder{limit: 10} // even 1000 runes is too long
	if _, err := embedDocument(context.Background(), emb, strings.Repeat("x", 5000)); err == nil {
		t.Error("expected error when every budget fails, got nil")
	}
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
