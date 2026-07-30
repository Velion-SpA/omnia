# Tasks: Omnia v0.4 — La frontera de la memoria (Memory Frontier)

> Planning artifact only. Every file:line citation was independently re-verified against the live tree at
> `/private/tmp/omnia-v04-planning` (third pass, after proposal and design). One correction found: the local
> Ollama client lives in `internal/embed/client.go`, not `embed.go` (design.md already cites the correct path;
> tasks below use `client.go`). Written for a different AI tool (Codex) to implement directly — no other
> context assumed.

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~4,300–4,600 across 11 PRs (5 new packages: `keychain`, `enforce`, `consolidate`, `ranker`, `cartridge`; 3 heavily-modified core files: `config.go`, `store.go`, `embed/store.go`; MCP/CLI wiring; full TDD suites per capability) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 11, stacked to main, in the order below |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

Two of the seven capabilities (`sqlite-vec-index`, `memory-at-rest-security`) and the flagship
(`memory-enforcement-gate`) are each pre-split into 2 PRs specifically because either half alone is estimated
to exceed 400 lines — this is why the release is 11 PRs, not 7. PR 9 (consolidation), PR 10 (ranker), and PR 11
(cartridge) remain the largest single-capability PRs (~430–540 lines each); if a realized diff exceeds budget,
split further at apply time or request a maintainer `size:exception` on that PR specifically — do not silently
inflate a later PR's diff to compensate.

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Config scaffolding: 7 default-OFF blocks + `applyDefaults` | PR 1 | Base `main`. Foundation all other units need; independently landable/revertible. |
| 2 | `sqlite-vec-index` A: dual-driver + vec0 table + dual-write + backfill | PR 2 | Base `main` after PR 1. Introduces `ncruces/go-sqlite3`. |
| 3 | `sqlite-vec-index` B: vec-KNN reads + parity + fallback | PR 3 | Base `main` after PR 2. |
| 4 | `memory-at-rest-security` A: `internal/keychain` + adiantum VFS wiring | PR 4 | Base `main` after PR 3. Shares the ncruces driver seam from PR 2. |
| 5 | `memory-at-rest-security` B: non-destructive migration + CLI + audit/provenance | PR 5 | Base `main` after PR 4. |
| 6 | `code-decision-graph`: `BlameLine`/`CodeDecisionGraph` + `mem_blame`/`omnia blame` | PR 6 | Base `main` after PR 5. No new deps. |
| 7 | `memory-enforcement-gate` A: matcher + sandboxed command runner | PR 7 | Base `main` after PR 6. |
| 8 | `memory-enforcement-gate` B: MCP/CLI wiring + override + audit taxonomy | PR 8 | Base `main` after PR 7. Flagship completes here. |
| 9 | `sleep-consolidation`: clustering + `Client.Generate` + digest writer + CLI | PR 9 | Base `main` after PR 8. |
| 10 | `learned-ranker`: pure-Go logistic re-ranker + eval-gated promotion | PR 10 | Base `main` after PR 9. |
| 11 | `repo-cartridge`: build/load/invalidate | PR 11 | Base `main` after PR 10. Consumes PR 6 + PR 10 output, degrades gracefully if either disabled. |

---

## Phase 1: Config Scaffolding (PR 1, base: `main`)

Satisfies: all 7 capabilities' REQ-4x0/REQ-450/REQ-460 "Default-Off Config Gate" scenarios (foundation only).

- [x] 1.1 [RED] `internal/config/config_test.go`: assert `Config{}` zero-value has `CodeGraph`/`Enforcement`/
  `Consolidation`/`Encryption`/`Ranker`/`Cartridge`/`VecIndex`, all `Enabled == false`; assert `Load` on YAML
  mentioning none of the 7 keys produces a `Config` byte-for-byte identical to a pre-v0.4 golden fixture — fails,
  types don't exist.
- [x] 1.2 [GREEN] Add the 7 structs (design's "Interfaces / Contracts") to `internal/config/config.go` near
  `TimeTravelConfig` (`:114`); wire `applyDefaults` (`:694`) param-only defaults (Enforcement.Mode="flag";
  Consolidation.MinScore/K/MinClusterSize/MaxClusterSize; Ranker.MinTrainExamples=50; Cartridge.TopMemories;
  VecIndex.Quantization="none") — no `Enabled` field gets a default.
- [x] 1.3 [REFACTOR] Match doc-comment style of `TimeTravelConfig`/`ReviewConfig` (`:114`/`:238`); confirm no
  `*KeyPresent` probe (`:591`/`:612` pattern) is needed anywhere — all 7 are plain-bool default-false.
- [x] 1.4 Verification: `CGO_ENABLED=0 go build ./... && go vet ./...` clean; `go test ./internal/config/...`
  green; zero-v0.4-keys config round-trips byte-for-byte vs a v0.3.2 fixture.

## Phase 2: `sqlite-vec-index` A — Driver + Dual-Write (PR 2, base: `main` after PR 1)

Satisfies: REQ-460 (gate), REQ-461 (non-destructive), REQ-464 (CGO-free), REQ-466/467 (migration reporting/intact).

- [ ] 2.1 [RED] `internal/embed/store_ncruces_test.go`: open `Store` with `vector_index.enabled=true` over a
  1,000-row fixture; assert a `vec_embeddings` table now exists AND all 1,000 `embeddings` rows are unchanged
  (REQ-461) — fails, no driver switch yet.
- [ ] 2.2 [GREEN] Add `github.com/ncruces/go-sqlite3`; implement driver selection at `OpenStore`'s `sql.Open`
  call (`embed/store.go:79`, mirrors ADR-1's `store.go:35`/`:847`); create `vec_embeddings` vec0 table alongside
  `embeddings` (`store.go:52`).
- [ ] 2.3 [RED] Backfill test: seed 998 valid + 2 dimension-mismatched rows; run first-enable backfill; assert
  report shows 998 indexed / 2 skipped with reason (REQ-466) and `embeddings` row count still 1,000 (REQ-467).
- [ ] 2.4 [GREEN] Implement `Upsert` dual-write (`store.go:95`) and a one-time backfill pass on first enable /
  `omnia embed --reindex`.
- [ ] 2.5 [REFACTOR] Extract the backfill loop shared by first-enable and `--reindex`; confirm
  `CGO_ENABLED=0 go build ./...` still succeeds (REQ-464).
- [ ] 2.6 Verification: `go test ./internal/embed/... -run VecIndex` green; disabled-path
  (`vector_index.enabled=false`) confirms zero `vec_embeddings` writes, `embeddings` shape unchanged (REQ-460).

## Phase 3: `sqlite-vec-index` B — Vec-KNN Reads + Parity (PR 3, base: `main` after PR 2)

Satisfies: REQ-462 (parity), REQ-463 (KNN-flat only), REQ-465 (fallback).

- [ ] 3.1 [RED] Parity test: 500-embedding fixture, `Search` with flag on vs off returns identical top-k
  `sync_id`s in identical order (REQ-462) — fails, `Search` still brute-forces only.
- [ ] 3.2 [GREEN] Implement vec0-KNN `Search`/`SearchScoped` (`store.go:164`/`:179`) reading `vec_embeddings`
  when enabled/populated; keep `search` (`:186`) untouched as the fallback body.
- [ ] 3.3 [RED] Corrupted-index test: force the vec table to fail open/query; assert `Search` still returns
  correct results via brute-force fallback, no error surfaced (REQ-465).
- [ ] 3.4 [GREEN] Wrap vec0 query calls with a fallback branch to `search` (`:186`) on any open/query error.
- [ ] 3.5 [RED] `Graph`/`GraphScoped` test: per-node vec0 KNN (`store.go:264`/`:282`) produces the same edge set
  as the existing O(N²) scan on a fixture; no HNSW/ANN artifact on disk (REQ-463).
- [ ] 3.6 [GREEN] Implement per-node vec0 KNN lookup inside `Graph`/`GraphScoped`, gated by the same flag;
  brute-force remains the else-branch.
- [ ] 3.7 [REFACTOR] Share the vec0-query helper between `Search`/`SearchScoped` and `Graph`/`GraphScoped`.
- [ ] 3.8 Verification: full `internal/embed` suite green under both driver paths; `CGO_ENABLED=0 go build ./...`
  clean; disabled-path byte-for-byte vs a golden brute-force fixture (REQ-460).

## Phase 4: `memory-at-rest-security` A — Keychain + Adiantum VFS (PR 4, base: `main` after PR 3)

Satisfies: REQ-430 (gate), REQ-431 (keychain key), REQ-432 (transparent encryption), REQ-434 (degradation).

- [ ] 4.1 [RED] `internal/keychain/keychain_test.go` with an injectable runner fake (mirrors `Probe.runGit`'s
  pattern used by `repoRoot`/`HeadSHA`/`Blame`, `internal/anchor/anchor.go`): assert `Get`/`Set` shell out with
  the right args, and a missing-CLI runner returns a typed "unavailable" error — fails, package doesn't exist.
- [ ] 4.2 [GREEN] Create `internal/keychain/keychain.go`: `Get`/`Set(service, account, value)` via `os/exec` to
  macOS `/usr/bin/security` or Linux `secret-tool`, injectable runner; `GenerateKey()` via `crypto/rand` (32
  bytes), service=`omnia`, account=`db-key-v1`.
- [ ] 4.3 [RED] Driver-selection test: `encryption.enabled=true` + fake keychain with a key present opens via
  ncruces+adiantum; a written observation reads back identical to the plaintext path (REQ-432) — fails, no
  adiantum wiring yet.
- [ ] 4.4 [GREEN] Wire `openDB` (`store.go:35`/`:847`/`:897`; `embed/store.go:79`) to resolve the ncruces+
  adiantum VFS when `encryption.enabled`, sourcing the key via `internal/keychain` (generate-once-on-first-enable,
  REQ-431).
- [ ] 4.5 [RED] Keychain-unavailable test: `encryption.enabled=true` + fake runner returns "CLI not found"; with
  `allow_plaintext_fallback` at its default `false`, assert the store REFUSES to open with a clear error; with
  it `true`, assert the store opens unencrypted with a stderr warning + audit entry (REQ-434).
- [ ] 4.6 [GREEN] Implement the degradation branch: unreachable keychain + `allow_plaintext_fallback=false`
  (default) → refuse to open; `=true` → open unencrypted, stderr warning, audit entry.
- [ ] 4.7 [REFACTOR] Extract the keychain-or-fail decision into one helper shared by `store.go` and
  `embed/store.go` driver selection.
- [ ] 4.8 Verification: `internal/keychain` + affected `internal/store`/`internal/embed` tests green;
  `CGO_ENABLED=0 go build ./...` clean; disabled-path (`encryption.enabled=false`) byte-for-byte (REQ-430).

## Phase 5: `memory-at-rest-security` B — Migration + CLI + Audit (PR 5, base: `main` after PR 4)

Satisfies: REQ-433 (threat model), REQ-435 (reversible), REQ-436 (provenance), REQ-437 (audit coverage).

- [ ] 5.1 [RED] Migration test: seed a 1,000-row plaintext `omnia.db`; run first-enable migration; assert an
  encrypted file atomically replaces it, row counts match pre/post, and a timestamped `.bak` of the original
  survives until next clean startup — fails, no migration exists.
- [ ] 5.2 [GREEN] Implement migrate-on-first-enable: fetch/generate key → `VACUUM INTO` (or dump+load) an
  encrypted target → fsync + row-count verify → atomic rename → retain timestamped `.bak`; abort-and-keep-
  plaintext on any step failure (never a half-encrypted DB).
- [ ] 5.3 [RED] `omnia security decrypt` test: given an encrypted store, decrypt restores a plaintext DB
  readable by the default modernc driver, identical row counts, never locking the user out (REQ-435).
- [ ] 5.4 [GREEN] Implement `omnia security encrypt`/`decrypt`/`rotate-key` CLI (dispatch `main.go:730`),
  reversing the migration via the keychain key.
- [ ] 5.5 [RED] Provenance/audit test: an observation written with `source="user"` is retrievable with
  `trust_tag: "user"` in its receipt (REQ-436, `provenance.go:26`); confirm PR 7/8's gate decisions and PR 9's
  consolidation actions each produce one audit entry (REQ-437, cross-phase check once those land).
- [ ] 5.6 [GREEN] Surface `TrustTag` consistently in read receipts (already carried by `Entry.TrustTag`,
  `audit.go:31`) — no schema change needed.
- [ ] 5.7 [REFACTOR] Document the explicit threat model (REQ-433: protects disk theft/lost-laptop while the
  process is stopped; does NOT protect live-memory-dump or an attacker holding the unlocked keychain) in `omnia
  doctor`/status output.
- [ ] 5.8 Verification: full `internal/store`/`internal/embed`/`internal/audit` suites green under both driver
  paths; migration test against a 10k-row fixture; disabled-path byte-for-byte.

## Phase 6: `code-decision-graph` (PR 6, base: `main` after PR 5)

Satisfies: REQ-400–406.

- [x] 6.1 [RED] `internal/store/anchors_test.go`: two anchors both covering line 50 of a fixture file —
  `BlameLine(repoRoot, file, 50)` must return BOTH linked memories, each tagged its own `anchor_status` (REQ-402)
  — fails, method doesn't exist.
- [x] 6.2 [GREEN] Implement `BlameLine(repoRoot, file string, line int) ([]BlameHit, error)` over
  `memory_anchors` (reusing `ListActiveAnchors`/`GetAnchorsForObservations`, `anchors.go:163`/`:362`) with
  design's overlap ranking: active-before-stale/traveled, narrowest-range-first, `blame_at` desc, `id` asc.
- [x] 6.3 [RED] Stale-anchor test: a stale anchor covering the line still appears, tagged
  `anchor_status: "stale"`, never hidden (REQ-403).
- [x] 6.4 [GREEN] Confirm 6.2's ranking surfaces stale rows rather than filtering them.
- [x] 6.5 [RED] `CodeDecisionGraph(project)` test: 5 active anchors → 3 memories yields exactly 5 edges
  (REQ-404) — fails, method doesn't exist.
- [x] 6.6 [GREEN] Implement `CodeDecisionGraph(project string) (nodes, edges, error)` as a thin projection over
  `ListActiveAnchors` — no new table/engine.
- [x] 6.7 [RED] Not-a-git-repo test: `mem_blame` on a file outside any repo returns zero anchors with a clear
  reason, never a crash-like error (REQ-406).
- [x] 6.8 [GREEN] Wire `mem_blame` MCP tool (gated `code_graph.enabled` in `mcp.MCPConfig`) and `omnia blame
  <file>:<line>` CLI (dispatch `main.go:730`, alongside `forget-scan` `:753`), mapping `ErrGitNotInstalled`/
  `ErrNotAGitRepo` to the empty-result contract.
- [x] 6.9 [REFACTOR] Share the `file:line` → repo-relative-path normalization helper between `mem_blame` and
  the CLI.
- [x] 6.10 Verification: `internal/store`/`internal/mcp`/CLI tests green; disabled-path
  (`code_graph.enabled=false`) confirms "capability disabled", no anchor table touched (REQ-400); no LLM call
  anywhere in this path (REQ-405).
  - Remediation evidence: MCP returns grouped preview-only hits with an explicit empty array on no match;
    fractional lines and non-git explicit repo roots are rejected; runtime tests cover enabled MCP/CLI,
    disabled MCP registration, and the 5-anchor → 3-memory graph projection.

## Phase 7: `memory-enforcement-gate` A — Matcher + Command Runner (PR 7, base: `main` after PR 6)

Satisfies: REQ-411 (trusted-only feed), REQ-412 (mechanical, no LLM), REQ-413 (verdict contract), REQ-414
(flag-default).

- [ ] 7.1 [RED] `internal/enforce/matcher_test.go`: one `trusted` procedure matches touched files alongside two
  matching `candidate`/`retired` procedures; assert only the trusted one is selected (REQ-411) — fails, package
  doesn't exist.
- [ ] 7.2 [GREEN] Create `internal/enforce/matcher.go`: candidate set = `ListProcedures{State:trusted,
  Project}` narrowed via `SearchProcedures` (FTS5 over trigger+steps_summary, `procedures.go:390`) against
  touched file paths.
- [ ] 7.3 [RED] Runner test: a `tests_pass` procedure with a configured command that exits non-zero yields a
  failing postcondition with exit code + output preview, no LLM call anywhere (REQ-412).
- [ ] 7.4 [GREEN] Create `internal/enforce/runner.go`: `exec.CommandContext` in repo root with a hard timeout
  (`EnforcementConfig.TimeoutSeconds`); exit 0 = pass for `tests_pass`/`lint_clean`/`build_green`; `custom`
  evaluates `postcondition_expr` only when `AllowCustomCommands` is true.
- [ ] 7.5 [RED] Verdict-contract test: all applicable postconditions pass → verdict exactly `pass`; one fails
  with `Mode` unset (default "flag") → verdict exactly `flag`, never halts (REQ-413/414).
- [ ] 7.6 [GREEN] Implement the verdict decision: any failing postcondition ⇒ `flag` (default) or `block`
  (`mode: "block"` explicit).
- [ ] 7.7 [REFACTOR] Extract the "command not configured for this kind" skip-with-note branch (fail-safe, never
  block) into its own helper.
- [ ] 7.8 Verification: `internal/enforce` unit suite green with injected fake command runner; `go vet`/
  `go build` clean; nothing yet reachable from MCP/CLI (wiring deferred to Phase 8).

## Phase 8: `memory-enforcement-gate` B — MCP/CLI + Audit (PR 8, base: `main` after PR 7)

Satisfies: REQ-410 (gate), REQ-415 (override), REQ-416 (audit), REQ-417 (no auto-fix), REQ-418 (tool/CLI parity).

- [ ] 8.1 [RED] Override test: a failing postcondition re-invoked with `override: true` returns `pass`, audit
  records a distinct `override` verdict, not `pass` (REQ-415) — fails, no override plumbing yet.
- [ ] 8.2 [GREEN] Add `override`/`override_reason` params to `mem_enforce`/`omnia enforce`; record
  `verdict: override` distinctly in the audit entry.
- [ ] 8.3 [RED] Audit-taxonomy test: any pass/flag/block/override outcome writes exactly one `internal/audit`
  entry with verdict, procedure sync_id(s), postcondition kind, exit code (REQ-416).
- [ ] 8.4 [GREEN] Add `ActionEnforce` to the `Action` enum (`audit.go:17`) + additive `omitempty` `Entry` fields
  (`:31`) per ADR-7; call `audit.Append` at every gate decision.
- [ ] 8.5 [RED] No-auto-fix test: a file touched by a failing postcondition is never written to by the gate
  itself (REQ-417).
- [ ] 8.6 [GREEN] Assert (unit-test-enforced) that the matcher/runner only read/execute — never call any
  file-write API.
- [ ] 8.7 [RED] Disabled-gate test: `enforcement.enabled=false` → `mem_enforce`/`omnia enforce` return disabled
  response, no command executed, no audit entry (REQ-410).
- [ ] 8.8 [GREEN] Gate `mem_enforce` registration behind `enforcement.enabled` in `mcp.MCPConfig` (mirrors
  `ProceduralWiring` `main.go:1342`/`:1343`); register `omnia enforce` in CLI dispatch (`:730`).
- [ ] 8.9 [REFACTOR] Consolidate the pass/flag/block/override → audit-entry mapping into one function shared by
  MCP and CLI entry points (REQ-418 contract parity).
- [ ] 8.10 Verification: full `internal/enforce`/`internal/mcp`/`internal/audit` suites green; disabled-path
  byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 9: `sleep-consolidation` (PR 9, base: `main` after PR 8)

Satisfies: REQ-420–426.

- [ ] 9.1 [RED] `internal/consolidate/cluster_test.go`: 3 memories connected above `min_score` via a fixture
  `embed.GraphScoped` output form one cluster via union-find (REQ-421) — fails, package doesn't exist.
- [ ] 9.2 [GREEN] Create `internal/consolidate/cluster.go`: union-find over `embed.GraphScoped(project)`
  (`store.go:282`) edges ≥ `MinScore` (default 0.5), per-node cap `K` (default 8).
- [ ] 9.3 [RED] Cluster-size-bound tests: a 2-member cluster with `min_cluster_size=3` produces no digest,
  reported "below minimum size" (REQ-423); a 40-member cluster with `max_cluster_size=20` still references all
  40 sources across one or more digests, none dropped (REQ-423).
- [ ] 9.4 [GREEN] Implement size-bound handling: skip-below-minimum; cap-or-split-above-maximum via
  highest-degree-node top-K neighborhood.
- [ ] 9.5 [RED] `internal/embed/client_test.go`: `Client.Generate(ctx, prompt)` posts to Ollama's `/api/chat`
  on `Embeddings.BaseURL`, sibling to `Embed` (`embed/client.go:78`) — fails, method doesn't exist.
- [ ] 9.6 [GREEN] Add `Generate(ctx, prompt) (string, error)` to `embed.Client`: low-temperature, fixed prompt,
  `/api/chat` call.
- [ ] 9.7 [RED] Digest-write test: a qualifying 3-memory cluster produces one `observations` row `type="digest"`
  + 3 `memory_relations` rows with a new `RelationConsolidates` verb, system provenance; all 3 sources remain
  independently retrievable via `mem_search` after (REQ-422/426).
- [ ] 9.8 [GREEN] Add `RelationConsolidates` to the relation vocabulary (`relations.go:32`); implement the
  digest writer (system-provenance relation rows, mirrors `MarkAnchorStale`'s supersedes-row pattern,
  `anchors.go:260`); add `digest` to `DefaultImportanceWeight` (weight 3, `config.go:398`).
- [ ] 9.9 [RED] Ollama-unreachable test: consolidation exits cleanly with a log line, no digest/relation
  written, no panic (REQ-424 degradation).
- [ ] 9.10 [GREEN] Wrap the `Generate` call with the same degrade-on-unreachable pattern as recall's Ollama
  auto-detect.
- [ ] 9.11 [RED] Disabled/idle-off test: `consolidation.enabled=false` → `omnia consolidate` no-op, no idle
  worker starts even with `consolidation.idle=true` (REQ-420/425).
- [ ] 9.12 [GREEN] Gate `omnia consolidate` CLI (dispatch `:730`) and the optional idle worker (mirrors
  `buildAutoEmbedWorker`, `main.go:1331`) behind `consolidation.enabled`.
- [ ] 9.13 [REFACTOR] Extract the cluster-selection + digest-writing pipeline into one orchestration function
  shared by the CLI and idle worker.
- [ ] 9.14 Verification: `internal/consolidate`/`internal/embed`/`internal/store` suites green; disabled-path
  byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 10: `learned-ranker` (PR 10, base: `main` after PR 9)

Satisfies: REQ-440–446.

- [ ] 10.1 [RED] `internal/ranker/features_test.go`: every feature (lexical RRF contribution, semantic cosine,
  recency, `DefaultImportanceWeight`, outcome/judgment history) traces to an existing recall/config/store field
  — no invented signal (REQ-443) — fails, package doesn't exist.
- [ ] 10.2 [GREEN] Create `internal/ranker/features.go`: build the normalized [0,1] feature vector from existing
  `recall`/`config`/`store` fields.
- [ ] 10.3 [RED] `internal/ranker/model_test.go`: batch gradient descent on a small labeled fixture separates
  positive (`compatible`/`worked`) from negative (`supersedes`/`conflicts_with`/`did_not_work`) examples — fails,
  package doesn't exist.
- [ ] 10.4 [GREEN] Create `internal/ranker/model.go`: L2-regularized logistic regression, batch gradient
  descent, serialize to `<dataDir>/ranker/model-<version>.json` + `current` pointer, versioned by a hash of
  `{feature-schema, train-set size, trained-at}` (REQ-445).
- [ ] 10.5 [RED] Cold-start test: `enabled=true`, zero judgments/outcomes, ranking byte-for-byte identical to
  `DefaultFuseParams`/`AdaptiveFloor` (`recall.go:74`/`:96`) (REQ-441).
- [ ] 10.6 [GREEN] Implement the cold-start fallback: below `MinTrainExamples` (default 50), no `current`
  model, or decode error → use `DefaultFuseParams`/`AdaptiveFloor` unchanged (REQ-446).
- [ ] 10.7 [RED] `omnia rank-train` test: training runs `eval.RunOnce` (`harness.go:37`) after fitting and
  refuses to write `current` on any regression (REQ-444).
- [ ] 10.8 [GREEN] Implement `omnia rank-train` CLI (dispatch `:730`): train → `eval.RunOnce` gate → promote
  `current` only if no regression.
- [ ] 10.9 [RED] Model-invalidation test: a model trained against an older feature shape is detected invalid
  and not used live (REQ-445); a corrupted model file falls back silently to default floors, no error surfaced
  (REQ-446).
- [ ] 10.10 [GREEN] Implement the feature-schema-hash check on load; wrap model load in a recover-to-fallback
  path.
- [ ] 10.11 [REFACTOR] Place the re-rank pass at the `internal/mcp` wiring boundary — the same seam as
  `RecallRanking`/`RankResults` (`main.go:1300`/`:1304`) — keeping `internal/recall`/`internal/store` untouched.
- [ ] 10.12 Verification: `internal/ranker` suite + `internal/eval` gate integration green; disabled-path and
  cold-start byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 11: `repo-cartridge` (PR 11, base: `main` after PR 10)

Satisfies: REQ-450–456.

- [ ] 11.1 [RED] `internal/cartridge/build_test.go`: building at commit `abc123` produces a JSON artifact tagged
  commit `abc123` + a format version, containing `top_memories`/`anchors`/`trusted_procedures` (REQ-451/456) —
  fails, package doesn't exist.
- [ ] 11.2 [GREEN] Create `internal/cartridge/build.go`: `{schema_version, repo_root, head_sha, built_at,
  top_memories[], anchors[], trusted_procedures[], ranker_model_version?}` at
  `<dataDir>/cartridges/<repo-id>-<head-sha>.json`, keyed via `HeadSHA` (`anchor.go:192`); `top_memories` ranked
  by the active ranker (PR 10) or current fused order if disabled; anchors from PR 6's `CodeDecisionGraph`;
  `trusted` procedures for the project.
- [ ] 11.3 [RED] Stale-commit test: a cartridge built at `abc123`, current HEAD `def456` — `cartridge load`
  reports stale (commit mismatch), never served as current (REQ-452).
- [ ] 11.4 [GREEN] Implement `cartridge load`'s `HeadSHA` comparison and stale/cold-start branch.
- [ ] 11.5 [RED] Missing-cartridge test: no cartridge file exists → session falls back to cold-query, no error
  (REQ-453).
- [ ] 11.6 [GREEN] Implement the missing/corrupt-file → cold-start fallback path (never an error to the
  caller).
- [ ] 11.7 [RED] Version-mismatch test: an older-format cartridge is detected and rejected, falling back to
  cold-start rather than misreading (REQ-456).
- [ ] 11.8 [GREEN] Implement the `schema_version` check on load.
- [ ] 11.9 [RED] Disabled-gate CLI test: `cartridge.enabled=false` → `cartridge build`/`load` report disabled,
  no file written/read (REQ-450).
- [ ] 11.10 [GREEN] Wire `omnia cartridge build [--repo]` / `load [--repo]` CLI (nested-subcommand-namespace,
  mirrors `cloud`/`embed`/`migrate`/`eval` `main.go:775-791`); gate both behind `cartridge.enabled`.
- [ ] 11.11 [REFACTOR] Add an explicit content-shape assertion in the build test confirming no weight-level/
  KV-cache artifact is ever included in the cartridge payload (REQ-455); confirm the artifact never syncs to
  cloud (REQ-454).
- [ ] 11.12 Verification: `internal/cartridge` suite green; disabled-path confirms build/load are no-ops,
  session startup unchanged (REQ-450); `CGO_ENABLED=0 go build ./...` clean; **release-wide regression**:
  `go test ./...` green with every v0.4 flag off = byte-for-byte v0.3.2 across all 11 PRs.
