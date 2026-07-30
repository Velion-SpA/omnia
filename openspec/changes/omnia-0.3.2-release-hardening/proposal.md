# Proposal: omnia-0.3.2-release-hardening

## Intent

Remove the v0.3.2 publication blockers identified by acceptance reports before publication. Installation guidance must describe Omnia as shipped, downloads must not bypass checksum verification, and the release handoff must be auditable.

## Scope

### In Scope
- Correct `docs/INSTALLATION.md` repository, tap, binary/asset, migration, and Go references to Omnia (`Velion-SpA/omnia`, `velion-spa/tap/omnia`, Go 1.26.4).
- Make `scripts/install.sh` fail closed when checksum tooling, `checksums.txt`, or the selected archive entry is unavailable; add deterministic tests for valid, mismatch, missing-manifest, missing-entry, and unavailable-verifier paths.
- Define the Homebrew formula/release-asset path around `.goreleaser.yaml` and `.github/workflows/release.yml` (manual tap update while upload is disabled, or tested automation), with local asset/checksum validation.
- Add provenance checks binding tag, commit, toolchain, targets, assets, and checksums to published evidence.

### Out of Scope
- Product behavior, storage/schema changes, cloud sync, or runtime UX.
- Editing `docs/omniapresentacion.html`.
- Publishing v0.3.2, mutating the external tap, or claiming unavailable Linux/cloud acceptance.
- Making bilingual recall a release gate here.

## Capabilities

### New Capabilities
- `release-distribution-hardening`: Fail-closed installer verification, deterministic release asset/formula handoff, and provenance validation.

### Modified Capabilities
- None (existing specs cover product/runtime behavior, not distribution controls).

## Approach

Keep release logic thin: isolate checksum decisions in POSIX-compatible helpers and preserve archive naming. Align docs and release metadata with the module and tap. Add a validation command emitting machine-readable evidence and failing on omissions; document the manual formula update until automation is enabled. Deliver strict-TDD slices under 400 changed lines; no product code edits.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `docs/INSTALLATION.md` | Modified | Correct install references. |
| `scripts/install.sh` + tests | Modified/New | Fail-closed verification and coverage. |
| `.goreleaser.yaml`, workflow | Modified | Formula handoff and validation. |
| `scripts/` fixtures | New | Asset/checksum/provenance checks. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Legacy names remain in user workflows | Med | Keep migration notes, make current commands Omnia-first. |
| Partial releases break installs | Med | Actionable failures and prepublish missing-artifact tests. |
| Tap automation diverges | Med | Explicit manual path plus formula/checksum evidence. |

## Rollback Plan

Revert docs, installer, workflow/configuration, validation, fixtures, and tests together; no data/schema rollback is required.

## Dependencies

Go 1.26.4, GoReleaser v2, and external GitHub/tap access.

## Success Criteria

- [ ] Docs contain no stale repository/tap/asset names and state Go 1.26.4.
- [ ] Installer exits non-zero for every missing/invalid checksum condition and passes valid archives.
- [ ] Validation proves exact tag/commit, target assets, checksum coverage, and formula handoff.
- [ ] `go test ./...` and deterministic installer/release checks pass.
- [ ] `docs/omniapresentacion.html` and product/runtime behavior are unchanged.
