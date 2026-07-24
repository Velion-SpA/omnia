# Delta for Session-Hooks

Note: no pre-existing `openspec/specs/session-hooks/spec.md` was found to diff against; written in full under `ADDED Requirements`. This addresses tracked issue #147, following the precedent of fix #146.

## ADDED Requirements

### Requirement: Hook Project Resolution Matches mem_save

`sessionStart` and `sessionEnd` hooks MUST resolve the active project using the same resolution logic and precedence as `mem_save`, including honoring any explicit process-level project override, matching the precedent established by fix #146.

#### Scenario: Override honored identically across hooks and mem_save

- GIVEN a process-level project override is set
- WHEN `sessionStart` fires and `mem_save` is subsequently called in the same process
- THEN both resolve to the overridden project, not divergent values

#### Scenario: No override falls back to the same auto-detection

- GIVEN no process-level override is set
- WHEN `sessionStart`/`sessionEnd` and `mem_save` each resolve a project
- THEN both use the same auto-detection path (e.g., cwd-based) and agree on the result

#### Scenario: Session-end consistency within a session

- GIVEN `sessionStart` resolved project P for the current session
- WHEN `sessionEnd` fires later in that same session
- THEN it resolves the same project P, not a re-derived divergent value
