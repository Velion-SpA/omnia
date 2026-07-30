# Apply Progress: Omnia v0.4 Memory Frontier
## PR 1 — Config Scaffolding
- Completed tasks: 1.1, 1.2, 1.3, 1.4
- Remaining tasks: 2.1–11.12 remain unchecked in `tasks.md`.
- Boundary: seven default-OFF configuration blocks and parameter defaults only; no capability wiring or runtime behavior was added.
### TDD Cycle Evidence
| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|---|---|---|---|---|---|---|---|
| 1.1 | `internal/config/v04_config_test.go` | Unit | `go test ./internal/config/...` passed before change | Test referenced the seven absent `Config` fields; compile failed | Added the config fields; test passed | v0.3.2 fixture snapshot covers non-default legacy values | N/A |
| 1.2 | `internal/config/v04_config_test.go` | Unit | Covered by 1.1 baseline | Existing RED test could not compile until the seven types existed | Added structs and parameter-only defaults; targeted test passed | Opt-in-only config test verifies documented defaults | N/A |
| 1.3 | `internal/config/v04_config_test.go` | Unit | Covered by config suite | N/A (style-only) | Config tests passed after doc/style cleanup | Skipped: structural/documentation-only task | Confirmed no v0.4 `*KeyPresent` probe is present |
| 1.4 | `internal/config/v04_config_test.go` | Unit | Covered by config suite | N/A (verification-only) | Config suite, cgo-free build, and vet passed | Skipped: verification-only task | `git diff --check` passed |
### Verification
- `go test ./internal/config/...` — passed
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `git diff --check` — passed
- The v0.3.2 fixture has no v0.4 keys and its legacy config snapshot is byte-for-byte equal to `v0.3.2-config.golden.yaml`.
### Design Deviations
None. The design, tasks, code, and security specification now consistently use `EncryptionConfig` with the `encryption` key; no `security` alias is provided.


## PR 10 — Learned Ranker
- Completed tasks: 10.1–10.12.
- Boundary: local pure-Go ranker, candidate model persistence, CLI training/eval promotion gate, and MCP-only final rerank; no repo-cartridge work.
- Stack status: development branch based on post-PR1 main; MUST rebase onto post-PR9 main before opening a PR. No issue or PR was created.
### TDD Cycle Evidence
| Tasks | RED | GREEN | REFACTOR |
|---|---|---|---|
| 10.1–10.2 | `go test ./internal/ranker` failed because feature symbols did not exist. | Added normalized existing-signal features and labels. | One schema constant protects model compatibility. |
| 10.3–10.4 | Package test failed before model APIs existed. | Added deterministic L2 logistic training and versioned persistence. | Centralized shape/schema checks in `LoadCurrent`. |
| 10.5–10.6 | `TestApplyLearnedRankerDisabledAndColdStartAreNoops` failed before the pass existed. | Disabled/nil-model paths return the original result slice unchanged. | Preserved RankResults sentinel preemption. |
| 10.7–10.10 | Model corruption/schema rejection tests failed before load validation. | Candidate is persisted, eval is run, then `current` is promoted only on success. | Composition code silently omits invalid models. |
| 10.11–10.12 | N/A | Full suite, vet, and cgo-free build passed. | Kept ranking out of `internal/recall` and retrieval store behavior. |
### Verification
- `go test ./...` — passed
- `go vet ./...` — passed
- `CGO_ENABLED=0 go build ./...` — passed
- `git diff --check` — passed
