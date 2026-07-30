# Tasks: Omnia v0.4 — La frontera de la memoria (Memory Frontier)

> Planning artifact only. Every file:line citation was independently re-verified against the live tree at
> `/private/tmp/omnia-v04-planning` (third pass, after proposal and design). One correction found: the local
> Ollama client lives in `internal/embed/client.go`, not `embed.go` (design.md already cites the correct path;
> tasks below use `client.go`). Written for a different AI tool (Codex) to implement directly — no other
> context assumed.

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~4,600–5,100 across 15 chained PRs. The original PR 2 map (~430–560) omitted direct consumers; revised PR 2A is ~230–300, PR 2B ~270–350, and PR 3 ~280–360. |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 2A → PR 2B → PR 3 → PR 4 → PR 5 → PR 6A → PR 6B → PR 7 → PR 8 → PR 9A → PR 9B → PR 10A → PR 10B → PR 11 |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

The cached chain strategy is **stacked-to-main**: every slice targets `main` only after its predecessor has
merged; feature-branch-chain is not permitted for this release. Merge order is PR 1 → PR 2A → PR 2B → PR 3 →
PR 4 → PR 5 → PR 6A → PR 6B → PR 7 → PR 8 → PR 9A → PR 9B → PR 10A → PR 10B → PR 11. `ask-on-risk` applies
only to newly discovered >400-line splits or a required `size:exception`; it does not re-open the cached chain
strategy. Every unit must record planned and actual changed lines before apply: a forecast or actual diff above
400 MUST split into the next named child PR or obtain an explicit maintainer `size:exception`. PR 6, PR 9, and
PR 10 are pre-split below; PR 11 retains its prior ~430–540 estimate and therefore MUST be split or receive
`size:exception` before its apply phase. PR 2A proves connector/state, PR 2B owns lifecycle and production
composition while reads stay brute-force, and PR 3 alone routes Vec1 reads.

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Config scaffolding: 7 default-OFF blocks + `applyDefaults` | PR 1 | Complete; base `main`. |
| 2 | Vec1 connector, derived state, active dimension, maintenance API, config correction | PR 2A | Depends on PR 1; no production composition or read routing. |
| 3 | Derived-index lifecycle, all embed-store composition, `embed --reindex` | PR 2B | Depends on PR 2A; reads remain brute force. |
| 4 | Vec1 KNN reads, parity, fallback, and graph routing | PR 3 | Depends on PR 2B. |
| 5 | Keychain + adiantum VFS wiring | PR 4 | Depends on PR 3; reforecast before apply. |
| 6 | Non-destructive encryption migration + CLI + audit/provenance | PR 5 | Depends on PR 4; reforecast before apply. |
| 7 | Code-decision graph store/query foundation | PR 6A | Complete (232 lines). Landed ahead of PR 2–5 — no hard dependency on the vec-index/encryption chain per design, only the suggested review order. |
| 8 | `mem_blame`/`omnia blame` wiring and contract tests | PR 6B | Complete (488 lines incl. docs). Depends on PR 6A only. |
| 9 | Enforcement matcher + command runner | PR 7 | Depends on PR 6B; reforecast before apply. |
| 10 | Enforcement MCP/CLI, override, and audit taxonomy | PR 8 | Depends on PR 7; reforecast before apply. |
| 11 | Consolidation cluster + Ollama client foundation | PR 9A | Complete (~197 lines). Landed independently of PR 2-8 — no hard dependency, only the suggested review order. |
| 12 | Consolidation digest, CLI, and idle integration | PR 9B | Depends on PR 9A; ~263 lines. |
| 13 | Learned-ranker feature/model foundation (cold-start integration lands in PR 10B, not here) | PR 10A | Complete (350 lines). Landed independently of PR 2-9 — no hard dependency, only the suggested review order. |
| 14 | Ranker train/eval/live integration, incl. cold-start fallback at the MCP boundary | PR 10B | Complete (~300 lines). Depends on PR 10A only. |
| 15 | Repo cartridge | PR 11 | Depends on PR 10B; previous ~430–540 estimate requires split or `size:exception`. |

---

## Phase 1: Config Scaffolding (PR 1, base: `main`)

Satisfies: all 7 capabilities' REQ-4x0/REQ-450/REQ-460 "Default-Off Config Gate" scenarios (foundation only).

- [x] 1.1 [RED] `internal/config/config_test.go`: assert `Config{}` zero-value has `CodeGraph`/`Enforcement`/
  `Consolidation`/`Encryption`/`Ranker`/`Cartridge`/`VecIndex`, all `Enabled == false`; assert `Load` on YAML
  mentioning none of the 7 keys produces a `Config` byte-for-byte identical to a pre-v0.4 golden fixture — fails,
  types don't exist.
- [x] 1.2 [GREEN] Add the 7 structs (design's "Interfaces / Contracts") to `internal/config/config.go` near
  `TimeTravelConfig` (`:114`); wire `applyDefaults` (`:694`) param-only defaults (Enforcement.Mode="flag";
  Consolidation.MinScore/K/MinClusterSize/MaxClusterSize; Ranker.MinTrainExamples=50; Cartridge.TopMemories).
  `VecIndex` has only `Enabled`; no format, quantization, int8, or binary option is part of v0.4.
- [x] 1.3 [REFACTOR] Match doc-comment style of `TimeTravelConfig`/`ReviewConfig` (`:114`/`:238`); confirm no
  `*KeyPresent` probe (`:591`/`:612` pattern) is needed anywhere — all 7 are plain-bool default-false.
- [x] 1.4 Verification: `CGO_ENABLED=0 go build ./... && go vet ./...` clean; `go test ./internal/config/...`
  green; zero-v0.4-keys config round-trips byte-for-byte vs a v0.3.2 fixture.

## Phase 2: `sqlite-vec-index` A — Vec1 Connector + Derived State (PR 2A, depends on PR 1)

Satisfies: REQ-460, REQ-461, REQ-463, REQ-464, REQ-466–468. This PR proves only the isolated
foundation: no production composition and all read surfaces remain brute force.

- [ ] 2.1 [RED] `internal/embed/store_vec1_test.go`: the options-based `OpenStore` seam keeps an absent/false
  flag on modernc with byte-for-byte brute-force behavior; enabled test setup requires a private Vec1 connector,
  not a globally registered driver (REQ-460/464).
- [ ] 2.2 [GREEN] Pin `github.com/ncruces/go-sqlite3@v0.35.2` in `go.mod`/`go.sum`; add options-based connector
  selection in `internal/embed/store.go` using `driver.Open(dsn, vec1.Register)` on every physical connection.
  Keep the connector private to `internal/embed`; do not route production callers yet.
- [ ] 2.3 [RED] In `internal/embed/store_vec1_test.go`, assert enabled setup creates same-DB
  `vec_embeddings USING vec1(vector, project)`, rebuilds `{index:"flat", distance:"cos"}`, and never creates
  an int8, binary, ANN, or second-database artifact (REQ-461/463).
- [ ] 2.4 [GREEN] Add derived Vec1 DDL and readiness/state metadata in `internal/embed/store.go`; map only the
  stable `embeddings.rowid` to `vec_embeddings.rowid`, mirror `COALESCE(project,'')`, and preserve
  `embeddings` as authoritative.
- [ ] 2.5 [RED] Test first valid source vector establishes `active_dim` in rowid order; canonical little-endian
  source blobs are re-encoded into a separate native-endian Vec1 float32 blob on supported little-endian targets.
  A byte-order/state mismatch or unsupported host leaves Vec1 unavailable (REQ-466/468).
- [ ] 2.6 [GREEN] Implement active-dimension and byte-order state validation plus native-endian derived encoding;
  skip mixed dimensions without changing source rows or resetting a ready index.
- [ ] 2.7 [RED] Seed 998 matching + 2 mixed-dimension rows; first-enable backfill and the internal reindex API
  report indexed/skipped counts and reasons while verifying all 1,000 canonical rows survive (REQ-461/466/467).
- [ ] 2.8 [GREEN] Implement first-enable backfill and a source-preserving `Reindex`/report API that discards only
  derived state, writes readiness only after count/dimension verification, and leaves failed/crashed work unready.
- [ ] 2.9 [REFACTOR] Share DDL/state/backfill helpers; run focused RED→GREEN→REFACTOR tests plus
  `CGO_ENABLED=0 go build ./...`. Assert disabled-path byte parity and explicitly document that v0.4 has no
  int8/binary format or production/read routing in PR 2A.
- [ ] 2.10 [RED] In `internal/config/v04_config_test.go`, add a failing contract test that `VecIndexConfig`
  exposes only `Enabled` and `vector_index` accepts no quantization/int8/binary knob; name this the correction
  to historical checked task 1.2, whose unreleased PR1 field/default/test expectation is now invalid.
- [ ] 2.11 [GREEN] Remove `VecIndexConfig.Quantization`, its `applyDefaults` value, and every
  `vector_index.quantization` config/environment parsing, fixture, and test expectation from
  `internal/config/config.go` and `internal/config/v04_config_test.go`; retain only `enabled` defaulting false.
- [ ] 2.12 [REFACTOR] Audit config comments and fixtures for the removed knob; run the focused config suite and
  disabled byte-parity assertion. Keep PR1's historical boxes checked, but make this unchecked PR2A amendment
  the sole strict-TDD owner of the correction.

## Phase 2B: `sqlite-vec-index` B — Derived Lifecycle + Composition (PR 2B, depends on PR 2A)

Satisfies: REQ-460, REQ-461, REQ-463–468. This PR wires every embed-store opener and maintenance only;
`Search`, `SearchScoped`, `Graph`, and `GraphScoped` remain brute force until PR 3.

- [ ] 2.13 [RED] `internal/embed/store_vec1_test.go`: upsert, update, delete, and prune mirror their source-rowid
  lifecycle into the derived table transactionally; forced Vec1 failure preserves the source mutation, marks the
  index stale/unhealthy, and does not surface an index-only write failure.
- [ ] 2.14 [GREEN] Implement derived-row upsert/update/delete/prune lifecycle in `internal/embed/store.go` with
  source-first fallback transactions, stale diagnostics, active-dimension skips, and no alternative int8/binary
  write path.
- [ ] 2.15 [RED] Add enabled/disabled composition tests for direct openers: `cmdEmbed` (`cmd/omnia/embed.go`),
  `buildAutoEmbedWorker`/`buildCLIEmbedPurgeStore` (`cmd/omnia/autoembed.go`), `buildRecallService` and
  `buildRecallServiceForCLI` (`cmd/omnia/recall.go`), and eval's `defaultRunEvalHarness` reuse of that recall
  builder (`cmd/omnia/eval.go`). Enabled paths receive Vec1 options; disabled paths keep modernc and no derived writes.
- [ ] 2.16 [GREEN] Thread the Vec1-aware `OpenStore` options/config through those seams, including MCP, serve,
  CLI search, and eval through their shared recall builder; preserve project metadata and brute-force read methods.
- [ ] 2.17 [RED] Add `cmd/omnia/dashboard_test.go` and `internal/dashboard/local_datasource_test.go` coverage:
  `cmdDashboard` → `dashboard.Config` (`internal/dashboard/handlers.go`) → `newLocalDataSource`
  (`internal/dashboard/local_datasource.go`) forwards enabled Vec1 options, while disabled stays modernc/FTS-safe.
- [ ] 2.18 [GREEN] Add the dashboard config field and thread it through `cmd/omnia/dashboard.go`,
  `internal/dashboard/handlers.go`, and `newLocalDataSource`; dashboard semantic/graph searches remain their
  existing brute-force store methods until PR 3.
- [ ] 2.19 [RED] `cmd/omnia/embed_test.go`: `omnia embed --reindex` invokes the maintenance API, reports
  indexed/skipped reasons and source-row integrity, and remains unavailable/inert when the capability is off.
- [ ] 2.20 [GREEN] Add `--reindex` CLI plumbing and report rendering through the shared maintenance API.
- [ ] 2.21 [REFACTOR] Extract one shared options builder for all direct openers; verify focused
  lifecycle/composition/CLI tests, disabled byte parity, and `CGO_ENABLED=0 go build ./...` under strict
  RED→GREEN→REFACTOR evidence.

## Phase 3: `sqlite-vec-index` C — Vec1 KNN Reads + Parity (PR 3, depends on PR 2B)

Satisfies: REQ-460, REQ-462, REQ-463, REQ-465, REQ-466, REQ-468, REQ-469.

- [ ] 3.1 [RED] `internal/embed/store_vec1_test.go`: pinned v0.35.2 flat/cos distance conversion gives scores
  `1 - distance` for normalized self/orthogonal/antipodal vectors: 1, 0, and -1 (within tolerance), never newer
  trunk or `2 - distance` semantics (REQ-469).
- [ ] 3.2 [GREEN] Add the private Vec1 score-conversion helper and lock it to the pinned dependency contract.
- [ ] 3.3 [RED] On a 500-vector/two-project fixture, enabled `Search` and `SearchScoped` return brute-force
  equivalent top-k IDs/scores except ties; scoped KNN cannot return another project's vector (REQ-462).
- [ ] 3.4 [GREEN] Route ready Vec1 `Search`/`SearchScoped` through metadata-filtered KNN, join canonical rows by
  rowid only after filtering, convert scores, and retain the existing brute-force search as the fallback body.
- [ ] 3.5 [RED] Force unavailable/corrupt/open/query/stale-index paths and a mixed-dimension query; assert each
  silently serves the correct brute-force result, while canonical source-table errors still propagate (REQ-465/466/468).
- [ ] 3.6 [GREEN] Centralize availability, dimension, and error fallback so any unhealthy derived index disables
  Vec1 for that `Store` instance without weakening project scoping or default-OFF byte parity.
- [ ] 3.7 [RED] `Graph`/`GraphScoped` parity test: per-node Vec1 KNN yields the existing O(N²) edge set (except
  ties), respects project filtering, and produces no HNSW, ANN, int8, or binary artifact (REQ-462/463).
- [ ] 3.8 [GREEN] Route only ready `Graph`/`GraphScoped` through the shared exact Vec1 query helper; keep their
  brute-force branches for disabled, unavailable, corrupt, and mixed-dimension cases.
- [ ] 3.9 [REFACTOR] Consolidate KNN query/score/fallback helpers without changing the canonical source codec;
  cover default-OFF byte parity, scoped filtering, score anchors, and all fallback branches.
- [ ] 3.10 Verification: `go test ./internal/embed/... -run VecIndex` and the full embed suite green under both
  paths; `CGO_ENABLED=0 go build ./...` clean; record strict RED→GREEN→REFACTOR evidence and v0.4's explicit
  no-int8/binary, flat/cos-only contract.

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
  `trust_tag: "user"` in its receipt (REQ-436, `provenance.go:26`); confirm PR 7/8's gate decisions and PR 9B's
  consolidation actions each produce one audit entry (REQ-437, cross-phase check once those land).
- [ ] 5.6 [GREEN] Surface `TrustTag` consistently in read receipts (already carried by `Entry.TrustTag`,
  `audit.go:31`) — no schema change needed.
- [ ] 5.7 [REFACTOR] Document the explicit threat model (REQ-433: protects disk theft/lost-laptop while the
  process is stopped; does NOT protect live-memory-dump or an attacker holding the unlocked keychain) in `omnia
  doctor`/status output.
- [ ] 5.8 Verification: full `internal/store`/`internal/embed`/`internal/audit` suites green under both driver
  paths; migration test against a 10k-row fixture; disabled-path byte-for-byte.

## Phase 6: `code-decision-graph` A — Store + Graph Foundation (PR 6A, depends on PR 5)

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
### Phase 6B: `code-decision-graph` B — MCP/CLI Surface (PR 6B, depends on PR 6A)

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

## Phase 7: `memory-enforcement-gate` A — Matcher + Command Runner (PR 7, base: `main` after PR 6B)

Satisfies: REQ-411 (trusted-only feed), REQ-412 (mechanical, no LLM), REQ-413 (verdict contract), REQ-414
(flag-default).

- [x] 7.1 [RED] `internal/enforce/matcher_test.go`: one `trusted` procedure matches touched files alongside two
  matching `candidate`/`retired` procedures; assert only the trusted one is selected (REQ-411) — fails, package
  doesn't exist.
- [x] 7.2 [GREEN] Create `internal/enforce/matcher.go`: candidate set = `ListProcedures{State:trusted,
  Project}` narrowed via `SearchProcedures` (FTS5 over trigger+steps_summary, `procedures.go:390`) against
  touched file paths.
- [x] 7.3 [RED] Runner test: a `tests_pass` procedure with a configured command that exits non-zero yields a
  failing postcondition with exit code + output preview, no LLM call anywhere (REQ-412).
- [x] 7.4 [GREEN] Create `internal/enforce/runner.go`: `exec.CommandContext` in repo root with a hard timeout
  (`EnforcementConfig.TimeoutSeconds`); exit 0 = pass for `tests_pass`/`lint_clean`/`build_green`; `custom`
  evaluates `postcondition_expr` only when `AllowCustomCommands` is true.
- [x] 7.5 [RED] Verdict-contract test: all applicable postconditions pass → verdict exactly `pass`; one fails
  with `Mode` unset (default "flag") → verdict exactly `flag`, never halts (REQ-413/414).
- [x] 7.6 [GREEN] Implement the verdict decision: any failing postcondition ⇒ `flag` (default) or `block`
  (`mode: "block"` explicit).
- [x] 7.7 [REFACTOR] Extract the "command not configured for this kind" skip-with-note branch (fail-safe, never
  block) into its own helper.
- [x] 7.8 Verification: `internal/enforce` unit suite green with injected fake command runner; `go vet`/
  `go build` clean; nothing yet reachable from MCP/CLI (wiring deferred to Phase 8).

## Phase 8: `memory-enforcement-gate` B — MCP/CLI + Audit (PR 8, base: `main` after PR 7)

Satisfies: REQ-410 (gate), REQ-415 (override), REQ-416 (audit), REQ-417 (no auto-fix), REQ-418 (tool/CLI parity).

- [x] 8.1 [RED] Override test: a failing postcondition re-invoked with `override: true` returns `pass`, audit
  records a distinct `override` verdict, not `pass` (REQ-415) — fails, no override plumbing yet.
- [x] 8.2 [GREEN] Add `override`/`override_reason` params to `mem_enforce`/`omnia enforce`; record
  `verdict: override` distinctly in the audit entry.
- [x] 8.3 [RED] Audit-taxonomy test: any pass/flag/block/override outcome writes exactly one `internal/audit`
  entry with verdict, procedure sync_id(s), postcondition kind, exit code (REQ-416).
- [x] 8.4 [GREEN] Add `ActionEnforce` to the `Action` enum (`audit.go:17`) + additive `omitempty` `Entry` fields
  (`:31`) per ADR-7; call `audit.Append` at every gate decision.
- [x] 8.5 [RED] No-auto-fix test: a file touched by a failing postcondition is never written to by the gate
  itself (REQ-417).
- [x] 8.6 [GREEN] Assert (unit-test-enforced) that the matcher/runner only read/execute — never call any
  file-write API.
- [x] 8.7 [RED] Disabled-gate test: `enforcement.enabled=false` → `mem_enforce`/`omnia enforce` return disabled
  response, no command executed, no audit entry (REQ-410).
- [x] 8.8 [GREEN] Gate `mem_enforce` registration behind `enforcement.enabled` in `mcp.MCPConfig` (mirrors
  `ProceduralWiring` `main.go:1342`/`:1343`); register `omnia enforce` in CLI dispatch (`:730`).
- [x] 8.9 [REFACTOR] Consolidate the pass/flag/block/override → audit-entry mapping into one function shared by
  MCP and CLI entry points (REQ-418 contract parity).
- [x] 8.10 Verification: full `internal/enforce`/`internal/mcp`/`internal/audit` suites green; disabled-path
  byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 9: `sleep-consolidation` A — Cluster + Client Foundation (PR 9A, depends on PR 8)

Satisfies: REQ-420–426.

- [x] 9.1 [RED] `internal/consolidate/cluster_test.go`: 3 memories connected above `min_score` via a fixture
  `embed.GraphScoped` output form one cluster via union-find (REQ-421) — fails, package doesn't exist.
- [x] 9.2 [GREEN] Create `internal/consolidate/cluster.go`: union-find over `embed.GraphScoped(project)`
  (`store.go:282`) edges ≥ `MinScore` (default 0.5), per-node cap `K` (default 8).
- [x] 9.3 [RED] Cluster-size-bound tests: a 2-member cluster with `min_cluster_size=3` produces no digest,
  reported "below minimum size" (REQ-423); a 40-member cluster with `max_cluster_size=20` still references all
  40 sources across one or more digests, none dropped (REQ-423).
- [x] 9.4 [GREEN] Implement size-bound handling: skip-below-minimum; cap-or-split-above-maximum via
  highest-degree-node top-K neighborhood.
- [x] 9.5 [RED] `internal/embed/client_test.go`: `Client.Generate(ctx, prompt)` posts to Ollama's `/api/chat`
  on `Embeddings.BaseURL`, sibling to `Embed` (`embed/client.go:78`) — fails, method doesn't exist.
- [x] 9.6 [GREEN] Add `Generate(ctx, prompt) (string, error)` to `embed.Client`: low-temperature, fixed prompt,
  `/api/chat` call.
### Phase 9B: `sleep-consolidation` B — Digest + Integration (PR 9B, depends on PR 9A)

- [x] 9.7 [RED] Digest-write test: a qualifying 3-memory cluster produces one `observations` row `type="digest"`
  + 3 `memory_relations` rows with a new `RelationConsolidates` verb, system provenance; all 3 sources remain
  independently retrievable via `mem_search` after (REQ-422/426).
- [x] 9.8 [GREEN] Add `RelationConsolidates` to the relation vocabulary (`relations.go:32`); implement the
  digest writer (system-provenance relation rows, mirrors `MarkAnchorStale`'s supersedes-row pattern,
  `anchors.go:260`); add `digest` to `DefaultImportanceWeight` (weight 3, `config.go:398`).
- [x] 9.9 [RED] Ollama-unreachable test: consolidation exits cleanly with a log line, no digest/relation
  written, no panic (REQ-424 degradation).
- [x] 9.10 [GREEN] Wrap the `Generate` call with the same degrade-on-unreachable pattern as recall's Ollama
  auto-detect.
- [x] 9.11 [RED] Disabled/idle-off test: `consolidation.enabled=false` → `omnia consolidate` no-op, no idle
  worker starts even with `consolidation.idle=true` (REQ-420/425).
- [x] 9.12 [GREEN] Gate `omnia consolidate` CLI (dispatch `:730`) and the optional idle worker (mirrors
  `buildAutoEmbedWorker`, `main.go:1331`) behind `consolidation.enabled`.
- [x] 9.13 [REFACTOR] Extract the cluster-selection + digest-writing pipeline into one orchestration function
  shared by the CLI and idle worker.
- [x] 9.14 Verification: `internal/consolidate`/`internal/embed`/`internal/store` suites green; disabled-path
  byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 10: `learned-ranker` A — Feature + Model Foundation (PR 10A, depends on PR 9B)

Satisfies: REQ-440–446.

- [x] 10.1 [RED] `internal/ranker/features_test.go`: every feature (lexical RRF contribution, semantic cosine,
  recency, `DefaultImportanceWeight`, outcome/judgment history) traces to an existing recall/config/store field
  — no invented signal (REQ-443) — fails, package doesn't exist.
- [x] 10.2 [GREEN] Create `internal/ranker/features.go`: build the normalized [0,1] feature vector from existing
  `recall`/`config`/`store` fields.
- [x] 10.3 [RED] `internal/ranker/model_test.go`: batch gradient descent on a small labeled fixture separates
  positive (`compatible`/`worked`) from negative (`supersedes`/`conflicts_with`/`did_not_work`) examples — fails,
  package doesn't exist.
- [x] 10.4 [GREEN] Create `internal/ranker/model.go`: L2-regularized logistic regression, batch gradient
  descent, serialize to `<dataDir>/ranker/model-<version>.json` + `current` pointer, versioned by a hash of
  `{feature-schema, train-set size, trained-at}` (REQ-445).
- [x] 10.5 [RED] Cold-start test: `enabled=true`, zero judgments/outcomes, ranking byte-for-byte identical to
  `DefaultFuseParams`/`AdaptiveFloor` (`recall.go:74`/`:96`) (REQ-441).
- [x] 10.6 [GREEN] Implement the cold-start fallback: below `MinTrainExamples` (default 50), no `current`
  model, or decode error → use `DefaultFuseParams`/`AdaptiveFloor` unchanged (REQ-446).
### Phase 10B: `learned-ranker` B — Train + Live Integration (PR 10B, depends on PR 10A)

- [x] 10.7 [RED] `omnia rank-train` test: training runs `eval.RunOnce` (`harness.go:37`) after fitting and
  refuses to write `current` on any regression (REQ-444).
- [x] 10.8 [GREEN] Implement `omnia rank-train` CLI (dispatch `:730`): train → `eval.RunOnce` gate → promote
  `current` only if no regression.
- [x] 10.9 [RED] Model-invalidation test: a model trained against an older feature shape is detected invalid
  and not used live (REQ-445); a corrupted model file falls back silently to default floors, no error surfaced
  (REQ-446).
- [x] 10.10 [GREEN] Implement the feature-schema-hash check on load; wrap model load in a recover-to-fallback
  path.
- [x] 10.11 [REFACTOR] Place the re-rank pass at the `internal/mcp` wiring boundary — the same seam as
  `RecallRanking`/`RankResults` (`main.go:1300`/`:1304`) — keeping `internal/recall`/`internal/store` untouched.
- [x] 10.12 Verification: `internal/ranker` suite + `internal/eval` gate integration green; disabled-path and
  cold-start byte-for-byte; `CGO_ENABLED=0 go build ./...` clean.

## Phase 11: `repo-cartridge` (PR 11, depends on PR 10B; split or obtain `size:exception` if >400)

Satisfies: REQ-450–456.

- [x] 11.1 [RED] `internal/cartridge/build_test.go`: building at commit `abc123` produces a JSON artifact tagged
  commit `abc123` + a format version, containing `top_memories`/`anchors`/`trusted_procedures` (REQ-451/456) —
  fails, package doesn't exist.
- [x] 11.2 [GREEN] Create `internal/cartridge/build.go`: `{schema_version, repo_root, head_sha, built_at,
  top_memories[], anchors[], trusted_procedures[], ranker_model_version?}` at
  `<dataDir>/cartridges/<repo-id>-<head-sha>.json`, keyed via `HeadSHA` (`anchor.go:192`); `top_memories` ranked
  by the active ranker (PR 10B) or current fused order if disabled; anchors from PR 6A's `CodeDecisionGraph`;
  `trusted` procedures for the project.
- [x] 11.3 [RED] Stale-commit test: a cartridge built at `abc123`, current HEAD `def456` — `cartridge load`
  reports stale (commit mismatch), never served as current (REQ-452).
- [x] 11.4 [GREEN] Implement `cartridge load`'s `HeadSHA` comparison and stale/cold-start branch.
- [x] 11.5 [RED] Missing-cartridge test: no cartridge file exists → session falls back to cold-query, no error
  (REQ-453).
- [x] 11.6 [GREEN] Implement the missing/corrupt-file → cold-start fallback path (never an error to the
  caller).
- [x] 11.7 [RED] Version-mismatch test: an older-format cartridge is detected and rejected, falling back to
  cold-start rather than misreading (REQ-456).
- [x] 11.8 [GREEN] Implement the `schema_version` check on load.
- [x] 11.9 [RED] Disabled-gate CLI test: `cartridge.enabled=false` → `cartridge build`/`load` report disabled,
  no file written/read (REQ-450).
- [x] 11.10 [GREEN] Wire `omnia cartridge build [--repo]` / `load [--repo]` CLI (nested-subcommand-namespace,
  mirrors `cloud`/`embed`/`migrate`/`eval` `main.go:775-791`); gate both behind `cartridge.enabled`.
- [x] 11.11 [REFACTOR] Add an explicit content-shape assertion in the build test confirming no weight-level/
  KV-cache artifact is ever included in the cartridge payload (REQ-455); confirm the artifact never syncs to
  cloud (REQ-454).
- [x] 11.12 Verification: `internal/cartridge` suite green; disabled-path confirms build/load are no-ops,
  session startup unchanged (REQ-450); `CGO_ENABLED=0 go build ./...` clean; **release-wide regression**:
  `go test ./...` green with every v0.4 flag off = byte-for-byte v0.3.2 across all 11 PRs.
