# Tasks: Human-Like Memory — Complete, Trustworthy Recall (Phase 1)

> Word-budget note: exceeds the 530-word guidance (same exception the spec
> took) — 5 packages, 2 new leaf packages, cloud+local+MCP wiring, TDD-paired
> tasks. Kept to 1 line/task; no prose padding.

Strict TDD active (`go test ./...`). Every implementation task is preceded by
its RED test task. `[REQx]` tags map to `semantic-recall`/`cloud-dashboard`
spec requirements (obs #1400).

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~1,900–2,200 (additions+deletions) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 2 → PR 3 → PR 4 → PR 5 (see below) |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending — orchestrator must ask user |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Model A/B gate + embed HTTP migration (`/api/embed`, jina, no prefixes) | PR 1 | ~320 lines. Foundation; can invalidate model choice before PR 2+. No feature flag needed — transparent under existing `embeddings.enabled`. |
| 2 | Recall core: `embed.Searcher` port + `recall.Fuse`/`recall.Service` (pure, unwired) | PR 2 | ~650 lines. Depends on PR 1 (Embedder signature). No MCP/config wiring yet — reviewable in isolation. |
| 3 | MCP wiring: `RecallConfig`, `handleSearch` fusion, flag-off regression pin, bilingual gate | PR 3 | ~350 lines. Depends on PR 2. Ships `recall.enabled` master flag (default off = zero migration). |
| 4 | Async auto-embed worker (MCP stdio + `omnia serve` HTTP, both entrypoints) | PR 4 | ~500 lines. Depends on PR 1 (Embedder); independent of PR 2/3 — could reorder before PR 2 if preferred. |
| 5 | Cloud semantic parity: `cloud_embeddings` table, `SyncEntityEmbedding`, `clouddash.Source.Semantic()` | PR 5 | ~650 lines. Depends on PR 2 (shares `recall.Fuse`) and PR 4 (needs vectors to sync). |

Base-branch boundary depends on the chosen chain strategy (stacked-to-main:
each targets `main` in order; feature-branch-chain: PR1 targets the tracker
branch, PR2 targets PR1's branch, etc.) — orchestrator to confirm before apply.

## Phase 0: Model Validation Gate (blocking, PR 1)

- [x] 0.1 [RED] `internal/embed/model_ab_test.go` (new, Ollama-gated build tag): golden A/B harness over ~30-50 real ES↔EN query/memory pairs, computing recall@10 for `jina/jina-embeddings-v2-base-es` vs `bge-m3`. Fails to compile (harness doesn't exist).
- [x] 0.2 [GREEN] Implement the harness (`internal/embed/model_ab.go` + `testdata/ab_pairs.json`, 30 pairs); double-gated behind `-tags ollama_live` + `OLLAMA_LIVE_TEST=1` so it never runs in default CI. **Live run deferred to the orchestrator** (Ollama/model pulls unavailable in this environment) — config currently defaults to `jina/jina-embeddings-v2-base-es` (768-dim) per engram obs #1401's research pick, pending the empirical >=0.8 recall@10 confirmation.

## Phase 1: Embed Substrate Migration (PR 1)

- [x] 1.1 [RED] `internal/config/config_test.go`: assert `Embeddings.Model` default is `jina/jina-embeddings-v2-base-es`, `Dim` stays 768.
- [x] 1.2 [GREEN] `internal/config/config.go` `applyDefaults`: change default model; keep dim default 768 (or the gate-selected alternative).
- [x] 1.3 [RED] `internal/embed/client_test.go`: `Embed()` POSTs to `{baseURL}/api/embed` (not `/api/embeddings`) with `{model, input, truncate:true, options:{num_ctx:8192}}`; no `search_document:`/`search_query:` prefix is prepended to input.
- [x] 1.4 [GREEN] `internal/embed/client.go`: rewrite `Embed()` for `/api/embed`; delete `prefixFor`, `TaskDocument`/`TaskQuery` prefix strings; simplify `Embedder` interface to `Embed(ctx, text) ([]float32, error)` (drop `task` param — jina has no asymmetric convention). Update call sites: `internal/embed/reconcile.go`, `internal/dashboard/local_datasource.go` (`localSemantic.EmbedQuery`).
- [x] 1.5 [RED] `internal/embed/reconcile_test.go`: a >5000-char memory embeds in ONE `Embed` call (no successive-shrink retries).
- [x] 1.6 [GREEN] `internal/embed/reconcile.go`: delete `embedBudgets`/`truncateRunes` retry loop in `embedDocument`; single call, relying on server-side `truncate:true`.

## Phase 2: Recall Core (PR 2) — pure, unwired

- [x] 2.1 [RED] `internal/embed/searcher_test.go`: `Searcher.EmbedQuery`/`Search` delegate correctly to a fake store+client (promoted `localSemantic` contract). [REQ6]
- [x] 2.2 [GREEN] `internal/embed/searcher.go` (new): `Searcher` interface + `NewSearcher(store, client)`, promoting `dashboard.localSemantic`.
- [x] 2.3 `internal/dashboard/local_datasource.go`: adopt `embed.Searcher`, delete the local duplicate `localSemantic` type. [REQ6]
- [x] 2.4 [RED] `internal/recall/recall_test.go`: table-driven `Fuse()` — RRF order (`k=60`), tie-break (both-lists → `updated_at DESC` → `id ASC`), `maxResults` cap, `topic_key` sentinel (-1000) pre-empts. [REQ1]
- [x] 2.5 [RED] same file: adaptive floor — sparse-hit widen (baseFloor 0.55), dense-hit tighten (strongFloor 0.65, denseK=5). [REQ3]
- [x] 2.6 [GREEN] `internal/recall/recall.go` (new): `FuseParams` + `Fuse(lexical, semantic, params)` pure function; no Ollama/DB import.
- [x] 2.7 [RED] `internal/recall/service_test.go`: `Service.Search` degrades to FTS5-only on nil `Searcher`, `EmbedQuery` error, or Ollama-down; zero-FTS5-match returns empty set, not an error. [REQ4]
- [x] 2.8 [GREEN] `internal/recall/recall.go` (or `service.go`): `Service{store, searcher, cfg}` composing `store.Search` + `Searcher` + `Fuse`, degrade-safe.
- [x] 2.9 [RED] `internal/recall/bilingual_test.go` (Ollama-gated): fixed ES-query→EN-memory and EN-query→ES-memory pairs appear within top-k. [REQ2]
- [x] 2.10 [GREEN] N/A (gate passes with embeddings-only per Phase 0); if it fails, stub `recall.bilingual_expansion` (default off) reusing `internal/llm` `AgentRunner` — flagged, deferred unless gate fails.

## Phase 3: MCP Wiring + Flag (PR 3)

- [ ] 3.1 [RED] `internal/config/config_test.go`: `RecallConfig` defaults (`enabled:false`, `rrf_k:60`, `dense_k:5`, `strong_floor:0.65`, `base_floor:0.55`, `max_results:50`).
- [ ] 3.2 [GREEN] `internal/config/config.go`: add `RecallConfig` struct + yaml tag + defaults.
- [ ] 3.3 [RED] `internal/mcp/mcp_test.go`: `recall.enabled=false` → `handleSearch` output byte-identical to today's FTS5-only path (regression pin). [REQ7 / rollback]
- [ ] 3.4 [RED] same file: `recall.enabled=true` with a fake `recall.Service` → fused results surface a paraphrase FTS5 alone would miss. [REQ1]
- [ ] 3.5 [GREEN] `internal/mcp/mcp.go`: add `Recall *recall.Service` to `MCPConfig`; `handleSearch` calls it when non-nil+enabled, else `s.Search` unchanged.
- [ ] 3.6 [GREEN] `cmd/omnia/main.go` `cmdMCP`: construct `embed.Store`/`embed.Searcher`/`recall.Service` from config, pass into `mcp.MCPConfig`.

## Phase 4: Async Auto-Embed Worker (PR 4, both entrypoints)

- [ ] 4.1 [RED] `internal/embed/worker_test.go`: enqueue→upsert happy path; full-queue (cap 256) drop is silent (relies on `Reconcile` sweep); one Ollama error doesn't stop the worker; `Enqueue` never blocks (slow-fake embedder + timeout assertion). [REQ5]
- [ ] 4.2 [GREEN] `internal/embed/worker.go` (new): `Worker{queue chan string, store, embedder, reader}`, `Start(ctx)` goroutine, non-blocking `Enqueue(syncID)` (`select{case ch<-id: default:}`), periodic `Reconcile` tick + startup sweep.
- [ ] 4.3 [RED] `internal/mcp/mcp_test.go`: `handleSave` enqueues the saved `sync_id` to a spy worker; save returns before a slow-fake embed completes.
- [ ] 4.4 [GREEN] `internal/mcp/mcp.go` `handleSave`: call `worker.Enqueue(savedSyncID)` post-save when configured; wire `Worker` field through `registerTools`/`NewServerWithConfig`.
- [ ] 4.5 [RED] `internal/server/server_test.go`: `handleAddObservation` (POST /observations) enqueues to the same worker (HTTP save path).
- [ ] 4.6 [GREEN] `internal/server/server.go` `handleAddObservation`: call `worker.Enqueue` post-save (mirrors 4.4).
- [ ] 4.7 [GREEN] Wire `Worker.Start(ctx)` in BOTH `cmd/omnia/main.go` `cmdMCP` (stdio) and `cmdServe` (HTTP), sharing the PR-3 `embed.Store`/`Client`; stop on the existing shutdown `ctx`/signal path.

## Phase 5: Cloud Semantic Parity (PR 5)

- [ ] 5.1 [RED] `internal/cloud/cloudstore/embeddings_test.go`: upsert + brute-force cosine search round trip (test Postgres fixture) — top-k order, dim-mismatch skip, project scoping. [cloud REQ1]
- [ ] 5.2 [GREEN] `internal/cloud/cloudstore/embeddings.go` (new): `cloud_embeddings(sync_id PK, project, model, dim, vector BLOB, content_hash, updated_at)` + `UpsertEmbedding` + `SearchEmbeddings(project, vec, k)` (reuse `decodeVector`/`dot`, CGO-free).
- [ ] 5.3 [GREEN] `internal/cloud/cloudstore/cloudstore.go` `migrate()`: add `CREATE TABLE IF NOT EXISTS cloud_embeddings` to the DDL slice.
- [ ] 5.4 [RED] `internal/sync` test: `SyncEntityEmbedding` push/pull round-trip (opaque BLOB payload, idempotent apply) — mirrors `SyncEntityRelation` precedent.
- [ ] 5.5 [GREEN] `internal/store/store.go`: add `SyncEntityEmbedding` constant near `SyncEntityRelation`; `internal/sync/sync.go`: push/pull case handling; `embed.Worker` emits the mutation on upsert.
- [ ] 5.6 [RED] `internal/cloud/clouddash/source_test.go`: `TestSemanticAvailable` (`Semantic()` returns a real index, not `(nil,false)`); `TestSemanticScopeIsolation` (out-of-scope project excluded despite high score); `TestSearchDegradesWithoutEmbedding` (unembedded-but-synced memory still returned lexically). [cloud REQ1, REQ3, REQ4]
- [ ] 5.7 [GREEN] `internal/cloud/clouddash/source.go`: `Semantic()` returns a `cloudstore`-backed `dashboard.SemanticIndex`, scoped by `scopeFrom(ctx)`/`CanView`; fuse cloud lexical (`cloudRecords.Search`) + cloud semantic via `recall.Fuse`.
- [ ] 5.8 [RED] same file: ES query returns EN-authored synced+embedded memory (cloud bilingual parity). [cloud REQ2]
- [ ] 5.9 [GREEN] Covered by 5.7 wiring — verification-only, no new prod code.

## Phase 6: Cleanup / Full-Suite Verification (tail of PR 5)

- [ ] 6.1 Update config docs (`config.yaml` example / README config section) with `recall:` block and new `embeddings.model` default.
- [ ] 6.2 Run `go test ./...` and `go test -cover ./...`; report per-package coverage for `internal/recall`, `internal/embed`, `internal/cloud/cloudstore`, `internal/cloud/clouddash`, `internal/mcp`.
- [ ] 6.3 Confirm rollback: `recall.enabled=false` and `cloud_semantic.enabled=false` both reproduce pre-change behavior byte-for-byte (regression pins from 3.3 and 5.6 stay green).
