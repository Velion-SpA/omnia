# Omnia — Obligations

Self-contained work orders for finishing the Omnia cloud/multi-device/multi-cloud
implementation and getting it **ready to deploy to a homelab server and test across
two Linux notebooks**.

Each `OBL-*.yaml` is written so a fresh agent (Opus 4.8 or Sonnet 5) can execute it
**without re-exploring the codebase**: it carries context, current-state evidence
(`file:line`), concrete tasks, acceptance criteria, and verification commands.

## How to use

1. Pick the lowest-numbered `pending` obligation whose `depends_on` are all `done`.
2. Read its YAML in full. The `references` already point at the exact code.
3. Implement `tasks` in order. Respect `do_not_touch`.
4. Satisfy every `acceptance_criteria`, run `verification`, keep `go build ./...`
   and `go vet ./...` green.
5. Flip `status: pending` → `done` in the YAML and commit (conventional commit, no
   AI attribution — repo rule).

## Priority tiers

- **P0** — blocks the homelab / two-notebook test. Do these first.
- **P1** — security + correctness needed for a multi-user "ready" system (this is
  where the Engram Cloud v1.18.0 parity gap lives).
- **P2** — completeness/hardening/polish.
- **P3** — large, deferred; not needed for the first homelab test.

| ID | Title | Priority | Blocks homelab test | Effort | Model | Depends on |
|----|-------|----------|:---:|:---:|-------|-----------|
| OBL-01 | Managed tokens + user lifecycle (hash+pepper, revoke, disable) | P1 | no | XL | opus-4.8 | — |
| OBL-02 | First-admin bootstrap + gate open signup | P0 | yes | M | opus-4.8 | OBL-01 (soft) |
| OBL-03 | Dashboard operator privilege separation | P1 | no | M | opus-4.8 | — |
| OBL-04 | Wire project sync controls (pause/resume) to route + CLI | P2 | no | M | sonnet-5 | — |
| OBL-05 | Expand + harden the audit log | P2 | no | M | sonnet-5 | OBL-01 |
| OBL-06 | Fix multi-cloud fan-out (alias-scope the mutation queue) | P1 | no | L | opus-4.8 | — |
| OBL-07 | Thread `--cloud-name` through status/upgrade/autosync | P2 | no | M | sonnet-5 | OBL-06 |
| OBL-08 | Device management CLI + last_seen_at | P0 | yes | M | sonnet-5 | — |
| OBL-09 | Phase 5: per-account project namespacing | P3 | no | XL | opus-4.8 | — |
| OBL-10 | Homelab deployment (systemd, TLS, env, cross-compile) | P0 | yes | L | sonnet-5 | — |
| OBL-11 | Rebrand tail (TUI/CLI strings, MCP identity, flag names, docs) | P2 | no | M | sonnet-5 | — |
| OBL-12 | Dashboard sync-health UI (render degraded/blocked) | P2 | no | S | sonnet-5 | — |

## Suggested order for the homelab goal

**Minimum to run the two-notebook test:** OBL-02 → OBL-08 → OBL-10.
Each notebook connects to ONE cloud (the "personal" one), registers as a device with
a project scope, and you verify isolation. That path does NOT need the multi-cloud
fan-out fix (OBL-06) because each local DB targets a single cloud.

**To make it a "ready" multi-user product:** add OBL-01, OBL-03, OBL-06.

**Deferred:** OBL-09 (namespacing) only matters once two different accounts reuse the
same project name on the same server — not the case for a single-owner homelab.

## Source of truth

These obligations were derived from a full read of `internal/cloud/**`,
`cmd/omnia/**`, `internal/dashboard/cloudpresence*`, `internal/setup/**`, and
`internal/store/store.go` on branch `merge-omnia-cloud`. Every `evidence:` line is a
real citation. If code has moved since, re-anchor by symbol name, not line number.
