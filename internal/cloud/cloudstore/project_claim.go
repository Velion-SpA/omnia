package cloudstore

import (
	"context"
	"fmt"
	"strings"
)

// ClaimProjectOwnership atomically attempts to make accountID the owner of
// project, granting perms/role only if the project has no existing members.
//
// This closes a CRITICAL cross-tenant race in cloudserver.claimOrphanProject
// (see cloudserver/members.go): the prior ListProjectMembers-then-
// GrantMembership sequence was a classic check-then-act with no lock between
// the read and the write. cloud_memberships' unique key is
// (account_id, project) — a COMPOSITE key — so it does nothing to stop two
// DIFFERENT accounts from each inserting an owner row for the SAME
// never-before-seen project when both read "zero members" before either
// write commits. That is a genuine cross-tenant isolation breach (two
// unrelated tenants both become full owner of one project), not merely a
// corrupted count.
//
// The fix mirrors SetProjectParent (project_links.go): the whole
// check-then-act sequence runs inside ONE transaction, serialized by a
// pg_advisory_xact_lock taken BEFORE the read, so only one caller can ever
// observe "no members yet" for a given project and commit the first (and
// only) owner row.
//
// Lock key choice: unlike projectLinkLockKey (one fixed, GLOBAL key shared by
// every SetProjectParent/ClearProjectParent call — necessary there because a
// single link mutation touches TWO projects, child and parent, at once, so a
// per-project key could not serialize a race between two DIFFERENT project
// pairs, e.g. A->B racing B->C), a claim only ever touches ONE project. A
// PER-PROJECT lock key is therefore strictly better here: hashtext(project)
// fully serializes concurrent claims of the SAME project while letting
// claims of DIFFERENT projects proceed in parallel, instead of needlessly
// serializing every claim in the system behind one global key.
// pg_advisory_xact_lock takes a bigint; hashtext returns int4, which Postgres
// implicitly widens to int8 for this call — no explicit cast needed.
//
// Returns claimed=true iff THIS call inserted the owner row. claimed=false
// means the project already had at least one member — the existing
// membership is left completely untouched, and the caller of THIS method
// gets no membership at all (not even at reduced perms). That is
// intentional: the project namespace belongs to whichever account won the
// race, and the loser's subsequent per-operation authorization check
// correctly denies it (no membership => no perms).
func (cs *CloudStore) ClaimProjectOwnership(ctx context.Context, accountID, project string, perms int, role string) (bool, error) {
	if cs == nil || cs.db == nil {
		return false, fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	project = strings.TrimSpace(project)
	if accountID == "" {
		return false, fmt.Errorf("cloudstore: account_id is required")
	}
	if project == "" {
		return false, fmt.Errorf("cloudstore: project is required")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = "member"
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("cloudstore: begin claim project ownership tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Serialize claims of THIS project before checking for existing members —
	// see the lock-key discussion above.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, project); err != nil {
		return false, fmt.Errorf("cloudstore: lock project claim: %w", err)
	}

	var hasMembers bool
	const existsQ = `SELECT EXISTS(SELECT 1 FROM cloud_memberships WHERE project = $1)`
	if err := tx.QueryRowContext(ctx, existsQ, project).Scan(&hasMembers); err != nil {
		return false, fmt.Errorf("cloudstore: check existing members: %w", err)
	}
	if hasMembers {
		// Already claimed by someone else — do not touch existing membership.
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("cloudstore: commit claim project ownership (no-op): %w", err)
		}
		tx = nil
		return false, nil
	}

	const insertQ = `
		INSERT INTO cloud_memberships (account_id, project, perms, role)
		VALUES ($1, $2, $3, $4)`
	if _, err := tx.ExecContext(ctx, insertQ, accountID, project, perms, role); err != nil {
		return false, fmt.Errorf("cloudstore: insert claim membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("cloudstore: commit claim project ownership: %w", err)
	}
	tx = nil
	return true, nil
}
