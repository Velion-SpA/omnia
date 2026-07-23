# Design: omnia-structural-forgetting (living memory, cheap code anchor)

**Change**: omnia-structural-forgetting
**Artifact store**: hybrid (engram + openspec)
**Primitive**: P1 — Living memory / structural forgetting (0.2 flagship)

---

## Technical Approach

Ship an ADDITIVE, opt-in layer over the existing conflict/relation substrate. A memory optionally carries one or more **cheap code anchors** (`file + symbol + git-blame line range + blame SHA + content hash`) captured at `mem_save`. A reconcile pass (`omnia forget-scan`, later the subconscious daemon) re-blames each anchor; when the anchored range **materially changed** since the stored SHA it is marked stale — **never hard-deleted** — the memory is downranked at retrieval and surfaced for confirmation via the existing `review_after` spaced-repetition field. Candidate generation **rides memory-conflict-semantic's reserved `FindCandidates`/`ScanProject` seam** by adding a third candidate SOURCE (anchor-changed) beside FTS5 (Phase 3) and embeddings (Phase 5). Git access is by shelling to the system `git` binary, matching the `internal/llm` AgentRunner precedent, so nothing new touches cgo. The full tree-sitter AST graph is explicitly deferred.

---

## Architecture Decisions

### Decision: git access = shell out to system `git` (primary); go-git = optional fallback
| Option | Tradeoff | Decision |
|--------|----------|----------|
| Shell to `git` (`blame -L`, `hash-object`, `rev-parse`, `log -L`) | Zero deps, cgo-free by definition, exact `-L` range blame, mirrors `internal/llm` shell-out | **Chosen (primary)** |
| `go-git` (pure Go, cgo-free) | Compiles under `CGO_ENABLED=0`, but blame is slow/memory-heavy and `-L` range support is weak/buggy | Fallback only; NOT required for the slice |
| cgo tree-sitter AST graph | Breaks `CGO_ENABLED=0`; the locked risk we are dodging | Rejected / deferred |

**Rationale**: shelling to `git` sidesteps BOTH the cgo tree-sitter risk AND go-git's blame immaturity. `git` on PATH inside a checkout is a safe assumption for the target user (a developer). The layer degrades gracefully: no `git`, or not-a-repo → anchoring is silently skipped, `mem_save` never fails.

### Decision: anchors in a separate `memory_anchors` table (1:N), not columns on `observations`
| Option | Tradeoff | Decision |
|--------|----------|----------|
| New `memory_anchors` table keyed by obs `sync_id` | One memory → many code sites; clean lifecycle/status per anchor | **Chosen** |
| Nullable columns on `observations` | Forces exactly one anchor; pollutes the hot row | Rejected |

**Rationale**: bugfix memories legitimately span multiple files/symbols. Mirrors the `memory_relations` precedent (own table, `sync_id` keys, cross-machine portable).

### Decision: invalidation = supersede/tombstone (anchor_status + review_after), never hard-delete
| Option | Tradeoff | Decision |
|--------|----------|----------|
| Mark `anchor_status='stale'` + `staled_at` + set obs `review_after=now` | Non-destructive, surfaces for user "still true?", downranked at recall | **Chosen** |
| System-provenance `supersedes` row in `memory_relations` when a newer memory exists | Reuses conflict substrate for true replacement | **Chosen (when a target memory exists)** |
| Hard-delete the memory | Violates locked decision + provable-forgetting substrate | Rejected |

**Rationale**: honors the locked decision. Terminal write is `Store.MarkAnchorStale` (parallel to `JudgeBySemantic`, system provenance). Hard deletion stays owned by the separate `omnia-provable-forgetting` slice.

### Decision: ride the `FindCandidates`/`ScanProject` seam via a new candidate SOURCE
| Option | Tradeoff | Decision |
|--------|----------|----------|
| Add `CandidateSource ∈ {fts, embedding, anchor}` + an `AnchorProvider` into existing `ScanOptions`; reuse the worker pool + system-provenance write path | No second scanning pipeline; matches proposal's "extend, not fork" | **Chosen** |
| New standalone staleness scanner | Forks the reserved seam; violates locked decision | Rejected |

**Rationale**: the seam's job is candidate GENERATION feeding the judge/mark loop. Structural forgetting adds a deterministic anchor-diff generator alongside FTS5/embeddings; the terminal mark (`MarkAnchorStale`) is analogous to `JudgeBySemantic`, not a parallel mechanism.

### Decision: materiality = deterministic normalized-hash + line-delta (primary); LLM confirm optional
| Option | Tradeoff | Decision |
|--------|----------|----------|
| Normalized content-hash of the range changed AND non-whitespace line-delta ≥ threshold | Zero-cost, deterministic, cgo-free | **Chosen (default)** |
| Reuse `internal/llm` AgentRunner to judge "does this diff change what the memory claims?" | Higher fidelity; opt-in cost | Optional `--semantic` gate |

**Rationale**: keeps the default pass free and offline; reuses the shipped AgentRunner for the optional high-fidelity gate — no new LLM plumbing.

### Decision: refactor travel before declaring stale
Before marking stale, re-locate the anchor by `symbol` + content-hash at a new line range; if the same body is found shifted, UPDATE `line_start/line_end` (memory travels with the refactor) instead of staling. This is what makes it "living" memory rather than a brittle line pin.

---

## Data Flow

```
mem_save(code_anchors?) ──► internal/anchor.Probe (shell git blame/hash/rev-parse)
                              └─► Store: INSERT memory_anchors (sha, hash, range, status=active)

omnia forget-scan ──► ScanProject{Source:anchor} ──► AnchorProvider
     re-blame each active anchor ──► hash unchanged? ─ yes ─► skip
                                     hash changed  ─► symbol relocated? ─ yes ─► UPDATE range (travel)
                                                                          no  ─► material? (delta ≥ thr
                                                                                  or optional LLM judge)
                                                                                  └─► MarkAnchorStale
                                                                                      + obs.review_after=now
                                                                                      (+ supersedes row if
                                                                                       a newer memory exists)

mem_search ──► recall.Fuse (pure) ──► mcp wiring: stale-anchor penalty × score
                                        + receipt line "anchor <file>:<lines> changed <old→new sha>"
mem_review ──► surfaces review_after memories: "code changed — still true? [keep/forget]"
```

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/anchor/probe.go` | Create | `Probe` with injectable `runGit(ctx,args,dir) ([]byte,error)`; `Blame`, `RangeHash`, `HeadSHA`, `Locate(symbol)`; graceful no-git/not-repo degradation |
| `internal/anchor/probe_test.go` | Create | Table-driven parser tests via fake `runGit` (porcelain blame, hash, rev-parse) — the parsing risk surface |
| `internal/store/store.go` | Modify | Add `memory_anchors` table + indexes in `migrate()` (`CREATE TABLE IF NOT EXISTS`, additive) |
| `internal/store/anchors.go` | Create | `UpsertAnchor`, `ListActiveAnchors`, `UpdateAnchorRange` (travel), `MarkAnchorStale` (system provenance) |
| `internal/store/anchors_test.go` | Create | Real-SQLite: insert, travel-update, stale idempotency, provenance, review_after set |
| `internal/store/relations.go` | Modify | `ScanOptions.Source` enum + `AnchorProvider`; anchor candidates flow the existing worker pool; extend `ScanResult` with `AnchorsChecked/Traveled/Staled` |
| `internal/store/scan_anchor_test.go` | Create | Fake-provider `ScanProject{Source:anchor}` counters, travel vs stale, cap isolation |
| `internal/mcp/mcp.go` | Modify | `mem_save` gains optional `code_anchors: [{file,symbol,line_start,line_end}]`; resolves via `internal/anchor`; recall wiring applies stale penalty + receipt line |
| `internal/mcp/mcp_test.go` | Modify | anchor persisted on save; stale memory downranked + receipt present; save without anchors unchanged |
| `cmd/omnia/forget_scan.go` | Create | `omnia forget-scan [--project] [--semantic] [--apply] [--repo <path>]` reconcile command |
| `cmd/omnia/forget_scan_test.go` | Create | flag parsing; dry-run vs apply; not-a-repo graceful skip |
| `docs/STRUCTURAL_FORGETTING.md` | Create | anchor contract, `forget-scan` UX, staleness semantics, flag |

No deletions. No destructive migration.

---

## Interfaces / Contracts

```go
// internal/anchor
type Probe struct {
    runGit func(ctx context.Context, args []string, dir string) ([]byte, error) // injectable
}
type Anchor struct {
    File, Symbol           string
    LineStart, LineEnd     int
    BlameSHA, BlameAt      string
    ContentHash            string // sha256 of normalized range bytes
}
func (p *Probe) Capture(ctx context.Context, repo, file, symbol string, start, end int) (Anchor, error)
func (p *Probe) Recheck(ctx context.Context, repo string, a Anchor) (changed bool, cur Anchor, err error)

// internal/store — memory_anchors
// id, sync_id UNIQUE, obs_sync_id, repo_root, file_path, symbol,
// line_start, line_end, blame_sha, blame_at, content_hash,
// anchor_status ('active'|'stale'|'traveled'), created_at, checked_at, staled_at
func (s *Store) MarkAnchorStale(p MarkAnchorStaleParams) error // system provenance + sets obs.review_after

// internal/store/relations.go
type CandidateSource int // SourceFTS (default) | SourceEmbedding | SourceAnchor
// ScanOptions.Source + ScanOptions.AnchorProvider extend the reserved seam.
```

`mem_save` extension is backward-compatible: `code_anchors` omitted → identical to today.

---

## Testing Strategy (strict TDD active — `go test ./...`)

| Layer | What | Approach |
|-------|------|----------|
| Unit | git porcelain-blame / hash / rev-parse parsing; no-git degradation | fake `runGit` fixtures |
| Unit | normalized-hash equality, line-delta materiality, symbol relocation | pure funcs |
| Store | anchor upsert, travel-update, `MarkAnchorStale` idempotency + provenance + `review_after` | real SQLite `:memory:` |
| Store | `ScanProject{Source:anchor}` counters, travel-vs-stale, per-anchor isolation, cap | fake AnchorProvider |
| MCP | anchor captured on `mem_save`; stale downrank + receipt; unanchored save unchanged | in-process harness |
| CLI | `forget-scan` dry-run/apply; not-a-repo skip; `--semantic` optional gate | `runGit`/runner injection |
| Regression | all Phase 3/4 conflict tests GREEN; `CGO_ENABLED=0` build GREEN | CI |

---

## Migration / Rollout

Additive `CREATE TABLE IF NOT EXISTS memory_anchors` in `migrate()`; no backfill, no schema break. Feature is opt-in on both ends: anchors only exist if `mem_save` supplies `code_anchors`; staleness only runs on explicit `omnia forget-scan`. Flag `structural_forgetting.enabled` (default false, no-op without anchors) gates the recall downrank. Rollout order: (1) `internal/anchor` shell-out leaf; (2) `memory_anchors` table + store methods; (3) `mem_save` capture; (4) `ScanProject` anchor source + `forget-scan`; (5) recall downrank + receipt. Each lands GREEN independently.

---

## Open Questions

- [ ] Anchor capture is explicit-only this slice (agent supplies `code_anchors`). Auto-inference from recently-edited files is deferred — confirm that is acceptable for the flagship demo.
- [ ] Materiality default threshold (non-whitespace line-delta ratio) needs one empirical tuning pass; starts conservative (any non-ws change in range = candidate, LLM/`review_after` decides).
- [ ] `repo_root` resolution when a project spans multiple repos — v1 assumes one repo per project cwd.
- [ ] Cross-machine portability: blame SHAs are repo-global but anchors sync via `sync_id`; a peer without the repo checkout can store but not re-check — reconcile is host-local by design.
