# Memory Bisect Specification

## Purpose

`mem_bisect` — an interactive, git-bisect-style binary search over the recorded timeline (built on `time-travel-query`'s history substrate) that helps a user locate the memory revision that introduced a regression. Judgment is always human ("good"/"bad"); there is no automated oracle in this release. Deterministic, local, no LLM.

## Requirements

### Requirement: Bisect Session Requires Explicit Good/Bad Bounds

Starting a bisect session MUST require an explicit known-good point and known-bad point on the timeline. The system MUST reject a start request missing either bound.

#### Scenario: Bisect starts with both bounds
- GIVEN a known-good timestamp/revision and a known-bad timestamp/revision
- WHEN a bisect session starts
- THEN the system computes the candidate range between them and begins stepping

### Requirement: Deterministic Midpoint Selection

Each bisect step MUST select exactly one deterministic midpoint from the current candidate range (stable, timeline-order median) — no randomness. The same good/bad bounds and the same underlying history MUST always produce the same sequence of midpoints.

#### Scenario: Repeatable bisect sequence
- GIVEN identical good/bad bounds run twice against unchanged history
- WHEN each session steps through candidates
- THEN both sessions visit the same midpoints in the same order

### Requirement: Compact Per-Step State Presentation

Each step MUST present a compact summary of the candidate memory (title, type, and a short decision/change summary) — MUST NOT dump full observation content, consistent with existing token-economy conventions.

#### Scenario: Step output is bounded
- GIVEN a candidate memory with a long content body
- WHEN its step summary is shown
- THEN only title/type/summary fields are shown, not the full content

### Requirement: No Automated Oracle — User Judgment Required Per Step

The system MUST NOT auto-classify a candidate as good or bad. Each step MUST wait for an explicit user mark before narrowing the range further.

#### Scenario: Step without a mark does not advance
- GIVEN a presented candidate with no user mark yet
- WHEN no mark is provided
- THEN the session remains at the same step and does not narrow the range

### Requirement: Bisect Converges and Terminates Deterministically

Bisect MUST converge to a single implicated revision through repeated user marks. Given zero revisions in the candidate range, the system MUST report "no revisions in range" without stepping. Given exactly one revision in range, the system MUST identify it immediately as the answer without requiring a further split.

#### Scenario: Zero revisions in range
- GIVEN good and bad bounds with no revisions between them
- WHEN bisect starts
- THEN the system reports no revisions in range and does not begin stepping

#### Scenario: Single revision in range
- GIVEN exactly one revision between good and bad
- WHEN bisect starts
- THEN that revision is reported as the answer immediately

### Requirement: Session State Is Resumable and Explicitly Restartable

Bisect session state MUST persist across process restarts and MUST be resumable from the last recorded step. The system MUST also support an explicit restart/reset command that discards session state and starts over.

#### Scenario: Resume after interruption
- GIVEN an in-progress bisect session
- WHEN the process restarts
- THEN the session resumes at the same candidate range and step count

#### Scenario: Explicit restart clears state
- GIVEN an in-progress bisect session
- WHEN the user issues a restart/reset command
- THEN session state is discarded and a new session can begin cleanly

### Requirement: Hard-Delete Mid-Session Is Handled Deterministically

If a candidate memory is hard-deleted while a bisect session references it, the system MUST detect this via the same tombstone check used by `time-travel-query` and either deterministically skip past the tombstoned candidate or terminate the session with a clear explanation — MUST NOT crash or resurrect content.

#### Scenario: Candidate hard-deleted mid-bisect
- GIVEN a bisect session with a pending candidate
- WHEN that candidate's memory is hard-deleted before it is marked
- THEN the next step reports the candidate is unavailable (tombstoned) and advances deterministically

### Requirement: Deterministic, Local-Only, No LLM

Bisect MUST NOT invoke an LLM or network call for midpoint selection, state summarization, or good/bad classification.

#### Scenario: Fully offline bisect session
- GIVEN no network or LLM access
- WHEN a bisect session runs end to end
- THEN it completes using only local history data and user input
