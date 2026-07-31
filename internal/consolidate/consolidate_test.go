package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
