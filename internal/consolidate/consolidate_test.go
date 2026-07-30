package consolidate

import (
	"context"
	"fmt"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
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
