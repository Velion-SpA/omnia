# Memory Provenance Specification

## Purpose

Verifiable provenance and trust substrate for Omnia memory: write-time source-trust tagging, provable physical deletion with a durable tombstone (local store + embeddings + cloud replica), and a read/write/delete audit trail reusing `internal/audit`. Encrypt-at-rest is deferred to 0.3 (see Non-Goals); 0.2 instead ships filesystem-permissions hardening and a `doctor` exposure warning.

## Requirements

### Requirement: Write-Time Source-Trust Tagging

Every `mem_save`/`AddObservation` MUST persist a `source` and derived `trust_tag`, classified at write time, not retrieval time. Valid classes: `user`, `agent`, `ingest:tool`, `ingest:web`, `ingest:doc`. Absent or unrecognized `source` MUST default `trust_tag` to `unverified`. The tag is attribution, not authentication: the system MUST NOT reject, block, or alter a save based on `trust_tag`.

#### Scenario: Explicit source is classified and stored
- GIVEN `mem_save` with `source: "ingest:web"`
- WHEN the observation is persisted
- THEN the row stores `source="ingest:web"` and its `trust_tag`, and the save succeeds regardless of content verifiability

#### Scenario: Missing source defaults to unverified
- GIVEN `mem_save` with no `source`
- WHEN persisted
- THEN `trust_tag` is `unverified`

#### Scenario: Legacy rows read as unverified
- GIVEN a pre-migration row with no `source`/`trust_tag`
- WHEN read after migration
- THEN `trust_tag` reads `unverified` and nothing else changes

### Requirement: Hard Delete Physically Purges Store and Embeddings

`DeleteObservation(hard=true)` MUST physically remove the observation row (not mark it) and MUST cause physical removal of its embedding vector via `embed.Store.DeleteBySyncID`, fanned out from the MCP layer — `internal/store` MUST NOT import `internal/embed`.

#### Scenario: Hard delete purges row and vector
- GIVEN an observation with a stored vector
- WHEN it is hard-deleted
- THEN the row and its FTS entry are gone and the vector count for that `sync_id` is 0

### Requirement: Deletion Is Provably Tombstoned on Push and Pull

Every hard delete — executed locally, or applied via `applyObservationDeleteTx` when pulling an inbound `SyncOpDelete{HardDelete}` mutation — MUST write a durable `deletion_tombstones` proof row, independent of the (prunable) sync mutation journal.

#### Scenario: Local hard delete writes a tombstone
- GIVEN a local `DeleteObservation(id, hard=true)` call
- WHEN it completes
- THEN a `deletion_tombstones` row exists for that observation's `sync_id`

#### Scenario: Pulled hard delete also writes a tombstone
- GIVEN a cloud pull applying a `SyncOpDelete{HardDelete}` mutation
- WHEN `applyObservationDeleteTx` runs
- THEN the row is purged locally AND its own `deletion_tombstones` row is written — proof replicates on both the push and pull paths

### Requirement: Soft Delete Remains Out of Physical-Purge Scope

Soft delete (`hard=false`) MUST only set `deleted_at`; it MUST NOT purge the embedding vector and MUST NOT write a tombstone.

#### Scenario: Soft delete leaves vector and row intact
- GIVEN a soft-delete call
- WHEN it completes
- THEN the row persists with `deleted_at` set, its vector is untouched, and no tombstone is written

### Requirement: Save and Delete Are Audited via the Existing Audit Log

`internal/audit.Entry` MUST gain `Source`, `TrustTag`, `SyncID`, `SessionID`, and the `Action` taxonomy MUST gain `ActionRead`/`ActionWrite` alongside existing `edit`/`soft_delete`/`hard_delete` — no parallel log. `mem_save` and `mem_delete` (soft and hard) MUST append an entry carrying these fields. An audit-append failure MUST be logged and MUST NOT block or roll back the underlying save/delete.

#### Scenario: Save appends a write entry
- GIVEN a successful `mem_save`
- WHEN it completes
- THEN `internal/audit` gains one write entry with `source` and `trust_tag`

#### Scenario: Hard delete appends an entry after purge
- GIVEN a successful hard delete (row + vector purged, tombstone written)
- WHEN it completes
- THEN `internal/audit` gains one hard-delete entry for that observation

#### Scenario: Audit failure does not block the save
- GIVEN the audit log path is unwritable
- WHEN `mem_save` is called
- THEN the observation still persists successfully despite the audit append failing

### Requirement: Store Permissions and Cloud-Sync Exposure Are Hardened

The store directory and database file MUST be created owner-only (no group/world read or write). `omnia doctor` MUST report a warning-level check when the store path resides inside a recognized cloud-backup/sync folder (iCloud Drive, Dropbox, OneDrive) or its permissions are group/world-readable.

#### Scenario: Fresh store has locked-down permissions
- GIVEN a fresh install
- WHEN the store is created
- THEN neither the directory nor the file is group- or world-readable/writable

#### Scenario: Doctor flags a cloud-synced store path
- GIVEN the store directory resolves inside an iCloud Drive folder
- WHEN `omnia doctor` runs
- THEN it reports a warning naming the cloud-sync exposure

## Non-Goals

Encrypt-at-rest for `content`/embeddings is deferred to 0.3 — app-level ciphertext breaks FTS5 and vector-cosine search, and FileVault only covers powered-off theft. This spec does not claim at-rest encryption in 0.2.
