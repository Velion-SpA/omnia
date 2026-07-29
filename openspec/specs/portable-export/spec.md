# Portable Export Specification

## Purpose

First formal spec for Omnia's export/import surface (`omnia export [file]`, `omnia import <file.json>`, `store.Export`/`store.Import`). No prior `openspec/specs/` entry exists for this domain — today's code exports only sessions/observations/prompts and re-import duplicates rows. This spec establishes the target end-state: a COMPLETE, versioned, idempotent, deterministically-ordered full-graph export (observations + relations + anchors + procedures + provenance/trust + sessions + prompts) with a compatible legacy-format import path. Deterministic, local, no LLM.

## Requirements

### Requirement: Complete Full-Graph Export

Export MUST include observations (with `source`/`trust_tag` provenance fields), `memory_relations`, `memory_anchors`, `procedures`, sessions, and `user_prompts` — not only the current subset (sessions/observations/prompts).

#### Scenario: Full-graph export includes every entity
- GIVEN a store containing relations, anchors, and procedures in addition to observations/sessions/prompts
- WHEN export runs
- THEN the output file contains all six entity types

#### Scenario: Export on an empty store
- GIVEN a store with no data
- WHEN export runs
- THEN the output is a valid, schema-versioned file with empty arrays for every entity, not an error

### Requirement: Explicit Format Version Field

Every export file MUST carry a `schema_version` field identifying the export format precisely, distinct from any ad hoc version string.

#### Scenario: Exported file identifies its schema version
- GIVEN any export
- WHEN the file is inspected
- THEN `schema_version` is present and identifies the format

### Requirement: Idempotent Round-Trip Import

Re-importing the same export file MUST produce zero duplicate rows across all entity types, using upsert-by-`sync_id` semantics (reusing the existing claude-memory-import idempotency precedent).

#### Scenario: Re-import creates no duplicates
- GIVEN a store already populated by one import of a file
- WHEN the same file is imported again
- THEN zero new rows are created for every entity type

#### Scenario: Round-trip preserves relations and provenance
- GIVEN a store with relations and provenance-tagged observations
- WHEN it is exported, wiped, and re-imported
- THEN relations, anchors, procedures, and provenance fields are restored exactly

### Requirement: Deterministic Output Ordering

Export output MUST use a stable, deterministic ordering per entity so that two exports of an unchanged store are byte-for-byte diffable.

#### Scenario: Repeated export is byte-identical
- GIVEN an unchanged store
- WHEN exported twice
- THEN both output files are byte-for-byte identical

### Requirement: Legacy JSON Import Path Stays Compatible

The existing `omnia import <file.json>` path for pre-0.3.2 export files (missing `schema_version`, relations, anchors, procedures) MUST continue to succeed using documented defaults for the missing fields.

#### Scenario: Import of a pre-0.3.2 export file
- GIVEN an export file produced before this change (no `schema_version`, no relations/anchors/procedures)
- WHEN it is imported
- THEN import succeeds, applying documented defaults for the missing fields

### Requirement: Refuse Newer Format Versions Gracefully

Import of a file whose `schema_version` is newer than the running binary supports MUST fail with a clear, actionable error and MUST NOT partially apply any data.

#### Scenario: Import of a future-versioned file
- GIVEN an export file with a `schema_version` newer than supported
- WHEN import runs
- THEN it exits with an explicit "unsupported schema version" error and the store is left untouched

### Requirement: Deterministic, Local-Only, No LLM

Export and import MUST NOT invoke an LLM or network call.

#### Scenario: Fully offline export/import
- GIVEN no network or LLM access
- WHEN export then import runs
- THEN both complete successfully using only local data
