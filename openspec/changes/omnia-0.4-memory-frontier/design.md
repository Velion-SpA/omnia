# Design: Omnia v0.4 — La frontera de la memoria (Memory Frontier)

> **Planning artifact only.** This is the HOW (architecture) for the 7 capabilities defined in
> `proposal.md`. It creates no code. It is written to be implemented by a different tool (Codex) from
> this design + the per-capability specs + a tasks breakdown. Every file:line citation below was
> re-verified against the live tree at `/private/tmp/omnia-v04-planning` (independent of the proposal).

## Technical Approach

v0.4 is strictly additive behind 7 default-OFF config blocks (the `TimeTravelConfig`/`WriteHygieneConfig`
convention, `internal/config/config.go:114`/`:136`, wired in `applyDefaults` `:694`). Every capability is
a leaf package or a new read/method over existing tables plus a CLI subcommand (dispatch switch
`cmd/omnia/main.go:730`) and/or an MCP tool gated by its flag in `mcp.MCPConfig` (nil-means-disabled,
`cmd/omnia/main.go:1287`/`:1342`). OFF (or absent) = byte-for-byte v0.3.2, by construction.

The unifying architectural move that resolves BOTH hardest problems is a **dual-driver strategy**: keep
`modernc.org/sqlite` (`internal/store/store.go:32`, `internal/embed/store.go:14`) as the default, and add
`github.com/ncruces/go-sqlite3` (pure-Go SQLite-on-wazero, `CGO_ENABLED=0`) as an OPT-IN driver selected
only when encryption (4) or the vector index (7) is enabled. The on-disk SQLite file format is identical
between the two drivers, so a plaintext DB written by modernc opens unchanged under ncruces and vice
versa — only the encrypting VFS changes on-disk bytes. This lets encryption and vec adopt ncruces without
touching the default path, and makes both migrations non-destructive.

## Cross-Cutting Architecture Decisions (ADR)

### ADR-1 — Dual SQLite driver, flag-selected, coexisting

**Choice**: Introduce `openDB` driver selection at the two composition points (`internal/store/store.go:847`
`New`, and `:897` `newWithoutRepair`; `internal/embed/store.go:79` `OpenStore`). Default driver name stays
`"sqlite"` (modernc). When `encryption.enabled` or `vec_index.enabled` is set, resolve to the ncruces driver
(registered under its own name) with the appropriate VFS/extension. `openDB` is already a package var
(`store.go:35`) — the seam exists.
**Alternatives**: (a) swap everything to ncruces unconditionally — rejected: needless blast radius on the
default path and a WASM startup cost every install pays even with all flags off; (b) stay on modernc and
solve encryption/vec in-process — rejected: modernc exposes no stable encrypting-VFS API and no vector
extension.
**Rationale**: keeps the OFF path literally the current code; confines ncruces to opted-in installs; one
driver abstraction serves both hard problems.

### ADR-2 — Encryption at rest = FULL-DATABASE via ncruces `adiantum` VFS (resolves hardest problem #1)

**Choice**: Full-database, page-level encryption through ncruces' pure-Go encrypting VFS
(`github.com/ncruces/go-sqlite3/vfs/adiantum`), NOT application-layer field encryption.
**Alternatives**: SQLCipher (rejected — cgo, violates the hard invariant); modernc encrypted VFS (does not
exist); **application-layer AES-GCM on sensitive columns** (content/title/steps).
**Rationale**: field-level encryption is disqualified by THIS codebase's retrieval engine, not by effort.
The primary lexical path is FTS5 (`observations_fts`, `procedures_fts` `internal/store/procedures.go:401`)
and semantic recall decodes a `vector BLOB` per row (`internal/embed/store.go:52`,`:207`). You cannot
full-text-index or cosine-scan ciphertext, so field encryption would force either a plaintext FTS index
(leaks the very content you encrypted) or abandoning FTS5 (guts the product). A page-level encrypting VFS
decrypts in memory, so FTS5, triggers, and the vec index all keep working transparently. `adiantum` is
pure Go and length-preserving, sized for at-rest disk encryption. This is the ONLY pure-Go path that
satisfies "encrypted at rest" without breaking search. Applies to both `omnia.db` and `embeddings.db`.

### ADR-3 — Encryption key from OS keychain by SHELLING OUT to the platform CLI

**Choice**: Access the OS keychain via `os/exec` to the platform binary — macOS `/usr/bin/security`
(`add-generic-password`/`find-generic-password`), Linux `secret-tool` (libsecret) — NOT a linked keychain
library.
**Alternatives**: `github.com/keybase/go-keychain` / `99designs/keyring` — REJECTED: their macOS backends
link `Security.framework` via cgo, breaking `CGO_ENABLED=0`.
**Rationale**: this is EXACTLY the de-risk pattern the repo already locked for git — `internal/anchor`
shells to the `git` binary specifically to avoid cgo (`internal/anchor/anchor.go:1-11`,
`defaultRunGit:100`). Reusing "shell to the system tool" for the keychain is idiomatic here and keeps the
binary pure-Go. Service=`omnia`, account=`db-key-v1`; a 32-byte key from `crypto/rand` generated once on
first enable. Injectable runner (mirror `Probe.runGit`) so tests never touch the real keychain.
**Degradation (explicit, never silent)**: if `encryption.enabled` is true but no keychain CLI is reachable
(headless/Linux-without-libsecret/CI), the store REFUSES to open and returns a clear error by default. An
explicit `encryption.allow_plaintext_fallback: true` opt-in downgrades to the unencrypted modernc path with
a prominent stderr warning and an audit entry — a conscious operator choice, never an automatic silent
downgrade. Losing the keychain entry = data unrecoverable; stated plainly in the spec threat model.

### ADR-4 — Vec index = ADDITIVE, dual-write, brute-force retained (resolves hardest problem #2)

**Choice**: When `vec_index.enabled`, open `embeddings.db` with ncruces + the sqlite-vec extension and
create a `vec_embeddings` vec0 virtual table ALONGSIDE the existing `embeddings` table. `Upsert`
(`internal/embed/store.go:95`) dual-writes to both. Reads (`Search`/`SearchScoped`/`Graph`/`GraphScoped`)
consult the vec index when enabled, else the current brute-force scan (`:186`), which is NEVER removed.
**Alternatives**: clean swap to vec0-only (rejected — destructive, no fallback); keep modernc and add vec
(rejected — modernc cannot load the extension).
**Rationale**: the `embeddings` table + `vector BLOB` stay the source of truth, so no existing data can be
lost or corrupted; the vec table is a derived index rebuildable from it. A one-time backfill pass builds
`vec_embeddings` from existing rows on first enable. This is the proposal's "additive index alongside the
current vector BLOB column, a feature flag deciding which path serves reads" made concrete.

### ADR-5 — Config: 7 default-OFF blocks + `applyDefaults`, params-only defaults

**Choice**: One struct per capability on `Config` (`internal/config/config.go:15`), each with an `Enabled
bool` whose zero value IS the default (the `Recall.Enabled`/`Ranking.Enabled` idiom, NOT the inverted
default-ON `writeHygieneEnabledKeyPresent` probe). Only tuning params get `applyDefaults` values, so opting
in with just `enabled: true` still yields sane constants.
**Rationale**: matches every existing gate; guarantees the "opts into none ⇒ byte-for-byte v0.3.2"
success criterion.

### ADR-6 — Enforcement postconditions = one sandboxed command runner, exit-code = verdict

**Choice**: All four `postcondition_kind` values (`tests_pass`/`lint_clean`/`build_green`/`custom`,
`internal/store/procedures.go:51`) are verified by running a command via `exec.CommandContext` in the
repo root with a hard timeout; exit 0 = pass. `tests_pass`/`lint_clean`/`build_green` resolve their command
from operator config; `custom` runs the procedure's own `postcondition_expr` and is gated OFF behind
`enforcement.allow_custom_commands` (procedure-supplied strings are less trusted).
**Alternatives**: embed an expression-language interpreter for `custom` (rejected — a new dependency and a
code-exec surface for LLM-derived text); LLM-judged verification (rejected — the locked "mechanical, not
LLM-judged" product decision).
**Rationale**: unifies verification to "run a command, check exit code" — deterministic, cgo-free,
auditable, and reusing the shell-to-binary pattern. No LLM in the enforcement path.

### ADR-7 — Audit stays JSONL; extend the action taxonomy

**Choice**: `internal/audit` is an append-only JSONL file (`audit.jsonl`, `internal/audit/audit.go:52`),
not a DB table. Add `ActionEnforce` and `ActionConsolidate` to the `Action` enum (`:17`) and additive
omitempty fields to `Entry` (`:31`) for gate decisions (decision, procedure sync_id, postcondition kind,
exit code, override reason). Encryption never touches this file (it is provenance/audit, and already
`0600`); it is intentionally outside the encrypted DBs.
**Rationale**: reuses the existing fail-open audit writer (`Append` never blocks the caller, `:60`); keeps
gate/consolidation records queryable via the existing `Read`/`EntriesForObservation` API.

---

## Capability 1 — `code-decision-graph`

**Data model**: NO new tables. The reverse walk is a new read over `memory_anchors`
(`internal/store/anchors.go:25`) joined to `observations` (`deleted_at IS NULL`).

**Interfaces**:
- New store methods: `BlameLine(repoRoot, file string, line int) ([]BlameHit, error)` and
  `CodeDecisionGraph(project string) (nodes, edges, error)` — the graph is a thin projection over anchor
  rows, NOT a new engine (reuses `ListActiveAnchors`/`GetAnchorsForObservations` `:163`/`:362`).
- MCP tool `mem_blame` `{file, line, repo_root?}` → `{line, hits:[{anchor_status, range, blame_sha,
  memories:[{sync_id, type, title, preview}]}]}`; gated by `code_graph.enabled` in `MCPConfig`.
- CLI `omnia blame <file>:<line>` (explicit invocation, like `forget-scan` `:753`).

**Algorithm — overlap resolution** (a line may sit in multiple ranges): normalize the input path to
repo-root-relative using the anchor probe (`git rev-parse --show-toplevel`, `anchor.go:178`) + `filepath.Rel`;
select anchors where `file_path` matches AND `line_start <= line <= line_end` (AND `repo_root` matches when
supplied). Rank matched anchors by: (1) `active` before `stale`/`traveled`; (2) NARROWEST range first
(`line_end-line_start` asc = most specific); (3) `blame_at` desc; (4) `id` asc (determinism). Within one
anchor's memories, order by `DefaultImportanceWeight` (`config.go:398`: decision/architecture=3,
bugfix/pattern=2) then `updated_at` desc. Stale anchors are returned and clearly marked, never hidden.

**Failure/degradation**: git missing or not-a-repo → anchor sentinels (`ErrGitNotInstalled`/`ErrNotAGitRepo`)
→ `mem_blame` returns an empty result with a "no repo context" note, never an error to the agent. No anchor
covers the line → empty hits with a clear message.

**Interactions**: feeds capability 2 (trigger/scope enrichment) and capability 6 (code-graph state in the
cartridge).

## Capability 2 — `memory-enforcement-gate` (flagship)

**Data model**: NO new procedures columns. Decisions recorded to the audit JSONL (ADR-7). Feed = `trusted`
procedures only (`ProcedureStateTrusted` `procedures.go:42`), whose polarity/postcondition derive from
`bugfix outcome` (`:16`). Candidate/retired never gate.

**Interfaces**:
- MCP tool `mem_enforce` `{repo_root, files_touched[], project, override?, override_reason?}` →
  `{decision: pass|flag|block, violations:[{procedure_sync_id, kind, command, exit_code, output_preview}],
  overridable: true}`; wired like `ProceduralWiring` (`main.go:1342`).
- CLI `omnia enforce [--files ...] [--block] [--override --reason ...]` for hooks/CI.
- Config `EnforcementConfig{Enabled bool; Mode string; Commands struct{Tests,Lint,Build string};
  AllowCustomCommands bool; TimeoutSeconds int}`; `Mode` default `flag` (locked product decision:
  flag-with-override; blocking opt-in via `mode: block`).

**Algorithm — change→procedure matching**: candidate set = `ListProcedures{State:trusted, Project}` scoped
to the change's project + `global` scope. Narrow via `SearchProcedures` (FTS5 over trigger+steps_summary,
`procedures.go:390`) using the touched file paths + optional decision keywords pulled from capability 1's
`BlameLine` on those files (soft dependency — works without it). For each matched procedure run its
postcondition through the ADR-6 runner. Verdict: any failing postcondition ⇒ `flag` (default) or `block`
(if `mode: block`); an explicit `override: true` records an override and returns `pass`. Every outcome is
audited (`ActionEnforce`).

**Failure/degradation (fail-safe by design — a wrong block is worse than a miss)**: no matching trusted
procedures → `pass`. Command for a needed kind not configured → SKIP that procedure with a "command not
configured" note, never block. Runner error/timeout → `flag` (not block) with reason, audited. git/repo
missing → cannot scope → `pass` with note.

## Capability 3 — `sleep-consolidation`

**Data model**: a digest is an ordinary `observations` row with `type = "digest"`. Source pointers are
`memory_relations` rows with a NEW verb `RelationConsolidates` (digest → each source), written with system
provenance (`marked_by_actor="omnia"`, `marked_by_kind="system"`, mirroring `MarkAnchorStale`
`anchors.go:319`). Sources are never edited or deleted (never-hard-delete invariant). Add `digest` to
`DefaultImportanceWeight` (weight 3) so digests compete at retrieval without burying sources.

**Interfaces**:
- CLI `omnia consolidate [--project ...]` (primary, idle/CI). Optional idle worker built like
  `buildAutoEmbedWorker` (`main.go:1331`), started in `cmdServe`/`cmdMCP` when `consolidation.idle: true`.
- Config `ConsolidationConfig{Enabled bool; Model string; MinScore float32; K int; MinClusterSize int;
  MaxClusterSize int; Idle bool; IdleAfterMinutes int}`.

**Algorithm**: cluster discovery reuses `embed.GraphScoped(project)` (`store.go:282`); build connected
components via union-find over edges ≥ `MinScore` (default 0.5, `K` default 8); consolidate components with
≥ `MinClusterSize` (default 3) members; if a component exceeds `MaxClusterSize` (default 12), take the
highest-degree node's top-K neighborhood. Summarize via a LOCAL Ollama chat call — a new
`embed.Client.Generate(ctx, prompt)` hitting Ollama `/api/chat` on the existing `Embeddings.BaseURL`
(sibling to `Embed`, `embed/client.go:78`), model from `consolidation.model`. Fixed English prompt →
`{title, content}` digest; low temperature.

**Failure/degradation**: Ollama unreachable → no-op that logs and exits cleanly (mirrors recall's
Ollama auto-detect degradation, `config.go:302` doc). No cluster meets threshold → no-op. Audited
(`ActionConsolidate`).

**Interactions**: consumes the same k-NN `Graph` that capability 7 accelerates; digests are eligible
cartridge content (capability 6).

## Capability 4 — `memory-at-rest-security`

**Data model**: encryption is VFS-level (ADR-2/3) — NO schema change. Provenance is already stored
(`source`/`trust_tag`, `store.go:125`, `classifyTrust` `provenance.go:26`); this capability ensures the
tag is surfaced in read receipts and every audit `Entry` (already carries `TrustTag`, `audit.go:46`), and
that capability 2/3 decisions are audited (ADR-7).

**Interfaces**: Config `EncryptionConfig{Enabled bool; KeychainService string; AllowPlaintextFallback bool}`.
CLI `omnia security encrypt` / `omnia security decrypt` / `omnia security rotate-key`.

**Migration (non-destructive, mirrors `datadir.Migrate` `datadir.go:140`)**: on first enable, for each of
`omnia.db` and `embeddings.db`: (1) fetch-or-generate the keychain key; (2) create a NEW encrypted file via
ncruces+adiantum and copy every page in (SQLite `VACUUM INTO` an encrypted target, or dump+load); (3) fsync
and verify row counts; (4) atomically rename the encrypted file into place, keeping the plaintext as a
timestamped `.bak` until the next clean startup. `omnia security decrypt` reverses it (read via keychain,
write plaintext). Reversible; never leaves the user locked out.

**Failure/degradation**: keychain unavailable → ADR-3 (refuse by default; explicit opt-in fallback with
loud warning). Migration failure at any step → keep the original plaintext file, abort enable, log — never
a half-encrypted DB.

## Capability 5 — `learned-ranker`

**Data model**: model persisted OUTSIDE the DBs at `<dataDir>/ranker/model-<version>.json` + a `current`
pointer file; versioned by a hash of `{feature-schema, train-set size, trained-at}`.

**Model/algorithm**: a pure-Go L2-regularized LOGISTIC REGRESSION (linear weights + sigmoid), trained by
batch gradient descent — small, no dependency, deterministic. (Gradient-boosted trees rejected for v1:
harder to implement well in pure Go for marginal gain on small corpora.) Features per candidate, normalized
to [0,1]: lexical RRF contribution, semantic cosine, recency (existing half-life, `RankingConfig` `:364`),
type importance (`DefaultImportanceWeight`), and outcome/judgment history (from `observations.outcome` and
`memory_relations` verdicts). Labels: positive = kept/`compatible`/`outcome=worked`; negative =
`supersedes`/`conflicts_with`/`did_not_work`.

**Placement**: a re-rank pass at the `internal/mcp` wiring boundary — the SAME seam as the existing
`RecallRanking`/`RankResults` pass (`main.go:1304`), keeping `internal/recall` and `internal/store` pure.
It runs AFTER `recall.Fuse` produces the fused list; when active it replaces the hand-weighted sum,
otherwise the current ranking stands.

**Interfaces**: CLI `omnia rank-train` (trains, then runs the eval harness `internal/eval.RunOnce`
`harness.go:37` and only promotes `current` if no regression). Config `RankerConfig{Enabled bool;
MinTrainExamples int; ModelDir string}`.

**Failure/degradation — cold start is byte-for-byte today**: fewer than `MinTrainExamples` (default 50)
labels, no `current` model, or a decode error → fall back to today's hand-tuned floors
(`DefaultFuseParams` `recall.go:74`), identical output. Eval regression → refuse to promote. Never crashes
recall.

**Interactions**: the ranker scores memories that fill the cartridge (capability 6); both it and
consolidation lean on the eval harness / k-NN graph.

## Capability 6 — `repo-cartridge`

**Data model**: on-disk JSON artifact at `<dataDir>/cartridges/<repo-id>-<head-sha>.json` (`repo-id` =
short hash of the repo root). Contents: `{schema_version, repo_root, head_sha, built_at, top_memories[],
anchors[], trusted_procedures[], ranker_model_version?}`.

**Algorithm**: `top_memories` = top-N (default 50) for the repo's project ranked by the active ranker
(capability 5) or the current fused/`RankResults` order; plus active anchors for the repo (capability 1's
graph state) and `trusted` procedures for the project. Keyed/invalidated by `HeadSHA`
(`anchor.go:192`): FULL rebuild on commit change (partial invalidation deferred, matching the "KNN flat, no
HNSW yet" minimalism). Load compares live `HeadSHA` to the cartridge's; mismatch ⇒ ignore (start cold).

**Interfaces**: CLI `omnia cartridge build [--repo ...]` / `omnia cartridge load [--repo ...]`. Optional
MCP `mem_cartridge` returning the warm digest at session start. Config `CartridgeConfig{Enabled bool;
TopMemories int; Dir string}`.

**Failure/degradation**: git missing / no commit → cannot key → skip (cold start), no crash. HEAD mismatch
or missing/corrupt file → ignored, cold start; a stale cartridge is NEVER served as fresh.

## Capability 7 — `sqlite-vec-index`

**Data model**: additive `vec_embeddings` vec0 virtual table alongside the current `embeddings` table
(`internal/embed/store.go:52`); `vector BLOB` stays the source of truth (ADR-4).

**Algorithm/reads**: with the flag on and the vec table populated, `Search`/`SearchScoped` run a vec0 KNN
query; `Graph`/`GraphScoped` (`:264`/`:282`) build the k-NN graph via per-node vec KNN (top-K neighbors)
instead of the O(N²) pairwise scan, keeping "KNN flat." Quantization defaults to `none` (float32) to
guarantee identical top-k vs brute-force; `int8` is opt-in (`vec_index.quantization`). `Upsert` dual-writes.

**Interfaces**: Config `VecIndexConfig{Enabled bool; Quantization string}`. No new user-facing command;
migration/backfill runs on first open when enabled (or via `omnia embed --reindex`).

**Failure/degradation**: ncruces/extension unavailable, vec table empty, or a dim-mismatch row → fall back
to the retained brute-force scan (`:186`), zero data change. Backfill failure → keep brute-force, log,
never corrupt `embeddings`.

**Interactions**: accelerates the k-NN `Graph` that capability 3 consumes; shares the ncruces driver with
capability 4 (when both on, `embeddings.db` uses ncruces + adiantum + vec together).

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/config/config.go` | Modify | 7 new blocks (`CodeGraph`,`Enforcement`,`Consolidation`,`Encryption`,`Ranker`,`Cartridge`,`VecIndex`) + `applyDefaults` param defaults; all `Enabled` default-OFF. |
| `internal/store/store.go` | Modify | Driver selection at `New`/`newWithoutRepair` (ADR-1); route `openDB` to ncruces+adiantum when `encryption.enabled`. |
| `internal/store/anchors.go` | Modify | Add `BlameLine` reverse walk + `CodeDecisionGraph` projection (1). |
| `internal/store/relations.go` | Modify | Add `RelationConsolidates` verb (3). |
| `internal/store/procedures.go` | Reuse | `ListProcedures`/`SearchProcedures` feed the gate (2). |
| `internal/embed/store.go` | Modify | ncruces+vec0 path, dual-write, backfill, vec-KNN reads; brute-force retained (7). |
| `internal/embed/client.go` | Modify | Add `Generate` (Ollama `/api/chat`) for consolidation (3). |
| `internal/audit/audit.go` | Modify | `ActionEnforce`/`ActionConsolidate` + gate fields (ADR-7). |
| `internal/recall/…` | Reuse | Fusion untouched; ranker sits above it at the mcp boundary (5). |
| `internal/eval/harness.go` | Reuse | Ranker promotion gate (5). |
| `internal/anchor/anchor.go` | Reuse | `repoRoot`/`Blame`/`HeadSHA` for 1 and 6. |
| new `internal/keychain/` | Create | Shell-to-CLI keychain (macOS `security`, Linux `secret-tool`), injectable runner (ADR-3). |
| new `internal/enforce/` | Create | Change→procedure matcher + sandboxed command runner (2/ADR-6). |
| new `internal/consolidate/` | Create | Clustering + prompt + digest writer (3). |
| new `internal/ranker/` | Create | Pure-Go logistic re-ranker + train/serialize (5). |
| new `internal/cartridge/` | Create | Build/load/invalidate JSON cartridge (6). |
| `cmd/omnia/main.go` | Modify | Dispatch `blame`/`enforce`/`consolidate`/`security`/`rank-train`/`cartridge`; register gated MCP tools; optional idle worker. |

## Interfaces / Contracts (new config blocks — mirror existing shape)

```go
type CodeGraphConfig struct{ Enabled bool `yaml:"enabled"` }
type EnforcementConfig struct {
    Enabled bool `yaml:"enabled"`; Mode string `yaml:"mode"` // flag|block, default flag
    Commands struct{ Tests, Lint, Build string } `yaml:"commands"`
    AllowCustomCommands bool `yaml:"allow_custom_commands"`; TimeoutSeconds int `yaml:"timeout_seconds"`
}
type ConsolidationConfig struct {
    Enabled bool `yaml:"enabled"`; Model string `yaml:"model"`; MinScore float32 `yaml:"min_score"`
    K, MinClusterSize, MaxClusterSize, IdleAfterMinutes int; Idle bool `yaml:"idle"`
}
type EncryptionConfig struct {
    Enabled bool `yaml:"enabled"`; KeychainService string `yaml:"keychain_service"`
    AllowPlaintextFallback bool `yaml:"allow_plaintext_fallback"`
}
type RankerConfig struct{ Enabled bool; MinTrainExamples int `yaml:"min_train_examples"`; ModelDir string `yaml:"model_dir"` }
type CartridgeConfig struct{ Enabled bool; TopMemories int `yaml:"top_memories"`; Dir string `yaml:"dir"` }
type VecIndexConfig struct{ Enabled bool; Quantization string `yaml:"quantization"` } // none|int8, default none
```

## Testing Strategy

| Layer | What | Approach |
|-------|------|----------|
| Unit | overlap ranking (1), matcher (2), clustering/union-find (3), logistic train (5), invalidation (6) | Table tests; inject fake git/keychain/Ollama runners (the `Probe.runGit` pattern). |
| Unit | encryption round-trip (4), vec vs brute-force parity (7) | Encrypt→reopen→read equals plaintext; assert vec top-k == brute-force top-k on a fixture store. |
| Integration | driver selection (ADR-1), non-destructive migrations (4/7) | Full `internal/store` suite MUST pass under the ncruces path; migrate a seeded plaintext DB, assert data intact + reversible. |
| Invariant | "opts into none ⇒ byte-for-byte v0.3.2" | Each capability's disabled-is-no-op test (existing convention). |
| Eval | ranker non-regression (5) | `internal/eval` gate blocks promotion on any regression. |

## Migration / Rollout

Per-capability rollback = set the flag `false` (or leave absent) → inert (1,2,3,5,6). Encryption (4):
`omnia security decrypt` restores plaintext via the keychain key. Vec (7): disabling reverts reads to the
retained brute-force path with no data change. Both DB migrations are copy-then-atomic-rename with a
retained backup (mirroring `datadir.Migrate`), never in-place destructive.

## Open Questions

These are EMPIRICAL/validation items, not unresolved architecture — Codex has a concrete decision for every
capability:
- [ ] Confirm the exact ncruces symbols for adiantum VFS registration and sqlite-vec extension loading at
  implementation time (driver+VFS+additive strategy are fixed; only the API surface needs pinning). Keep
  the brute-force + modernc fallbacks as the safety net if a symbol is unavailable.
- [ ] One empirical tuning pass on consolidation thresholds (`MinScore`/`K`/cluster sizes) and the ranker's
  `MinTrainExamples`, same "needs one tuning pass" caveat the procedural defaults already carry.
- [ ] Linux keychain coverage beyond `secret-tool` (e.g. headless servers) — degrade per ADR-3.
