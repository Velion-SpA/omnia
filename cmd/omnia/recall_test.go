package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

// TestBuildRecallService_DisabledReturnsNil locks the D7 rollback guarantee
// at the cmdMCP construction seam (task 3.6): recall.enabled=false (the
// default) must yield a nil *recall.Service, so cmdMCP never opens the
// embeddings store or constructs an Ollama HTTP client on the default path.
func TestBuildRecallService_DisabledReturnsNil(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	got := buildRecallService(s, config.RecallConfig{Enabled: false}, config.EmbeddingsConfig{}, "")
	if got != nil {
		t.Fatalf("buildRecallService(disabled) = %v, want nil (recall.enabled=false must not construct a Service)", got)
	}
}

// TestBuildRecallService_EnabledBuildsWiredService is the flag-ON
// counterpart: recall.enabled=true must produce a *recall.Service with both
// the lexical and semantic sides wired, using RecallConfig's fusion params.
func TestBuildRecallService_EnabledBuildsWiredService(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	embCfg := config.EmbeddingsConfig{
		BaseURL: "http://127.0.0.1:11434",
		Model:   "jina/jina-embeddings-v2-base-es",
		Dim:     768,
		DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
	}
	recallCfg := config.RecallConfig{
		Enabled:     true,
		RRFK:        60,
		DenseK:      5,
		StrongFloor: 0.65,
		BaseFloor:   0.55,
		MaxResults:  50,
	}

	got := buildRecallService(s, recallCfg, embCfg, "")
	if got == nil {
		t.Fatal("buildRecallService(enabled) = nil, want a non-nil *recall.Service")
	}
	if got.Lexical == nil {
		t.Error("expected non-nil Lexical (store-backed LexicalSearcher)")
	}
	if got.Semantic == nil {
		t.Error("expected non-nil Semantic (embed.Searcher) when the embeddings store opens successfully")
	}
	if got.Params.RRFK != recallCfg.RRFK ||
		got.Params.DenseK != recallCfg.DenseK ||
		got.Params.StrongFloor != recallCfg.StrongFloor ||
		got.Params.BaseFloor != recallCfg.BaseFloor ||
		got.Params.MaxResults != recallCfg.MaxResults {
		t.Errorf("Params = %+v, want fields copied from RecallConfig %+v", got.Params, recallCfg)
	}
}

// TestBuildRecallService_EnabledButStoreUnavailableReturnsNil covers the
// graceful-degradation branch: if the embeddings store can't even be
// opened, buildRecallService must fail closed to nil (routing mem_search
// back through the already-tested cfg.Recall == nil / FTS5-only path)
// rather than starting a half-wired recall.Service.
func TestBuildRecallService_EnabledButStoreUnavailableReturnsNil(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	// A regular file where a directory component is expected forces
	// embed.OpenStore's os.MkdirAll to fail deterministically.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	got := buildRecallService(s, config.RecallConfig{Enabled: true}, config.EmbeddingsConfig{
		DBPath: filepath.Join(blocker, "embeddings.db"),
	}, "")
	if got != nil {
		t.Fatal("buildRecallService: expected nil when the embeddings store cannot be opened")
	}
}

// fakeCLIEmbedSearcher is a hermetic, in-memory embed.Searcher fake — mirrors
// internal/mcp/recall_wiring_test.go's fakeEmbedSearcher — used to exercise
// recallOrFTSSearch's fused path without a live Ollama.
type fakeCLIEmbedSearcher struct {
	hits []embed.Hit
}

func (f fakeCLIEmbedSearcher) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func (f fakeCLIEmbedSearcher) Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error) {
	return f.hits, nil
}

// failingLexicalSearcher always errors, so recall.Service.Search's lexical
// leg fails deterministically — used to exercise recallOrFTSSearch's
// error-fallback branch without depending on a malformed FTS5 query string.
type failingLexicalSearcher struct{}

func (failingLexicalSearcher) Search(ctx context.Context, query string, opts recall.LexicalSearchOptions) ([]recall.LexicalHit, error) {
	return nil, errors.New("forced lexical search error")
}

// TestRecallOrFTSSearch_RecallWiredSurfacesSemanticParaphrase is the
// flag-ON fusion test (issue #86, task "search with recall wired returns
// semantically-ranked results"): with a recall.Service wired to a fake
// embed.Searcher, a memory that is NOT a lexical (FTS5) match for the query
// must still surface — proving `omnia search`/HTTP GET /search's shared
// recallOrFTSSearch actually changes recall behavior, not just passing
// lexical results through untouched. Mirrors
// internal/mcp/recall_wiring_test.go's TestHandleSearch_RecallEnabled_
// SurfacesSemanticOnlyParaphrase at the CLI/HTTP wiring seam instead of MCP.
func TestRecallOrFTSSearch_RecallWiredSurfacesSemanticParaphrase(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-cli-fuse", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	lexicalID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-fuse",
		Type:      "bugfix",
		Title:     "Fix login timeout",
		Content:   "Fix login timeout under load",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add lexical observation: %v", err)
	}

	// Paraphrase: no lexical overlap with the query "login timeout" at all,
	// so storeSearch alone would never surface it.
	paraphraseID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-fuse",
		Type:      "bugfix",
		Title:     "Session drops under heavy traffic",
		Content:   "Users get disconnected when the server is busy",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add paraphrase observation: %v", err)
	}

	semantic := fakeCLIEmbedSearcher{hits: []embed.Hit{{ObsID: int(paraphraseID), Score: 0.9}}}
	recallSvc := recall.NewService(mcp.NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	results, err := recallOrFTSSearch(context.Background(), s, recallSvc, "login timeout", store.SearchOptions{
		Project: "engram",
		Scope:   "project",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("recallOrFTSSearch: %v", err)
	}

	foundLexical, foundParaphrase := false, false
	for _, r := range results {
		if r.ID == lexicalID {
			foundLexical = true
		}
		if r.ID == paraphraseID {
			foundParaphrase = true
		}
	}
	if !foundLexical {
		t.Fatalf("expected lexical match (id %d) in fused results, got %+v", lexicalID, results)
	}
	if !foundParaphrase {
		t.Fatalf("expected semantic-only paraphrase (id %d) surfaced by fusion, got %+v", paraphraseID, results)
	}
}

// TestRecallOrFTSSearch_NilRecallFallsBackToFTS is the flag-OFF regression
// pin: recallSvc == nil (recall disabled, config missing, or unavailable —
// buildRecallServiceForCLI's graceful-degradation cases all collapse to
// this) must fall back to storeSearch and produce the exact same results as
// calling storeSearch directly — no crash, no hang, plain FTS5 results.
func TestRecallOrFTSSearch_NilRecallFallsBackToFTS(t *testing.T) {
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-cli-nilrecall", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-nilrecall",
		Type:      "bugfix",
		Title:     "Fix panic in parser",
		Content:   "Fix panic in parser when args are missing",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	opts := store.SearchOptions{Project: "engram", Scope: "project", Limit: 5}
	want, err := storeSearch(s, "panic", opts)
	if err != nil {
		t.Fatalf("storeSearch: %v", err)
	}
	if len(want) != 1 || want[0].ID != wantID {
		t.Fatalf("test setup invalid: storeSearch = %+v, want exactly [%d]", want, wantID)
	}

	got, err := recallOrFTSSearch(context.Background(), s, nil, "panic", opts)
	if err != nil {
		t.Fatalf("recallOrFTSSearch(nil recall): %v", err)
	}
	if len(got) != len(want) || got[0].ID != want[0].ID {
		t.Fatalf("recallOrFTSSearch(nil recall) = %+v, want storeSearch's %+v", got, want)
	}
}

// TestRecallOrFTSSearch_RecallSearchErrorFallsBackToFTS covers the other
// graceful-fallback path: recallSvc is non-nil but its lexical leg errors
// (e.g. a malformed FTS5 query in production) — recallOrFTSSearch must fall
// back to storeSearch instead of propagating the recall-specific error,
// still returning correct FTS results and never crashing.
func TestRecallOrFTSSearch_RecallSearchErrorFallsBackToFTS(t *testing.T) {
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-cli-recallerr", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-recallerr",
		Type:      "bugfix",
		Title:     "Fix panic in loader",
		Content:   "Fix panic in loader when config is missing",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	recallSvc := recall.NewService(failingLexicalSearcher{}, nil, recall.DefaultFuseParams())

	got, err := recallOrFTSSearch(context.Background(), s, recallSvc, "panic", store.SearchOptions{
		Project: "engram", Scope: "project", Limit: 5,
	})
	if err != nil {
		t.Fatalf("expected graceful fallback to storeSearch, got error: %v", err)
	}
	if len(got) != 1 || got[0].ID != wantID {
		t.Fatalf("expected fallback FTS result [%d], got %+v", wantID, got)
	}
}

// TestRecallOrFTSSearchWithRelevance_NilRecallReportsFusionDidNotRun locks the
// fusionRan=false case for a nil recallSvc — the "recall disabled" branch.
func TestRecallOrFTSSearchWithRelevance_NilRecallReportsFusionDidNotRun(t *testing.T) {
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	_, _, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, nil, "panic", store.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("recallOrFTSSearchWithRelevance(nil recall): %v", err)
	}
	if fusionRan {
		t.Error("fusionRan = true, want false when recallSvc is nil (no fusion ever ran)")
	}
}

// TestRecallOrFTSSearchWithRelevance_SuccessReportsFusionRan locks the
// fusionRan=true case: recallSvc.Search succeeding must report that fusion
// actually ran, so --explain can correctly label the result as fusion.
func TestRecallOrFTSSearchWithRelevance_SuccessReportsFusionRan(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-cli-fusionran", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-fusionran",
		Type:      "bugfix",
		Title:     "Fix login timeout",
		Content:   "Fix login timeout under load",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	semantic := fakeCLIEmbedSearcher{hits: []embed.Hit{{ObsID: int(obsID), Score: 0.9}}}
	recallSvc := recall.NewService(mcp.NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	_, _, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, recallSvc, "login timeout", store.SearchOptions{
		Project: "engram", Scope: "project", Limit: 10,
	})
	if err != nil {
		t.Fatalf("recallOrFTSSearchWithRelevance: %v", err)
	}
	if !fusionRan {
		t.Error("fusionRan = false, want true when recallSvc.Search succeeds")
	}
}

// TestRecallOrFTSSearchWithRelevance_FallbackReportsFusionDidNotRun is the RED
// test for blocking fix 2: cmdSearch's --explain used the static
// `recallSvc != nil` check to decide fusion-vs-lexical labeling, but
// recallOrFTSSearchWithRelevance silently falls back to storeSearch+lexical
// when recallSvc.Search errors mid-query — so a configured-but-erroring
// recall.Service (recallSvc != nil) must still report fusionRan=false here,
// proving the caller has a way to label the receipt correctly instead of
// mislabeling a lexical fallback as "fusion".
func TestRecallOrFTSSearchWithRelevance_FallbackReportsFusionDidNotRun(t *testing.T) {
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-cli-fusionfallback", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-fusionfallback",
		Type:      "bugfix",
		Title:     "Fix panic in loader",
		Content:   "Fix panic in loader when config is missing",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	recallSvc := recall.NewService(failingLexicalSearcher{}, nil, recall.DefaultFuseParams())

	results, relevance, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, recallSvc, "panic", store.SearchOptions{
		Project: "engram", Scope: "project", Limit: 5,
	})
	if err != nil {
		t.Fatalf("expected graceful fallback to storeSearch, got error: %v", err)
	}
	if fusionRan {
		t.Error("fusionRan = true, want false: recallSvc.Search errored and fell back to storeSearch+lexical, so --explain must not label this as fusion")
	}
	if len(results) != 1 || results[0].ID != wantID {
		t.Fatalf("expected fallback FTS result [%d], got %+v", wantID, results)
	}
	if _, ok := relevance[wantID]; !ok {
		t.Errorf("expected a lexical relevance entry for the fallback result id %d, got %v", wantID, relevance)
	}
}

// TestBuildRecallServiceForCLI_UsesSharedLoader proves buildRecallServiceForCLI
// (the cmdSearch/cmdServe seam) routes through the shared
// loadAppConfigWithRecallAutodetect var rather than reading config.yaml
// itself directly — stubbing the loader controls the outcome deterministically,
// without touching the real filesystem config (issue #86 "avoid divergence").
func TestBuildRecallServiceForCLI_UsesSharedLoader(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	oldLoader := loadAppConfigWithRecallAutodetect
	t.Cleanup(func() { loadAppConfigWithRecallAutodetect = oldLoader })

	t.Run("loader error yields nil", func(t *testing.T) {
		loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
			return nil, errors.New("no config file")
		}
		if got := buildRecallServiceForCLI(s, ""); got != nil {
			t.Fatalf("expected nil when loader errors, got %v", got)
		}
	})

	t.Run("loader returns recall-disabled config yields nil", func(t *testing.T) {
		loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
			return &config.Config{Recall: config.RecallConfig{Enabled: false}}, nil
		}
		if got := buildRecallServiceForCLI(s, ""); got != nil {
			t.Fatalf("expected nil when recall.enabled=false, got %v", got)
		}
	})

	t.Run("loader returns recall-enabled config yields wired service", func(t *testing.T) {
		loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
			return &config.Config{
				Recall: config.RecallConfig{Enabled: true, RRFK: 60, DenseK: 5, StrongFloor: 0.35, BaseFloor: 0.25, MaxResults: 50},
				Embeddings: config.EmbeddingsConfig{
					BaseURL: "http://127.0.0.1:11434",
					Model:   "jina/jina-embeddings-v2-base-es",
					Dim:     768,
					DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
				},
			}, nil
		}
		got := buildRecallServiceForCLI(s, "")
		if got == nil {
			t.Fatal("expected a wired recall.Service when the loader reports recall.enabled=true")
		}
	})
}
