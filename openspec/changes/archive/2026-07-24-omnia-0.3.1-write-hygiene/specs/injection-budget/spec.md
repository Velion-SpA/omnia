# Delta for Injection-Budget

Note: no pre-existing `openspec/specs/injection-budget/spec.md` was found to diff against; written in full under `ADDED Requirements`. `sdd-archive` should seed `openspec/specs/injection-budget/spec.md` from this file.

## ADDED Requirements

### Requirement: context_budget Remains Unchanged

`injection.context_budget` (governing `FormatContext`'s aggregation across its buckets) MUST remain 1500 — this change MUST NOT alter `context_budget` or its consumers.

#### Scenario: context_budget path unaffected

- GIVEN the `injection.budget` default is recalibrated
- WHEN `FormatContext` runs
- THEN it still aggregates against a 1500 `context_budget`, unchanged

### Requirement: injection.budget Default Recalibrated Within The Eval-Justified Range

The default value of `injection.budget` (governing the `mem_search` injection/preview-aggregation path) MUST be set within the effective range demonstrated by formal eval evidence (structurally inert above ~750 tokens on this path; target range ~300-500). The exact number MUST be chosen and justified by design using the existing `omnia eval --injection` methodology, and MUST show no accuracy regression versus the prior default at the chosen value.

#### Scenario: New default reduces token cost without accuracy loss

- GIVEN the recalibrated `injection.budget` default
- WHEN `omnia eval --injection` is run against it
- THEN accuracy is unchanged or better versus the prior default, with reduced token cost per case

### Requirement: injection.budget Remains User-Configurable

The recalibration MUST change only the shipped default; `injection.budget` MUST remain overridable via config.

#### Scenario: Explicit override wins over new default

- GIVEN a user config setting `injection.budget` to a custom value
- WHEN `mem_search` runs
- THEN the custom value is honored, not the new shipped default
