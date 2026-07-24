# Write-Gate Specification

## Purpose

Deterministic, no-LLM pre-insert classification for `mem_save` that decides whether an incoming save is a NOOP (already stored), an AUTO-UPDATE (extends an existing observation), or a normal SAVE — with full transparency in the response envelope and a global kill-switch.

## Requirements

### Requirement: Default-On With Kill-Switch

The write-gate MUST be enabled by default (`write_hygiene.enabled: true`). When `write_hygiene.enabled: false`, `mem_save` MUST behave byte-for-byte identically to pre-write-hygiene (v0.3) behavior: no similarity search, no normalization, no NOOP/AUTO-UPDATE classification — every save is a plain new observation.

#### Scenario: Kill-switch disables the gate entirely

- GIVEN `write_hygiene.enabled: false`
- WHEN `mem_save` is called with content identical to an existing observation
- THEN a new observation is created exactly as in v0.3 (no NOOP, no gate logic applied)

#### Scenario: Default config runs the gate

- GIVEN no `write_hygiene.enabled` override in config
- WHEN `mem_save` is called
- THEN the gate evaluates the decision ladder before the observation is persisted

### Requirement: Deterministic, No-LLM Classification

The write-gate MUST classify every incoming save using only deterministic lexical primitives (token-set Jaccard similarity, FTS candidate lookup, token estimation). It MUST NOT invoke any LLM or embedding-inference call as part of the classification decision.

#### Scenario: Classification uses only lexical primitives

- GIVEN a `mem_save` call
- WHEN the gate evaluates NOOP/AUTO-UPDATE/SAVE
- THEN no network or model-inference call is made to reach the decision

### Requirement: Decision Ladder Precedence

The gate MUST evaluate, in this exact order: (1) content-identical after normalization → NOOP; (2) `topic_key` exact match OR similarity strictly greater than 0.9 → AUTO-UPDATE; (3) otherwise → normal SAVE via the existing `mem_judge`/`judgment_required` relate flow.

#### Scenario: Identical content

- GIVEN an existing observation with normalized content C
- WHEN `mem_save` is called with content that normalizes to C
- THEN the gate returns NOOP against the existing observation; no new row is created

#### Scenario: topic_key match triggers auto-update

- GIVEN an existing observation with `topic_key: "x"`
- WHEN `mem_save` is called with `topic_key: "x"` and different (superset/newer) content
- THEN the gate updates the existing observation in place (AUTO-UPDATE)

#### Scenario: High similarity without topic_key triggers auto-update

- GIVEN an existing observation with no `topic_key` set
- WHEN `mem_save` content scores similarity > 0.9 against it
- THEN the gate updates that existing observation (AUTO-UPDATE)

#### Scenario: Similarity exactly at threshold does not auto-update

- GIVEN an existing observation
- WHEN `mem_save` content scores similarity of exactly 0.9 (not greater than 0.9) and no `topic_key` match
- THEN the gate does NOT auto-update; it falls through to normal SAVE + judgment_required flow

#### Scenario: Similar-but-distinct falls through to normal save

- GIVEN an existing observation scoring similarity below 0.9 with no `topic_key` match
- WHEN `mem_save` is called
- THEN a new observation is created and the existing `mem_judge`/`judgment_required` relate flow applies unchanged

### Requirement: Envelope Transparency

Every gate decision MUST be visible in the `mem_save` response envelope. On NOOP, the envelope MUST return the existing observation's ID with a NOOP notice. On AUTO-UPDATE, the envelope MUST name the updated observation's ID explicitly (e.g., "updated #N instead of duplicating").

#### Scenario: NOOP envelope names the existing ID

- GIVEN a NOOP decision
- WHEN the response is returned
- THEN it includes the existing observation's ID and a "NOOP" indicator

#### Scenario: Auto-update envelope names the target

- GIVEN an AUTO-UPDATE decision on observation #42
- WHEN the response is returned
- THEN it states the update occurred against #42, distinguishable from a new-save response
