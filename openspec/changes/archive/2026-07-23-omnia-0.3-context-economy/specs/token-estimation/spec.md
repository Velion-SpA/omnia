# Token Estimation Specification

## Purpose

Provides a deterministic, dependency-free token-count estimate for memory content. Serves as the shared foundation every injection-economy feature (budget, diversity, gating, lens) is built on, so all consumers use one primitive instead of drifting per-implementation estimates.

## Requirements

### Requirement: Pure, Dependency-Free Estimator

The system MUST expose a token-count estimator implemented in pure Go, with no CGO and no external tokenizer dependency (e.g., no tiktoken binding).

#### Scenario: Estimating a typical content string
- GIVEN a plain-text memory content string
- WHEN the estimator computes its token count
- THEN it returns a positive integer without invoking CGO or any external process

#### Scenario: No new dependency introduced
- GIVEN the estimator module is imported by any consumer
- WHEN the build runs with CGO_ENABLED=0
- THEN the build succeeds with no new external tokenizer dependency added

### Requirement: Deterministic Output

The estimator MUST be deterministic: identical input MUST always yield an identical estimate, across repeated calls, processes, and platforms.

#### Scenario: Repeated calls agree
- GIVEN the same content string
- WHEN the estimator is invoked twice
- THEN both calls return the identical token estimate

### Requirement: Heuristic, Documented Accuracy

The estimator MUST be documented as a heuristic approximation (e.g., character-count based), not an exact tokenizer-parity count, and this accuracy expectation MUST be stated wherever the estimate is surfaced or consumed.

#### Scenario: Estimate diverges from exact tokenizer count
- GIVEN a content string whose true BPE token count differs from the heuristic estimate
- WHEN the estimate is used by a downstream budget/consumer feature
- THEN the divergence is an accepted, documented approximation, not treated as a defect

### Requirement: Bounded Edge-Case Behavior

The estimator MUST handle empty input, whitespace-only input, and very large input without error or unbounded resource use.

#### Scenario: Empty string
- GIVEN content = ""
- WHEN the estimator runs
- THEN it returns 0 without error

#### Scenario: Large content
- GIVEN a multi-megabyte content string
- WHEN the estimator runs
- THEN it returns a result in bounded time without panicking

### Requirement: Non-Disruptive Introduction

Introducing the estimator MUST NOT alter any existing output-generating code path by itself; it MUST only affect output when explicitly invoked by a gated consumer feature (injection-budget, injection-diversity, type-as-lens).

#### Scenario: No consumer feature enabled
- GIVEN all features that consume the estimator (injection-budget, injection-diversity, type-as-lens) are disabled
- WHEN handleSearch and FormatContext run
- THEN the estimator is not invoked in that path and output is unchanged from pre-v0.3 behavior
