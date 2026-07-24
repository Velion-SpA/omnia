# Save-Normalization Specification

## Purpose

Deterministic pre-save content cleaning and non-blocking junk warnings for `mem_save`. Normalization feeds the write-gate's identical-content check; warnings inform the caller without ever preventing a save.

## Requirements

### Requirement: Canonical Normalization For Comparison

Before the write-gate's identical-content check, the system MUST apply a canonical normalization (whitespace, case, punctuation) to content used ONLY for that comparison. The persisted content of a SAVE or SAVE+RELATE MUST remain exactly as submitted — normalization MUST NOT alter what is stored.

#### Scenario: Whitespace/case-only differences normalize to identical

- GIVEN an existing observation with content "Fixed bug in X"
- WHEN `mem_save` is called with "fixed bug in x " (case/whitespace-only difference)
- THEN normalization treats both as content-identical, triggering the write-gate's NOOP path

#### Scenario: Persisted content is not mutated by normalization

- GIVEN a SAVE+RELATE decision
- WHEN the observation is persisted
- THEN the stored content matches the caller's original submission verbatim, not the normalized form

### Requirement: Non-Blocking Junk Warnings

The system MUST detect and surface, as warnings only, these conditions: empty content, content below a configured minimum length, missing a Keywords section, and content above a configured maximum size. Warnings MUST NEVER block, reject, or refuse a save — the save MUST always complete regardless of how many warnings fire.

#### Scenario: Empty content still saves

- GIVEN `mem_save` is called with empty content
- WHEN the gate/normalization step runs
- THEN the save completes (subject to write-gate classification) and the envelope includes an "empty content" warning

#### Scenario: Missing Keywords section warns but saves

- GIVEN content with no Keywords line
- WHEN `mem_save` is called
- THEN the save completes and a "missing Keywords" warning is present in the envelope

#### Scenario: Oversized content warns but saves

- GIVEN content exceeding the configured max size
- WHEN `mem_save` is called
- THEN the save completes untruncated and an "oversized" warning is present

### Requirement: Warnings Are Itemized In The Envelope

Every triggered warning condition MUST appear as a distinct entry in the `mem_save` response envelope so the caller can act on each one without an additional round-trip.

#### Scenario: Multiple warnings all reported

- GIVEN content that is both below minimum length and missing a Keywords section
- WHEN `mem_save` is called
- THEN the envelope lists both warnings separately
