# Type-as-Lens Specification

## Purpose

Extends the existing type importance-tier system with a situational lens: a type boost or filter inferred from context signals (e.g., an error context implying bugfix-type memories are more relevant), applied on top of today's 3-tier importance weighting, with an explicit user-passed type filter always taking precedence.

## Requirements

### Requirement: Situational Type Boost/Filter

When enabled and a situational signal implies a type is more relevant, the system MUST apply a boost (reweight) or a restrictive filter to that type in the ranked result set, on top of the existing 3-tier importance weighting (decision/architecture=3, bugfix/pattern=2, else=1).

#### Scenario: Error context boosts bugfix type
- GIVEN the current context signals an error/bug situation and no explicit user type filter is passed
- WHEN type-as-lens is enabled
- THEN bugfix-type results receive a situational boost in ranking relative to pre-v0.3 baseline

### Requirement: Explicit User Filter Always Wins

An explicit user-passed type filter MUST always take precedence over the situational lens; the lens MUST NOT override, widen, or add results outside the explicitly requested type.

#### Scenario: Explicit filter overrides the lens
- GIVEN the user explicitly passes a type filter (e.g., type=decision) and a situational signal would suggest a different type (e.g., bugfix)
- WHEN type-as-lens is enabled
- THEN only decision-type results are returned/ranked; the lens does not add or surface bugfix results

### Requirement: Neutral Context Leaves Ranking Unchanged

When no situational signal is detected, the lens MUST NOT alter existing type ranking/filtering behavior.

#### Scenario: No signal, no boost
- GIVEN no situational signal is detected in the current context
- WHEN type-as-lens is enabled
- THEN ranking output is unchanged from the pre-v0.3 baseline

### Requirement: Composes With Existing Importance Tiers

The situational boost MUST compose with (adjust on top of), not replace, the existing 3-tier importance weighting.

#### Scenario: Boost adjusts, tier system remains the baseline
- GIVEN a situational boost applies to bugfix type, which already carries tier weight 2
- WHEN type-as-lens is enabled
- THEN the situational boost is applied in addition to the existing tier weight, and the tier system continues to govern the baseline ordering of decision/architecture/bugfix/pattern/other

### Requirement: Sentinel and Signature Pre-Emption Invariant

The situational lens MUST NOT alter the always-first, complete, budget-exempt status of sentinel and signature-match rows; boosting or filtering by type applies only to the non-preempted portion of the result set.

#### Scenario: Preempted rows unaffected by type lens
- GIVEN a result set includes a sentinel row of a type the situational lens would otherwise deprioritize
- WHEN type-as-lens is enabled
- THEN the sentinel row is still emitted first and complete, unaffected by the type boost/filter

### Requirement: Disabled by Default, No-Op When Off

The feature MUST be controlled by a dedicated config flag, default OFF. When OFF, type ranking/filtering MUST be identical to pre-v0.3 behavior (exact-match filter plus static 3-tier importance weight only).

#### Scenario: Flag off preserves current behavior
- GIVEN the type-as-lens flag is OFF
- WHEN a search runs, regardless of situational signals present
- THEN type filtering/ranking is identical to pre-v0.3 behavior
