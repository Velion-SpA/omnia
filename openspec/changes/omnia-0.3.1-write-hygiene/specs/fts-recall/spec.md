# Delta for FTS-Recall

Note: no pre-existing `openspec/specs/fts-recall/spec.md` was found in this repo to diff against; the requirements below are written in full (baseline + new fallback) under `ADDED Requirements` rather than a true `MODIFIED` block, since there was nothing to copy from. `sdd-archive` should seed `openspec/specs/fts-recall/spec.md` from this file.

## ADDED Requirements

### Requirement: Strict AND-Match Is The Primary Query Strategy

The FTS recall path MUST continue to perform an AND-of-all-terms match (including stopwords) as its first-pass query strategy.

#### Scenario: Strict query with hits returns immediately

- GIVEN a query whose strict AND-of-terms match returns 1+ hits
- WHEN the search executes
- THEN those hits are returned and no relaxation attempt occurs

### Requirement: Zero-Hit Relaxation Fallback

If the first-pass strict query returns exactly zero hits, the system MUST retry using a progressively relaxed term set (e.g., dropping stopwords, then loosening AND to OR) before returning an empty result. The fallback MUST fire ONLY on zero hits from the strict pass.

#### Scenario: Zero strict hits, relaxed pass finds results

- GIVEN a query that returns 0 hits under strict AND-of-terms matching
- WHEN the relaxation fallback runs
- THEN a relaxed pass executes and returns matching results if any exist

#### Scenario: Non-zero strict hits skip the fallback

- GIVEN a query returning at least 1 strict hit
- WHEN search executes
- THEN the relaxation fallback is never invoked

### Requirement: Bounded Relaxation Retries

Relaxation retries MUST be bounded to a fixed maximum number of attempts; the system MUST NOT retry indefinitely.

#### Scenario: All relaxation levels exhausted

- GIVEN a query that returns 0 hits at the strict pass and at every relaxation level
- WHEN the bounded retries are exhausted
- THEN an empty result is returned and the response indicates the fallback was exhausted, not silently retried further

### Requirement: Fallback Transparency

Whether the fallback fired, and at which relaxation level results were found (or that all levels were exhausted), MUST be visible in the search response metadata.

#### Scenario: Relaxed result is labeled

- GIVEN a query resolved via the relaxed pass
- WHEN results are returned
- THEN the response metadata indicates relaxation was used
