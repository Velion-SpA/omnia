# Design: omnia-0.3.2-release-hardening

## Technical Approach

Use a thin, POSIX-compatible release boundary: correct the installation guide, make archive verification mandatory, and add a local release-bundle validator. GoReleaser remains the sole artifact producer; the workflow performs a no-publish build, validates its `dist/` output and provenance, then publishes. Product/runtime code and `docs/omniapresentacion.html` are not touched.

## Architecture Decisions

| Decision | Choice | Alternatives / rationale |
|---|---|---|
| Checksum enforcement | Keep verification in `scripts/install.sh`; fail when no SHA-256 verifier, manifest, entry, or matching digest exists. | Do not warn-and-continue: an unverified download violates the release contract. Use `sha256sum` or `shasum -a 256` only; no new dependency. |
| Installer testing | Deterministic POSIX harness drives the script with temporary archives and stubbed `curl`, `uname`, and checksum tools. | Avoid network/OS-dependent tests and avoid test-only production flags. Cover valid, mismatch, missing manifest, missing entry, and unavailable verifier. |
| Release validation | `scripts/validate-release.sh` validates tag→commit, Go toolchain, target asset set, checksum coverage/digests, and generated formula; emits JSON evidence. | Shell keeps the gate runnable on CI and locally without adding a Go package or runtime dependency. `FORMULA_PATH`/`DIST_DIR` overrides make the path testable. |
| Homebrew handoff | Retain `.goreleaser.yaml` `skip_upload: true`; workflow validates generated formula and documents a manual copy to `Velion-SpA/homebrew-tap`. | Automatic tap mutation is external and requires a write token; enabling it here would make publication depend on unavailable credentials. |

## Data Flow

```text
v0.3.2 tag + checked-out commit
        │
        ▼
GoReleaser (no publish) ──► dist/{archives,checksums.txt,formula}
        │                                  │
        └──────────────► validate-release.sh ──► release-provenance.json
                                                        │
                                                        ▼
                                             GoReleaser publish + manual tap handoff

install.sh ──► archive + checksums.txt ──► SHA-256 verifier ──► extract only on match
```

## File Changes

| File | Action | Description |
|---|---|---|
| `docs/INSTALLATION.md` | Modify | Replace stale Gentleman-Programming/engram names, tap, assets, migration text, and Go requirement with Omnia (`Velion-SpA/omnia`, `velion-spa/tap/omnia`, Go 1.26.4); retain historical migration notes. |
| `scripts/install.sh` | Modify | Centralize fail-closed checksum decision and actionable errors; preserve archive naming, OS/arch detection, and install behavior. |
| `scripts/install_test.sh` | Create | POSIX table-driven harness using generated fixtures/stubs for five checksum outcomes. |
| `scripts/validate-release.sh` | Create | Validate release metadata/assets/checksums/formula and write deterministic machine-readable evidence. |
| `scripts/validate-release_test.sh` | Create | Exercise validator success and each omission/mismatch path with temporary dist trees. |
| `.goreleaser.yaml` | Modify | Keep Omnia target/archive/checksum/formula settings explicit and annotate the manual tap handoff while upload is disabled. |
| `.github/workflows/release.yml` | Modify | Add no-publish GoReleaser preflight, validator invocation, evidence upload, then the existing publish step. |

No changes to product packages, schemas, cloud behavior, or `docs/omniapresentacion.html`.

## Interfaces / Contracts

`validate-release.sh [--tag TAG] [--commit SHA] [--dist DIR] [--evidence FILE]` defaults to `GITHUB_REF_NAME`, `GITHUB_SHA`, `dist`, and `dist/release-provenance.json`. It MUST require the six GoReleaser targets (linux/darwin/windows × amd64/arm64, with Windows `.zip`), one checksum entry per archive, matching SHA-256 values, and a formula referencing `Velion-SpA/omnia`. Evidence contains `tag`, `commit`, `toolchain`, `targets`, `assets`, `checksums_file`, `formula`, and `formula_handoff: "manual"`.

`install.sh` MUST exit non-zero before extraction if a verifier, `checksums.txt`, selected archive entry, or digest is unavailable/invalid. Error text identifies the missing condition and archive name.

## Testing Strategy

Strict TDD (RED → GREEN → REFACTOR) per slice. Run `sh scripts/install_test.sh`, `sh scripts/validate-release_test.sh`, then `go test ./...` and `go test -cover ./...`. Keep docs/tests with the behavior they verify. Slice 1 (docs, installer, installer harness) and slice 2 (validator, GoReleaser/workflow, validator harness) each target ≤400 changed lines and can be reverted independently.

## Migration / Rollout

No data migration. Before publication, run the preflight on the tagged commit and retain JSON evidence. External prerequisites remain separate: a valid GitHub token/tag and GoReleaser v2; a maintainer with write access must copy the generated formula/checksums to `Velion-SpA/homebrew-tap` while `skip_upload` remains true; Linux/cloud acceptance requires its own environment and report and is not claimed by this change.

## Open Questions

None blocking. The validator accepts an explicit formula path so local GoReleaser output-layout differences do not weaken the release gate.
