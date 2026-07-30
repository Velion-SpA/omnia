# Apply Progress: omnia-0.3.2-release-hardening

## Batch

- **Delivery strategy:** ask-on-risk
- **Chain strategy:** stacked-to-main
- **PR boundary:** PR 2 — release validator, provenance evidence, and GoReleaser/workflow handoff (stacked on PR 1)
- **Mode:** Strict TDD (RED → GREEN → REFACTOR)

## Completed Tasks

- [x] 1.1 Add deterministic POSIX installer fixtures/stubs and checksum outcome coverage.
- [x] 1.2 Require a SHA-256 verifier, manifest, exact archive entry, valid digest, and matching digest before extraction.
- [x] 1.3 Centralize checksum diagnostics and keep installer logic POSIX-compatible.
- [x] 1.4 Align installation documentation with Omnia repository, tap, assets, toolchain, and migration-only legacy references.
- [x] 2.1 Add deterministic release-validator fixtures covering complete output, missing target, checksum omission/mismatch, wrong tag/commit/toolchain, and formula-owner failures.
- [x] 2.2 Create `scripts/validate-release.sh` to bind tag/commit, require Go 1.26.4, validate six target assets/checksums/formula, and emit deterministic JSON provenance.
- [x] 2.3 Keep validator layout-testable via `--dist`/`--evidence` and preserve explicit pending/blocked external acceptance states.
- [x] 2.4 Document manual Homebrew tap handoff with upload disabled; wire release workflow no-publish preflight, validator, evidence upload, and publish.

## TDD Cycle Evidence

| Task | RED | GREEN | REFACTOR |
|---|---|---|---|
| 1.1 | Added `scripts/install_test.sh`; it failed because missing manifests were accepted and extraction continued. | N/A (test harness task) | Harness now covers seven outcomes (including malformed digest and verifier failure) with controlled PATH stubs, deterministic diagnostics, and extraction markers. |
| 1.2 | Existing installer behavior demonstrated by the failing missing-manifest case. | `verify_checksum` now fails closed for unavailable verifier/manifest/entry/invalid digest/mismatch and verifier execution failure; valid case extracts. | Digest output is written before parsing so verifier exit status cannot be masked by an `awk` pipeline. |
| 1.3 | The RED harness asserted stable failure diagnostics and no extraction. | All seven scenarios pass under `sh` and `dash`. | POSIX shell constructs only; verifier selection supports `sha256sum` and `shasum -a 256`. |
| 1.4 | Documentation and live update hints contained stale repository/module references and implied unverified manual downloads. | Updated `README.md`, `docs/INSTALLATION.md`, and `internal/version/check.go` with canonical clone/build guidance, architecture-aware same-release `checksums.txt` verification for Windows/manual assets, and a fail-closed PowerShell check requiring one archive, one matching entry, a 64-hex digest, and successful hash computation before extraction. | The explicitly labeled historical beta guide retains its version-pinned legacy command; current docs and update hints contain no stale module path. |
| 2.1 | New validator harness initially failed because `scripts/validate-release.sh` did not exist. | Harness passes complete (including uppercase checksum manifests and invalid-tag escaping) and all omission/mismatch/toolchain/formula-owner scenarios with temporary fixtures and deterministic diagnostics. | `git` and `go` stubs isolate tag/toolchain checks; no network, publication, tap mutation, Linux runtime, or cloud fixture is used. |
| 2.2 | RED harness failed on missing validator implementation. | Validator binds `${TAG}^{commit}` to the claimed SHA, enforces Go 1.26.4, validates six canonical archives and SHA-256 entries, checks formula ownership, and emits machine-readable provenance. | Checksums are generated/validated with `sha256sum` or `shasum -a 256`; external acceptance is never inferred locally. |
| 2.3 | Initial implementation needed shell syntax cleanup before green. | `--dist`, `--evidence`, and `FORMULA_PATH` seams support layout tests; fixed target ordering and no timestamps make JSON byte-for-byte deterministic; SHA-256 values are normalized to lowercase and tag/commit/formula-path inputs are restricted to JSON-safe characters. | POSIX `sh`/`dash` syntax validated; formula path is normalized relative to dist for reproducible evidence. |
| 2.4 | Workflow previously published directly with no local evidence gate, and a clean publish rebuild could invalidate validated provenance. | `.goreleaser.yaml` keeps `skip_upload: true` with manual `Velion-SpA/homebrew-tap` instructions; workflow builds `--skip=publish`, validates/uploads evidence, then `gh release create --verify-tag` uploads the exact six validated archives plus checksums. | GoReleaser remains the sole artifact producer; `gh` is only the release uploader. External tap mutation, Linux runtime, and cloud acceptance remain pending/blocked. |

## Verification

- `sh scripts/install_test.sh` — PASS
- `dash scripts/install_test.sh` — PASS
- `dash -n scripts/install.sh scripts/install_test.sh` — PASS
- `git diff --check` — PASS
- `goreleaser check --config .goreleaser.yaml` — configuration valid; existing `brews` deprecation warning remains
- Workflow static assertions — one GoReleaser preflight, `gh release create --verify-tag`, exact six archives, and checksums upload; no post-validation clean rebuild
- Workflow comment cleanup — documents single preflight plus exact `gh` upload; YAML parse and `git diff --check` PASS
- Unsupported-OS installer guidance now points to canonical clone/build commands; no external `github.com/velion/...@latest` hint remains.
- `go test ./internal/version` — PASS; current update instructions assert the stale module path is absent.
- `docs/omniapresentacion.html` unchanged
- No product/runtime package changes
- `sh scripts/validate-release_test.sh` — PASS
- `sh scripts/install_test.sh` — PASS (including uppercase checksum manifest)
- `dash scripts/validate-release_test.sh` — PASS
- `dash -n scripts/validate-release.sh scripts/validate-release_test.sh` — PASS
- `git diff --check` — PASS
- `docs/omniapresentacion.html` unchanged

### Task 3.1 Verification Evidence (2026-07-29)

- `sh scripts/install_test.sh` — PASS (8 checksum cases)
- `dash scripts/install_test.sh` — PASS (8 checksum cases)
- `sh scripts/validate-release_test.sh` — PASS
- `dash scripts/validate-release_test.sh` — PASS
- `dash -n` changed POSIX scripts (`scripts/install.sh`, `scripts/install_test.sh`, `scripts/validate-release.sh`, `scripts/validate-release_test.sh`) — PASS
- YAML parse (`.goreleaser.yaml`, `.github/workflows/release.yml`, Ruby Psych) — PASS
- `goreleaser check --config .goreleaser.yaml` — configuration valid; exits non-zero only for the existing `brews` deprecation warning
- `go test ./...` — PASS
- `go test -cover ./...` — PASS; `internal/version` package 90.6% line coverage (per-package output)
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `go mod verify` — PASS (`all modules verified`)
- `go mod tidy -diff` — PASS (no diff)
- `git diff --check` — PASS
- Changed paths are release/docs guidance and test/SDD artifacts; no product/runtime behavior was changed. `docs/omniapresentacion.html` has no status or diff entry.
- Repository-wide `dash -n` also reports syntax errors in four pre-existing Bash scripts (`plugin/claude-code/scripts/session-start.sh`, `plugin/claude-code/scripts/user-prompt-submit.sh`, `plugin/claude-code/scripts/user-prompt-submit_test.sh`, `scripts/dev-multicloud-up.sh`); these are untouched and use Bash shebangs.

Task 3.2 remains pending/blocked: external publication, tap mutation, and Linux/cloud acceptance require credentials/environments outside this checkout.

## Remaining Tasks (PR 2)

- [x] 2.1–2.3 Release validator and deterministic provenance tests/implementation.
- [x] 2.4 GoReleaser manual tap handoff comments and release workflow preflight/evidence upload.
- [x] 3.1 Full project and coverage verification.
- [ ] 3.2 External publication, tap update, and Linux/cloud acceptance evidence.

### Task 3.2 External Acceptance Evidence (2026-07-29)

- **PASS:** Go 1.26.4 and GoReleaser 2.17.0 are installed; `goreleaser release --snapshot --clean --config .goreleaser.yaml` completed successfully and produced six target archives, `checksums.txt`, and a formula (local-only, no publication).
- **PASS:** `Velion-SpA/homebrew-tap` exists and its `Formula/omnia.rb` is readable; the authenticated GitHub account reports admin/maintain/push permission. No tap mutation was performed.
- **PASS:** Public cloud probe `GET https://omnia.velioncorp.cl/health` returned HTTP 200 `{"service":"omnia-cloud","status":"ok"}`; unauthenticated protected routes correctly returned HTTP 401.
- **PASS:** `docker compose -f docker-compose.cloud.yml config` parses successfully.
- **WARNING:** No local or remote `v0.3.2` tag/release exists; only `v0.3.1` is published. `goreleaser check` exits 2 solely for the existing deprecated `brews` configuration.
- **HISTORICAL (pre-remediation):** Validator integration against a real GoReleaser-shaped bundle failed `missing generated formula: omnia.rb` because GoReleaser emits `dist/homebrew/Formula/omnia.rb`; the remediation below adds that nested path to default discovery. Exact reproduction is retained at `/tmp/omnia-v032-validator-path-20260729`.
- **BLOCKED:** GitHub publication and tap mutation were not attempted (explicit no-mutation constraint; no `GITHUB_TOKEN`/`HOMEBREW_TAP_TOKEN` env credentials). Linux/container runtime is blocked because Docker Desktop daemon is unavailable (`Cannot connect to the Docker daemon ... docker.sock`). Authenticated cloud acceptance is blocked: available `ENGRAM_CLOUD_TOKEN` was rejected as `unauthorized: invalid bearer token` by the public endpoint.

Task 3.2 remains unchecked; external publication, tap update, Linux runtime, and authenticated cloud acceptance are not proven.

### Task 3.2 Release-Validator Remediation (2026-07-29)

- **RED:** Extended `scripts/validate-release_test.sh` with a GoReleaser-shaped fixture placing the formula at `dist/homebrew/Formula/omnia.rb`; the pre-fix validator failed with `missing generated formula: omnia.rb`.
- **GREEN:** Added `dist/homebrew/Formula/omnia.rb` to the validator's default discovery candidates without changing explicit `FORMULA_PATH` handling or existing failure diagnostics.
- **Triangulation:** The harness now covers flat and nested formula layouts plus an explicit `FORMULA_PATH` override; `sh` and `dash` runs pass.
- **Focused checks:** `dash -n scripts/validate-release.sh scripts/validate-release_test.sh`, YAML parsing for `.goreleaser.yaml` and `.github/workflows/release.yml`, and `git diff --check` all pass.
- **Boundary:** This fixes local validator preflight evidence only. External publication, tap mutation, Linux runtime, and authenticated cloud acceptance remain task 3.2 pending/blocked and are not marked complete.
