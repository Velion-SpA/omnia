package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
	_ "modernc.org/sqlite"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr for the duration of fn, returning
// everything written to it — mirrors cmd/omnia's own captureOutput helper
// (same os.Pipe technique), reimplemented here since internal/consolidate
// has no existing test-output-capture seam of its own.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	_ = r.Close()
	return string(out)
}

func setupRun(t *testing.T, reachable bool) (*store.Store, *embed.Store, *embed.Client, config.ConsolidationConfig, []int64) {
	t.Helper()
	cfg, _ := store.DefaultConfig()
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CreateSession("s", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	es, err := embed.OpenStore(t.TempDir() + "/embeddings.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { es.Close() })
	ids := []int64{}
	for i := 0; i < 3; i++ {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "s", Type: "note", Title: fmt.Sprintf("memory %d", i), Content: fmt.Sprintf("source %d", i), Project: "p", Scope: "project"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		o, _ := s.GetObservation(id)
		if err := es.Upsert(context.Background(), embed.Row{SyncID: o.SyncID, ObsID: int(id), Project: "p", Title: o.Title, Vector: []float32{1, 0}, ContentHash: o.SyncID, Model: "m", Dim: 2, EmbeddedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reachable {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"digest"}}`))
	}))
	t.Cleanup(h.Close)
	return s, es, embed.New(h.URL, "m", 0), config.ConsolidationConfig{Enabled: true, MinScore: .5, K: 8, MinClusterSize: 3, MaxClusterSize: 12, Model: "m"}, ids
}
func TestRunWritesDigestAndSourceRelations(t *testing.T) {
	s, es, c, cfg, ids := setupRun(t, true)
	n, err := Run(context.Background(), s, es, c, cfg, "p")
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	r, err := s.Search("digest", store.SearchOptions{Project: "p"})
	if err != nil || len(r) == 0 || r[0].Type != "digest" {
		t.Fatalf("digest %#v %v", r, err)
	}
	rels, err := s.GetRelationsForObservations([]string{r[0].SyncID})
	if err != nil || len(rels[r[0].SyncID].AsSource) != 3 {
		t.Fatalf("relations %#v %v", rels, err)
	}
	for _, id := range ids {
		if _, err := s.GetObservation(id); err != nil {
			t.Fatalf("source missing: %v", err)
		}
	}
}
func TestRunDegradesWhenOllamaUnreachable(t *testing.T) {
	s, es, c, cfg, _ := setupRun(t, false)
	n, err := Run(context.Background(), s, es, c, cfg, "p")
	if err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	r, _ := s.Search("digest", store.SearchOptions{Project: "p"})
	if len(r) != 0 {
		t.Fatalf("digest written")
	}
}
func TestRunDisabledIsNoop(t *testing.T) {
	s, es, c, cfg, _ := setupRun(t, true)
	cfg.Enabled = false
	n, err := Run(context.Background(), s, es, c, cfg, "p")
	if err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	r, _ := s.Search("digest", store.SearchOptions{Project: "p"})
	if len(r) != 0 {
		t.Fatal("digest written")
	}
}

// TestRun_AppendsActionConsolidateAuditEntry (v0.4 memory-at-rest-security,
// spec REQ-437 cross-phase check: "every consolidation action (capability
// 3) MUST produce a corresponding entry in the native audit log"): confirms
// the ActionConsolidate audit.Append call already wired in Run (ADR-7)
// actually produces exactly one entry per written digest, correlated to
// the digest's own observation ID. Isolates HOME so this exercises the
// REAL audit.Append (not a test seam) without touching the developer's own
// audit.jsonl, mirroring TestHandleSave_AuditAppendFailure_DoesNotBlockSave's
// (internal/mcp/provenance_test.go) isolation convention.
func TestRun_AppendsActionConsolidateAuditEntry(t *testing.T) {
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	s, es, c, cfg, _ := setupRun(t, true)
	n, err := Run(context.Background(), s, es, c, cfg, "p")
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	r, err := s.Search("digest", store.SearchOptions{Project: "p"})
	if err != nil || len(r) == 0 || r[0].Type != "digest" {
		t.Fatalf("digest %#v %v", r, err)
	}

	entries, err := audit.EntriesForObservation(int(r[0].ID))
	if err != nil {
		t.Fatalf("EntriesForObservation: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry for the digest, got %d: %+v", len(entries), entries)
	}
	if entries[0].Action != audit.ActionConsolidate {
		t.Errorf("Action = %q, want %q", entries[0].Action, audit.ActionConsolidate)
	}
	if entries[0].Result != "ok" {
		t.Errorf("Result = %q, want %q", entries[0].Result, "ok")
	}
}

// TestRun_ReturnedCountMatchesActualDigestRowsWritten is finding #2's core
// regression test: `omnia consolidate`'s printed "wrote N digests" must
// always equal the number of NEW type='digest' rows Run actually persisted
// this run — real E2E testing found a run that printed "wrote 19 digests"
// while the store ended up with 25 new digest rows.
//
// Root cause (found by direct reproduction, not by inspecting
// cluster.go's splitting math in isolation): Run's digest title
// ("Consolidated memory digest") is a fixed literal, identical across
// EVERY cluster. Every digest write goes through the store's ALWAYS-ON
// dedupe-hash-window check (internal/store's SaveObservation, predates
// write-hygiene) keyed on normalized content + project + scope + type +
// title. When cfg.MaxClusterSize forces one oversized connected component
// to split into several balanced sub-clusters (cluster.go's Clusters), and
// the LLM-generated digest content for two or more of those sub-clusters
// happens to normalize to the SAME hash within the dedupe window — a real
// possibility with short/templated model output, and deterministic here
// via a mock LLM that always returns the same text — SaveObservation
// silently reuses the FIRST sub-cluster's existing row (Decision: "noop")
// instead of inserting a new one. Run's old written++/audit.Append fired
// on every loop iteration regardless, so the printed/returned count
// counted phantom digests that were never actually written.
//
// This test builds 30 near-duplicate source observations (guaranteeing one
// oversized connected component well above MaxClusterSize=12, forcing a
// split into multiple sub-clusters) against a mock LLM that always returns
// identical digest text, then asserts Run's returned count equals the
// actual number of type='digest' rows in the store.
func TestRun_ReturnedCountMatchesActualDigestRowsWritten(t *testing.T) {
	cfg, _ := store.DefaultConfig()
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("s", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	es, err := embed.OpenStore(t.TempDir() + "/embeddings.db")
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	const n = 30
	for i := 0; i < n; i++ {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "s", Type: "note", Title: fmt.Sprintf("memory %d", i), Content: fmt.Sprintf("source %d", i), Project: "p", Scope: "project"})
		if err != nil {
			t.Fatal(err)
		}
		o, _ := s.GetObservation(id)
		if err := es.Upsert(context.Background(), embed.Row{SyncID: o.SyncID, ObsID: int(id), Project: "p", Title: o.Title, Vector: []float32{1, 0}, ContentHash: o.SyncID, Model: "m", Dim: 2, EmbeddedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}

	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"digest"}}`))
	}))
	defer h.Close()

	consCfg := config.ConsolidationConfig{Enabled: true, MinScore: .5, K: 40, MinClusterSize: 3, MaxClusterSize: 12, Model: "m"}
	written, err := Run(context.Background(), s, es, embed.New(h.URL, "m", 0), consCfg, "p")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "omnia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var actual int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE type = 'digest' AND deleted_at IS NULL`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if written != actual {
		t.Fatalf("MISMATCH (finding #2): Run returned written=%d but the DB has %d actual digest rows", written, actual)
	}
}

// TestRun_UpdateDecisionIsCountedAndLinked is finding #3's (adversarial
// review of the finding #2 fix) regression test: Run's post-save guard
// treated WriteGateDecisionUpdate exactly like WriteGateDecisionNoop
// ("continue", skip counting/auditing/relation-linking). Unlike Noop, Update
// performs a REAL `UPDATE ... WHERE id = ?` that overwrites an EXISTING
// digest row's title/content/normalized_hash in place (store.go's
// evaluateWriteGate Update branch) — a genuine write that was silently
// dropped from the count/audit, AND whose pre-existing `consolidates`
// relations (from whichever cluster last owned that row) were left pointing
// at content that no longer matched, while the NEW cluster that caused the
// update was never linked at all.
//
// This test forces two clusters through Run in the same call: the first
// writes a brand-new digest (Save). The second's LLM-mocked content is a
// near-duplicate of the first (Jaccard strictly between UpdateThreshold=0.9
// and NoopThreshold=0.98, same technique internal/store's own
// TestSaveObservation_LadderAutoUpdate uses to force this exact decision
// deterministically) — write-hygiene's ladder classifies it as Update
// against the first cluster's just-written row (same literal digest title,
// matched via evaluateWriteGate's FTS candidate search). Asserts: (1) the
// Update write IS counted in Run's returned total and audited, and (2) the
// second cluster's 3 sources ARE linked via a `consolidates` relation to the
// (now-updated) row — same observation ID/sync_id as the first cluster's
// digest, confirmed via SaveResult.ID's documented "existing/matched row's
// ID for update" contract.
func TestRun_UpdateDecisionIsCountedAndLinked(t *testing.T) {
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	cfg, _ := store.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.WriteHygieneEnabled = true
	cfg.NoopThreshold = 0.98
	cfg.UpdateThreshold = 0.9
	cfg.ShrinkGuard = 0.9
	cfg.CandidateLimit = 10
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("s", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	es, err := embed.OpenStore(t.TempDir() + "/embeddings.db")
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	// Two orthogonal (cosine similarity 0), well-separated vector groups —
	// same technique setupRun already uses for one cluster — guarantee two
	// disjoint connected components (MinScore=.5 well above 0), each exactly
	// at MinClusterSize=3. Insertion order (A before B) gives group A's
	// observation IDs the lower range, so Clusters' out[0].ObsID-ascending
	// sort processes A first regardless of map iteration order.
	addGroup := func(label string, vector []float32) {
		for i := 0; i < 3; i++ {
			id, err := s.AddObservation(store.AddObservationParams{SessionID: "s", Type: "note", Title: fmt.Sprintf("%s memory %d", label, i), Content: fmt.Sprintf("%s source %d", label, i), Project: "p", Scope: "project"})
			if err != nil {
				t.Fatal(err)
			}
			o, _ := s.GetObservation(id)
			if err := es.Upsert(context.Background(), embed.Row{SyncID: o.SyncID, ObsID: int(id), Project: "p", Title: o.Title, Vector: vector, ContentHash: o.SyncID, Model: "m", Dim: 2, EmbeddedAt: "now"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	addGroup("groupA", []float32{1, 0})
	addGroup("groupB", []float32{0, 1})

	// Jaccard(original, revision) lands strictly between 0.9 and 0.98 — the
	// exact fixture internal/store's TestSaveObservation_LadderAutoUpdate
	// uses to pin the same Update decision deterministically. Same length
	// class (one word swapped near the end) trivially satisfies the
	// shrink-guard too.
	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	revision := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical rollback path"

	var callCount int
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		content := original
		if callCount > 1 {
			content = revision
		}
		body, err := json.Marshal(map[string]any{"message": map[string]string{"content": content}})
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer h.Close()

	consCfg := config.ConsolidationConfig{Enabled: true, MinScore: .5, K: 8, MinClusterSize: 3, MaxClusterSize: 12, Model: "m"}
	written, err := Run(context.Background(), s, es, embed.New(h.URL, "m", 0), consCfg, "p")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected exactly 2 LLM calls (one per cluster), got %d", callCount)
	}
	if written != 2 {
		t.Fatalf("MISMATCH (finding #3): expected written=2 (1 save + 1 update, both real writes), got %d", written)
	}

	// The Update write reused the FIRST cluster's row in place — exactly one
	// digest row should exist, not two.
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "omnia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var digestCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE type = 'digest' AND deleted_at IS NULL`).Scan(&digestCount); err != nil {
		t.Fatal(err)
	}
	if digestCount != 1 {
		t.Fatalf("expected exactly 1 digest row (the update reuses the existing row in place), got %d", digestCount)
	}

	r, err := s.Search("rollback", store.SearchOptions{Project: "p"})
	if err != nil || len(r) == 0 {
		t.Fatalf("expected the updated digest content to be findable via search: %#v %v", r, err)
	}
	digestSyncID := r[0].SyncID

	// Both groups' sources (6 total) must be linked via `consolidates` to the
	// single (now-updated) digest row — the second group's link is the part
	// finding #3 fixes.
	rels, err := s.GetRelationsForObservations([]string{digestSyncID})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rels[digestSyncID].AsSource); got != 6 {
		t.Fatalf("MISMATCH (finding #3): expected 6 consolidates relations (3 from each cluster) to the updated digest, got %d", got)
	}

	// Both the Save and the Update write must be audited (Run's existing
	// audit.Append call), correlated to the SAME observation ID since Update
	// mutates the existing row in place rather than inserting a new one.
	entries, err := audit.EntriesForObservation(int(r[0].ID))
	if err != nil {
		t.Fatalf("EntriesForObservation: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("MISMATCH (finding #3): expected 2 audit entries (save + update) for observation %d, got %d: %+v", r[0].ID, len(entries), entries)
	}
}

// TestRun_PrintsProgressPerClusterToStderr is finding #4's progress-output
// regression test for consolidate: `omnia consolidate` ran for minutes with
// ZERO stdout on real data (19-25 sequential Ollama-backed digest writes),
// giving no way to tell "working" from "hung." Run must print a line per
// cluster as its digest is written, to stderr (so it never interferes with
// any stdout envelope a future caller might rely on), non-empty and
// mentioning the actual written count.
func TestRun_PrintsProgressPerClusterToStderr(t *testing.T) {
	s, es, c, cfg, _ := setupRun(t, true)

	var stderr string
	var written int
	var runErr error
	stderr = captureStderr(t, func() {
		written, runErr = Run(context.Background(), s, es, c, cfg, "p")
	})
	if runErr != nil || written != 1 {
		t.Fatalf("written=%d err=%v", written, runErr)
	}
	if stderr == "" {
		t.Fatal("expected non-empty progress on stderr as the digest was written")
	}
	if !strings.Contains(stderr, "1") {
		t.Errorf("expected progress to mention the digest count, got stderr=%q", stderr)
	}
}
