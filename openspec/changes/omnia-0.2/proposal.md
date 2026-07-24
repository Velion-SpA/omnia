# Proposal: Omnia 0.2 — the local-first memory compiler (EPIC)

> **Type**: master / epic planning proposal. This is NOT a single implementable change.
> It defines the 0.2 vision, organizes all researched improvements under 4 new primitives,
> and slices them into dependency-ordered sub-changes (each becomes its own SDD change).
> It intentionally exceeds the 450-word single-change budget.

## Intent

Omnia today is a competent local-first RAG memory: brute-force cosine over jina-768d in SQLite, FTS5+BM25 conflict detection, `mem_judge` relations, a bugfix `outcome` field, and a recall@10 eval. That is table-stakes — every cloud vendor (Mem0/Zep/Letta/Cognee) and both hyperscalers now ship comparable retrieval. The research corpus (#1563 and 13 briefs) surfaces one consistent gap the whole market shares: **remembering is not applying, is not forgetting, is not cheap.** TRACE shows Mem0-class memory re-violates 57.5% of corrected preferences; no vendor compiles memory into enforcement, anchors it to code, forgets it provably, or runs consolidation privately on-device.

Omnia 0.2 reframes the product from "a store you query" to **"a compiler that turns memory into things an agent must obey, load cheaply, and can trust."** North-star (ambition, not 0.2 scope): the local-first memory compiler for the agent ecosystem, consumed by any agent/IDE over MCP.

## Scope

### In scope (0.2 as a planning envelope)
- Ship all of **Horizon 1** (foundation: eval, ranking, embeddings, receipts, provenance) end-to-end.
- Ship the leading edge of **Horizon 2**: at least one flagship differentiator (assumed: structural forgetting) proven end-to-end.
- Establish the 4 primitives as the organizing spine and the slicing roadmap below.

### Out of scope / explicitly deferred
- **Horizon 3 (the moat)** — the local subconscious daemon and full cartridges — is stated as direction; committed 0.2 delivery is a Benja decision (Q1).
- **SQLite driver swap** (`ncruces`) and vector-index migration — deferred until brute-force actually hits its documented ceiling; Matryoshka truncation is the H1 cost lever instead.
- **Parametric / in-weights memory** — verdict (#1575): never ready to replace the store; only the precompute-once/load-cheap pattern is adopted (as cartridges).
- **Bundled models & direct LLM API keys** — reuse the shipped `internal/llm/` AgentRunner shell-out; Omnia ships no model.
- **Multi-agent blackboard / PAM interchange / neutral Memory-MCP facade** — ecosystem lane deferred (Q7).

## The 4 primitives (the spine)

The 5 apuestas / 15 jugadas A–O are implementations OF these; each primitive absorbs several.

### P1 — Living memory / structural forgetting
- **What**: memory anchored to AST nodes (tree-sitter), travelling with refactors via git blame, and **auto-invalidating when the code it explains materially changes**. Forgetting is code-diff-driven, not timer-driven.
- **Why new**: the identified market whitespace (#1574) — nobody links decision/bugfix memory to a deterministic code graph. Bi-temporal validity (Zep) and Ebbinghaus decay become *consequences of code change*, not clocks.
- **Absorbs**: apuesta 1 (code-graph↔decisions), bi-temporal edges, forgetting/decay.
- **Packages**: new `internal/source` code-graph, `internal/store`, `internal/embed`.

### P2 — The memory compiler
- **What**: memory is a SOURCE that compiles to three artifacts — (a) **guardrails**: runtime enforcement checks (fixes TRACE's 57.5% re-violation gap); (b) a session **cartridge**: cheap hot context loaded at SessionStart; (c) verifiable **playbooks**: two-sided procedures from bugfix outcomes.
- **Why new**: closes remembering≠applying; no coding-agent product compiles memory into gates (#1574). Programmatic/verifiable playbooks beat prose +11.3% (ASI, #1578).
- **Absorbs**: apuesta 2 (enforcement/TRACE), apuesta 4 (procedural slot / ReasoningBank), cartridge/precompute (Cartridges/KV, #1575).
- **Packages**: `internal/mcp` (hooks/injection), `internal/llm`, claude-code plugin (thin injection point).

### P3 — The context economy
- **What**: injection is a budgeted market — each memory has a token **price** and a locally-learned value **bid**; a knapsack solver fills a hard token budget; every recall returns an explainable **receipt**; the whole thing is optimized against quality-per-token.
- **Why new**: context rot means over-injection actively harms (#1579/#1583); no vendor has a token-cost-normalized metric (#1584). Turns "inject by recency/category" into a measured market.
- **Absorbs**: apuesta 5 (eval harness), gating/injection-budget, MMR diversity, learning-to-rank, `mem_search --explain`.
- **Packages**: `internal/embed`, `internal/store`, `internal/mcp`.

### P4 — The local subconscious
- **What**: an on-device idle-time tier where expensive intelligence runs — consolidate episodic→semantic, induce playbooks from bugfix outcomes, re-embed with better models, invalidate stale code-anchored memory, run the eval regression, precompute cartridges.
- **Why new**: the moat the cloud structurally cannot copy — private + free idle compute (#1571 sleep-time, #1575 precompute-once). It is where P1–P3 get maintained.
- **Absorbs**: apuesta 3 (sleep/consolidation), eval regression, re-embed, cartridge build.
- **Packages**: new `internal/subconscious` daemon, `internal/embed`, `internal/store`, `internal/cloud`.

### Cross-cutting substrate — verifiable provenance
Every memory carries write-time source/trust tags and is signed/traceable; forgetting is provable (real row-level deletion + tombstone + audit log), and encrypt-at-rest is default. Underlies security + trust and is the honest local-first story (#1585/#1589/#1590). Packages: `internal/store`, `internal/audit`.

## Slicing roadmap (each row = a future SDD change)

Sizes S/M/L. Deps reference slice IDs. Overlap = existing `openspec/changes/*`.

### Horizon 1 — foundation (cheap, reuses infra, gives measurable numbers)
| ID | Intent (one line) | Primitive | Size | Deps | Overlap |
|----|-------------------|-----------|------|------|---------|
| `omnia-eval-harness` | Token-cost-normalized quality-per-1k-token eval + adversarial staleness/conflict suite, versioned CI gate | P3 | M | — | extends recall-eval (#1401/#1521); consumes `mem_judge` relations (memory-conflict-* shipped) |
| `omnia-recall-ranking` | Add recency×importance signal to `Store.Search` beyond raw cosine | P3 | S | eval-harness | `internal/embed/store.go` brute-force path |
| `omnia-embed-matryoshka` | Benchmark + adopt EmbeddingGemma-300m native Matryoshka; dim-truncation as cost lever | P3/substrate | M | eval-harness | `internal/embed` (jina-v2-es today, no MRL) |
| `omnia-recall-receipts` | `mem_search --explain` receipt: per-memory score breakdown / why surfaced | P3 | S | recall-ranking | `internal/mcp` |
| `omnia-provenance-foundation` | Write-time source/trust tagging + memory audit log (read/write/delete + trust + session) | Provenance | M | — | reuse cloud-sync-audit pattern; `internal/store`, `internal/audit` |

### Horizon 2 — differentiators
| ID | Intent | Primitive | Size | Deps | Overlap |
|----|--------|-----------|------|------|---------|
| `omnia-injection-market` | Budgeted injection: token price + learned value bid + knapsack under hard budget + MMR diversity | P3 | M | recall-ranking, recall-receipts | — |
| `omnia-code-graph` | Pure-Go tree-sitter code-structure graph (symbol/call/import) in SQLite, content-hash incremental sync | P1 | L | — | `internal/source`; **RISK: cgo-free tree-sitter** |
| `omnia-memory-code-anchor` | Anchor decision/bugfix memories to code-graph nodes; git-blame travel across refactors (THE whitespace) | P1 | M | code-graph | — |
| `omnia-structural-forgetting` | Auto-invalidate/decay anchored memory when the anchored node materially changes (code-diff-driven) | P1 | M | memory-code-anchor | memory-conflict-semantic staleness / bi-temporal |
| `omnia-procedural-slot` | Two-sided playbook/anti-playbook procedural type induced from bugfix `outcome` | P2 | M | — (uses existing outcome field) | ReasoningBank shape (#1578) |
| `omnia-compiler-guardrails` | Compile corrections/preferences into runtime enforcement checks (TRACE), surfaced at PreToolUse/lint | P2 | L | procedural-slot, provenance-foundation | claude-code plugin |
| `omnia-provable-forgetting` | Real row-level deletion + tombstone + encrypt-at-rest default | Provenance | M | provenance-foundation | `internal/store` |

### Horizon 3 — moat
| ID | Intent | Primitive | Size | Deps | Overlap |
|----|--------|-----------|------|------|---------|
| `omnia-session-cartridge` | Precompute-once / load-cheap project session cartridge at SessionStart | P2 | M | injection-market | `internal/mcp` session hooks |
| `omnia-local-subconscious` | On-device idle daemon: consolidation, playbook induction, re-embed, stale-anchor invalidation, eval regression, cartridge precompute | P4 | L | structural-forgetting, procedural-slot, eval-harness, session-cartridge | new `internal/subconscious` |
| `omnia-vector-index` | Migrate past brute-force cosine: sqlite-vec (ncruces) or coder/hnsw; cloud pgvector fanout | P3/substrate | L | embed-matryoshka, eval-harness | memory-conflict-semantic **Phase 5** `FindCandidates`; integrate-engram-cloud (cloud side) |

## Reconciliation with in-flight work (do not duplicate)
- **memory-conflict-semantic (Phase 4, active)** — owns LLM-judge over FTS5 candidates and explicitly reserves embedding candidate generation for its **Phase 5** via the single `FindCandidates` seam. `omnia-vector-index` and `omnia-structural-forgetting` must extend that seam, not fork it. Its `internal/llm/` AgentRunner is reused by the subconscious.
- **memory-conflict-{surfacing,audit,surfacing-cloud-sync} (shipped/active)** — the `mem_judge` relation substrate (conflicts_with/supersedes/compatible/scoped/related) is the input to the eval harness (staleness suite) and to structural forgetting; reuse, don't re-model.
- **integrate-engram-cloud (active)** — the cloud auth/autosync/dashboard seam; `omnia-vector-index` cloud pgvector fanout and any subconscious→cloud consolidation ride on it.
- **cloud-dashboard-* (active)** — the surface where receipts, eval frontiers, and subconscious activity are visualized; new dashboard panels slot into it.
- **doctor-* (active)** — orthogonal (operational diagnostics); no overlap.

## Affected areas
| Area | Impact | Description |
|------|--------|-------------|
| `internal/embed` | Modified | ranking signal, EmbeddingGemma/Matryoshka, re-embed, vector index |
| `internal/store` | Modified | code-anchoring, structural forgetting, procedural type, provenance, consolidation |
| `internal/mcp` | Modified | receipts (`--explain`), injection market, cartridge load, enforcement hooks |
| `internal/llm` | Modified | reuse AgentRunner for subconscious LLM-grade work, guardrail compilation |
| `internal/source` | New substrate | tree-sitter code-graph + git-blame anchoring |
| `internal/subconscious` | New | idle-time daemon orchestrating P4 jobs |
| `internal/audit` | Modified | memory read/write/delete provenance log |
| `internal/cloud` | Modified | pgvector fanout, consolidation sync |
| claude-code plugin | Modified (thin) | guardrail/cartridge injection at SessionStart/PreToolUse |

## Risks
| Risk | Likelihood | Mitigation |
|------|------------|------------|
| cgo-free tree-sitter path may not exist / be immature (breaks CGO_ENABLED=0) | High | Prototype spike first; fallback = file+symbol+git-blame anchor without full AST graph (Q3) |
| Epic sprawl — 15 slices, unclear 0.2 release boundary | High | Q1 forces an explicit 0.2 cut; H1 ships independently and is self-justifying |
| Enforcement runtime has no home (who executes guardrails?) | Med | Q4 decides own-runtime vs emit-checks-others-consume; start as PreToolUse hook |
| Subconscious battery/CPU cost + consent | Med | Idle-only, opt-in, reuse AgentRunner (no bundled model); Q5 |
| Overbuilding P4 before P1–P3 prove value | Med | Horizon ordering enforces it; P4 depends on P1–P3 slices |
| Structural forgetting deletes memory a dev still wanted | Med | Provable-forgetting keeps tombstones; invalidate = supersede/decay, not hard-delete by default |

## Rollback plan
Epic is a planning artifact — no code. Each sub-change carries its own rollback. All slices are additive and feature-flagged (matching the `--semantic` opt-in precedent from memory-conflict-semantic). Abandoning 0.2 = not starting the next slice; nothing shipped needs reverting.

## Success criteria (0.2 as a whole)
- [ ] A versioned, reproducible token-cost-normalized eval harness exists and gates releases (quality-per-1k-token Pareto + staleness/conflict suite).
- [ ] Recall demonstrably improves quality-per-token vs the 0.1 cosine baseline on that harness (not just recall@k).
- [ ] Every recall can produce a receipt explaining why each memory was injected.
- [ ] At least one flagship differentiator (assumed structural forgetting) works end-to-end: a memory auto-invalidates when its anchored code materially changes.
- [ ] Provenance substrate: every memory is trust-tagged, deletion is provable (row-level + tombstone + audit).
- [ ] No regression: `go test ./...` green; CGO_ENABLED=0 preserved; local-first defaults unchanged when cloud unconfigured.

## Proposal question round — open product questions for Benja
> Delegated sub-agent cannot ask interactively; these are returned verbatim for orchestrator/user review.
> Assumptions made (correct any): 0.2 = full H1 + one flagship H2 differentiator; flagship = structural forgetting; provenance foundation lands in H1; no SQLite driver swap in 0.2; code-graph first ships over a cheap file+symbol+git-blame anchor; subconscious reuses the AgentRunner shell-out.

1. **0.2 release boundary** — Is 0.2 = "prove the context economy" (H1 only), or H1 + one flagship H2 differentiator, or the whole H1+H2? Where exactly does 0.2 end and 0.3 begin?
2. **Flagship differentiator** — Which single primitive leads the Hugo/market pitch: living memory (structural forgetting), the memory compiler (enforcement), or the local subconscious (moat)? You cannot headline all four.
3. **Code-graph depth** — Full tree-sitter AST graph in 0.2 (L, cgo-free risk), or ship structural forgetting first over a cheaper file+symbol+git-blame anchor and defer the real graph?
4. **Enforcement boundary** — Does Omnia own an enforcement runtime, or does the memory compiler only *emit* checks that other tools (Claude Code hooks, linters) consume? This is a product-surface decision, not just implementation.
5. **Subconscious compute model** — Idle daemon needs LLM-grade work: hard-require a local model (Ollama), shell out to the user's agent CLI (like `--semantic` already does), or stay cloud-optional? Acceptable battery/CPU cost, and explicit opt-in?
6. **Provenance positioning** — Is encrypt-at-rest + provable deletion a *marketed* 0.2 headline (compliance/trust story) or a quiet substrate? This decides whether provenance is H1-visible or H3-background.
7. **Ecosystem timing** — North-star is "consumed by any agent via MCP." Do we invest in the neutral Memory-MCP facade / PAM interchange in 0.2, or keep 0.2 Omnia-internal and defer the interop lane?
8. **Eval harness publish** — Publish the token-cost-normalized eval + numbers openly (first-mover differentiation, invites scrutiny) or keep it internal QA for 0.2?
