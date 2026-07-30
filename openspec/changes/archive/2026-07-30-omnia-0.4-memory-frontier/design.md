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
`github.com/ncruces/go-sqlite3` (pure-Go SQLite-on-wazero, `CGO_ENABLED=0`) only at opt-in composition
points. Encryption selects ncruces for each encrypted DB; the Vec1 index selects it only for
`internal/embed`'s existing `embeddings.db`. The on-disk SQLite file format is identical between the two
drivers, so a plaintext DB written by modernc opens unchanged under ncruces and vice versa — only the
encrypting VFS changes on-disk bytes. This keeps vector ownership out of the core store, preserves the
default path, and makes both migrations non-destructive.

## Cross-Cutting Architecture Decisions (ADR)

### ADR-1 — Dual SQLite driver, flag-selected, coexisting

**Choice**: Keep modernc as the default. `internal/store` selects ncruces only for encryption at `New` /
`newWithoutRepair`; `internal/embed.OpenStore` selects it when encryption or Vec1 is enabled. Vec1 registration
is owned only by the embedding connector (ADR-4), not the core-store `openDB` seam. The package must use the
ncruces connector API where per-connection initialization is required, rather than relying on a globally
registered driver name.
**Alternatives**: (a) swap everything to ncruces unconditionally — rejected: needless blast radius on the
default path; (b) stay on modernc and solve encryption/Vec1 in-process — rejected: modernc exposes no stable
encrypting-VFS API and no bundled Vec1 registration path.
**Rationale**: keeps the OFF path literally the current code and confines ncruces to opted-in installs without
coupling the core store to the embedding index.

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

### ADR-4 — Vec1 index = additive, exact, and recoverable (resolves hardest problem #2)

**Choice**: `internal/embed` alone owns the optional Vec1 connector for the existing `embeddings.db`; it
calls `driver.Open(dsn, vec1.Register)` from pinned `github.com/ncruces/go-sqlite3@v0.35.2`. `driver.Open`
installs the callback on every physical connection, so the extension is never assumed to be process-global.
The existing `MaxOpenConns(1)` remains a concurrency policy, not the registration mechanism. With the flag
off, `OpenStore` continues to use modernc and makes no Vec1 calls. With it on, the same database contains an
additive `vec_embeddings` Vec1 virtual table; no second embedding database is created. The standard
`embeddings` table remains authoritative and brute-force remains available.

**Pinned contract**: configure only float32 Vec1 NN mode: `flat` + `cos`; do not train a model or enable
quantization, int8, binary, IVF, or ANN. The supported registration and test path are
[`driver.Open`](https://github.com/ncruces/go-sqlite3/blob/v0.35.2/driver/driver.go#L132-L151),
[`vec1.Register`](https://github.com/ncruces/go-sqlite3/blob/v0.35.2/ext/vec1/vec1.go#L11-L14), and the
maintainer's [Vec1 integration test](https://github.com/ncruces/go-sqlite3/blob/v0.35.2/ext/vec1/vec1_test.go#L16-L48).

**Rationale**: Vec1 flat mode is exhaustive/exact; it is a derived acceleration structure, not a source of
truth. Keeping it in `embeddings.db` preserves the existing lifecycle and lets a disabled or unhealthy index
fall back without moving data. The connector is deliberately private to `internal/embed`; core-store driver
and encryption composition must not acquire a dependency on Vec1.

**Config correction ownership**: PR 2A removes PR 1's unreleased `VecIndexConfig.Quantization` field, its
`applyDefaults` value, and every YAML/environment mapping, fixture, or test expectation for it. No compatibility
alias is retained: the supported v0.4 contract is exactly `vector_index.enabled`. Float32 flat/cos is an
implementation invariant, not a selectable format.

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

**Data model and query contract**: `embeddings` remains the sole authoritative table. Create the derived
virtual table in that same DB and configure it once before population:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vec_embeddings USING vec1(vector, project);
INSERT INTO vec_embeddings(cmd, vector)
VALUES ('rebuild', '{index:"flat", distance:"cos"}');
-- Scoped (omit the predicate for unscoped Search):
SELECT e.sync_id, e.obs_id, v.distance
FROM vec_embeddings(?1, ?2) AS v JOIN embeddings AS e ON e.rowid = v.rowid
WHERE v.project = ?3;
```

`vec_embeddings.rowid` is always the current `embeddings.rowid`; it is never an observation ID or an
application-generated key. Each write mirrors `COALESCE(project,'')` as Vec1 metadata, so scoped KNN filters
inside Vec1 before K results are accumulated. The join obtains current `sync_id`/`obs_id` only after the
metadata-filtered KNN result. Delete/prune remove the derived row by source rowid in the same mutation
transaction. A maintenance operation that can rewrite source rowids must mark the index stale and run a full
reindex; no caller persists a Vec1 rowid.

**Dimensions and native bytes**: Vec1 accepts only machine-endian packed IEEE-754 float32 BLOBs and fixes a
table's dimension at its first vector. Omnia retains its canonical little-endian `embeddings.vector` BLOB.
On an empty index, backfill selects the first valid source vector in `rowid` order, records its `active_dim`
and byte order in derived-index metadata, then indexes only valid vectors of that dimension. A write or query
with another dimension remains in `embeddings` and uses brute force; it cannot poison or reset the active
index. `omnia embed --reindex` is the explicit recovery/model-change operation: discard only derived Vec1
state, choose the first valid source dimension again, and report indexed/skipped counts and reasons.

The v0.4 release supports Vec1 only on little-endian target architectures. It encodes a separate native-endian
float32 BLOB for Vec1 from the canonical source vector; it never reuses the little-endian source BLOB by
assumption. If the persisted byte-order marker does not match the process, disable Vec1 and use brute force;
a compatible little-endian release may recreate and rebuild from `embeddings`. This is required because the
[Vec1 format is machine-endian](https://sqlite.org/vec1/doc/trunk/doc/vec1ref.md#vector-format), whereas
Omnia's source codec is deliberately little-endian. Cross-endian index portability is not supported.

**Write, backfill, and recovery**: normal upsert/update/delete first attempts source and derived changes in
one transaction. If any Vec1 operation fails, roll back that transaction, commit the source-table mutation in
a source-only transaction, mark the derived index unhealthy/stale, log the cause, and serve all reads by
brute force until a successful reindex. First-enable backfill/reindex writes a completion marker only after
its derived transaction, count verification, and dimension report succeed; a failure or crash leaves no
"ready" marker and never changes source rows. Vec1 registration/open/schema/integrity/query errors, an empty
or stale index, and index corruption all disable Vec1 for that `Store` instance and fall back silently to the
existing caller contract (with diagnostic logging); source-table errors still propagate normally.

**Production composition and PR ownership**: PR 2A adds the private options-based connector; PR 2B owns
production propagation of one shared `OpenStore` option derived from `Config.VecIndex.Enabled`. Every direct
production opener must pass it: `cmd/omnia/embed.go`; both `buildAutoEmbedWorker` and
`buildCLIEmbedPurgeStore` in `cmd/omnia/autoembed.go`; `buildRecallService` in `cmd/omnia/recall.go` (therefore
CLI search, serve/MCP, and `cmd/omnia/eval.go`); and `internal/dashboard/local_datasource.go`, with
`cmd/omnia/dashboard.go` carrying the config into that boundary. Callers never invoke `driver.Open` or
`vec1.Register` themselves. PR 2B wires connector/lifecycle behavior only and leaves `Search`,
`SearchScoped`, `Graph`, and `GraphScoped` on brute force. PR 3 alone owns KNN score conversion and read
routing for those four methods.

**Score parity**: current Omnia stores non-zero unit-normalized vectors and returns `dot(query, stored)`.
The pinned Vec1 artifact used by `go-sqlite3@v0.35.2` computes cosine distance as `d = 1 - cosine` (including
its generated bundled source at
[`_vec1CosDist`](https://github.com/ncruces/go-sqlite3-wasm/blob/v3.2.35303/vec1/vec1.go#L10155-L10186)).
Therefore, for Omnia's normalized vectors, `score = float32(1 - d)`, not `2 - d`; this recovers the current
dot-product score. The newer Vec1 trunk reference documents `2 - cosine`, so implementation must pin the
module, assert self/orthogonal/antipodal distance semantics in focused tests, and retain score-parity tests
with a numeric tolerance. No throughput claim is part of this design.

**Interfaces**: `VecIndexConfig{Enabled bool}` only. No new top-level command is added. PR 2B adds the opt-in
`--reindex` maintenance flag to the existing `omnia embed` command; it reports indexed/skipped reasons and
rebuilds only derived state. Capability 3 may use Vec1 only when it is ready; otherwise
`Graph`/`GraphScoped` retain their current brute-force algorithm.

**Interactions**: encryption may compose its VFS initialization and `vec1.Register` in the same `internal/embed`
connection initializer, in documented order and with an integration test. Vec1 itself has no ownership outside
that package.

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/config/config.go`, `internal/config/v04_config_test.go` | Modify | Keep 7 default-OFF blocks; PR 2A removes the unreleased VecIndex quantization field/default/mapping/test contract so VecIndex exposes only `Enabled`. |
| `internal/store/store.go` | Modify | Driver selection at `New`/`newWithoutRepair` (ADR-1); route `openDB` to ncruces+adiantum when `encryption.enabled`. |
| `internal/store/anchors.go` | Modify | Add `BlameLine` reverse walk + `CodeDecisionGraph` projection (1). |
| `internal/store/relations.go` | Modify | Add `RelationConsolidates` verb (3). |
| `internal/store/procedures.go` | Reuse | `ListProcedures`/`SearchProcedures` feed the gate (2). |
| `internal/embed/store.go` | Modify | PR 2A/B: private connector, rowid mapping, native-float32 derived lifecycle and reindex; PR 3: KNN read routing/score fallback. Brute force retained. |
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
| `cmd/omnia/embed.go`, `cmd/omnia/autoembed.go`, `cmd/omnia/recall.go`, `cmd/omnia/eval.go` | Modify | PR 2B passes the shared VecIndex store option through CLI writer/reindex, autoembed/purge, recall, serve/MCP, and eval composition without changing reads. |
| `cmd/omnia/dashboard.go`, `internal/dashboard/local_datasource.go` | Modify | PR 2B carries VecIndex config to the dashboard's direct embedding-store opener; PR 3's store methods decide KNN versus brute force. |
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
type VecIndexConfig struct{ Enabled bool `yaml:"enabled"` } // Vec1 float32 flat/cos only
```

## Testing Strategy

| Layer | What | Approach |
|-------|------|----------|
| Unit | overlap ranking (1), matcher (2), clustering/union-find (3), logistic train (5), invalidation (6) | Table tests; inject fake git/keychain/Ollama runners (the `Probe.runGit` pattern). |
| Unit | encryption round-trip (4), vec vs brute-force parity (7) | Encrypt→reopen→read equals plaintext; assert Vec1 top-k and converted scores match brute-force on a fixture store. |
| Composition | every direct production embedding-store opener (7) | PR 2B proves enabled passes the shared connector option and disabled stays modernc; PR 3 proves only store read methods route KNN. |
| Config | unreleased VecIndex scaffold removal (7) | PR 2A proves only `enabled` remains: no quantization field, default, environment/YAML mapping, fixture, or behavioral branch. |
| Integration | driver selection (ADR-1), non-destructive migrations (4/7) | Full `internal/store` suite MUST pass under the ncruces path; migrate a seeded plaintext DB, assert data intact + reversible. |
| Invariant | "opts into none ⇒ byte-for-byte v0.3.2" | Each capability's disabled-is-no-op test (existing convention). |
| Eval | ranker non-regression (5) | `internal/eval` gate blocks promotion on any regression. |

## Migration / Rollout

Per-capability rollback = set the flag `false` (or leave absent) → inert (1,2,3,5,6). Encryption (4):
`omnia security decrypt` restores plaintext via the keychain key. Vec (7): disabling reverts reads to the
retained brute-force path with no data change. Because the PR 1 quantization scaffold was never released,
PR 2A removes it without a compatibility migration; any pre-release config must remove that unsupported key.
Encryption DB migrations are copy-then-atomic-rename with a retained backup (mirroring `datadir.Migrate`),
never in-place destructive. Vec1 rebuilds only its derived table from unchanged source rows through
`omnia embed --reindex`.

## Open Questions

These are EMPIRICAL/validation items, not unresolved architecture — Codex has a concrete decision for every
capability:
- [ ] Verify the combined adiantum-VFS + `vec1.Register` initializer against the pinned ncruces release before
  implementation. Vec1 registration, flat/cos DDL, score conversion, and fallback ownership are fixed; keep
  modernc/brute-force as the safety net if the combined initializer cannot open.
- [ ] One empirical tuning pass on consolidation thresholds (`MinScore`/`K`/cluster sizes) and the ranker's
  `MinTrainExamples`, same "needs one tuning pass" caveat the procedural defaults already carry.
- [ ] Linux keychain coverage beyond `secret-tool` (e.g. headless servers) — degrade per ADR-3.
