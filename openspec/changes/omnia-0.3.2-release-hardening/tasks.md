# Tasks: Omnia v0.3.2 Release Hardening

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~600–700 across docs, installer, two validators/tests, and release wiring |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 2 (stacked to main) |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Omnia docs + fail-closed installer | PR 1 | Tests and docs travel with installer; independently revertible; base `feat/v0.3.2-release-hardening`. |
| 2 | Release validator + GoReleaser/workflow handoff | PR 2 | Base PR 1 branch; validator evidence and CI preflight remain independently testable. |

## Phase 1: Installer and Documentation (PR 1)

- [x] 1.1 [RED] Add `scripts/install_test.sh` POSIX fixtures/stubs covering valid, checksum mismatch, missing `checksums.txt`, missing archive entry, and unavailable verifier; assert non-zero failures, stable diagnostics, and no extraction.
- [x] 1.2 [GREEN] Refactor `scripts/install.sh` checksum path to require `sha256sum` or `shasum -a 256`, manifest, exact archive entry, valid digest, and match before `tar`; preserve archive naming and install flow.
- [x] 1.3 [REFACTOR] Centralize actionable checksum errors and keep the script POSIX (`dash`/BusyBox compatible); rerun `sh scripts/install_test.sh`.
- [x] 1.4 Update `docs/INSTALLATION.md` to `Velion-SpA/omnia`, `velion-spa/tap/omnia`, actual `omnia_*` assets, Go 1.26.4, and explicit migration-only legacy references; validate examples against repository metadata.

## Phase 2: Release Evidence and Handoff (PR 2)

- [x] 2.1 [RED] Add `scripts/validate-release_test.sh` temporary `dist/` fixtures for complete output plus missing target, checksum omission/mismatch, wrong tag/commit/toolchain, and formula-owner failures; assert JSON evidence and non-zero diagnostics.
- [x] 2.2 [GREEN] Create `scripts/validate-release.sh [--tag --commit --dist --evidence]` validating six GoReleaser targets, archive naming, checksum coverage/digests, Go 1.26.4, formula reference, and `formula_handoff:"manual"`; emit deterministic `release-provenance.json`.
- [x] 2.3 [REFACTOR] Make validator layout-testable via `DIST_DIR`/`FORMULA_PATH` (or equivalent), quote shell inputs, and keep external Linux/cloud checks explicitly pending rather than inferred from fixtures.
- [x] 2.4 Update `.goreleaser.yaml` comments/settings to preserve `skip_upload: true` and manual `Velion-SpA/homebrew-tap` handoff; update `.github/workflows/release.yml` with no-publish preflight → validator/evidence upload → existing publish.

## Phase 3: Verification and External Gates

- [x] 3.1 Run installer/release harnesses, full Go test/coverage/build/vet/module checks, shell/YAML validation, and confirm no product/runtime package changes or `docs/omniapresentacion.html` changes (evidence recorded in `apply-progress.md`).
- [ ] 3.2 External acceptance (not repo implementation): execute tagged GoReleaser/GitHub publication with valid credentials, manually copy formula/checksums to `Velion-SpA/homebrew-tap`, and obtain separate Linux/cloud runtime reports; record pending/blocked evidence when unavailable.

> **Remediation evidence (2026-07-29):** Local validator preflight now discovers the real GoReleaser formula path `dist/homebrew/Formula/omnia.rb`; external publication, tap mutation, Linux runtime, and cloud acceptance remain pending/blocked, so task 3.2 stays unchecked.
