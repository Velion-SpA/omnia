// Package purge is the composition point ABOVE internal/store that
// orchestrates a hard delete's full side-effect fan-out: the store-level
// physical purge + tombstone, the embedding vector purge, and the audit
// entry recording the outcome.
//
// internal/store MUST NOT import internal/embed (architecture-guardrails,
// documented in openspec/changes/omnia-provenance-foundation/design.md) —
// the original omnia-provenance-foundation slice honored that boundary by
// fanning the embed purge out from internal/mcp's handleDelete only, since
// internal/mcp already imports both internal/store and internal/embed. That
// left internal/server's HTTP DELETE /observations/{id}?hard=true (also used
// by the dashboard's hard-delete button) and cmd/omnia's `omnia delete
// --hard` calling store.DeleteObservation directly, silently orphaning the
// embedding vector and — for the CLI — never writing an audit entry at all.
//
// internal/purge exists so all three hard-delete entry points (MCP
// mem_delete, the HTTP handler, and the CLI command) share exactly ONE
// implementation of "purge the row, purge the vector, record the outcome" —
// imported by internal/mcp, internal/server, and cmd/omnia, never the
// reverse, and never by internal/store.
package purge

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/store"
)

// EmbedPurger is satisfied by *embed.Store (its DeleteBySyncID method). It is
// declared here — rather than importing *embed.Store's concrete type —
// purely to keep this package's dependency surface to the one method it
// actually calls, and to make HardDeleteWithPurge trivially fakeable in
// tests without spinning up a real embeddings.db to exercise the failure
// path (Blocking 2's "propagate the purge error" requirement).
type EmbedPurger interface {
	DeleteBySyncID(ctx context.Context, syncID string) (int, error)
}

// AuditAppender matches audit.Append's signature. It is accepted as a
// parameter (rather than this package calling audit.Append directly) so
// that callers with their own audit seam for testing — internal/mcp's
// package-level auditAppend var, overridden in tests via captureAudit — can
// route hard deletes through this helper without losing that seam, and so
// this package's own tests never have to touch the real on-disk audit log.
type AuditAppender func(audit.Entry)

// ErrEmbedPurgeFailed wraps an embed.Store.DeleteBySyncID failure so callers
// can distinguish it from an outright delete failure via errors.Is. When
// this is returned, the store-level row delete + tombstone write already
// SUCCEEDED and were NOT rolled back — only the vector purge failed. See
// HardDeleteWithPurge's doc comment for the full error-semantics contract
// (Blocking 2).
var ErrEmbedPurgeFailed = errors.New("embed purge failed")

// HardDeleteWithPurge is the single orchestration point for a hard delete:
// store row purge + tombstone (store.DeleteObservationWithActor), embed
// vector purge fan-out (embedStore.DeleteBySyncID), and one audit.Entry
// recording the outcome. All three of Omnia's hard-delete entry points — MCP
// mem_delete, HTTP DELETE /observations/{id}?hard=true, and `omnia delete
// --hard` — go through this instead of calling store.DeleteObservation
// directly, so vector-purge and audit coverage cannot silently regress on
// any one path (omnia-provenance-foundation review fix, Blocking 1).
//
// The observation is fetched by id BEFORE the store delete runs (the row is
// physically gone once it returns), so this function owns the full
// fetch-then-delete-then-fan-out sequence atomically on the caller's behalf
// rather than requiring every call site to duplicate that dance.
//
// embedStore may be nil — embeddings disabled or unavailable — in which case
// the vector purge is a no-op, mirroring enqueueAutoEmbed's existing
// cfg.AutoEmbed nil-guard convention: there is nothing to purge if nothing
// was ever embedded.
//
// Error semantics (Blocking 2 — an embed-purge failure must never be
// silently swallowed or misreported as "ok"):
//
//   - If the store-level hard delete itself fails (e.g.
//     store.ErrObservationNotFound), that error is returned as-is (checkable
//     via errors.Is), nothing was deleted, and no audit entry is written.
//   - If the row delete + tombstone SUCCEED but the embed vector purge
//     fails, HardDeleteWithPurge still returns a non-nil error — wrapping
//     ErrEmbedPurgeFailed (checkable via errors.Is) — so the caller can
//     surface the honest partial-failure instead of silently relying on the
//     `omnia embed` Prune backstop to eventually clean up the orphaned
//     vector. The audit entry's Result field reflects this too:
//     "embed_purge_failed" instead of "ok". The delete itself is NOT rolled
//     back — the physical purge already committed.
//
// actor identifies the caller for the audit entry and the deletion_tombstones
// proof row (e.g. "mcp", "http", "cli") — attribution only, never validated.
func HardDeleteWithPurge(ctx context.Context, s *store.Store, embedStore EmbedPurger, appendAudit AuditAppender, actor string, id int64) error {
	// Fetch BEFORE deleting: the row is physically gone after
	// DeleteObservationWithActor(hard=true) returns, so this is the only
	// chance to capture sync_id/project/title/source/trust_tag for the
	// embed fan-out and the audit entry below.
	pre, preErr := s.GetObservation(id)

	if err := s.DeleteObservationWithActor(id, true, actor); err != nil {
		return err
	}

	result := "ok"
	var purgeErr error
	if embedStore != nil && preErr == nil && pre.SyncID != "" {
		if _, embErr := embedStore.DeleteBySyncID(ctx, pre.SyncID); embErr != nil {
			result = "embed_purge_failed"
			purgeErr = fmt.Errorf("%w: %v", ErrEmbedPurgeFailed, embErr)
			fmt.Fprintf(os.Stderr, "omnia: embed purge fan-out failed for sync_id=%s: %v\n", pre.SyncID, embErr)
		}
	}

	if appendAudit != nil && preErr == nil {
		appendAudit(audit.Entry{
			Ts:            audit.Now(),
			Actor:         actor,
			Action:        audit.ActionHardDelete,
			ObservationID: int(id),
			Project:       strFromPtr(pre.Project),
			Summary:       pre.Title,
			Result:        result,
			Source:        strFromPtr(pre.Source),
			TrustTag:      strFromPtr(pre.TrustTag),
			SyncID:        pre.SyncID,
		})
	}

	return purgeErr
}

func strFromPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
