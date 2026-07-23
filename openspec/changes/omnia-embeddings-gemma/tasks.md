# Tasks: Memory Embeddings Model (EmbeddingGemma candidate, jina stays default)

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~380-480 |
| 400-line budget risk | Medium |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 (registry+validation+truncation) -> PR 2 (regression locks+eval evidence+docs) |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: Medium

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Model registry (EMBM-3) + config validation (EMBM-3) + client truncation (EMBM-4) | PR 1 | Phases 1-3; the actual new capability. Base = main (or tracker branch if feature-branch-chain). |
| 2 | Regression locks (EMBM-5/6) + eval-gated swap evidence (EMBM-2) + docs | PR 2 | Phases 4-6; depends on PR 1 for phase 5 only. Base = PR 1 branch if feature-branch-chain, else main. |

No design.md exists for this light slice; architecture calls below (registry location, config->embed import direction) are made here, grounded in existing code (`embed` never imports `config`, so `config` importing `embed` introduces no cycle).

## Phase 1: Model Capability Registry (Foundation)

- [x] 1.1 Resolve/confirm the exact Ollama tag for EmbeddingGemma-300m (`ollama show`/library page — do not assume; candidate `embeddinggemma:300m`). Blocks 1.3, 2.1, 3.1, 5.1. **Confirmed**: `embeddinggemma:300m` resolves to a real manifest on the Ollama registry (`GET https://registry.ollama.ai/v2/library/embeddinggemma/manifests/300m` → HTTP 200, layered on `embeddinggemma:300m-bf16`); `ollama.com/library/embeddinggemma` confirms "EmbeddingGemma is a 300M parameter embedding model from Google." Native output is 768-dim, Matryoshka-trained (truncatable to 512/256/128 per Google's model card). Not locally pulled in this session (no live A/B run performed) — see Risks.
- [x] 1.2 [RED] `internal/embed/models_test.go`: `TestLookupModel_JinaIsNotMRL`, `TestLookupModel_EmbeddingGemmaIsMRL`, `TestLookupModel_UnknownModelNotFound`.
- [x] 1.3 [GREEN] `internal/embed/models.go`: `ModelInfo{NativeDim int; MRL bool}`, `knownModels` map, `LookupModel(name) (ModelInfo, bool)` seeded with jina (768,false) + confirmed EmbeddingGemma tag (768,true). Satisfies EMBM-3.

## Phase 2: Config Validation (depends on Phase 1; parallel with Phase 3)

- [x] 2.1 [RED] `internal/config/embeddings_config_test.go`: `TestValidateEmbeddings_RejectsTruncationForNonMRLModel`, `TestValidateEmbeddings_AcceptsTruncationForMRLModel`, `TestValidateEmbeddings_UnknownModelSkipsGuard`.
- [x] 2.2 [GREEN] Add `ValidateEmbeddings(cfg EmbeddingsConfig) error` to `internal/config/config.go` (imports `internal/embed.LookupModel`); wire into `cmd/omnia/embed.go` and `cmd/omnia/autoembed.go` right after `config.Load`, before any Ollama call. Satisfies EMBM-3's config-validation scenarios. (Also fixed `cmd/omnia/autoembed_test.go`'s `TestBuildAutoEmbedWorker_WithStoreStillBuildsWorker`, which used jina@dim=3 — now dim=768 to satisfy the new guard; behavior-neutral, the test only asserted non-nil worker.)

## Phase 3: Matryoshka Truncation in Client (depends on Phase 1; parallel with Phase 2)

- [x] 3.1 [RED] `internal/embed/client_test.go`: `TestClient_Embed_TruncatesAndRenormalizes_MRLModel`, `TestClient_Embed_FullDimensionLeavesVectorUntouched_MRLModel`, `TestClient_Embed_NonMRLDimMismatchStillErrors` (locks existing `TestClient_Embed_DimMismatch`).
- [x] 3.2 [GREEN] Update `Client.Embed` in `internal/embed/client.go`: on `len(vec) != c.dim`, call `LookupModel(c.model)`; if found+MRL+`c.dim < len(vec)`, truncate before `normalize`; else keep today's error. Satisfies EMBM-4.

## Phase 4: Regression Locks — Search Isolation & Full Re-embed (parallel with Phases 1-3)

- [x] 4.1 [TEST] `internal/embed/store_test.go`: `TestStore_SearchScoped_SkipsDimMismatch768vs256` (seed 768d+256d rows; 256d query returns only 256d rows via `Search` and `SearchScoped`).
- [x] 4.2 [TEST] `internal/embed/reconcile_test.go`: `TestReconcile_DimChangeTriggersFullReembed` (same model, dim 768->256, `Stats.Reused==0`), `TestReconcile_InterruptedMigrationIsResumable` (embedder errors mid-migration; re-run reconciles only mismatched rows, reuses migrated).
- [x] 4.3 4.1/4.2 passed immediately — no prod change needed; `store.go`'s `search()` dim-skip and `reconcile.go`'s Model/Dim trigger already implement EMBM-5/EMBM-6 correctly. Both are now regression-locked.

## Phase 5: Eval-Gated Swap Evidence (depends on Phase 1+3; ties to omnia-eval-harness EVAL-7)

- [x] 5.1 [TEST] Added `TestModelAB_JinaVsEmbeddingGemma` to `internal/embed/model_ab_test.go`, same `ollama_live`+`OLLAMA_LIVE_TEST=1` double-gate as the existing jina-vs-bge-m3 test: `RunModelAB` for jina@768 vs EmbeddingGemma@768(native) and @256(truncated) over `testdata/ab_pairs.json`; logs recall@k; asserts no regression vs jina. Does NOT touch `applyDefaults` (EMBM-2). Compiles clean under `go vet -tags ollama_live ./internal/embed/...`; NOT executed live in this session (embeddinggemma:300m is not pulled locally — see Risks), so no recall@k numbers are recorded yet.
- [x] 5.2 No implementation task — 5.1's logged recall@k comparison (once run live) IS the "recorded evidence" EMBM-2 requires before any future default-swap proposal. jina remains the shipped default; `applyDefaults` untouched.

## Phase 6: Docs

- [x] 6.1 Updated `config.example.yaml`'s commented `embeddings:` block: `model` also accepts the confirmed EmbeddingGemma-300m tag (MRL-capable, dim truncatable to 256/128); jina stays default.
- [x] 6.2 Updated `EmbeddingsConfig.Model`/`Dim` doc comments in `internal/config/config.go` mentioning EmbeddingGemma-300m as a selectable MRL-capable alternative. Also updated `README.md`'s embeddings/recall section and config table (docs-alignment).
- [x] 6.3 Ran `go test ./...` (full suite): all packages pass except pre-existing, unrelated `internal/mcp` failures (project-detection tests failing on this checkout both BEFORE and AFTER this change — confirmed via `git stash`). `go test ./internal/embed/... ./internal/config/... -race` is clean. `gofmt`/`go vet` clean on all touched/new files. `CGO_ENABLED=0 go build ./...` succeeds (cgo-free).

## Delivery Note

Implemented as ONE consolidated PR (size:exception) per explicit orchestrator instruction, overriding the Suggested Work Units' PR1/PR2 split above. Net diff: ~424 changed lines across 12 tracked files + 2 new files (~101 lines) ≈ 525 total — above the 400-line budget, accepted under size:exception.

## Parallelization

Sequential: 1 -> {2, 3} -> 5 -> 6. Parallel: Phase 2 and Phase 3 (once Phase 1 lands); Phase 4 anytime (no dependency). Phase 5 needs both 1 (tag) and 3 (truncation). Phase 6 last.
