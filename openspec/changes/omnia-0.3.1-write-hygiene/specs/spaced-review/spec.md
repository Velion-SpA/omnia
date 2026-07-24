# Spaced-Review Specification

## Purpose

Play G — active spaced-repetition/review resurfacing. This capability EXTENDS Omnia's existing `review_after` lifecycle field and existing `mem_review` MCP tool (`action=list` / `action=mark_reviewed`, backed by `Store.ObservationsNeedingReview` / `Store.MarkReviewed`) with a compact, token-economical review-due surface. It introduces no new mutation path — resolution continues to flow through the existing `mem_review` and update/state-change tools. Deterministic, no LLM.

## Requirements

### Requirement: Compact Review-Due Surface

The system MUST provide a review-due surface (e.g., an `omnia review` CLI report) that lists observations whose `review_after` has passed, reusing the existing `ObservationsNeedingReview` query. The surface MUST be compact and grouped (count per group, e.g., by type) plus each entry's ID and title. It MUST NOT dump any observation's full `content` field, consistent with the token-economy principle applied elsewhere in write-hygiene.

#### Scenario: Nothing due

- GIVEN no observation has a `review_after` value in the past
- WHEN the review-due surface runs
- THEN it prints a quiet, empty summary (e.g., "0 memories due for review") and exits cleanly with no error

#### Scenario: Some due, grouped listing

- GIVEN 3 observations of type "decision" and 2 of type "policy" have `review_after` in the past
- WHEN the review-due surface runs
- THEN it prints a grouped count per type (e.g., "decision: 3", "policy: 2") plus each entry's ID and title, and prints no observation's `content` field

### Requirement: Resolution Reuses Existing Tools — No New Mutation Path

Resolving a due observation MUST route through tools that already exist: confirm-still-valid MUST bump `review_after` via the existing `mem_review` `action=mark_reviewed` (`Store.MarkReviewed`); obsolete MUST be recorded via an existing update/state-change path (e.g., `mem_update` or equivalent). This capability MUST NOT introduce any new observation-mutation RPC, CLI mutation flag, or store method — its own surface MUST be read-only.

#### Scenario: Confirm-still-valid bumps review_after via the existing tool

- GIVEN observation #42 appears in the review-due surface
- WHEN the caller confirms it is still valid
- THEN resolution occurs by invoking the existing `mem_review` `mark_reviewed` action against #42, which bumps its `review_after` per its type's decay policy — no new mutation path is invoked

#### Scenario: Obsolete resolves via an existing update path

- GIVEN observation #42 appears in the review-due surface and the caller determines it is obsolete
- WHEN that decision is recorded
- THEN it is recorded through an existing update/state-change path already defined outside this capability; spaced-review introduces no store mutation of its own

### Requirement: Existing-Tool Output Changes Are Gated Default-OFF

Any change to an EXISTING tool's output — e.g., a due-count nudge appended to `mem_context`/the session envelope — MUST be gated behind a config flag defaulting to OFF (D7 convention). When the gate is off, the existing tool's output MUST remain byte-for-byte identical to its pre-spaced-review behavior. The standalone review-due surface itself is inherently opt-in (a separate command the caller must invoke) and requires no additional gate.

#### Scenario: Gate off — existing tool output is byte-identical

- GIVEN the spaced-review due-count-nudge gate is off (its default)
- WHEN `mem_context`/the session envelope is invoked while observations are due for review
- THEN its output is byte-for-byte identical to pre-spaced-review output — no due-count nudge appears

#### Scenario: Gate on — due-count nudge appears without altering other fields

- GIVEN the spaced-review due-count-nudge gate is explicitly enabled
- WHEN `mem_context`/the session envelope is invoked while observations are due for review
- THEN a due-count nudge (e.g., "N memories due for review") is appended, without altering the tool's other existing output fields

### Requirement: Deterministic, No LLM

The review-due surface and any due-count nudge MUST be computed deterministically from stored `review_after` timestamps. No LLM or embedding-inference call MUST occur as part of computing or summarizing what is due.

#### Scenario: Computation uses only stored timestamps

- GIVEN the review-due surface or a due-count nudge is computed
- WHEN it runs
- THEN the result is derived solely from comparing stored `review_after` values against the current time, with no model-inference call
