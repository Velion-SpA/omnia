# Design: omnia-procedural-memory (two-sided procedural memory: playbook + anti-playbook)

**Change**: omnia-procedural-memory
**Artifact store**: hybrid (engram + openspec)
**Primitive**: Apuesta 4 — the procedural (CoALA) slot Omnia lacks (0.2, novel)
**Grounding**: repo `/Users/benja/Documents/01.- Velion/omnia`, module `github.com/velion/omnia`. Proposal artifact absent; grounded on locked scope #1598/#1592 + research #1578.

---

## Technical Approach

Promote the shipped bugfix `outcome` signal (`worked`/`did_not_work`, `internal/store/signature.go`) from a per-row ranking flag into a first-class, retrievable **procedural object** carrying a **polarity**: `worked → playbook` (steps-to-follow), `did_not_work → anti_playbook` (guardrail, steps-to-avoid). A procedure is a **verifiable parameterized program**, not prose (ASI: programmatic > prose, +11.3%): trigger/condition, ordered slot-templated steps, expected outcome, a machine-checkable postcondition, confidence, and state. It lives in a NEW local-first `procedures` table owned by `internal/store` — additive `CREATE TABLE IF NOT EXISTS`, `sync_id`-keyed, mirroring the `memory_relations`/`memory_anchors` precedent. Induction shells to the shipped `internal/llm` runner (same boundary as the semantic-conflict `AgentRunner.Compare` path): **online** after `mem_save` records an outcome, and **offline** via a batch pass over historical outcome-tagged bugfixes that rides the reserved `ScanProject` worker-pool shape. A governance gate (SSGM) keeps induced procedures `candidate` until N confirmed reuses promote them to `trusted`; ExpeL UPVOTE/DOWNVOTE moves confidence, floor-hit auto-retires (never hard-deletes). Retrieval surfaces a **contrastive pair** — nearest trusted playbook + nearest trusted anti-playbook — at the `internal/mcp` wiring boundary; `internal/recall` stays pure. cgo-free throughout; gated by `procedural.enabled`.

---

## Architecture Decisions

### Decision: distinct `procedures` table, NOT a new `type=procedure` observation
| Option | Tradeoff | Decision |
|---|---|---|
| New `procedures` table (typed: polarity, steps JSON, postcondition, confidence, state) | Governance + contrastive queries in SQL; keeps episodic space clean; "programmatic > prose" at the STORAGE layer | **Chosen** |
| Observation `type=procedure` (JSON stuffed in `content`) | Free FTS/sync reuse, but polarity/confidence/state are unqueryable and it pollutes episodic recall | Rejected (reserve the `procedure` type label for display only) |

**Rationale**: a procedure is a program, not prose — it needs typed columns for the governance gate and for contrastive retrieval. Mirrors `memory_anchors`; retrieval reuses recall via a companion `procedures_fts` external-content FTS5 table over `trigger + steps_summary` (same pattern as `observations`/`observations_fts`).

### Decision: induction reuses the `internal/llm` shell-out via a new `ProcedureInducer` (do NOT touch `Compare`)
| Option | Tradeoff | Decision |
|---|---|---|
| Add `ProcedureInducer.Induce(ctx,prompt)(InducedProcedure,error)` implemented by the same shell-out runners | Keeps `internal/llm` the single external-LLM boundary; leaves the locked `AgentRunner.Compare` vocabulary untouched | **Chosen** |
| Overload `AgentRunner.Compare` | Breaks its single-purpose Verdict contract | Rejected |
| Deterministic template extraction (no LLM) | Free but cannot generalize a trajectory into parameterized steps | Rejected as primary (kept as degraded fallback) |

**Rationale**: mirrors the shipped shell-out precedent (`internal/store/relations.go` → `Runner.Compare`); `worked→playbook`, `did_not_work→anti_playbook` mapping is set by the caller from the source `outcome`, not by the model.

### Decision: governance = confidence + state machine on `procedures`, system-provenance (mirror `JudgeBySemantic`)
`candidate → trusted` at `procedural.trust_threshold` confirmed reuses (default 3); UPVOTE bumps confidence + `reuse_confirmed` on a worked reuse, DOWNVOTE decays on contradiction; unused `trusted` procedures decay via the existing `review_after` spaced-repetition field; at `procedural.confidence_floor` they auto-`retired` (never deleted). Only `trusted` procedures auto-inject as steps; `candidate` ones surface only as a labeled "unverified suggestion". Terminal writes (`ConfirmReuse`/`Contradict`/`RetireProcedure`) carry system/agent provenance exactly like `JudgeBySemantic`.

### Decision: online reuse feedback via explicit `applied_procedure` ref (explicit-or-derived, mirrors `error_signature`)
`mem_save`/`mem_update` gain optional `applied_procedure` (a procedure `sync_id`). When set alongside `outcome`, `worked → ConfirmReuse`, `did_not_work → Contradict` — deterministic attribution. Absent → best-effort trigger-signature match in the same session. This is the SSGM confirmation loop.

### Decision: contrastive retrieval at the mcp boundary; `internal/recall` stays pure
`recall.Fuse` is reused to rank `procedures_fts` lexical (+ optional trigger embedding) hits; the contrastive PAIRING (top trusted playbook + top trusted anti-playbook → one "procedure card" on the recall receipt) happens in `internal/mcp`, the same seam structural-forgetting uses for its penalty/receipt. Keeps `internal/recall` a dependency-free leaf.

### Decision: local-only this slice (not in the sync payload)
Mirrors the recall-reliability signature lane ("does not touch `internal/cloud/cloudstore` or the sync payload"). Cross-machine procedure sync is deferred to keep the slice lean and local-first.

---

## Data Flow

```
mem_save(type=bugfix, outcome) ─► handleSave (internal/mcp)
      └─ async, best-effort (never fails save; like FindCandidates):
         enqueue induction ─► ProcedureInducer.Induce (shell internal/llm)
              worked→playbook / did_not_work→anti_playbook
              └─► Store.UpsertProcedure(state=candidate, confidence=seed)

omnia procedure-induct [--project][--apply]  (offline batch)
   observations WHERE outcome IS NOT NULL AND bugfix-family, cluster by signature
      └─► ScanProject-shaped worker pool ─► Induce ─► UpsertProcedure(candidate)

mem_save/mem_update(outcome, applied_procedure=sync_id)  (SSGM feedback)
      worked      ─► Store.ConfirmReuse  (UPVOTE: +confidence,+reuse; ≥N → trusted)
      did_not_work─► Store.Contradict    (DOWNVOTE: -confidence; ≤floor → retired)

mem_search / mem_context ─► recall.Fuse (pure, over procedures_fts)
      └─ internal/mcp: pick top trusted playbook + top trusted anti-playbook
         └─► "procedure card": DO <playbook.steps> / AVOID <anti.steps>
                              + postcondition + confidence receipt
mem_review ─► surfaces review_after procedures: "still useful? [keep/retire]"
```

---

## File Changes

| File | Action | Description |
|---|---|---|
| `internal/store/store.go` | Modify | `procedures` + `procedures_fts` tables + indexes in `migrate()` (additive `CREATE TABLE IF NOT EXISTS`) |
| `internal/store/procedures.go` | Create | `Procedure`/`InducedProcedure` types; `UpsertProcedure`, `ListProcedures`, `SearchProcedures`, `ConfirmReuse`, `Contradict`, `RetireProcedure`, `DecayProcedures` (system provenance) |
| `internal/store/procedures_test.go` | Create | Real SQLite: upsert, candidate→trusted at threshold, UPVOTE/DOWNVOTE, floor-retire idempotency, `review_after` set, FTS retrieval |
| `internal/store/relations.go` | Modify | Add `InduceProject` (or `ScanOptions.Mode=induct`) reusing the worker pool + `ProcedureInducer`; extend result counters |
| `internal/store/scan_procedure_test.go` | Create | Fake inducer: batch counters, polarity mapping, cap isolation |
| `internal/llm/procedure.go` | Create | `ProcedureInducer` interface + `InducedProcedure`; runners implement `Induce` by shell-out |
| `internal/llm/procedure_test.go` | Create | Prompt build + JSON parse via fake CLI; malformed-JSON/unknown-polarity errors |
| `internal/mcp/mcp.go` | Modify | `mem_save`/`mem_update` gain `applied_procedure`; async candidate induction on outcome; SSGM `ConfirmReuse`/`Contradict`; recall attaches contrastive procedure card + receipt |
| `internal/mcp/mcp_test.go` | Modify | outcome→candidate induced; reuse→trusted; contradiction→retire; contrastive card present; no-outcome save unchanged |
| `cmd/omnia/procedure.go` | Create | `omnia procedure-induct` (batch) + `omnia procedure list/inspect/retire` |
| `cmd/omnia/procedure_test.go` | Create | flag parsing, dry-run vs `--apply`, no-LLM graceful skip |
| `internal/config/*` | Modify | `procedural.enabled` (default false), `trust_threshold` (3), `confidence_floor`, decay window |
| `docs/PROCEDURAL_MEMORY.md` | Create | object schema, polarity, governance lifecycle, retrieval card, forward-compat note to the deferred compiler |

No deletions. No destructive migration. cgo-free (shell-out only).

---

## Interfaces / Contracts

```go
// internal/store — procedures (local-only this slice)
// id, sync_id UNIQUE, project, scope,
// polarity ('playbook'|'anti_playbook'),
// trigger, steps (JSON: [{order, template, slots[]}]), expected_outcome,
// postcondition_kind ('tests_pass'|'lint_clean'|'build_green'|'custom'), postcondition_expr,
// confidence REAL, state ('candidate'|'trusted'|'retired'),
// reuse_confirmed INT, contradicted_count INT,
// source_obs_sync_ids (JSON), induced_by_actor/kind/model,
// created_at, updated_at, last_reused_at, review_after, retired_at
func (s *Store) UpsertProcedure(p Procedure) (string, error)
func (s *Store) SearchProcedures(query, polarity, state string, limit int) ([]Procedure, error)
func (s *Store) ConfirmReuse(syncID, actor string) (Procedure, error) // UPVOTE + maybe promote
func (s *Store) Contradict(syncID, actor string) (Procedure, error)   // DOWNVOTE + maybe retire

// internal/llm
type ProcedureInducer interface {
    Induce(ctx context.Context, prompt string) (InducedProcedure, error)
}
```

`mem_save`/`mem_update` and recall are backward-compatible: omit `applied_procedure` and disable `procedural.enabled` → behaviour identical to today.

**Distinct from siblings**: structural-forgetting invalidates a memory when *code* changes; this slice induces reusable programs from *outcomes* — orthogonal, but both ride the `ScanProject` seam and reuse `review_after` + system provenance. The postcondition field is authored to be **compiler-ready** for the deferred enforcement runtime (#1592), but this slice only STORES and RETRIEVES procedures — it never executes/enforces them.

---

## Testing Strategy (strict TDD active — `go test ./...`, `CGO_ENABLED=0`)

| Layer | What | Approach |
|---|---|---|
| Unit | induction prompt build + JSON parse; malformed/unknown-polarity errors | fake CLI runner |
| Store | upsert, candidate→trusted at N, UPVOTE/DOWNVOTE, floor-retire idempotency, `review_after` decay, FTS retrieval by trigger | real SQLite `:memory:` |
| Store | `InduceProject` batch counters, worked/did_not_work→polarity, cap | fake `ProcedureInducer` |
| MCP | outcome→candidate; `applied_procedure` worked→trusted / did_not_work→retire; contrastive card + receipt; no-outcome save unchanged | in-process harness |
| CLI | `procedure-induct` dry-run/apply; no-LLM skip; `list/inspect/retire` | runner injection |
| Regression | all Phase 3/4 conflict + signature-lane tests GREEN; cgo-free build GREEN | CI |

---

## Migration / Rollout

Additive `CREATE TABLE IF NOT EXISTS procedures` + `procedures_fts` in `migrate()`; no backfill, no schema break. Opt-in on both ends: nothing is induced unless an `outcome` is recorded, nothing auto-injects unless `procedural.enabled=true`. Rollout order, each landing GREEN independently: (1) `procedures` table + store methods; (2) `internal/llm` `ProcedureInducer`; (3) online candidate induction on `mem_save`; (4) SSGM `applied_procedure` feedback + promote/retire; (5) `procedure-induct` batch; (6) contrastive retrieval card + receipt.

---

## Open Questions

- [ ] Reuse attribution: is the explicit `applied_procedure` ref enough for the 0.2 demo, or is the same-session trigger-signature fallback required day one?
- [ ] Seed confidence + `trust_threshold` (default 3) and decay window need one empirical tuning pass against the live corpus.
- [ ] Should offline `procedure-induct` cluster by normalized `error_signature` (reuse `signature.go`) or by `topic_key`? Leaning signature — it already groups recurring bugs.
- [ ] Postcondition vocabulary: fixed enum vs free `custom_expr` — how much structure now vs. defer to the compiler slice.
- [ ] Local-only vs. syncing procedures: confirm deferring cross-machine sync is acceptable for 0.2.
