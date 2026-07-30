# Release Distribution Hardening Specification

## Purpose

Define the publication contract for Omnia v0.3.2 so installation, release handoff, and provenance evidence are deterministic and auditable without changing product or cloud runtime behavior.

## Requirements

### Requirement: Canonical Omnia installation guidance

`docs/INSTALLATION.md` MUST describe the shipped Omnia distribution using repository `Velion-SpA/omnia`, tap `velion-spa/tap/omnia`, the actual GoReleaser archive/binary names, and Go toolchain version 1.26.4. Legacy Engram references MAY appear only in explicit migration notes; current commands MUST be Omnia-first.

#### Scenario: Published commands match distribution metadata

- GIVEN the repository, tap, and release metadata are inspected
- WHEN installation examples are checked
- THEN every repository, tap, asset, migration, and Go-version reference resolves to the Omnia v0.3.2 path

#### Scenario: Stale reference is detected

- GIVEN an installation document contains a stale repository, tap, asset, or toolchain value
- WHEN documentation validation runs
- THEN validation fails and identifies the offending reference

### Requirement: Installer checksum verification fails closed

The installer MUST require an available checksum verifier, `checksums.txt`, and an exact entry for the selected archive before extraction. A missing verifier, manifest, entry, malformed checksum, or mismatch MUST produce a non-zero exit and an actionable error; the archive MUST NOT be installed.

#### Scenario: Valid archive is accepted

- GIVEN the verifier, manifest entry, and archive digest all agree
- WHEN the installer runs
- THEN it exits zero and proceeds with the existing archive installation flow

#### Scenario: Invalid or unavailable verification inputs are rejected

- GIVEN any of: digest mismatch, missing `checksums.txt`, missing selected-archive entry, or unavailable verifier
- WHEN the installer runs
- THEN it exits non-zero, names the failed condition, and performs no installation

#### Scenario: Deterministic error-path coverage

- GIVEN fixture archives and manifests for valid, mismatch, missing-manifest, missing-entry, and unavailable-verifier cases
- WHEN installer tests run
- THEN each case produces the specified exit status and stable diagnostic without network access

### Requirement: Release asset and Homebrew handoff is locally verifiable

Release configuration MUST preserve canonical archive naming and declare the Homebrew formula handoff through `.goreleaser.yaml` and `.github/workflows/release.yml`. The handoff MUST state whether tap upload is manual (while disabled) or automated. Local validation MUST prove target assets exist and every published asset has checksum coverage before handoff.

#### Scenario: Manual tap path is auditable

- GIVEN tap upload is disabled
- WHEN a release candidate is prepared
- THEN generated formula, release assets, checksums, and documented operator steps are retained as handoff evidence

#### Scenario: Asset/checksum omission blocks handoff

- GIVEN a target archive or its checksum entry is absent
- WHEN local release validation runs
- THEN validation fails before publication and identifies the missing artifact

### Requirement: Provenance evidence binds the release

The release evidence MUST record the exact tag, commit, Go toolchain, target matrix, asset filenames and digests, and formula handoff status. Evidence MUST be machine-readable and reproducible from the candidate checkout.

#### Scenario: Complete provenance is emitted

- GIVEN a candidate tag and commit produce the declared assets and checksums
- WHEN provenance validation runs
- THEN it emits evidence binding all required fields and passes

#### Scenario: External acceptance gates remain explicit

- GIVEN cloud or Linux runtime prerequisites are unavailable locally
- WHEN provenance validation runs
- THEN evidence marks those checks as external pending/blocked and MUST NOT claim them satisfied through local fixtures or unit tests
