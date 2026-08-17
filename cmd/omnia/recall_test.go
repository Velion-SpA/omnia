package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/server"
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

	got := buildRecallService(s, config.RecallConfig{Enabled: false}, config.EmbeddingsConfig{}, "", false, config.EncryptionConfig{})
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

	got := buildRecallService(s, recallCfg, embCfg, "", false, config.EncryptionConfig{})
	if got == nil {
		t.Fatal("buildRecallService(enabled) = nil, want a non-nil *recall.Service")
	}
	if got.Lexical == nil {
		t.Error("expected non-nil Lexical (store-backed LexicalSearcher)")
	}
	if dbHasVecEmbeddingsTable(t, embCfg.DBPath) {
		t.Error("buildRecallService(vecIndexEnabled=false, the default here) must not create vec_embeddings")
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

// TestBuildRecallService_VecIndexEnabledThreadsThroughToOpenStore (task
// 2.15/2.16, design capability 7): buildRecallService is the SHARED builder
// behind cmdMCP, cmdServe, `omnia search`, and `omnia eval --injection` — a
// single vecIndexEnabled=true here must make the resulting embeddings.db
// carry the derived Vec1 table for ALL of those read surfaces at once.
func TestBuildRecallService_VecIndexEnabledThreadsThroughToOpenStore(t *testing.T) {
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
	recallCfg := config.RecallConfig{Enabled: true, RRFK: 60, DenseK: 5, StrongFloor: 0.65, BaseFloor: 0.55, MaxResults: 50}

	got := buildRecallService(s, recallCfg, embCfg, "", true, config.EncryptionConfig{})
	if got == nil {
		t.Fatal("buildRecallService(vecIndexEnabled=true) = nil, want a non-nil *recall.Service")
	}
	if !dbHasVecEmbeddingsTable(t, embCfg.DBPath) {
		t.Error("buildRecallService(vecIndexEnabled=true) must create vec_embeddings in the SAME embeddings.db")
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
	}, "", false, config.EncryptionConfig{})
	if got != nil {
		t.Fatal("buildRecallService: expected nil when the embeddings store cannot be opened")
	}
}

// TestBuildRecallService_InvalidEmbeddingsConfigDegradesToNil is the
// blocking-fix regression test: recall.enabled=true with an
// internally-inconsistent embeddings config (a non-MRL model, jina, with a
// Dim truncated below its native 768) must NOT silently build a
// recall.Service backed by a broken embedder — config.ValidateEmbeddings
// must be consulted at this seam and the misconfiguration must fail closed
// to nil (mem_search degrades to FTS5-only, exactly like the
// embeddings-store-unavailable branch), surfacing the bad config instead of
// silently degrading semantic search into keyword-only results with zero
// diagnostic.
func TestBuildRecallService_InvalidEmbeddingsConfigDegradesToNil(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	embCfg := config.EmbeddingsConfig{
		BaseURL: "http://127.0.0.1:11434",
		Model:   "jina/jina-embeddings-v2-base-es", // NOT MRL-capable
		Dim:     256,                               // truncates jina's native 768
		DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
	}
	recallCfg := config.RecallConfig{
		Enabled:     true,
		RRFK:        60,
		DenseK:      5,
		StrongFloor: 0.35,
		BaseFloor:   0.25,
		MaxResults:  50,
	}

	got := buildRecallService(s, recallCfg, embCfg, "", false, config.EncryptionConfig{})
	if got != nil {
		t.Fatal("buildRecallService: expected nil for an invalid (non-MRL truncated-dim) embeddings config — must fail closed to FTS5-only instead of silently building a broken embedder")
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

	_, _, _, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, nil, "panic", store.SearchOptions{Limit: 5})
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

	_, _, _, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, recallSvc, "login timeout", store.SearchOptions{
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

	results, relevance, _, fusionRan, err := recallOrFTSSearchWithRelevance(context.Background(), s, recallSvc, "panic", store.SearchOptions{
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

// ─── P0 (docs/conversational-retrieval-plan.md): loadLearnedRankerForCLI /
// buildHTTPSearchFunc — GET /search's RankPipeline wiring ───────────────────

// TestLoadLearnedRankerForCLI_DisabledReturnsNilModel pins the common case:
// learned_ranker.enabled=false (the default) must never attempt to load a
// model from disk, and returns a nil model either way.
func TestLoadLearnedRankerForCLI_DisabledReturnsNilModel(t *testing.T) {
	appCfg := &config.Config{Ranker: config.RankerConfig{Enabled: false}}
	cfg, model := loadLearnedRankerForCLI(appCfg, t.TempDir())
	if cfg.Enabled {
		t.Fatal("expected the returned RankerConfig to still report Enabled=false")
	}
	if model != nil {
		t.Fatal("expected a nil model when learned_ranker.enabled is false")
	}
}

// TestLoadLearnedRankerForCLI_EnabledButNoModelOnDiskReturnsNilModel covers
// the graceful-degradation branch: enabled=true but nothing has ever been
// promoted at the model dir — ranker.LoadCurrent errors, and this helper
// must degrade to a nil model (RankPipeline's ApplyLearnedRanker is then a
// pure no-op) rather than propagating the error.
func TestLoadLearnedRankerForCLI_EnabledButNoModelOnDiskReturnsNilModel(t *testing.T) {
	appCfg := &config.Config{Ranker: config.RankerConfig{Enabled: true, ModelDir: t.TempDir()}}
	cfg, model := loadLearnedRankerForCLI(appCfg, t.TempDir())
	if !cfg.Enabled {
		t.Fatal("expected the returned RankerConfig to still report Enabled=true")
	}
	if model != nil {
		t.Fatal("expected a nil model when no model has been promoted at ModelDir")
	}
}

// TestLoadLearnedRankerForCLI_EnabledWithPromotedModelLoads is the flag-ON
// happy path: a model trained and promoted at ModelDir must load
// successfully, so GET /search can score against the SAME trained model
// mem_search does.
func TestLoadLearnedRankerForCLI_EnabledWithPromotedModelLoads(t *testing.T) {
	examples := []ranker.Example{
		{Features: ranker.Features{LexicalRRF: .9, SemanticCosine: .9, Recency: .9, Importance: 1, OutcomeHistory: 1}, Label: 1},
		{Features: ranker.Features{LexicalRRF: .1, SemanticCosine: .1, Recency: .1, Importance: .3, OutcomeHistory: 0}, Label: 0},
	}
	trained, err := ranker.Train(examples, ranker.TrainOptions{Iterations: 100, LearningRate: .4, L2: .01, TrainedAt: "2026-08-14T00:00:00Z"})
	if err != nil {
		t.Fatalf("ranker.Train: %v", err)
	}
	dir := t.TempDir()
	if err := ranker.Promote(dir, trained); err != nil {
		t.Fatalf("ranker.Promote: %v", err)
	}

	appCfg := &config.Config{Ranker: config.RankerConfig{Enabled: true, ModelDir: dir}}
	cfg, model := loadLearnedRankerForCLI(appCfg, t.TempDir())
	if !cfg.Enabled {
		t.Fatal("expected the returned RankerConfig to report Enabled=true")
	}
	if model == nil {
		t.Fatal("expected a loaded model when a promoted model exists at ModelDir")
	}
	if model.Version != trained.Version {
		t.Fatalf("Version = %q, want %q", model.Version, trained.Version)
	}
}

// TestLoadLearnedRankerForCLI_EmptyModelDirDefaultsToDataDirRankerSubdir
// proves an unset ModelDir falls back to <dataDir>/ranker, mirroring
// cmdMCP's own pre-extraction inline default exactly.
func TestLoadLearnedRankerForCLI_EmptyModelDirDefaultsToDataDirRankerSubdir(t *testing.T) {
	examples := []ranker.Example{
		{Features: ranker.Features{LexicalRRF: .9, SemanticCosine: .9, Recency: .9, Importance: 1, OutcomeHistory: 1}, Label: 1},
		{Features: ranker.Features{LexicalRRF: .1, SemanticCosine: .1, Recency: .1, Importance: .3, OutcomeHistory: 0}, Label: 0},
	}
	trained, err := ranker.Train(examples, ranker.TrainOptions{Iterations: 100, LearningRate: .4, L2: .01, TrainedAt: "2026-08-14T00:00:00Z"})
	if err != nil {
		t.Fatalf("ranker.Train: %v", err)
	}
	dataDir := t.TempDir()
	if err := ranker.Promote(filepath.Join(dataDir, "ranker"), trained); err != nil {
		t.Fatalf("ranker.Promote: %v", err)
	}

	appCfg := &config.Config{Ranker: config.RankerConfig{Enabled: true}} // ModelDir left empty
	_, model := loadLearnedRankerForCLI(appCfg, dataDir)
	if model == nil {
		t.Fatal("expected loadLearnedRankerForCLI to default ModelDir to <dataDir>/ranker and find the promoted model there")
	}
}

// TestBuildHTTPSearchFunc_NilRecallSurfacesResultsAndReportsLexicalDegraded
// is the flag-OFF baseline: recallSvc == nil (recall disabled) still returns
// the FTS5 results, and the envelope reports RecallDegraded=true with
// Mode-lexical semantics (mirroring mem_search's own
// EvaluateRecallHealth(semanticActive: false, ...) contract) since P0 item 2
// asks for GET /search to surface the SAME #226 signal mem_search does.
func TestBuildHTTPSearchFunc_NilRecallSurfacesResultsAndReportsLexicalDegraded(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-http-nilrecall", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-nilrecall", Type: "bugfix", Title: "Fix panic in parser",
		Content: "Fix panic in parser when args are missing", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	appCfg := &config.Config{}
	searchFn := buildHTTPSearchFunc(s, nil, appCfg, t.TempDir(), nil)

	envelope, err := searchFn(context.Background(), "panic", server.SearchRequest{
		SearchOptions: store.SearchOptions{Project: "engram", Scope: "project", Limit: 5},
	})
	if err != nil {
		t.Fatalf("buildHTTPSearchFunc: %v", err)
	}
	if len(envelope.Results) != 1 || envelope.Results[0].ID != wantID {
		t.Fatalf("expected the FTS5 result [%d], got %+v", wantID, envelope.Results)
	}
	if !envelope.RecallDegraded || envelope.RecallDegradedReason == "" {
		t.Fatalf("expected RecallDegraded=true with a reason when recallSvc is nil, got %+v", envelope)
	}
}

// TestBuildHTTPSearchFunc_MaxTokensOverridesDisabledBudget is P0 item 3's
// core promise: a caller-supplied max_tokens must cap the response even
// when injection.budget.enabled is false (the operator's default), so Vel
// can always bound response size on demand.
func TestBuildHTTPSearchFunc_MaxTokensOverridesDisabledBudget(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-http-maxtok", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-http-maxtok", Type: "manual", Title: "Budget fixture " + strconv.Itoa(i),
			Content: "token budget wiring fixture content " + strconv.Itoa(i), Project: "engram", Scope: "project",
		}); err != nil {
			t.Fatalf("add observation %d: %v", i, err)
		}
	}

	appCfg := &config.Config{} // Injection.Budget.Enabled left false (the default)
	searchFn := buildHTTPSearchFunc(s, nil, appCfg, t.TempDir(), nil)

	envelope, err := searchFn(context.Background(), "budget wiring fixture", server.SearchRequest{
		SearchOptions: store.SearchOptions{Project: "engram", Scope: "project", Limit: 10},
		MaxTokens:     1, // tiny — must trim well below the 5 seeded rows
	})
	if err != nil {
		t.Fatalf("buildHTTPSearchFunc: %v", err)
	}
	if len(envelope.Results) >= 5 {
		t.Fatalf("expected max_tokens=1 to trim below all 5 seeded rows even with injection.budget.enabled=false, got %d", len(envelope.Results))
	}
}

// TestBuildHTTPSearchFunc_ExplainPopulatesScoreBreakdown proves req.Explain
// routes into mcp.BuildResultReceipt, keyed by decimal Observation.ID.
func TestBuildHTTPSearchFunc_ExplainPopulatesScoreBreakdown(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-http-explain", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-explain", Type: "bugfix", Title: "Fix panic in parser",
		Content: "Fix panic in parser when args are missing", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	appCfg := &config.Config{}
	searchFn := buildHTTPSearchFunc(s, nil, appCfg, t.TempDir(), nil)

	envelope, err := searchFn(context.Background(), "panic", server.SearchRequest{
		SearchOptions: store.SearchOptions{Project: "engram", Scope: "project", Limit: 5},
		Explain:       true,
	})
	if err != nil {
		t.Fatalf("buildHTTPSearchFunc: %v", err)
	}
	key := strconv.FormatInt(wantID, 10)
	receipt, ok := envelope.ScoreBreakdown[key]
	if !ok {
		t.Fatalf("expected ScoreBreakdown[%q], got keys %v", key, envelope.ScoreBreakdown)
	}
	if _, ok := receipt["final"]; !ok {
		t.Fatalf("expected the receipt to carry a 'final' key, got %+v", receipt)
	}
}

// TestBuildHTTPSearchFunc_ExplicitTypeStandsDownTypeLens proves GET
// /search's own `type` query param still reaches the store-level type
// filter through this new pipeline wiring (SearchRequest.SearchOptions.Type
// threads straight through to recallOrFTSSearchWithRelevance, exactly as it
// did before P0) — and, since the SAME field also becomes
// RankPipelineOptions.ExplicitType, the situational type lens correctly
// receives a non-empty explicitType and would stand down for it (see
// internal/mcp's TestRankPipeline_ExplicitTypeStandsDownTypeLens for the
// pipeline-level proof of that specific "explicit filter always wins" rule
// in isolation, with a controlled result set FTS ranking can't perturb).
func TestBuildHTTPSearchFunc_ExplicitTypeStandsDownTypeLens(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-http-typelens", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// "decision" type row, ranked SECOND by relevance (lower rank than the
	// "note" row) — if the lens fired, it would lift this row to first.
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-typelens", Type: "decision", Title: "Chose Postgres",
		Content: "why did we choose postgres for storage", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add decision observation: %v", err)
	}
	noteID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-typelens", Type: "note", Title: "Postgres note",
		Content: "why did we choose postgres for storage, a plain note", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add note observation: %v", err)
	}

	appCfg := &config.Config{Injection: config.InjectionConfig{TypeLens: config.TypeLensConfig{Enabled: true}}}
	searchFn := buildHTTPSearchFunc(s, nil, appCfg, t.TempDir(), nil)

	envelope, err := searchFn(context.Background(), "why did we choose postgres", server.SearchRequest{
		SearchOptions: store.SearchOptions{Type: "note", Project: "engram", Scope: "project", Limit: 10},
	})
	if err != nil {
		t.Fatalf("buildHTTPSearchFunc: %v", err)
	}
	// An explicit type filter already narrows the store query itself, so
	// only the note row is even in the result set — this is really pinning
	// that Type flowed through to SearchOptions.Type at all (a type-scoped
	// store query), which is the precondition for ExplicitType reaching
	// RankPipelineOptions correctly.
	if len(envelope.Results) != 1 || envelope.Results[0].ID != noteID {
		t.Fatalf("expected only the type=note result (%d), got %+v", noteID, envelope.Results)
	}
}

// TestBuildHTTPSearchFunc_StructuralForgettingDownranksStaleAnchor mirrors
// internal/mcp's TestHandleSearch_StructuralForgettingEnabled_
// StaleMemoryDownrankedWithReceipt at the HTTP wiring seam: with
// structural_forgetting.enabled=true, a memory carrying a stale code anchor
// ranks below an equally-relevant fresh one, proving GET /search now runs
// ApplyStalenessDownrank too — not just RankResults/ApplyTypeLens/ApplyMMR/
// ApplyTokenBudget.
func TestBuildHTTPSearchFunc_StructuralForgettingDownranksStaleAnchor(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s-http-sf", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	staleID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-sf", Type: "bugfix", Title: "JWT auth token check",
		Content: "JWT auth token check implementation", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation stale: %v", err)
	}
	freshID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-http-sf", Type: "bugfix", Title: "JWT auth token validate",
		Content: "JWT auth token validate implementation", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation fresh: %v", err)
	}

	staleObs, err := s.GetObservation(staleID)
	if err != nil {
		t.Fatalf("GetObservation(stale): %v", err)
	}
	anchorSyncID, err := s.UpsertAnchor(store.UpsertAnchorParams{
		ObsSyncID: staleObs.SyncID, FilePath: "jwt.go", Symbol: "CheckJWT",
		LineStart: 1, LineEnd: 5, ContentHash: "h1",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if err := s.MarkAnchorStale(anchorSyncID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	appCfg := &config.Config{StructuralForgetting: config.StructuralForgettingConfig{Enabled: true}}
	searchFn := buildHTTPSearchFunc(s, nil, appCfg, t.TempDir(), nil)

	envelope, err := searchFn(context.Background(), "JWT auth token", server.SearchRequest{
		SearchOptions: store.SearchOptions{Project: "engram", Scope: "project", Limit: 5},
		Explain:       true,
	})
	if err != nil {
		t.Fatalf("buildHTTPSearchFunc: %v", err)
	}
	if len(envelope.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", envelope.Results)
	}
	if envelope.Results[0].ID != freshID {
		t.Errorf("expected fresh memory (id %d) ranked first, got id=%d", freshID, envelope.Results[0].ID)
	}
	if envelope.Results[1].ID != staleID {
		t.Fatalf("expected stale memory (id %d) ranked second, got id=%d", staleID, envelope.Results[1].ID)
	}
	staleReceipt := envelope.ScoreBreakdown[strconv.FormatInt(staleID, 10)]
	if staleReceipt == nil || staleReceipt["staleness_penalty"] == nil || staleReceipt["staleness_penalty"] == 0.0 {
		t.Fatalf("expected a non-zero staleness_penalty in the stale row's receipt, got %+v", staleReceipt)
	}
}
