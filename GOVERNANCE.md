# Omnia Governance

## Overview

This document describes the trust model, access roles, and governance mechanisms for the Omnia knowledge layer. Omnia sits on top of Engram and owns the mutation pipeline — every edit, soft-delete, and hard-delete flows through Omnia's audit and access controls before reaching Engram.

---

## The 3-Actor Model

### Ingestor

Automated pipelines and bots (e.g., the GitHub and Discord sync processes).

- Writes **derived memory only**: observations that represent external facts (PRs, issues, messages) tagged `layer: ingested`.
- No gate: ingestors bypass the manual approval flow because their writes are additive and low-risk.
- Autosync: runs on schedule without human interaction.
- Cannot edit or delete curated (human-authored) memory.

### Member

Team humans with a signed identity.

- Can **create and edit their own** observations.
- Can **view all** observations across all projects they have access to.
- Writes are **signed** with their identity token in the audit log.
- Cannot delete observations by default; soft-delete may be permitted for observations they own.

### Owner

Full administrative access.

- Full create, edit, soft-delete, and hard-delete across all projects.
- Manages token issuance for Members and Ingestors.
- Reviews and approves `protected` memory changes.
- Sole actor permitted to grant or revoke access.

---

## Step 1 Mechanisms (Current)

The following governance capabilities are live as of this release:

### Audit Log

Every mutation (edit, soft-delete, hard-delete) is recorded as a JSONL entry in `~/.local/state/omnia/audit.jsonl`.

Each entry includes:
- `ts`: timestamp (RFC3339 UTC)
- `actor`: provisional identity (see below)
- `action`: `edit | soft_delete | hard_delete`
- `observation_id`: the Engram observation ID
- `project`: the observation's project
- `summary`: short human-readable description; never includes full content
- `result`: `ok | error`

The audit log is append-only and Omnia-owned. It does NOT touch Engram.

### Soft-Delete

Soft-delete marks an observation hidden in Engram (the DB row is preserved). Hard-delete is permanent.

**Restore status**: soft-deleted observations do NOT appear in Engram search results. There is currently no Engram API endpoint for restore (`GET /observations/{id}/restore` returns 404). Restore-from-UI is **deferred to the gateway phase** (see below). At the DB level, soft-deleted rows remain recoverable by direct database access.

### Provisional Actor Identity

Until the full gateway is in place, actor identity is resolved in this order:
1. `X-Omnia-Actor` request header (for tooling/API callers)
2. `--actor` CLI flag passed to `omnia dashboard`
3. `USER` environment variable
4. `"unknown"` fallback

This is **provisional** and carries no authentication guarantee. It provides traceability for single-user and small-team setups while the gateway is being built.

---

## Future Architecture: Omnia Gateway

The current setup has a single-hub risk: every team member and bot shares the same `ENGRAM_CLOUD_TOKEN`, which means any actor can overwrite any observation without attribution or gate.

The gateway phase resolves this:

```
Team members → individual tokens → Omnia Gateway
Bots/ingestors → bot tokens     → Omnia Gateway
                                       │
                                       ▼
                              Authenticates actor
                              Applies role policy
                              Signs writes (actor + ts)
                              Records to audit log
                              Forwards to Engram
                                       │
                                       ▼
                                    Engram
                              (untouched behind gateway)
```

Key properties of the gateway design:
- **Engram is untouched**: the gateway is a proxy layer. Engram's API and data model do not change.
- **Per-actor tokens**: each human and each bot gets a unique token. The shared `ENGRAM_CLOUD_TOKEN` is replaced by a gateway credential issued by the Owner.
- **Role enforcement**: the gateway applies the 3-actor policy before forwarding. A Member cannot hard-delete another Member's observation.
- **Audit at the gateway**: all mutations are logged before they reach Engram, making the audit log the authoritative record.
- **Restore path**: once the gateway owns the mutation pipeline, soft-delete restore becomes a gateway operation (mark un-deleted + audit) without requiring Engram API changes.

---

## Protected Memory

Not all observations require the same level of scrutiny.

- **Default flow**: observations flow freely through create/edit/delete without async review.
- **Protected observations**: observations explicitly tagged `protected: true` (or observations in a protected project) require async Owner review before edits are visible.
- **Protected review queue**: the gateway (future phase) will surface a review queue for the Owner; approved edits are applied, rejected edits are reverted.

Protected review is opt-in per observation or per project. The overhead is proportional to the sensitivity of the memory.

---

## Roadmap

| Phase | Capability |
|-------|-----------|
| Step 1 (current) | Audit log, soft-delete, provisional actor identity |
| Step 2 | Gateway skeleton: token issuance, per-actor routing, role check |
| Step 3 | Restore-from-UI (gateway-owned soft-delete reversal) |
| Step 4 | Protected memory review queue |
| Step 5 | Member self-service: create and edit own observations via UI |
