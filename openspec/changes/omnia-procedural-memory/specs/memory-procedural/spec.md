# Memory Procedural Specification

**Change**: omnia-procedural-memory | **Store**: hybrid | Design: `openspec/changes/omnia-procedural-memory/design.md` (#1598/#1592)

## Purpose

Promotes bugfix `outcome` (`worked`/`did_not_work`) into a two-sided verifiable procedure — `playbook` or `anti_playbook` — stored in a new local-first `procedures` table, governed by a candidate→trusted→retired state machine, and retrieved as a contrastive pair at the MCP boundary. Additive and opt-in; `internal/recall` stays pure.

## Domain: procedures-store (NEW)

### Requirement: Typed `procedures` table with FTS retrieval

The system MUST persist procedures in an additive, `sync_id`-keyed `procedures` table (`polarity`: `playbook`|`anti_playbook`; `state`: `candidate`|`trusted`|`retired`; ordered slot-templated `steps`; `postcondition_kind`: `tests_pass`|`lint_clean`|`build_green`|`custom`; `confidence`; reuse/contradiction counters) plus a companion `procedures_fts` index. `UpsertProcedure` MUST reject an unknown `polarity` or `postcondition_kind`.

#### Scenario: valid procedure is upserted and searchable
- GIVEN a procedure with `polarity=playbook`, a trigger, steps, `postcondition_kind=tests_pass`
- WHEN `UpsertProcedure` is called
- THEN a row with a `sync_id` is stored and `SearchProcedures` returns it by trigger match

## Domain: induction (NEW)

### Requirement: outcome-triggered online induction sets polarity from the source outcome

On `mem_save`/`mem_update` recording `outcome`, the system MUST asynchronously call `ProcedureInducer.Induce` and `UpsertProcedure(state=candidate)`: caller sets `worked→playbook`, `did_not_work→anti_playbook` (never the model). Induction failure or a missing LLM runner MUST NOT fail the originating save.

#### Scenario: worked outcome induces a candidate playbook
- GIVEN a bugfix save with `outcome=worked` and a fake inducer returning a valid program
- WHEN `mem_save` completes
- THEN a `candidate` `playbook` procedure is created, linked via `source_obs_sync_ids`

#### Scenario: induction skips gracefully without an LLM runner
- GIVEN no agent runner configured
- WHEN a save records `outcome=did_not_work`
- THEN the save succeeds, no procedure is created, no error surfaces

### Requirement: offline batch induction rides the `ScanProject` seam, clustered by signature

`omnia procedure-induct [--project][--apply]` MUST select outcome-tagged bugfix-family observations, cluster them by normalized `error_signature`, and process clusters through the `ScanProject` worker-pool shape, one `Induce`+`UpsertProcedure(candidate)` per cluster. Without `--apply` it MUST report counts only.

#### Scenario: batch induces one procedure per signature cluster
- GIVEN 3 `worked` observations sharing one `error_signature` and 2 unrelated `did_not_work` observations
- WHEN `procedure-induct --apply` runs
- THEN exactly 2 candidate procedures are created

## Domain: governance-gate (NEW)

### Requirement: candidate promotes to trusted at `trust_threshold`; contradiction decays and auto-retires

`ConfirmReuse` MUST increment `reuse_confirmed`/confidence; at `trust_threshold` (default 3), `state→trusted`. Only `trusted` procedures auto-inject; `candidate` ones surface as "unverified suggestion". `Contradict` MUST decrement confidence; at `confidence_floor`, `state→retired` (never hard-deleted). Unused `trusted` procedures decay via `review_after`.

#### Scenario: third confirmed reuse promotes to trusted
- GIVEN a candidate with `reuse_confirmed=2`, `trust_threshold=3`
- WHEN `ConfirmReuse` is called again
- THEN `reuse_confirmed=3` and `state=trusted`

#### Scenario: repeated contradiction retires without deleting
- GIVEN a trusted procedure one decay step above `confidence_floor`
- WHEN `Contradict` is called
- THEN `state=retired` is set and the row remains queryable

## Domain: reuse-attribution (NEW)

### Requirement: `applied_procedure` drives confirm/contradict; falls back to session-signature match

`mem_save`/`mem_update` MUST accept optional `applied_procedure` (a procedure `sync_id`): `outcome=worked`→`ConfirmReuse`, `did_not_work`→`Contradict`. When absent, the system MUST attempt a best-effort same-session `trigger`-vs-`error_signature` match; on no match, no governance action is taken and the save MUST NOT fail.

#### Scenario: explicit reference confirms a playbook
- GIVEN a trusted playbook and a save with `applied_procedure=<sync_id>`, `outcome=worked`
- WHEN the save completes
- THEN `ConfirmReuse` is invoked and its counters update

#### Scenario: no reference and no session match is a safe no-op
- GIVEN a save with `outcome=worked`, no `applied_procedure`, no session trigger match
- WHEN the save completes
- THEN no `ConfirmReuse`/`Contradict` call is made and the save succeeds unchanged

## Domain: contrastive-retrieval (NEW)

### Requirement: contrastive pair surfaced at the MCP boundary; recall stays pure

`recall.Fuse` MUST remain a pure ranking function over `procedures_fts` with no pairing logic. `internal/mcp` MUST select the top-ranked `trusted` playbook and top-ranked `trusted` anti-playbook and attach one combined procedure card (DO/AVOID + postcondition + confidence) to `mem_search`/`mem_context` responses.

#### Scenario: matching query returns both polarities
- GIVEN a trusted playbook and a trusted anti-playbook both matching a query
- WHEN `mem_search` runs
- THEN the response includes one procedure card naming both, with confidence values

#### Scenario: only one polarity is trusted
- GIVEN only a trusted playbook matches
- WHEN `mem_search` runs
- THEN the card includes the playbook only; no anti-playbook slot is fabricated

## Domain: backward-compatibility (NEW)

### Requirement: procedural memory is additive, opt-in, and local-only

Procedural memory MUST be gated by `procedural.enabled` (default `false`). With the flag off, or no `outcome`/`applied_procedure` supplied, `mem_save`, `mem_update`, `mem_search`, `mem_context` MUST behave identically to their pre-existing contracts. `procedures`/`procedures_fts` MUST NOT participate in the cloud sync payload this slice.

#### Scenario: disabled flag preserves existing behavior
- GIVEN `procedural.enabled=false`
- WHEN a bugfix save records `outcome=worked`
- THEN no procedure is induced and the response is unchanged from before this feature

#### Scenario: procedures excluded from sync payload
- GIVEN `procedural.enabled=true` and trusted procedures exist
- WHEN a cloud sync payload is built
- THEN no `procedures` rows appear in it
