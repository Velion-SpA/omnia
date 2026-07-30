## Verification Report

**Change**: `omnia-0.3.2-release-hardening`
**Version**: Release Distribution Hardening Specification v1
**Mode**: Strict TDD

### Completeness

| Metric | Value |
|--------|-------|
| Tasks total | 10 |
| Tasks complete | 9 |
| Tasks incomplete | 1 (`3.2`, external acceptance) |
| Planning artifacts | proposal, design, tasks, apply-progress, current verify-report read; no dedicated `spec.md` exists for this change |

### Build & Tests Execution

| Command | Result | Evidence |
|---------|--------|----------|
| `sh scripts/install_test.sh` | ✅ PASS | 8 checksum cases: valid, uppercase, mismatch, missing manifest, missing entry, invalid digest, unavailable verifier, verifier failure |
| `dash scripts/install_test.sh` | ✅ PASS | Same 8 deterministic cases |
| `sh scripts/validate-release_test.sh` | ✅ PASS | Complete bundle plus flat, nested GoReleaser `dist/homebrew/Formula/omnia.rb`, explicit `FORMULA_PATH`, and target/checksum/tag/commit/toolchain/formula-owner failures |
| `dash scripts/validate-release_test.sh` | ✅ PASS | Same fixture matrix under `dash`, including nested formula discovery |
| `dash -n scripts/validate-release.sh scripts/validate-release_test.sh` | ✅ PASS | Validator and regression harness parse cleanly after nested formula-path remediation |
| `dash -n` changed POSIX scripts | ✅ PASS | `install.sh`, `install_test.sh`, `validate-release.sh`, `validate-release_test.sh` |
| YAML parse | ✅ PASS | Ruby Psych parses `.goreleaser.yaml` and `.github/workflows/release.yml` |
| `goreleaser check --config .goreleaser.yaml` | ⚠️ VALID WITH WARNING | Config is valid; GoReleaser exits non-zero because `brews` is deprecated (existing warning) |
| `go test ./...` | ✅ PASS | All packages passed |
| `go test -cover ./...` | ✅ PASS | All packages passed; `internal/version` reports 90.6% coverage |
| `go vet ./...` | ✅ PASS | No diagnostics |
| `go build ./...` | ✅ PASS | Build completed |
| `go mod verify` | ✅ PASS | `all modules verified` |
| `go mod tidy -diff` | ✅ PASS | No diff |
| `git diff --check` | ✅ PASS | No whitespace errors |

Go toolchain: `go version go1.26.4 darwin/arm64`.

A repository-wide `dash -n` audit reports four pre-existing Bash scripts with Bash-only syntax; all are untouched and have Bash shebangs: `plugin/claude-code/scripts/session-start.sh`, `plugin/claude-code/scripts/user-prompt-submit.sh`, `plugin/claude-code/scripts/user-prompt-submit_test.sh`, and `scripts/dev-multicloud-up.sh`. The four changed POSIX scripts parse cleanly.

### Spec Compliance Matrix

| Requirement | Scenario | Test/evidence | Result |
|-------------|----------|---------------|--------|
| Canonical Omnia installation guidance | Published commands match distribution metadata | Static inspection of `docs/INSTALLATION.md`, README, Go update guidance, `.goreleaser.yaml` | ✅ COMPLIANT |
| Canonical Omnia installation guidance | Stale reference is detected | Manual stale-reference scan; no dedicated docs validator exists | ⚠️ PARTIAL |
| Installer checksum verification fails closed | Valid archive is accepted | `scripts/install_test.sh` / `run_case valid`, `run_case uppercase` | ✅ COMPLIANT |
| Installer checksum verification fails closed | Invalid or unavailable inputs rejected | `scripts/install_test.sh` mismatch/missing/invalid/unavailable/verifier-failure cases | ✅ COMPLIANT |
| Installer checksum verification fails closed | Deterministic error-path coverage | `sh` and `dash` harness runs, stable diagnostics and no extraction markers | ✅ COMPLIANT |
| Release asset/Homebrew handoff locally verifiable | Manual tap path auditable | `scripts/validate-release_test.sh` covers flat and real GoReleaser nested `dist/homebrew/Formula/omnia.rb` layouts plus explicit `FORMULA_PATH`; workflow assertions and formula fixture | ✅ COMPLIANT |
| Release asset/Homebrew handoff locally verifiable | Asset/checksum omission blocks handoff | Validator missing-target, omission, mismatch cases | ✅ COMPLIANT |
| Provenance evidence binds release | Complete provenance emitted | Validator complete/uppercase fixtures; deterministic JSON repeat check | ✅ COMPLIANT |
| Provenance evidence binds release | External acceptance remains explicit | Evidence asserts `formula_handoff: manual`, Linux/cloud `blocked`, publication/tap `pending` | ✅ COMPLIANT |

**Compliance summary**: 8/9 scenarios fully compliant; 1 partial (documentation stale-reference detection is manual because no docs-validation executable is in scope).

### Correctness (Static Evidence)

| Requirement | Status | Notes |
|------------|--------|-------|
| Installer fail-closed checksum contract | ✅ Implemented | Verifier, manifest, exact archive entry, digest format, digest match, and verifier execution failures all stop before extraction. |
| Release validator contract | ✅ Implemented | Six canonical targets, checksum coverage/digests, tag→commit binding, Go 1.26.4, formula owner, deterministic JSON evidence. |
| Homebrew handoff | ✅ Implemented | `.goreleaser.yaml` keeps `skip_upload: true`; workflow preserves validated `dist/` and documents manual tap handoff. |
| Product/runtime boundary | ✅ Preserved | No product behavior or cloud/schema changes; `internal/version` edits are release/update guidance only. `docs/omniapresentacion.html` is untouched. |

### Coherence (Design)

| Decision | Followed? | Notes |
|----------|-----------|-------|
| POSIX shell release boundary | ✅ Yes | Changed installer/validator and harnesses pass under `sh` and `dash`. |
| GoReleaser sole artifact producer | ✅ Yes | Workflow performs no-publish preflight, validates the same `dist/`, then uploads exact assets with `gh`. |
| Manual Homebrew tap while upload disabled | ✅ Yes | Formula and checksums remain evidence; external mutation is not attempted. |
| External Linux/cloud gates explicit | ✅ Yes | Validator emits pending/blocked states and tests never infer runtime acceptance. |

### TDD Compliance

| Check | Result | Details |
|-------|--------|---------|
| TDD evidence reported | ✅ | `apply-progress.md` contains RED → GREEN → REFACTOR evidence for tasks 1.1–2.4. |
| All implementation tasks have tests | ✅ | Installer and validator harnesses exist and execute successfully. |
| RED confirmed (test files exist) | ✅ | `scripts/install_test.sh`, `scripts/validate-release_test.sh`, and modified Go tests exist. |
| GREEN confirmed (tests pass) | ✅ | Both harnesses pass under `sh` and `dash`; `go test ./...` passes. |
| Triangulation adequate | ✅ | Installer has 8 cases; validator covers complete output and omission/mismatch/tag/toolchain/formula failures. |
| Safety net | ⚠️ | New shell harnesses are new files; existing Go package suite passed. |

**TDD Compliance**: 5/6 checks fully evidenced (safety-net classification is informational).

### Test Layer Distribution

| Layer | Tests | Files | Tools |
|-------|-------|-------|-------|
| Unit | Go update-check tests | 1 modified Go test file | `go test` |
| Integration | Installer and release validator fixture harnesses | 2 shell test files | POSIX `sh`/`dash` |
| E2E | 0 | 0 | Not run; external publication/runtime out of scope |
| **Total** | **3 test files** | **3** | |

### Changed File Coverage

| File | Line % | Branch % | Uncovered Lines | Rating |
|------|--------:|---------:|----------------|--------|
| `internal/version/check.go` (package report) | 90.6% | — | Not emitted by `go test -cover` | ✅ Excellent |
| `scripts/install.sh` | Harness-covered (8 cases) | — | No instrumentation | ✅ Behavioral |
| `scripts/validate-release.sh` | Harness-covered (complete + failure matrix) | — | No instrumentation | ✅ Behavioral |

Shell coverage percentages are unavailable; deterministic fixture harnesses provide behavioral coverage instead.

### Assertion Quality

✅ All assertions in the changed shell and Go test files exercise production code or scripts and verify outcomes, diagnostics, side effects, and deterministic evidence. No tautologies, ghost loops, smoke-only checks, or mock-heavy assertions found.

### Issues Found

**CRITICAL**:
1. Task `3.2` remains incomplete: publication, Homebrew tap mutation, Linux/container runtime, and authenticated cloud acceptance are unavailable. This is an external release gate, not a product/runtime defect; the task must remain unchecked and archive readiness is blocked.

**WARNING**:
1. `goreleaser check --config .goreleaser.yaml` exits non-zero because the existing `brews` property is deprecated, although the configuration is otherwise valid.
2. Repository-wide `dash -n` is not clean for four untouched Bash scripts; changed POSIX release scripts are clean under `dash`.
3. Documentation stale-reference detection is a manual/static check; no dedicated executable docs validator is present.

**SUGGESTION**: Migrate the GoReleaser `brews` configuration to the supported replacement when external tap automation policy is decided.

### External Gates

The recorded external-acceptance report is `/tmp/omnia-v032-real-acceptance-20260729.md`. It deliberately separates local implementation evidence from unavailable publication/runtime gates. Counts below preserve the acceptance record (including the historical pre-remediation validator-path observation):

| Status | Count | Evidence |
|---|---:|---|
| **PASS** | **5** | (1) Go 1.26.4 + GoReleaser 2.17.0 installed and snapshot produced six archives, `checksums.txt`, and `dist/homebrew/Formula/omnia.rb`; (2) `Velion-SpA/homebrew-tap` exists, formula is readable, and account has admin/maintain/push API permissions; (3) public cloud `/health` returned HTTP 200 and protected routes returned HTTP 401; (4) `docker compose -f docker-compose.cloud.yml config` parsed; (5) bounded local Ollama bilingual recall passed 10/10 ES↔EN pairs in 1.72s. |
| **WARNING** | **2** | No local or remote `v0.3.2` tag/release exists (latest published release is `v0.3.1`); `goreleaser check` exits 2 solely for the existing deprecated `brews` property. |
| **BLOCKED** | **4** | Historical validator integration exposed the pre-remediation formula-path mismatch; GitHub publication was not authorized/credentialed; Homebrew tap mutation was not authorized and v0.3.2 assets are not published; Docker/Linux runtime and authenticated cloud acceptance remain unavailable. |

#### Post-remediation reconciliation

The historical validator blocker is resolved locally: default discovery now includes the real GoReleaser path `dist/homebrew/Formula/omnia.rb`, while explicit `FORMULA_PATH` remains authoritative. Fresh `sh` and `dash` validator harnesses pass, including flat, nested, and explicit-path fixtures. This remediation does **not** satisfy external task `3.2`; publication, tap mutation, Linux runtime, and authenticated cloud gates remain blocked, so task `3.2` stays unchecked.

### Verdict

**Implementation/remediation: PASS WITH WARNINGS.** All in-scope installer/release-validator tests, builds, coverage, module checks, and local handoff scenarios pass; the nested formula path is corrected.

**Overall release readiness: NOT READY / BLOCKED.** Task `3.2` remains unchecked because publication, tap mutation, Linux runtime, and authenticated cloud acceptance require external credentials/environments.
