# Procedural Memory (playbook / anti-playbook)

Procedural memory promotes a bugfix's recorded `outcome` (`worked` /
`did_not_work`) from a per-row ranking flag into a first-class, retrievable
**procedure**: a verifiable, parameterized program with a polarity —
`playbook` (steps to follow) or `anti_playbook` (steps to avoid) — governed
by a small trust state machine and surfaced as a contrastive pair at
recall time.

This feature is **additive and opt-in**. With `procedural.enabled: false`
(the default), nothing described below runs: `mem_save`, `mem_update`,
`mem_search`, and `mem_context` behave exactly as they did before this
feature existed.

## Enabling it

```yaml
# ~/.config/omnia/config.yaml
procedural:
  enabled: true
  trust_threshold: 3 # confirmed reuses to promote candidate -> trusted
  confidence_floor: 0.15 # confidence at/below which a procedure auto-retires
  review_after_days: 14 # spaced-repetition window for an unused trusted procedure
```

An LLM CLI (`claude` or `opencode`, selected via `OMNIA_AGENT_CLI`) is
optional. When configured, procedures are induced by the model. When
absent — or when the CLI errors — induction degrades to a deterministic,
verbatim-content fallback rather than failing. Induction (online or
offline) **never** fails the operation it's attached to.

## Polarity and the induction rule

Polarity is **always** derived by the caller from the source `outcome`,
never chosen by the model:

- `outcome=worked` → `playbook` (a program that reproducibly fixes this
  class of problem)
- `outcome=did_not_work` → `anti_playbook` (a guardrail: steps that were
  tried and made things worse or didn't help)

## Governance (SSGM: candidate → trusted → retired)

A newly induced procedure starts as `candidate` — a labeled "unverified
suggestion." Only `trusted` procedures auto-inject at retrieval.

- `ConfirmReuse` (UPVOTE): called when a procedure is applied again and the
  outcome is `worked`. Increments `reuse_confirmed` and bumps confidence.
  At `trust_threshold` confirmed reuses, the procedure promotes to
  `trusted`.
- `Contradict` (DOWNVOTE): called when a procedure is applied again and the
  outcome is `did_not_work`. Increments `contradicted_count` and decays
  confidence. At `confidence_floor`, the procedure auto-retires.
- `retired` procedures are **never hard-deleted** — the row stays queryable
  for audit, it's simply excluded from the contrastive retrieval card.
- Unused `trusted` procedures decay via their `review_after` deadline
  (spaced repetition) — an offline/administrative pass, not currently wired
  to a scheduled job in this slice.

## Online induction (`mem_save` / `mem_update`)

When a bugfix-family save or update records `outcome=worked` or
`outcome=did_not_work`, a candidate procedure is induced asynchronously
from the observation's title (trigger) and content (trajectory) and linked
to it via `source_obs_sync_ids`.

## Reuse attribution (`applied_procedure`)

`mem_save` and `mem_update` both accept an optional `applied_procedure`
argument — the `sync_id` of a procedure the agent applied before this
save/update's outcome:

```json
{
  "title": "Reused the slice-index guard playbook",
  "content": "Applied the stored steps; the fix worked again.",
  "type": "bugfix",
  "outcome": "worked",
  "applied_procedure": "proc-1a2b3c4d5e6f7890"
}
```

`outcome=worked` calls `ConfirmReuse`; `outcome=did_not_work` calls
`Contradict`. When `applied_procedure` is omitted, a best-effort
same-session match is attempted (a procedure induced from an observation
earlier in the same session, whose trigger text matches). If nothing
matches, this is a safe no-op — the save/update is unaffected either way.

## Offline batch induction (`omnia procedure-induct`)

For historical bugfix-family observations that already carry an
`error_signature` + `outcome` (recall reliability, #1399), a batch pass
clusters them by `(error_signature, outcome)` and induces one candidate
procedure per cluster:

```bash
omnia procedure-induct --project myproj              # dry-run: report counts only
omnia procedure-induct --project myproj --apply      # write induced candidates
omnia procedure-induct --project myproj --apply --concurrency 5 --max-clusters 100
```

Without `--apply`, only `observations_scanned` and `clusters_found` are
reported — nothing is written and no LLM call is made.

## Curation (`omnia procedure`)

```bash
omnia procedure list [--project P] [--polarity playbook|anti_playbook] [--state candidate|trusted|retired] [--limit N]
omnia procedure inspect <sync_id>
omnia procedure retire <sync_id>
```

## Contrastive-pair retrieval

`mem_search` attaches a `procedure_card` to its response when procedural
memory is enabled and at least one `trusted` procedure matches the query —
the top-ranked `trusted` playbook AND the top-ranked `trusted`
anti_playbook, paired together. If only one polarity has a trusted match,
the card contains only that polarity; the other side is never fabricated.

`mem_context` has no free-text query, so it surfaces the
most-recently-updated `trusted` procedure of each polarity for the current
project instead of a query match.

```json
{
  "procedure_card": {
    "playbook": {
      "sync_id": "proc-1a2b3c4d5e6f7890",
      "trigger": "slice index out of range in handler",
      "steps": ["add a length guard before indexing"],
      "postcondition_kind": "tests_pass",
      "confidence": 0.8
    },
    "anti_playbook": {
      "sync_id": "proc-90abcdef12345678",
      "trigger": "bare retry loop on a flaky assertion",
      "steps": ["retrying without backoff made the flake worse"],
      "postcondition_kind": "custom",
      "confidence": 0.65
    }
  }
}
```

`internal/recall` stays a pure ranking leaf: the pairing logic above lives
entirely at the `internal/mcp` wiring boundary.

## Storage and sync

Procedures live in a new, additive `procedures` table (+ `procedures_fts`
for retrieval), local-first and **excluded from the cloud sync payload**
this slice — `procedures`/`procedures_fts` never appear in
`sync_mutations`. Cross-machine procedure sync is deferred to a later
slice.

## Forward-compat note

Each procedure is authored with a machine-checkable `postcondition_kind`
(`tests_pass` | `lint_clean` | `build_green` | `custom` + `postcondition_expr`).
This slice only **stores and retrieves** that field — it never executes or
enforces it. A deferred compiler/enforcement runtime is expected to consume
this field in a future slice.
