# Memory Enforcement Gate Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: memory-enforcement-gate (⭐ flagship)
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-410 through REQ-418

## Purpose

Procedural memory already compiles repeated corrections into verifiable, parameterized `procedures`
(polarity playbook/anti_playbook, machine-checkable `postcondition_kind`/`postcondition_expr`, governed by an
SSGM candidate→trusted→retired state machine — `internal/store/procedures.go:34,41,51,134,490,558`) but
**never executes them** ("this slice only STORES these values, it never executes them",
`internal/store/procedures.go:49`). This capability builds that deferred execution runtime: a pre-completion
gate an agent calls before an edit is considered done, which mechanically runs the applicable trusted
procedures' postconditions and returns pass/flag/block. No LLM judges the result — only already
machine-checkable conditions are verified.

**Naming**: `mem_enforce` / `omnia enforce` mirror the `mem_blame`/`blame` symmetry (capability 1) and the
existing `mem_<verb>`/`<verb>` pattern. Verified against current conventions — no rename needed (`mem_check`
considered and rejected as less precise than `mem_enforce`).

## Requirements

### Requirement: REQ-410 Default-Off Config Gate

A new `EnforcementConfig` (`yaml: enforcement`, field `enabled`, default `false`) MUST gate this entire
capability. When disabled, `mem_enforce`/`omnia enforce` MUST NOT be registered/callable, no postcondition
command runs, and no enforcement audit entries are written.

#### Scenario: Disabled — gate is fully inert

- GIVEN `enforcement.enabled` is absent or `false`
- WHEN an agent calls `mem_enforce` or runs `omnia enforce`
- THEN the call returns a structured "capability disabled" response
- AND no procedure's postcondition command is executed and no audit entry is written

### Requirement: REQ-411 Trusted-Procedures-Only Feed

The gate MUST select only `procedures` rows with `state = trusted` whose `trigger`/scope apply to the current
change. Candidate and retired procedures MUST NEVER gate a completion.

#### Scenario: Happy path — trusted procedure gates a matching change

- GIVEN one trusted procedure whose trigger matches the files touched by the current edit
- WHEN `mem_enforce` runs
- THEN only that trusted procedure's postcondition is evaluated
- AND any candidate or retired procedure matching the same trigger is skipped

### Requirement: REQ-412 Mechanical Postcondition Execution, No LLM

For `postcondition_kind` = `tests_pass`/`lint_clean`/`build_green`, the gate MUST run the configured command;
for `custom`, it MUST evaluate `postcondition_expr`. No LLM call MUST occur anywhere in this evaluation path.

#### Scenario: tests_pass postcondition runs the configured command

- GIVEN a trusted procedure with `postcondition_kind = "tests_pass"` and a configured test command
- WHEN the gate evaluates that procedure
- THEN the configured command is executed and its exit status determines pass/fail
- AND no LLM/Ollama call occurs during evaluation

### Requirement: REQ-413 Pass / Flag / Block Verdict Contract

The gate MUST return exactly one of `pass`, `flag`, or `block` per invocation, derived from the evaluated
postcondition result(s) and the configured `Mode`.

#### Scenario: All applicable postconditions pass

- GIVEN every trusted procedure matching the current change has its postcondition satisfied
- WHEN `mem_enforce` runs
- THEN the verdict is `pass`

### Requirement: REQ-414 Default Mode Is Flag-With-Override, Blocking Is Opt-In

`EnforcementConfig.Mode` MUST default to `"flag"`. Hard blocking MUST require an explicit operator opt-in
(`mode: "block"`); a first-slice install that only sets `enabled: true` never hard-blocks.

#### Scenario: Edge case — violation under default mode is flagged, not blocked

- GIVEN `enforcement.enabled = true` and `mode` unset (defaults to `"flag"`)
- WHEN a trusted anti_playbook procedure's postcondition fails
- THEN the verdict is `flag`, not `block`, and the caller's workflow is not halted

#### Scenario: Block mode halts on violation

- GIVEN `enforcement.mode = "block"` explicitly set
- WHEN a trusted procedure's postcondition fails
- THEN the verdict is `block`

### Requirement: REQ-415 Explicit Override Escape Hatch

A violation MUST be overridable via an explicit override flag/param on the `mem_enforce`/`omnia enforce` call.
An override MUST be recorded as its own distinct outcome, never silently reported as `pass`.

#### Scenario: Edge case — false positive overridden by the caller

- GIVEN a trusted procedure's postcondition fails but the caller determines it is a false positive
- WHEN the caller re-invokes `mem_enforce` with the explicit override parameter
- THEN the gate proceeds without blocking, and the resulting audit entry records `verdict: override`, distinct
  from `pass`

### Requirement: REQ-416 Every Gate Decision Is Audited

Every gate invocation — `pass`, `flag`, `block`, or `override` — MUST write one entry to the native audit log
(`internal/audit`), including which procedure(s) were evaluated.

#### Scenario: Flag decision is audited

- GIVEN a gate invocation returns `flag`
- WHEN the invocation completes
- THEN an audit log entry exists recording the verdict, timestamp, and the procedure(s) evaluated

### Requirement: REQ-417 Verify-Only — No Auto-Fix

The gate MUST NOT modify the edit/diff it is checking. It verifies; it never edits code, tests, or configuration.

#### Scenario: Failing postcondition leaves the edit untouched

- GIVEN a trusted procedure's postcondition fails
- WHEN the gate returns `flag` or `block`
- THEN no file touched by the current edit is modified by the gate itself

### Requirement: REQ-418 Tool and CLI Registration

`mem_enforce` MUST be wired through `mcp.MCPConfig` following the existing `ProceduralWiring` pattern
(`cmd/omnia/main.go:1342`), and `omnia enforce` MUST be registered in the CLI dispatch
(`cmd/omnia/main.go:730`) alongside `procedure`/`procedure-induct`.

#### Scenario: CLI invocation available for hook/CI use

- GIVEN `enforcement.enabled = true`
- WHEN `omnia enforce` is run from a pre-commit hook or CI step
- THEN it returns the same pass/flag/block/override contract as the MCP tool

## Out of Scope (Non-Goals)

- Inducing new procedures (already handled by `procedure-induct`, `cmd/omnia/main.go:759`).
- LLM-based diff understanding or auto-fixing violations.
- Gating on candidate or retired procedures.
