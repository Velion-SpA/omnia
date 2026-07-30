# Proposal: Omnia v0.4 — La frontera de la memoria (Memory Frontier)

> **Planning artifact only.** This proposal defines WHAT and WHY for v0.4. It creates no code.
> It is the first phase of an SDD pipeline (proposal → spec → design → tasks) whose output will be
> handed to a DIFFERENT AI tool (Codex) to implement autonomously. Every WHAT/WHY product decision
> below is intended to be final; only HOW-level (implementation) questions should remain for spec/design.

## Intent

v0.3.x hardened Omnia's read/write economy (Context Economy, Write Hygiene, Time Travel). The strategic
research corpus "Omnia — La frontera de la memoria" (13 investigations, ~50 systems/papers surveyed; engram
obs #1563) identified where the local-first thesis can actually *win* against cloud-only competitors
(Mem0, Zep, Letta, Cognee, hyperscalers). v0.4 executes the 7 confirmed bets from that corpus as one
themed umbrella change — the same bundling convention v0.3.1 "Write Hygiene" used for ~13 independent-but-
related pieces.

The unifying thesis: **memory that stays on the device, connects to the code it explains, and is enforced
rather than merely re-injected.** Recalling a fact is not applying it (57.5% of corrections get re-violated
even with Mem0). Competitors consolidate in the cloud, cannot forget, cannot encrypt at rest, and cannot tie
a decision to the exact line of code it governs. v0.4 does all four, locally.

## Release-Wide Invariants (apply to ALL 7 capabilities)

- **Default-OFF, per-capability config flag.** Every capability ships behind its own config block that
  defaults disabled, exactly like `TimeTravelConfig` / `WriteHygieneConfig` (see `internal/config/config.go:99`,
  `:114`, `:136`). A fresh install or upgrade that never opts in sees ZERO behavior change vs v0.3.2 — byte-for-
  byte. Where a plain-bool zero value cannot distinguish "explicitly disabled" from "never mentioned," follow the
  established explicit-vs-absent probe idiom (`recallEnabledKeyPresent` `internal/config/config.go:591`,
  `writeHygieneEnabledKeyPresent` `:612`).
- **CGO_ENABLED=0, pure Go, single binary.** Hard requirement, unchanged. No capability may introduce cgo. The
  store uses `modernc.org/sqlite` (`internal/embed/store.go:14`); any new vector/crypto/index code must stay in
  that constraint.
- **Local-first, opt-in cloud only.** No capability may send memory off-device by default. LLM-backed features
  use the existing local Ollama integration, never a cloud API.
- **Never hard-delete / never silently supersede.** Consolidation and enforcement augment; they retain pointers
  to source observations and never replace or delete them (mirrors the anchor/procedure "never hard-delete"
  decisions already in the store).

## Scope

### In Scope
- Seven named capabilities (below): `code-decision-graph`, `memory-enforcement-gate`, `sleep-consolidation`,
  `memory-at-rest-security`, `learned-ranker`, `repo-cartridge`, `sqlite-vec-index`.
- One config block per capability, default-OFF, following `internal/config/config.go` conventions.
- New MCP tools / CLI subcommands as each capability requires, registered in the existing dispatch
  (`cmd/omnia/main.go:730`) and MCP wiring (`cmd/omnia/main.go:1287` `mcp.MCPConfig`) patterns.

### Out of Scope
- Cloud-side changes (cloud sync fan-out, clouddash) beyond making new local tables opt-out of sync where the
  local-first invariant requires it (procedures already do this — `internal/store/procedures.go:26`).
- Replacing the embedding model, the FTS5 lexical path, or the RRF fusion core (`internal/recall/recall.go`).
- HNSW / ANN indexing (capability `sqlite-vec-index` explicitly stays "KNN flat por ahora, sin HNSW aún").
- Any GUI/dashboard surfacing of the new capabilities (a later release may visualize the code↔decision graph;
  engram idea #1092).
- Cross-file anchor relocation, AST/tree-sitter parsing (already deferred in `internal/anchor/anchor.go:419`).

## Capabilities

> Contract with the sdd-spec phase. Each New Capability becomes its own `spec.md`. No existing spec's
> *requirements* change (v0.4 is strictly additive behind flags), so Modified Capabilities is None.

### New Capabilities
- `code-decision-graph`: complete the v0.2-partial code anchor machinery into a queryable code↔decision graph
  plus a new `mem_blame <file>:<line>` that surfaces the decision/bugfix memories explaining a line of code.
- `memory-enforcement-gate`: ⭐ flagship. A pre-completion gate that mechanically verifies an agent's edit against
  the machine-checkable postconditions already compiled into trusted `procedures`, blocking/flagging violations
  rather than re-injecting ignorable text.
- `sleep-consolidation`: local, idle-time, opt-in LLM consolidation of related-memory clusters into higher-level
  digests that always retain pointers back to their source observations.
- `memory-at-rest-security`: SQLite-file encryption at rest (OS-keychain key), completing the provenance
  trust-tag surface and the native memory audit log.
- `learned-ranker`: a locally-trained re-ranker over existing judgment/outcome signal, replacing hand-tuned
  similarity floors, with byte-for-byte cold-start fallback to today's floors.
- `repo-cartridge`: a precomputed, versioned per-repo digest (top memories + code-graph state) keyed to a
  commit, so a fresh agent session opens "warm" instead of cold-querying.
- `sqlite-vec-index`: a vector-native, still-CGO-free SQLite index replacing/augmenting the current brute-force
  O(N) cosine scan, with a non-destructive migration path.

### Modified Capabilities
- None. All behavior is additive and gated default-OFF.

---

## Capability Detail

### 1. `code-decision-graph` — Grafo de código ↔ decisiones (Bet 1: v0.2 partial → v0.4 complete)

**Problem.** Aider/Cody/Augment have a deterministic, zero-LLM code graph (symbols, calls). CLAUDE.md/Mem0 have
the "why." Nobody connects them. Omnia already stores half of the connection but exposes it in only one
direction and has no code→memory query. This is the exact whitespace that fits local-first + one-binary + SQLite
— "the Omnia signature."

**What already exists (verified in live code).**
- `memory_anchors` table: file + symbol + git-blame line range + blame SHA + content hash, linked 1:N to a
  memory via `obs_sync_id`, with status active/stale/traveled (`internal/store/anchors.go:9`, `:25`).
- Write path: `UpsertAnchor` (`internal/store/anchors.go:95`); reconcile path: `omnia forget-scan` →
  `ScanProject{Source:anchor}` (`cmd/omnia/forget_scan.go:105`); staleness surfacing: `MarkAnchorStale`
  (`internal/store/anchors.go:260`).
- The deterministic, cgo-free git probe: `Blame`/`RangeHash`/`Locate`/`Capture`/`HeadSHA`
  (`internal/anchor/anchor.go:221`, `:340`, `:462`, `:382`, `:192`).
- The ONLY existing read is memory→anchors: `GetAnchorsForObservations` (`internal/store/anchors.go:362`).

**In scope.**
- A new **reverse query**: given `file:line` (and optional repo root), find the anchor(s) whose git-blame range
  covers that line and return the decision/bugfix memories attached to them — the code→decision walk that does
  not exist today.
- Expose it as `mem_blame <file>:<line>` (new MCP tool + `omnia blame` CLI subcommand, registered like the
  existing `procedure`/`forget-scan` commands, `cmd/omnia/main.go:753`).
- Make the anchor set a first-class **queryable graph**: enumerate code→decision edges for a project/repo,
  reusing the existing anchor rows as edges (no new graph engine).

**Confirmed product decisions.**
- Zero-LLM and deterministic, reusing the existing content-hash/git-blame anchor machinery. No AST parser.
- `mem_blame` walks anchor → memory; stale anchors are returned but clearly marked stale (reuse the
  active/stale/traveled vocabulary), never silently hidden.

**Out of scope.** Symbol call-graph edges (caller/callee), cross-file relocation, and any LLM-assisted linking.

**Open (implementation-level, for design).** Exact `file:line` → anchor overlap resolution (a line may sit
inside multiple ranges); output shape/ranking when several memories attach to one line; whether the "graph"
surface is a new read method or a thin projection over `GetAnchorsForObservations`.

### 2. `memory-enforcement-gate` — Memoria como enforcement (Bet 2, ⭐ FLAGSHIP)

**Problem.** Remembering a fact ≠ applying it. Re-injecting "always use tabs here" as text the model can ignore
does not stop the model from re-violating it (57.5% re-violation even with Mem0). No code-memory product turns
repeated corrections into real execution gates. This is Omnia's trust differentiator.

**What already exists (verified — this is the decisive finding).** v0.3.1's procedural memory already *compiles*
repeated corrections into verifiable, parameterized programs and governs their trust, but **explicitly does not
execute them**:
- `procedures` table: polarity `playbook`/`anti_playbook`, a `trigger`, ordered slot-templated `steps`, an
  `expected_outcome`, a **machine-checkable `postcondition_kind`** (`tests_pass` / `lint_clean` / `build_green` /
  `custom`) with a `postcondition_expr`, plus `confidence`/`state` (candidate/trusted/retired) governed by an
  SSGM UPVOTE/DOWNVOTE state machine (`internal/store/procedures.go:34`, `:51`, `:134`, `:490`, `:558`).
- Polarity is always derived from the source bugfix `outcome` (worked → playbook; did_not_work → anti_playbook),
  never inferred by a model (`internal/store/procedures.go:16`).
- **The gap, stated verbatim in the code:** "this slice only STORES these values, it never executes them (the
  deferred compiler runtime, design obs #1592, does that later)" (`internal/store/procedures.go:49`).

v0.4 builds that deferred compiler runtime. The "rules" are already compiled; v0.4 executes them.

**In scope.**
- A **pre-completion enforcement gate**: an interface an agent calls BEFORE its edit/task is considered done,
  which selects the `trusted` procedures whose `trigger`/scope apply to the current change and mechanically runs
  their postcondition (`tests_pass`/`lint_clean`/`build_green` = run the configured command; `custom` = evaluate
  `postcondition_expr`), returning pass / block / flag.
- Expose it primarily as a new MCP tool the agent invokes at end-of-edit (e.g. `mem_enforce` / `mem_check`),
  wired through `mcp.MCPConfig` like the existing `ProceduralWiring` (`cmd/omnia/main.go:1342`), plus an
  `omnia enforce` CLI for hook/CI use.
- An **escape hatch**: a wrong block is worse than a missed catch for a first slice, so a violation must be
  overridable (explicit override flag/param) and the gate must default to *flag* rather than hard-*block* until
  an operator opts into blocking. Every gate decision is recorded (audit log, capability 4).

**Confirmed product decisions.**
- **Mechanical, not LLM-judged.** The gate verifies postconditions that are already machine-checkable — the whole
  point of "not text the model can ignore." No LLM is in the enforcement path.
- **Feed = trusted procedures only** (state `trusted`, `internal/store/procedures.go:41`), whose polarity and
  postcondition were derived from `bugfix outcome=worked/did_not_work` and preference/pattern corrections. Candidate
  and retired procedures never gate.
- **False positives handled by design:** default mode is flag-with-override, not block; blocking is opt-in; every
  decision (pass/flag/block/override) is audited.

**Out of scope.** Inducing new procedures (already done by `procedure-induct`, `cmd/omnia/main.go:759`); LLM-based
diff understanding; auto-fixing violations. The gate verifies, it does not edit.

**Open (implementation-level, for design).** How a change/diff is described to the gate so `trigger`/scope
matching is precise (path globs? code anchors from capability 1? the files touched?); how `custom`
`postcondition_expr` is evaluated safely and cgo-free; command-runner sandboxing for tests_pass/build_green;
exact block-vs-flag config surface.

### 3. `sleep-consolidation` — Consolidación "sleep" local (Bet 3: v0.3.1 partial → v0.4 complete)

**Problem.** Large systems synthesize memory in the background (Dreaming/Gemini) — but in the cloud, unrequested,
and unable to forget. Do it locally, in idle, opt-in, always keeping pointers to the source. You invert their
move and keep the benefit without the privacy or token cost.

**What already exists (verified).**
- v0.3.1 shipped the spaced-review scaffolding: the `review_after` field and review flow (used across
  `internal/store/anchors.go:296`, `internal/store/procedures.go`, `ReviewConfig`
  `internal/config/config.go:238`) — the "parcial."
- The k-NN semantic-similarity graph to find related-memory clusters already exists:
  `embed.Graph`/`GraphScoped` (`internal/embed/store.go:264`, `:282`).
- The local Ollama client (`internal/embed/embed.go:34`, `:78`).

**In scope.**
- An idle-time, opt-in consolidation pass that clusters related episodic memories via the existing k-NN graph and
  produces a higher-level **digest** memory per cluster.
- Each digest **always retains pointers** back to its source observations (relation rows, never replacing/
  deleting the sources — mirrors the never-hard-delete invariant).
- A CLI entry point (`omnia consolidate`) and/or an idle worker analogous to the auto-embed worker
  (`cmd/omnia/main.go:997`), gated by config.

**Confirmed product decisions.**
- **Local Ollama model** for summarization/synthesis — same pattern as `internal/embed`, zero cost, zero data
  leaving the device. NOT a cloud API.
- **Reuse the existing `Graph()` k-NN** for cluster discovery; do not build a new clustering engine.
- A digest **augments, never supersedes silently.** Sources stay live and independently retrievable.

**Out of scope.** Automatic deletion/forgetting of the consolidated sources; cloud consolidation; real-time
(non-idle) synthesis.

**Open (implementation-level, for design).** Cluster selection thresholds (minScore/k) and cluster-size caps;
the digest observation `type` and how it is ranked at retrieval; the prompt/format for the local model; how idle
is detected; the exact relation verb linking digest → sources.

### 4. `memory-at-rest-security` — Cifrado at-rest + procedencia + auditoría (Bet K: v0.2 partial → v0.4 plan)

**Problem.** Actionable differentiators competitors lack: encryption at rest by default, a provenance/trust tag at
write time (user vs ingested), and a native memory audit log. Screenpipe/Rewind/Mem0 do not encrypt at rest.

**What already exists (verified).**
- Provenance foundation: trust-tag classification at write time — `user`/`agent`/`ingest:*`/`unverified`
  constants and `classifyTrust` (`internal/store/provenance.go:9`, `:26`). It is attribution, never a block.
- A native audit-log package already exists (`internal/audit`, imported at `cmd/omnia/main.go:31`) plus the
  soft-delete + audit-log governance work (engram obs #1021).

**In scope.**
- **Encryption at rest** for the on-disk SQLite database(s), transparent to the user.
- Complete the provenance surface: ensure the write-time trust tag is captured/surfaced consistently and is
  visible in read receipts and the audit trail.
- Ensure enforcement-gate decisions (capability 2) and consolidation actions (capability 3) are written to the
  audit log.

**Confirmed product decisions.**
- **Key source = OS keychain** (macOS Keychain / platform equivalent): generated once, stored there, transparent,
  no passphrase to remember or lose.
- **Threat model is explicit and honest:** protects against disk theft / lost laptop while the process is NOT
  running. It does NOT protect against a live-process memory dump or an attacker who already has the unlocked
  keychain. Spec must state this plainly.

**Out of scope.** Cloud-side encryption changes; per-user multi-tenant key management; protection against live
memory inspection.

**Open (implementation-level, for design).** The single biggest HOW question: **full-database encryption vs.
application-level field encryption** under the hard `CGO_ENABLED=0` constraint. SQLCipher is cgo; investigate a
pure-Go path (e.g. an encrypted VFS / page-level encryption compatible with `modernc.org/sqlite`, or app-layer
encryption of sensitive columns) — this is a design decision, but the *product* requirement (encrypted at rest,
keychain key, stated threat model) is fixed. Also: keychain access on headless/Linux/CI, and key-rotation/
recovery UX.

### 5. `learned-ranker` — Learning-to-rank local (Bet I: planned in v0.3.2, deferred)

**Problem.** Search ranking currently leans on hand-tuned similarity floors that must be re-calibrated per
embedder (`StrongFloor`/`BaseFloor` = 0.35/0.25, jina-calibrated; `DefaultFuseParams`
`internal/recall/recall.go:74`; `AdaptiveFloor` `:96`). Omnia already captures the exact signal needed to learn a
ranking: `mem_judge` verdicts (supersedes/conflicts_with/compatible) and bugfix `outcome` (worked/did_not_work).

**In scope.**
- A locally-trained, lightweight **re-ranker** over existing features (BM25/lexical rank, embedding cosine,
  recency, type-match, importance-by-type `internal/config/config.go:398`, outcome/judgment history) that augments
  or replaces the hand-tuned floors.
- Training runs locally on the user's own corpus; a CLI entry point (e.g. `omnia rank-train`) following the
  existing subcommand pattern.
- Evaluation against the existing token-cost-normalized eval harness (`internal/eval/harness.go:37`,
  `omnia eval` `cmd/omnia/main.go:791`) to prove no accuracy regression before it can be enabled.

**Confirmed product decisions.**
- **Not a foundation model.** A small, pure-Go, no-CGO learned re-ranker (e.g. logistic / gradient-boosted over the
  features above), trainable on a small per-user corpus.
- **Cold-start = byte-for-byte today's behavior.** With zero judgments/outcomes, ranking falls back exactly to the
  current hand-tuned floors (`DefaultFuseParams`), identical output, until there is enough signal to train.

**Out of scope.** Replacing RRF fusion or the FTS5/embedding retrieval legs; online/continuous learning during a
session; cross-user/global models.

**Open (implementation-level, for design).** Exact model class and pure-Go training implementation; feature
vector definition and normalization; the "enough signal to train" threshold and how the trained model is stored/
versioned/invalidated; where the re-rank sits relative to `recall.Fuse` and `RankResults`.

### 6. `repo-cartridge` — Cartridge del repo (Bet J: planned in v0.3.2, deferred)

**Problem.** Precompute a digested state offline/idle/CI and load it near-free, so a session opens "warm" instead
of re-ingesting files and cold-querying (KV-cache / Cartridges / sleep-time-compute paradigm).

**In scope.**
- A precomputed, **versioned per-repo cartridge**: a serialized digest of a project's most-relevant memories plus
  code-graph/anchor state, keyed to a repo + commit.
- Build entry point (`omnia cartridge build`) runnable in idle/CI; load path that a fresh session uses to start
  warm.
- **Invalidation by content-hash / commit**, matching the existing anchor/forget-scan incremental pattern
  (`HeadSHA` `internal/anchor/anchor.go:192`; content-hash anchors `internal/store/anchors.go`).

**Confirmed product decisions.**
- Cartridge contents: top/most-relevant memories for the repo + code-graph (anchor) state (capability 1),
  serialized and versioned, keyed to repo/commit.
- Invalidation is commit/content-hash based, not time-based; a cartridge built at commit X is known-stale at a
  different HEAD.

**Out of scope.** KV-cache/weight-level precomputation (parametric memory is explicitly not adopted, obs #1575);
shipping cartridges between machines/cloud (local artifact for this slice).

**Open (implementation-level, for design).** On-disk cartridge format and location; exactly which/how-many
memories are included and how "most relevant" is computed; partial-invalidation vs. full-rebuild on commit change;
interaction with `learned-ranker` and `sleep-consolidation` outputs.

### 7. `sqlite-vec-index` — sqlite-vec sin CGO (Bet M: planned in v0.3.2, deferred)

**Problem.** Semantic search currently does a brute-force O(N) cosine scan that decodes every stored vector blob
per query (`internal/embed/store.go:164` `Search`, `:186` `search`, `:264` `Graph`), over a plain `embeddings`
table with a `vector BLOB` column (`internal/embed/store.go:52`). This is fine at ~1k vectors but does not scale.

**In scope.**
- A **vector-native index inside SQLite** (via a pure-Go SQLite→WASM path such as `ncruces/go-sqlite3`,
  supporting int8/binary quantization) replacing/augmenting the brute-force scan — still `CGO_ENABLED=0`.
- Cover the same read surfaces that brute-force serves today: `Search`, `SearchScoped`, and the `Graph`/
  `GraphScoped` k-NN used by consolidation (capability 3).
- A **non-destructive migration path** for existing `embeddings.db` data.

**Confirmed product decisions.**
- **KNN flat only for now — no HNSW/ANN yet** ("KNN flat por ahora, sin HNSW aún").
- Must not break existing embeddings data: the migration is additive (an index alongside/over existing vectors)
  or a safe dual-write, decided in design after verifying the current storage shape — never a destructive rewrite.

**Out of scope.** HNSW/approximate indexes; changing the embedding model or dimension; the separate memory
`engram.db` (this is the embeddings store only, which is Omnia-owned and writable — `internal/embed/store.go`).

**Open (implementation-level, for design).** Clean swap vs. dual-write vs. additive index; whether `modernc.org/
sqlite` gains an extension or the embeddings store moves to `ncruces/go-sqlite3`; quantization defaults; keeping
`Graph`'s O(N²) cluster build correct (or accelerated) on top of the new index.

---

## Approach

- Ship as ONE umbrella change `omnia-0.4-memory-frontier` with 7 spec capabilities, mirroring v0.3.1's bundling.
- Each capability is independently gated and independently landable; the sdd-tasks phase will slice these into
  reviewable PRs (several will exceed a single 400-line PR — expect chained/stacked delivery).
- **Natural build order / dependencies** (soft, for planning): `sqlite-vec-index` (7) and
  `memory-at-rest-security` (4) are storage-layer foundations that other capabilities benefit from;
  `code-decision-graph` (1) feeds `memory-enforcement-gate` (2, trigger/scope matching) and `repo-cartridge` (6,
  code-graph state); `sleep-consolidation` (3) and `learned-ranker` (5) both consume existing graph/eval infra.
  None hard-block another for a first slice, since all are behind flags.
- Reuse existing seams everywhere: config block + `applyDefaults` (`internal/config/config.go:694`); CLI dispatch
  (`cmd/omnia/main.go:730`); MCP wiring via `mcp.MCPConfig` (`cmd/omnia/main.go:1287`); local Ollama
  (`internal/embed`); anchors/git probe (`internal/anchor`); eval harness (`internal/eval`).

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/config/config.go` | Modified | 7 new default-OFF config blocks + `applyDefaults` entries. |
| `internal/store/` | Modified/New | reverse blame query + graph read (1); enforcement selection over `procedures` (2); digest + source-pointer relations (3); provenance surfacing + encrypted open path (4); audit writes (2,3). |
| `internal/anchor/` | Reused | git probe + `HeadSHA` for blame (1) and cartridge invalidation (6). |
| `internal/embed/` | Modified/New | vector-native index replacing brute-force scan (7); k-NN reuse for consolidation (3). |
| `internal/recall/`, `internal/eval/` | Modified/Reused | learned re-rank over fusion features (5); eval gate (5). |
| `internal/audit/` | Reused/Modified | gate + consolidation decisions recorded (2,3,4). |
| `internal/mcp/`, `cmd/omnia/main.go` | New | new MCP tools (`mem_blame`, enforcement) + CLI subcommands (`blame`, `enforce`, `consolidate`, `rank-train`, `cartridge`). |
| new: encryption/keychain, cartridge, learned-ranker packages | New | pure-Go, CGO-free leaves. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| CGO creep (SQLCipher/vec/crypto need cgo) | High | Hard gate: pure-Go only. Encryption (4) and vector index (7) design phases MUST prove a CGO_ENABLED=0 path or descope. |
| Enforcement false positives block valid work | Med | Default = flag-not-block; blocking opt-in; explicit override; every decision audited. |
| Encryption migration corrupts/locks existing DBs | Med | Non-destructive migration + backup + reversible; keychain-unavailable degrades to unencrypted-with-warning, never data loss. |
| Vector-index migration breaks existing `embeddings.db` | Med | Additive/dual-write migration; verified against live storage shape before implementation; brute-force path retained as fallback. |
| Learned ranker regresses recall on small corpora | Med | Cold-start = byte-for-byte current floors; enabling gated on eval-harness proof of no regression. |
| Scope too large for clean review | High | Independently-flagged capabilities; chained/stacked PRs planned at tasks phase. |
| Consolidation digests drift from / bury sources | Low | Digests augment only; sources stay live; pointers mandatory; opt-in + idle-only. |

## Rollback Plan

- **Per capability:** set its config flag to `false` (or leave absent) → the feature is fully inert and behavior
  is byte-for-byte v0.3.2, by construction of the default-OFF invariant. This is the primary rollback for 1, 2, 3,
  5, 6.
- **Encryption (4):** provide an explicit `omnia`-level decrypt/migrate-back command; disabling the flag on an
  already-encrypted store must transparently read via the keychain key and allow re-writing plaintext. Never leave
  a user locked out of their own memory.
- **Vector index (7):** the brute-force cosine path (`internal/embed/store.go:186`) is retained; disabling the
  index flag reverts reads to it with no data change.
- **Code/tag level:** the umbrella change is a set of additive commits behind flags; reverting the branch/tag
  removes all new surfaces without touching existing v0.3.2 data.

## Dependencies

- Local **Ollama** runtime (already an Omnia dependency for embeddings) — required for `sleep-consolidation`;
  degrades gracefully (feature disabled) when absent, like recall auto-detect.
- System **git** binary (already required by `internal/anchor`) — for `code-decision-graph` and `repo-cartridge`
  invalidation.
- OS **keychain** API access — for `memory-at-rest-security`; behavior on headless/Linux/CI is a design question.
- A pure-Go **sqlite-vec-capable SQLite** (`ncruces/go-sqlite3` or equivalent) — for `sqlite-vec-index`.
- Existing internal packages: `store`, `embed`, `anchor`, `recall`, `eval`, `audit`, `config`, `mcp`.

## Success Criteria

- [ ] All 7 capabilities land behind their own default-OFF config flag; a config that opts into none produces
      byte-for-byte v0.3.2 behavior (verified by the existing "disabled = no-op" test convention).
- [ ] Binary still builds and tests pass with `CGO_ENABLED=0`; no cgo introduced.
- [ ] `mem_blame <file>:<line>` returns the decision/bugfix memories anchored to that line (1).
- [ ] The enforcement gate mechanically runs trusted-procedure postconditions before edit completion and can
      flag/block a real violation, with a working override and an audit entry (2).
- [ ] `omnia consolidate` produces a digest over a k-NN cluster using a local Ollama model, with retained source
      pointers and no source deletion (3).
- [ ] The database is encrypted at rest with a keychain-sourced key, transparently, with a documented threat model
      and a reversible migration (4).
- [ ] With judgments/outcomes present, the learned ranker matches or beats hand-tuned floors on the token-cost-
      normalized eval harness; with none, it is byte-for-byte identical to today (5).
- [ ] A repo cartridge built at a commit loads a fresh session warm and is correctly invalidated at a new HEAD (6).
- [ ] Semantic search returns the same top-k as the brute-force path via the new vector index, with existing
      `embeddings.db` data intact after migration (7).

## Open Product Questions / Judgment Calls (for user review before spec)

The task supplied confirmed product decisions for most bets; these are the remaining *product-level* items I
resolved by judgment and that the user may want to confirm or correct:

1. **Enforcement default mode.** I set the first slice to **flag-with-override, blocking opt-in** (a wrong block is
   worse than a missed catch). Confirm this is the intended first-slice posture.
2. **Enforcement feed.** I scoped the gate to **`trusted` procedures only** (candidate/retired never gate). Confirm
   preference/pattern corrections should reach the gate only after they have been induced into a trusted procedure.
3. **Encryption scope.** Product requirement fixed (encrypted at rest, keychain key, stated threat model); the
   full-DB-vs-field-level choice is deferred to design as a HOW question under CGO=0. Confirm that is acceptable
   rather than a product decision you want to make now.
4. **Vector index = replace vs. augment.** I left this as an additive/non-destructive migration decided in design;
   confirm you do not require an immediate full swap.
5. **Naming.** MCP tool / CLI names (`mem_blame`/`blame`, `mem_enforce`/`enforce`, `consolidate`, `rank-train`,
   `cartridge`) are provisional and can be renamed at spec time.

If any answer differs, it changes spec-level requirements — surface before the spec phase runs.
