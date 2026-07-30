# Memory At-Rest Security Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: memory-at-rest-security
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-430 through REQ-437

## Purpose

Encrypt the on-disk SQLite store(s) at rest using an OS-keychain-sourced key, complete the provenance
trust-tag surface (`classifyTrust`, `internal/store/provenance.go:9,26`) so it is consistently visible in
read receipts and the audit trail, and route enforcement (capability 2) and consolidation (capability 3)
decisions into the existing native audit log (`internal/audit`, imported at `cmd/omnia/main.go:31`).
Competitors (Screenpipe, Rewind, Mem0) do not encrypt at rest by default; this is a direct trust
differentiator, delivered under the hard `CGO_ENABLED=0` constraint (no SQLCipher).

## Requirements

### Requirement: REQ-430 Default-Off Config Gate

A new `EncryptionConfig` (`yaml: encryption`, field `enabled`, default `false`) MUST gate encryption. When
disabled, the on-disk database file(s) remain exactly as readable/writable as pre-v0.4 — byte-for-byte.

#### Scenario: Disabled — plaintext database unchanged

- GIVEN `encryption.enabled` is absent or `false`
- WHEN the store is opened
- THEN the on-disk file format and read/write path are identical to pre-v0.4 behavior

### Requirement: REQ-431 Keychain-Sourced Key, Generated Once

The encryption key MUST come from the OS keychain (macOS Keychain / platform equivalent), generated once on
first enable and stored there. No user-supplied passphrase MUST be required.

#### Scenario: Happy path — key generated and stored on first enable

- GIVEN `encryption.enabled = true` is set for the first time
- WHEN the store is opened
- THEN a new key is generated, stored in the OS keychain, and no passphrase prompt occurs

### Requirement: REQ-432 Transparent Encryption At Rest

Once enabled, the on-disk database file(s) MUST be encrypted such that direct file inspection (e.g. reading
raw bytes) does not reveal plaintext observation content. Encryption/decryption MUST be transparent to normal
read/write callers — no API changes to `store.Store`.

#### Scenario: Happy path — file contents are not plaintext

- GIVEN encryption is enabled and observations have been written
- WHEN the raw database file bytes are inspected directly (process not running, key not supplied)
- THEN no observation content is recoverable as plaintext

### Requirement: REQ-433 Explicit Threat Model Statement

The feature's documentation and any user-facing status surface MUST state plainly: this protects against disk
theft or a lost laptop while the process is NOT running. It does NOT protect against a live-process memory
dump or an attacker who already has the unlocked keychain.

#### Scenario: Threat model is discoverable

- GIVEN encryption is enabled
- WHEN a user inspects `omnia doctor` or the equivalent status output
- THEN the stated threat model (protects at-rest/lost-device; does not protect live-process/unlocked-keychain)
  is present in that output or its linked documentation

### Requirement: REQ-434 Keychain-Unavailable Degradation

When keychain access fails (e.g. headless server, Linux without a keychain daemon, CI), the system MUST
degrade to unencrypted-with-warning. It MUST NEVER fail the write path or lose data because the keychain is
unavailable.

#### Scenario: Edge case — keychain unavailable on a headless host

- GIVEN `encryption.enabled = true` on a host with no accessible OS keychain
- WHEN the store is opened
- THEN the store opens unencrypted, a clear warning is logged/surfaced, and normal read/write operations
  succeed without data loss

### Requirement: REQ-435 Reversible Migration

Disabling encryption on an already-encrypted store MUST transparently decrypt via the keychain key and allow
re-writing plaintext. A user MUST NEVER be locked out of their own memory by this transition in either
direction.

#### Scenario: Disable encryption on an encrypted store

- GIVEN a store previously encrypted with a valid keychain key
- WHEN `encryption.enabled` is set back to `false`
- THEN the store is transparently decrypted and continues to serve reads/writes in plaintext, with no data loss

### Requirement: REQ-436 Provenance Trust-Tag Completeness

The write-time trust tag produced by `classifyTrust` (`internal/store/provenance.go:26`) MUST be captured
consistently at write time and surfaced in both read receipts and the audit trail.

#### Scenario: Trust tag visible in a read receipt

- GIVEN an observation written with source `"user"`
- WHEN that observation is retrieved
- THEN the response/receipt includes `trust_tag: "user"`

### Requirement: REQ-437 Enforcement and Consolidation Actions Are Audited

Every enforcement-gate decision (capability 2) and every consolidation action (capability 3) MUST produce a
corresponding entry in the native audit log.

#### Scenario: Enforcement decision reaches the audit log

- GIVEN the enforcement gate (capability 2) returns any verdict
- WHEN the audit log is queried for that time range
- THEN one matching entry is present, correlated to the gate invocation

## Out of Scope (Non-Goals)

- Cloud-side encryption changes.
- Per-user multi-tenant key management.
- Protection against live-process memory inspection or an attacker holding the unlocked keychain.
