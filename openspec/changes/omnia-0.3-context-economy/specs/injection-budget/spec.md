# Injection Budget Specification

## Purpose

Bounds the total token size of memory content injected into agent context — both `mem_search` results (handleSearch) and assembled context (FormatContext's sessions/pinned/recent/prompts buckets) — using one shared, token-based budget primitive instead of today's flat character/count truncation, while fixing FormatContext's pre-existing unbounded aggregate-size defect.

## Requirements

### Requirement: Token-Based Budget, Not Char-Based

When enabled, injected result sets MUST be trimmed against a configured token budget computed via the token-estimation primitive, not a raw character count or fixed item count.

#### Scenario: Budget replaces char truncation
- GIVEN a ranked result set whose estimated tokens exceed the configured budget
- WHEN injection-budget is enabled
- THEN trimming decisions are based on estimated token totals, not character length or a fixed item count

### Requirement: Top-N-Complete Trim Strategy

Trimming MUST include ranked items whole, in rank order, until the next item would exceed the budget; that item and all lower-ranked items MUST be excluded entirely. No included item MUST be partially truncated mid-content.

#### Scenario: Whole items kept, remainder dropped
- GIVEN ranked items where item 4 would push the total over budget
- WHEN injection-budget trims the set
- THEN items 1-3 are returned complete and items 4+ are omitted entirely, not partially truncated

### Requirement: Aggregate Cap on FormatContext Buckets

When enabled, the combined token size across FormatContext's four buckets (sessions, pinned, recent, prompts) MUST NOT exceed the configured budget.
(Previously: no aggregate cap existed; combined bucket size could grow unbounded.)

#### Scenario: Combined buckets respect the cap
- GIVEN sessions/pinned/recent/prompts buckets that together would have exceeded any reasonable size pre-v0.3
- WHEN injection-budget is enabled
- THEN the total emitted content across all four buckets does not exceed the configured token budget

### Requirement: One Shared Primitive Across Consumers

The same exported budget primitive MUST be used by handleSearch (mem_search MCP), FormatContext, and cmd/omnia/recall_fix.go, so trim behavior cannot drift between consumers.

#### Scenario: Consistent trim across call sites
- GIVEN the same result set and the same budget configuration
- WHEN both the mem_search path and the recall-fix CLI path apply injection-budget
- THEN both produce equivalent trim decisions (same items included/excluded)

### Requirement: Sentinel and Signature Pre-Emption Invariant

Sentinel rows (topic_key rank -1000) and signature-match rows MUST always be emitted in full and MUST be excluded from token-budget accounting; the budget MUST apply only to the remaining, non-preempted rows.

#### Scenario: Preempted rows always complete regardless of budget
- GIVEN a result set containing a sentinel row and/or a signature-match row plus other ranked rows whose combined size exceeds the budget
- WHEN injection-budget trims the set
- THEN the sentinel and signature-match rows are emitted complete and untrimmed, and the budget is applied only to the other ranked rows

#### Scenario: Budget smaller than the smallest eligible item
- GIVEN the configured budget is smaller than the smallest non-preempted item
- WHEN injection-budget trims the set
- THEN preempted rows (if any) are still returned complete, no non-preempted item is force-included beyond budget, and the response is not an error

### Requirement: Disabled by Default, No-Op When Off

The feature MUST be controlled by a dedicated config flag, default OFF. When OFF, output MUST be byte-for-byte identical to pre-v0.3 behavior (existing char/count truncation, uncapped FormatContext buckets).

#### Scenario: Flag off preserves current behavior
- GIVEN the injection-budget flag is OFF
- WHEN handleSearch or FormatContext run
- THEN output is byte-for-byte identical to current v0.2 behavior
