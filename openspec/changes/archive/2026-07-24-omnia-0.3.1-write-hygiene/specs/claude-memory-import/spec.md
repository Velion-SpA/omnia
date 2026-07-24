# Claude-Memory-Import Specification

## Purpose

`omnia import claude-memory <dir>`: bridges Claude Code's file-based auto-memory into first-class Omnia observations, provenance-tagged, routed through the write-gate so re-imports never duplicate.

## Requirements

### Requirement: MEMORY.md Is Skipped

The index file `MEMORY.md` MUST be skipped entirely — it MUST NOT be imported as an observation.

#### Scenario: Index file excluded

- GIVEN a directory containing `MEMORY.md` and several per-topic memory files
- WHEN `omnia import claude-memory <dir>` runs
- THEN no observation is created from `MEMORY.md`; every other file is considered for import

### Requirement: Each Memory File Is A First-Class Observation

Every non-index memory file MUST be imported as a first-class observation (searchable and retrievable the same as any natively-saved observation), not a degraded record.

#### Scenario: Directory of only the index file

- GIVEN a directory containing only `MEMORY.md`
- WHEN import runs
- THEN zero observations are created and no error is raised

### Requirement: Provenance Tag Required

Every imported observation MUST carry a source/provenance tag identifying it as originating from the Claude-memory import.

#### Scenario: Imported observation is tagged

- GIVEN a memory file imported successfully
- WHEN the resulting observation is inspected
- THEN it carries a provenance tag identifying the Claude-memory import as its source

### Requirement: Idempotent Re-Import Via Write-Gate

Every import MUST be routed through the write-gate (see Write-Gate Specification). When `write_hygiene.enabled: true` (the default), re-running import over unchanged directory contents MUST NOT create duplicate observations — repeat runs resolve to NOOP or AUTO-UPDATE, never a second SAVE. When `write_hygiene.enabled: false`, import follows plain save semantics per the kill-switch and the idempotency guarantee does not apply — this MUST be documented, not silent.

#### Scenario: Re-run over unchanged directory is a no-op

- GIVEN a directory already imported once, unchanged since
- WHEN `omnia import claude-memory <dir>` runs again
- THEN zero new observations are created (every file resolves to NOOP)

#### Scenario: Re-run after a file was edited

- GIVEN a previously imported memory file whose content was appended to since
- WHEN import runs again
- THEN the existing imported observation is AUTO-UPDATEd, not duplicated

#### Scenario: Kill-switch disables the idempotency guarantee

- GIVEN `write_hygiene.enabled: false`
- WHEN import is run twice over the same unchanged directory
- THEN each run creates plain new observations per v0.3 semantics (duplicates are possible), consistent with the global kill-switch
