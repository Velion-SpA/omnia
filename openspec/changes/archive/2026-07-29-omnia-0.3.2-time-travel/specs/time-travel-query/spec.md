# Time-Travel Query Specification

## Purpose

Recorded-time reads (`--as-of <timestamp>`) across search, get, and context assembly (`mem_search`/`omnia search`, `mem_get_observation`, `mem_context`/`omnia context`), so a user can ask "what did memory say on date X" without any LLM. Feature-flagged default-off (D7): absent `--as-of`, behavior is byte-for-byte identical to pre-0.3.2. Backed by a history substrate (design decides table vs. hardened outbox read) that starts recording at upgrade and honors existing hard-delete provable-deletion guarantees.

## Requirements

### Requirement: Default Behavior Unchanged Without `--as-of`

`search`, `get`, and `context` MUST produce byte-for-byte identical output to pre-0.3.2 behavior when `--as-of` is absent, regardless of whether the time-travel feature flag is enabled.

#### Scenario: No `--as-of` supplied
- GIVEN the time-travel feature is enabled
- WHEN `search`/`get`/`context` is called without `--as-of`
- THEN output matches pre-0.3.2 live-state behavior exactly

### Requirement: Recorded-Time Query Semantics

Given `--as-of <timestamp>`, the system MUST return each matching memory's content as it was RECORDED at or before that timestamp (recorded-time only — no valid-time/bitemporal tracking). A memory soft-deleted at time T MUST appear, as it existed at T, for any `--as-of` at or after T only if it was visible then; queries before its soft-delete MUST show it as live.

#### Scenario: `--as-of` returns prior content of an edited memory
- GIVEN a memory edited after timestamp T
- WHEN queried with `--as-of T`
- THEN the content returned matches what was recorded at T, not the current content

#### Scenario: Soft-deleted-at-T memory renders as it existed at T
- GIVEN a memory soft-deleted at T2
- WHEN queried with `--as-of T1` where T1 < T2
- THEN the memory appears live, as recorded at T1

### Requirement: Hard-Delete Purges History Too (Tombstone × Time-Travel Rule)

`--as-of` reads MUST NOT resurrect hard-deleted content at any timestamp. The history substrate MUST consult the existing `deletion_tombstones` / `internal/purge` hard-delete proof for a memory's `sync_id` and exclude all of that memory's recorded states once a hard-delete tombstone exists, extending — not duplicating — the current provable-deletion guarantee.

#### Scenario: `--as-of` before a hard delete still hides content
- GIVEN a memory hard-deleted at T2
- WHEN queried with `--as-of T1` where T1 < T2
- THEN the memory is absent from results, matching provable-deletion semantics

#### Scenario: Hard-delete occurring mid-bisect purges history immediately
- GIVEN an active `--as-of`/bisect session referencing a memory
- WHEN that memory is hard-deleted
- THEN subsequent `--as-of` reads at any timestamp no longer surface it

### Requirement: History Starts at Upgrade — Disclaimer Surface

The system MUST NOT claim or imply pre-upgrade history exists. `--as-of` older than the earliest recorded history point MUST return an explicit disclaimer stating history is unavailable before that point, instead of an empty or misleading result.

#### Scenario: `--as-of` predates the history substrate
- GIVEN history recording began at upgrade timestamp U
- WHEN queried with `--as-of T` where T < U
- THEN the response states plainly that history is unavailable before U, without silently returning empty results

### Requirement: `--as-of` in the Future Resolves to Current State

`--as-of` timestamps later than "now" MUST resolve deterministically to the current live state and MUST NOT error.

#### Scenario: Future `--as-of`
- GIVEN `--as-of T` where T is later than the current time
- WHEN the query runs
- THEN it returns the current live state, identical to no `--as-of`

### Requirement: Retention Is Unlimited by Default With Opt-In Cap

History retention MUST default to unlimited. An operator MAY configure a cap (revision count or time window) in `config.yaml`; only then are older history entries pruned.

#### Scenario: Default retention keeps all history
- GIVEN default config
- WHEN any number of edits accumulate over time
- THEN all recorded states remain queryable via `--as-of`

#### Scenario: Operator-configured cap prunes older history
- GIVEN a configured retention cap
- WHEN history exceeds that cap
- THEN older entries beyond the cap are pruned while recent history and the live row remain intact

### Requirement: Deterministic, Local-Only, No LLM

`--as-of` resolution MUST be deterministic and MUST NOT invoke an LLM or network call.

#### Scenario: Fully offline recorded-time read
- GIVEN no network or LLM access
- WHEN `--as-of` is queried
- THEN the correct historical state is returned using only the local store
