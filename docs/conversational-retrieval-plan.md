# Plan: Omnia for conversational agents

Ranked implementation plan answering `omnia-conversational-retrieval-brief.md`.
Consumer that motivates it: **Vel** (ESP32-S3 voice assistant → Hermes → `GET /search`).

Everything below is ranked by **measured impact per unit of effort**, states which
of the two goals it serves, and names the measurement that proves it worked —
including the baseline to take *before* the change.

---

## 0. Correction to the brief's premises (read this first)

Three of the brief's assumptions do not survive contact with the code. They
change the ranking, so they come before the plan.

### 0.1 `GET /search` bypasses Omnia's entire ranking pipeline

This is the single largest finding, and it was invisible from the outside.

`mem_search` (MCP, what a coding agent uses) runs, in order
(`internal/mcp/mcp.go:1490`–`1600`):

```
recall.Fuse (RRF)
  → RankResults          (relevance × recency × importance × salience)
  → ApplyLearnedRanker   (optional trained local model)
  → ApplyStalenessDownrank (memory_anchors — structural forgetting)
  → ApplyTypeLens        (soft boost for a requested observation type)
  → ApplyMMR             (near-duplicate removal / diversity)
  → ApplyTokenBudget     (fits the result set to a token ceiling)
  → recall-degradation envelope (recall_degraded, embeddings_behind_by, …)
```

`GET /search` (HTTP, what **Vel** uses) runs
(`cmd/omnia/main.go:1103` → `cmd/omnia/recall.go:154`):

```
recall.Fuse (RRF) → hydrate → JSON
```

That is all. No ranking pass, no MMR dedup, no token budget, no type lens, no
staleness downrank, and **no degradation envelope** — the response is a bare
`[]store.SearchResult` (`internal/server/server.go:479`–`513`).

Consequences that rewrite the brief's diagnosis:

- **"Recency re-ranking hurts identity questions"** — recency re-ranking never
  runs for Vel. `RankingConfig.Enabled` defaults to `false`
  (`internal/config/config.go:437`, no `applyDefaults` override), and even with
  it enabled the HTTP path never calls `RankResults`. What Vel actually gets is
  raw RRF, whose only recency influence is a **tie-break** on `UpdatedAt DESC`
  (`internal/recall/recall.go:237`). The recency bias is real but far weaker
  than assumed — and, more importantly, it is not the lever that will fix
  identity questions.
- **"Retrieval should be able to report low confidence"** — already built, for
  the other consumer. `mem_search` emits `recall_degraded`,
  `recall_degraded_reason`, `embeddings_stale`, `embeddings_behind_by`,
  `newest_embedded_at` (issue #226, memory #1892). `GET /search` emits none of
  them. The brief's "make degraded recall observable to API consumers" is
  ~80% a port, not a build.
- **MMR dedup and token budgeting already exist** and are exactly what Hermes'
  1400-character budget needs. Vel cannot reach them.

**A voice agent is currently on a strictly weaker retrieval path than a coding
agent, using the same store.** Closing that gap is P0 and is mostly wiring.

### 0.2 Query-time evidence already has a clean insertion point

`internal/core` is a `Source → Item → Sink` pipeline (`core/ports.go`,
`core/domain.go`) where `Item.TopicKey` drives **upsert** at the sink. Adding a
repository-documentation source is one new package satisfying `core.Source`;
re-ingest-without-duplication comes free from the existing topic_key upsert. The
brief's "how do we re-ingest on change without duplicating" question is already
answered by the architecture.

Note also that `internal/obsidian` is an **exporter** (Omnia → vault), not an
importer. There is no document-ingestion path today, in any direction.

### 0.3 A claim-lifecycle column already exists, half-built

`observations.state` is already a real column with a two-value vocabulary:
`active` | `needs_review` (`internal/store/store.go:136`). Relations already
carry `supersedes`, `conflicts_with`, `consolidates`
(`internal/store/relations.go:33`–`40`). The missing concept is not storage —
it is (a) a `closed`/`obsolete` state, and (b) anything that operates at
**claim** granularity rather than **observation** granularity.

`sync_id` (`obs-9437f9849e481520`) is present on every row and is the
replica-stable identifier the brief hypothesised. Confirmed usable.

---

## Baseline first: extend `omnia eval` with a conversational corpus

**Serves:** both goals. **Effort:** S (1–2 days). **Risk:** low.
**This is a prerequisite for every measurement below.**

`internal/eval` already exists (corpus, scoring, ranking metrics, runner) and
`omnia eval` already runs reproducibly (±0.000 across runs). It is currently
aimed at coding-agent recall.

**Change:** add a `conversational` corpus profile with the seven question kinds
from the brief — identity, status, delta, open-items, rationale, cross-project,
absence — with **hand-labelled** gold sets, ~15 cases per kind.

**Hard requirement, from a mistake this project already made:** memory #1898
published 48.3%/55.0% recall figures that memory #1912 then retracted — they
measured a defect in the corpus *builder*, not Omnia. Do not mechanically
generate the corpus from the store again. Hand-label it, and include an
**absence** class whose gold answer is literally "no evidence exists" so the
harness can score honest refusal, which no generated corpus can produce.

**Measure before:** run it against today's `GET /search` **and** today's
`mem_search`, separately. The delta between the two is the P0 opportunity,
quantified. Report accuracy@1, MRR, and — for identity/absence — a
hallucination rate scored by "did the top-4 context contain the evidence needed
to answer, yes/no".

**Proves it worked:** every item below reports against this harness. Without it,
every claim in this plan is an opinion.

---

## P0 — Put `GET /search` on the same pipeline as `mem_search`

**Serves:** Goal 2 (and indirectly Goal 1 — doc memories in P1 are worthless if
the HTTP path can't rank or dedup them). **Effort:** S–M (~3–4 days).
**Risk:** medium — it changes result order for the one consumer in production.

### The problem

Section 0.1. Vel gets raw RRF with none of the ranking, diversity, budgeting, or
honesty machinery that already exists and is already tested.

### The change

1. **Extract the post-fusion pipeline into a shared function.** Today it is
   inlined in `handleSearch` in `internal/mcp/mcp.go`. Lift `RankResults →
   ApplyLearnedRanker → ApplyStalenessDownrank → ApplyTypeLens → ApplyMMR →
   ApplyTokenBudget` into one exported `mcp.RankPipeline(results, relevance,
   cfg, now)`. Both callers then use it, so they cannot drift — the same
   "avoid divergence" argument that already justified `recallOrFTSSearch` as a
   shared seam for `omnia search` and `GET /search` (issue #86).
2. **Widen `SearchFunc`** (`internal/server/server.go:82`) to return a result
   *envelope* rather than a bare slice: `{results, recall_degraded,
   recall_degraded_reason, embeddings_stale, embeddings_behind_by,
   newest_embedded_at}`. Keep the array shape available under a
   back-compat flag so Hermes can migrate on its own schedule.
3. **Expose the knobs Vel needs as query params** on `GET /search`:
   `type` (already there — routes into `ApplyTypeLens`),
   `max_tokens` (routes into `ApplyTokenBudget`),
   `explain=1` (routes into `BuildResultReceipt`),
   `all_projects=1` (exists in `mem_search`, absent over HTTP — see P5).
4. **Ship a default config for the voice profile**: `recall.ranking.enabled:
   true`, `mmr.enabled: true`, `token_budget.max_tokens` sized to Hermes'
   ~1400 characters.

### Risk and containment

Turning ranking on changes ordering for a live consumer. Contain it the way this
codebase already contains ranking changes: every stage stays independently
config-gated and off by default, so the rollback is a config edit, not a
redeploy. Enable them one at a time against the eval harness.

### Measurement

- **Before:** eval-harness scores for `GET /search` vs `mem_search` on the same
  corpus (from the baseline item). Expect a measurable gap; that gap is the
  budget for this item.
- **After:** `GET /search` scores converge to `mem_search`'s, per question kind.
- **Latency:** `p50`/`p95` on `GET /search` before and after. The whole pipeline
  is pure in-process re-sorting over ≤50 hydrated rows — budget **< 5 ms**. If
  it measures above that, the pipeline is wrong, not the budget.
- **Field signal:** `omniaMs` in a real voice turn must stay ≤ ~90 ms.

---

## P1 — Ingest repository documentation as memories (`repodoc` source)

**Serves:** Goal 1. **Effort:** M (~1 week). **Risk:** medium (ranking pollution).

### The problem

Memory records **change**, never **state**. The save protocol has no trigger that
ever fires for "what this project IS". The reference behaviour the user accepted
is a coding agent that answers "what is Omnia" by **reading `README.md` and
`VISION.md` at question time**. This is the "give access to the EVIDENCE"
option, not the rejected "curate the ANSWER" option: the source of truth stays
the repo, and a wrong answer is fixed by fixing the README.

### The change

A new `internal/source/repodoc` package satisfying `core.Source`:

- **Scope:** `README*`, `VISION*`, `ARCHITECTURE*`, `CONTRIBUTING*`, `docs/**.md`,
  `adr/**.md`, `openspec/specs/**.md` — configurable allowlist, never the whole
  tree. Explicitly **not** source code (that is `internal/codegraph`'s problem).
- **Chunking:** by markdown heading, with the document title and heading path
  prefixed onto each chunk so a chunk retrieved alone still says what it is
  about. `internal/source/github` already chunks; reuse its shape.
- **Identity/upsert:** `TopicKey = repodoc:{project}:{path}#{heading-slug}`.
  Re-ingest on change is then a plain upsert — no duplicates, by construction.
- **Type:** a new observation type `doc` (not `manual`, not `digest`), so it can
  be lensed, weighted, and excluded independently.
- **Trigger:** on-demand `omnia collect -source repodoc`, plus a git-hash cursor
  in the existing `core.StateStore` so a scheduled run is a no-op when nothing
  changed. **No file watcher** — a repo doc changing 30 seconds sooner is worth
  nothing to a voice agent.
- **Provenance:** `source: ingest:doc` (the vocabulary already exists in
  `mem_save`), so doc-derived memories are distinguishable at query time.

### Keeping doc memories from drowning the delta log

This is the real design risk and it needs an explicit answer, not a weight tweak.
`docs/` in this repo alone is ~40 files; naive ingestion would swamp 479 delta
memories with hundreds of doc chunks and degrade the coding-agent path — which
the constraints forbid regressing.

The answer is **routing, not weighting**: doc memories are boosted for
identity-intent queries and **suppressed** for delta/status-intent queries, via
`ApplyTypeLens` (already built, `internal/mcp/type_lens.go`) driven by P2's
intent classifier. Absent an intent signal, doc memories rank normally with
`importance` weight 1 (below `decision`/`architecture` at 3). No new ranking
machinery is required — the lens already does soft-boost-by-type.

### Staleness

A doc memory carries the ingest git SHA. If the file's current SHA differs, the
existing `ApplyStalenessDownrank` / `memory_anchors` mechanism already downranks
it and `explain` already reports `staleness_penalty`. This is the whole reason
to prefer doc ingestion over a written identity record: **a stale quoted
document is detectable; a stale derived definition is not.**

### Measurement

- **Before:** identity-class accuracy on the eval corpus (expect near-zero — the
  brief's "Omnia es un cliente de línea de comandos para GitLab" case). Also
  record baseline delta/status accuracy, which must **not** move.
- **After:** identity-class accuracy, plus the "was the answer grounded in a
  retrievable document" rate. Target: identity ≥ 0.8, delta/status unchanged
  within noise.
- **Regression gate:** coding-agent `mem_search` scores on the pre-existing
  corpus must not drop. This is a hard gate, not an observation.
- **Corpus growth:** report doc-chunks added per project. If a project's doc
  chunks exceed ~30% of its total observations, the allowlist is too wide.

---

## P2 — Query intent classification and routing

**Serves:** Goal 2. **Effort:** M (~4–5 days). **Risk:** low (fails soft).

### The problem

Every question is one ranked list over one corpus. Identity wants a definition
and does not care about recency; status wants the latest state and recency is
decisive; open-items want claims that are still open. One ranking cannot serve
all three.

### The change

A `internal/intent` package: **lexical + pattern classification, no LLM, no
network**. Bilingual ES/EN from day one — the consumer speaks Spanish and
`internal/recall/bilingual_test.go` shows this codebase already treats that as a
first-class concern.

| intent | ES/EN cues | routing |
|---|---|---|
| `identity` | "qué es", "háblame sobre", "what is", "tell me about" | type lens → `doc`; ranking weight `recency: 0` |
| `status` | "cómo va", "cómo está", "how is X going" | ranking weight `recency` high (half-life ~3 days) |
| `delta` | "cómo arreglamos", "how did we fix", error strings | existing signature lane (already correct) |
| `open_items` | "qué falta", "qué queda", "próximos pasos", "what's next" | claim lane (P4); until P4 ships, flag the answer as unverified |
| `rationale` | "por qué elegimos", "why did we choose" | type lens → `decision`/`architecture` |
| `cross_project` | "en qué estoy bloqueado", "esta semana" | multi-project mode (P5) |

Unclassified → today's behaviour, byte-for-byte. That is the safety property:
the classifier can only ever *improve* a query it recognises.

Ship it as a **new ranking profile per intent**, not new ranking code —
`RankingConfig` already carries per-component weights and `ApplyTypeLens`
already does type boosting. This item is mostly a lookup table plus wiring.

### Latency

Regex/keyword matching over a ≤200-character query. Budget **< 1 ms**, which is
~1% of the 100 ms retrieval budget. The brief allowed 10 ms; we do not need it.
**Explicitly rejected:** any LLM round-trip inside retrieval for classification.

### Measurement

- **Before:** per-intent accuracy from the baseline harness (each of the seven
  classes scored separately — today they are all one number, which is why this
  problem was invisible).
- **Classifier quality:** precision/recall of the classifier itself against the
  hand-labelled corpus. Ship only if precision ≥ 0.9 — a *misrouted* query is
  worse than an unrouted one, and unrouted is the safe default.
- **After:** identity and status accuracy both improve, and neither improves at
  the other's expense. That trade is the entire point of the item.

---

## P3 — Answer-shaped context endpoint

**Serves:** Goal 2. **Effort:** M (~1 week). **Risk:** low.

### The problem

`summarizeOmniaMemories` in Hermes pulls `**What**` / `**Why**` and, for
`session_summary` entries that have neither, **falls back to a raw character
slice**. That is precisely how a markdown heading (`## Goal …`) reached the LLM
and produced the GitLab confabulation. That reassembly logic does not belong in
every consumer, and every consumer that reimplements it will reimplement the
bug.

### The change

`GET /answer?q=…&max_chars=1400` returning:

```json
{
  "context": "…assembled, budgeted, answer-shaped text…",
  "confidence": "high | low | none",
  "intent": "identity",
  "sources": [{"sync_id": "obs-…", "title": "…", "type": "doc", "updated_at": "…"}],
  "degraded": false
}
```

- Assembly runs server-side, reusing `ApplyTokenBudget` and the structured-field
  extraction, with **no blind character slicing** — a chunk that has no
  extractable structure is dropped, not truncated mid-sentence.
- `sources` cite **`sync_id`, never the integer `id`.** Verified: id `1918` in
  Hermes' replica is a session summary while id `1918` in the MCP-reached store
  is an unrelated cloud-sync investigation. Integer IDs do not resolve across
  replicas. This endpoint must not propagate that bug into a new API.
- **No LLM call inside the endpoint.** Assembly is deterministic string work.
  The synthesis stays in Hermes' LLM, which is going to run anyway.

### Calibrated "I don't have this"

`confidence` is derived, not guessed, from signals the pipeline already computes:

- `none` — zero hits above the adaptive floor, or the FTS relaxation ladder had
  to reach step 2 (OR-of-terms) to return anything. `SearchDiag`
  (`internal/store/store.go:229`) already reports this and is currently thrown
  away by every caller.
- `low` — hits exist but the top result's fused score is below a calibrated
  threshold, **or** `recall_degraded` is true.
- `high` — otherwise.

Hermes then says "no tengo eso registrado" on `none` instead of letting the LLM
fill the hole. This is the **absence** question kind from the brief, and it is
answerable today because the signal already exists and is simply not surfaced.

The threshold must be **calibrated against the eval corpus**, not picked. The
existing floors (0.35/0.25) are a cautionary tale: they were inherited from a
different embedding model and starved recall until issue #83 caught it.

### Measurement

- **Before:** on the absence-class corpus, how often does today's `/search`
  return something confident-looking for a question with no evidence? (Expect:
  always — there is no confidence signal at all.)
- **After:** false-confidence rate on the absence class. Target ≤ 0.1.
- **False-refusal rate** on the identity/status classes must stay ≤ 0.05 —
  a memory system that says "no sé" when it does know is a worse product than
  one that guesses.
- **Consumer simplification:** `summarizeOmniaMemories` deleted from Hermes.
  That deletion is the proof the abstraction landed in the right place.

---

## P4 — Claim lifecycle: open items that can close

**Serves:** Goal 2. **Effort:** L (2–3 weeks). **Risk:** high.

### The problem

The most *dangerous* failure class in the brief. Identity questions fail by
retrieving nothing. Open-item questions fail by retrieving something
**confidently obsolete** — an 11-day-old `DO NOT` order read aloud as current.
The store has no lifecycle for the claims inside a memory.

### The design call the brief asked for

**Model open items as first-class rows; do not extend the write gate.**

Reasoning: the write gate operates on whole observations and on *factual
contradiction*, raising `judgment_required` with `supersedes`/`conflicts_with`.
"This task is now done" is neither — a completed task does not contradict the
memory that recorded it, and superseding the whole observation would destroy the
delta-log record that the task once existed. Those are different relations over
different granularities. Overloading one mechanism onto both would make the
coding-agent conflict flow noisier, which violates the do-not-regress
constraint.

New table `claims`:

```
claim_id, observation_sync_id, text, kind (todo|blocker|prohibition|question),
state (open|done|abandoned|expired), opened_at, closed_at,
closed_by_observation_sync_id, confidence
```

- **Extraction** at write time from the shapes already used in practice:
  `## Next Steps` blocks, `🔲` / `- [ ]` items, `**DO NOT**` prohibitions. Purely
  structural, no LLM. Extraction confidence is stored, and low-confidence
  extractions never surface as authoritative.
- **Closing** is the hard part and it must be **evidence-driven**, not inferred:
  a later observation whose content semantically matches an open claim raises a
  `claim_closure_suggested` candidate through the existing `mem_judge`
  surfacing loop. The agent confirms; the store never auto-closes. An
  auto-closing store that is wrong is exactly as dangerous as the never-closing
  store we have now, just quieter.
- **Expiry** is the honest fallback: a claim with no touch in *N* days becomes
  `expired`, not `done`. Retrieval reports "recorded 47 days ago, never
  confirmed" — which is *true*, and lets the voice agent hedge correctly instead
  of asserting.
- Retrieval for `open_items` intent queries the `claims` table with
  `state='open'`, ordered by `opened_at`, **not** the observation corpus.

### Why identity and open-items are not "the same missing concept"

The brief suggests solving them once. They share a symptom (nothing expires) but
not a solution. Identity is fixed by **grounding in an external source of truth
that has its own update mechanism** (P1: the repo). Open items have no external
source of truth — nothing outside Omnia knows the task got done — so they need
an **internal lifecycle**. Merging them would force identity to depend on an
inference mechanism when a quoted document was available, which is the exact
failure mode the brief warns about. Solve them twice, deliberately.

### Measurement

- **Before:** on the open-items corpus, measure the **obsolescence rate** — how
  often the top result asserts something already completed. Score by hand
  against git/PR history. This number does not exist today and is the whole
  justification for a 2–3 week item; if it comes back low, **defer P4 below P5**.
- **After:** obsolescence rate; extraction precision/recall against hand-labelled
  claims; false-close rate (a claim marked done that was not). False-close must
  be ≈ 0 — a wrongly-closed blocker is worse than the status quo.

---

## P5 — Cross-project retrieval

**Serves:** Goal 2. **Effort:** S–M (~3–4 days). **Risk:** low.

### The problem

"What am I blocked on across everything" is structurally unaskable. Note this is
**half a consumer problem**: `mem_search` already has `all_projects`
(`internal/mcp/mcp.go:1342`), and `GET /search` with no `project` param already
searches globally. Hermes' `detectProject` latches onto one project per turn and
never releases it.

### The change

1. Expose `all_projects=1` and a repeated `project=` param on `GET /search` and
   `/answer` (P0 already widens this handler).
2. **Per-project score normalization** before the final merge, so the most
   active project does not dominate. Take top-N per project, min-max normalize
   within each project, then merge. `MinMaxNormalizeRelevance`
   (`internal/mcp/recall_ranking.go:247`) is already exported and already does
   exactly this arithmetic over a batch — apply it per group.
3. `internal/embed`'s `ScopedSearcher.SearchScoped` already computes semantic
   top-k *within* project (bugfix #1436) — call it once per project rather than
   falling back to a global top-k that a large project would crowd.
4. Hermes-side: P2's `cross_project` intent releases the project latch for that
   turn only.

### Measurement

- **Before:** cross-project corpus accuracy (expect ~0 — the query cannot be
  expressed).
- **After:** accuracy, plus **project diversity in the top-4**: for a
  genuinely cross-project question, results must span ≥ 2 projects. Single-project
  domination is the specific failure this item exists to prevent, so measure it
  directly rather than trusting the normalization.
- **Latency:** N parallel scoped searches. Budget +15 ms at N=5; it is I/O-bound
  on the same local SQLite, so verify rather than assume.

---

## P6 — Query embedding cache

**Serves:** neither goal directly — latency only. **Effort:** S (1–2 days).
**Risk:** none.

The brief is right that this is not where perceived latency lives (84 ms total,
dominated by the network hop to Ollama at `192.168.100.10`). Included only
because it is nearly free: an LRU keyed on the normalized query string, in the
existing `internal/embed` client, ~200 entries.

**Measure:** cache hit rate over a week of real voice traffic, and `omniaMs`
p50/p95. If the hit rate is under ~20%, delete the cache — a voice assistant may
simply never repeat a query verbatim, and dead code that looks like an
optimization is worse than no optimization.

Do **not** pursue a smaller in-process embedder yet. It changes the cosine
distribution, which would invalidate the 0.35/0.25 floors, the P3 confidence
threshold, and every baseline in this plan. Revisit only after the harness is
established.

---

## Operational items (do these, they are cheap)

Surfaced by `omnia doctor`, status **blocked**:

1. **`sync_mutation_required_fields` (blocked, 43 findings)** — session payloads
   missing `directory`, blocking cloud replication. Fix first: while replication
   is blocked, every measurement above is taken on a replica that is drifting
   from the one Vel reads. This corrupts the plan's own evidence. **Effort: S.**
2. **`embedding_lag` (warning)** — P0's degradation envelope makes this visible
   to Vel; the doctor's own wording ("recall silently narrows") is the argument.
   Also add a **hard** doctor threshold: lag > 100 observations should be an
   error, not a warning.
3. **`store_exposure` (warning)** — data dir is `0755`. One `chmod 0700` plus a
   setup-path fix. **Effort: XS.**

---

## What should NOT be built

Stated explicitly, as the brief asked.

- **LLM consolidation as the answer to "what is X".** `internal/consolidate`
  already exists — and it feeds the LLM **only the observation titles**
  (`consolidate.go`: `parts = append(parts, n.Title)`), not their content, then
  writes the result as an authoritative `digest` (importance weight 3, the top
  tier). That is a confidently-wrong-definition generator pointed at the highest
  ranking tier. Do not build on it for identity, and consider gating `digest`'s
  importance weight until it is grounded. A quoted README (P1) cannot be
  confidently wrong in this way; a derived definition can.
- **Hand-written identity memories.** Already rejected by the user, and the
  reasoning is correct: it does not scale and it is not understanding. P1 is the
  version that respects the distinction — access to the evidence, not a curated
  answer.
- **Reviving `omnia cartridge` for this.** It digests top *memories*, which are
  deltas. It would warm-start the wrong corpus. Leave it disabled.
- **Extending `collect -source github` to read repository files.** It is built
  around the GitHub API's issues/PRs/discussions model. A local `repodoc` source
  reads the working tree directly — no API, no rate limit, no token, and it
  works for private and unpushed repos. Local-first is a stated constraint.
- **Rewriting historical inter-memory references from `id` to `sync_id`.** The
  references live in free prose (`see #2207`). A regex rewrite over ~2058
  observations would corrupt real content — `#2207` is not always a memory
  reference. Emit `sync_id` in all **new** surfaces (P3), and leave history
  alone.
- **Any LLM call inside the retrieval path.** It cannot be justified against a
  100 ms budget. P2 and P3 are both designed to avoid needing one.
- **A new embedding model, now.** See P6.

---

## Ranked summary

| # | Item | Goal | Effort | Impact | Gate |
|---|---|---|---|---|---|
| — | Conversational eval corpus | both | S | prerequisite | — |
| — | `sync_mutation_required_fields` fix | — | S | prerequisite (measurement validity) | — |
| P0 | `GET /search` on the full pipeline | 2 | S–M | **very high** | latency < 5 ms added |
| P1 | `repodoc` ingestion | **1** | M | **very high** | no coding-agent regression |
| P2 | Intent classification + routing | 2 | M | high | classifier precision ≥ 0.9 |
| P3 | `/answer` + calibrated confidence | 2 | M | high | false-refusal ≤ 0.05 |
| P4 | Claim lifecycle | 2 | L | high but expensive | measure obsolescence rate first |
| P5 | Cross-project retrieval | 2 | S–M | medium | project diversity ≥ 2 in top-4 |
| P6 | Query embedding cache | — | S | low | delete if hit rate < 20% |

**P0 + P1 are the plan.** P0 is mostly wiring that unlocks machinery already
built and tested; P1 is the only item that puts "what a project IS" into the
store in a form that stays true because the repo stays true. P2 and P3 make
those two reachable from a voice turn. P4 is the most dangerous problem and the
most expensive fix — measure the obsolescence rate before committing three
weeks to it.
