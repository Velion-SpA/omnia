package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Command Center v2, Slice 1 security fix: the last-admin guard used to be a
// two-step "read CountAdmins, then separately mutate" sequence split across
// the HTTP handler layer and the store layer. That is a classic check-then-
// act TOCTOU: two concurrent requests (e.g. demoting account A and
// deactivating/hard-deleting account B, the only two admins) can each read
// count==2 BEFORE either mutation commits, and both proceed, leaving ZERO
// admins. This file provides the primitives that make the check-then-act
// ATOMIC by folding it into the SAME database transaction as the mutation,
// using row-level locking instead of a separate read.

// ErrLastAdmin is returned by SetUserAdmin (demote), SetUserDisabled
// (deactivate), and AdminHardDeleteUser when performing the action on an
// is_admin account would leave ZERO is_admin accounts. Unlike the prior
// two-step guard, this is race-safe: see lockAdminIDsForUpdate.
var ErrLastAdmin = errors.New("cloudstore: cannot act on the last remaining admin")

// lockAdminIDsForUpdate locks (SELECT ... FOR UPDATE) every row currently
// carrying is_admin = true and returns their ids. It MUST be called inside an
// open transaction, before the mutation it guards, and the transaction MUST
// go on to either mutate one of the returned rows (demote/hard-delete) or be
// otherwise serialized against concurrent mutations of that same row set
// (deactivate).
//
// FOR UPDATE cannot be combined with an aggregate (COUNT(*)) in the same
// statement, so this fetches the row ids (not a count) and the caller counts
// them in Go.
//
// Concurrency guarantee: a second, concurrent transaction attempting to lock
// ANY overlapping row — whether via this same helper, or via a direct
// UPDATE/DELETE targeting one of these rows — blocks until this transaction
// commits or rolls back. When it unblocks, Postgres re-evaluates the
// statement's WHERE clause against the freshly committed row version
// (EvalPlanQual) before proceeding, so a row that was demoted/deleted by the
// first transaction is correctly excluded from the second transaction's own
// lock set / count. This is what closes the TOCTOU: two concurrent
// operations that would both reduce the admin set below 1 can no longer both
// succeed — the second one to run sees the POST-COMMIT state of the first.
func lockAdminIDsForUpdate(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM cloud_users WHERE is_admin = true FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lock admin rows: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cloudstore: scan locked admin row: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// containsID reports whether ids contains target.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// DemoteUserAdminGuarded atomically revokes accountID's is_admin flag,
// refusing with ErrLastAdmin when accountID is the only remaining admin.
// This is distinct from the plain SetUserAdmin (admin.go), which is
// unconditional and stays that way — it is still used directly by promote
// (which never needs guarding, since granting admin can't reduce the admin
// count) and by existing tests/scripts that need to freely manipulate the
// flag without the last-admin business rule. DemoteUserAdminGuarded is the
// RACE-SAFE entry point the operator-facing demote HTTP handler calls
// instead — see lockAdminIDsForUpdate for the concurrency rationale.
func (cs *CloudStore) DemoteUserAdminGuarded(ctx context.Context, accountID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("cloudstore: account_id is required")
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin demote user admin tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	adminIDs, err := lockAdminIDsForUpdate(ctx, tx)
	if err != nil {
		return err
	}
	if containsID(adminIDs, accountID) && len(adminIDs) <= 1 {
		return ErrLastAdmin
	}

	res, err := tx.ExecContext(ctx, `UPDATE cloud_users SET is_admin = false WHERE id = $1::bigint`, accountID)
	if err != nil {
		return fmt.Errorf("cloudstore: demote user admin: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit demote user admin: %w", err)
	}
	tx = nil
	return nil
}

// DeactivateUserGuarded atomically soft-deactivates userID (stamps
// disabled_at), refusing with ErrLastAdmin when userID is the only
// remaining is_admin account. Distinct from the plain SetUserDisabled
// (tokens.go), which stays unconditional for the non-admin disable path and
// for existing tests. The operator-facing deactivate HTTP handler uses this
// guarded entry point when the concrete store supports it (type-asserted at
// the handler layer) — see lockAdminIDsForUpdate for the concurrency
// rationale.
func (cs *CloudStore) DeactivateUserGuarded(ctx context.Context, userID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("cloudstore: user_id is required")
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin deactivate user tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	adminIDs, err := lockAdminIDsForUpdate(ctx, tx)
	if err != nil {
		return err
	}
	if containsID(adminIDs, userID) && len(adminIDs) <= 1 {
		return ErrLastAdmin
	}

	res, err := tx.ExecContext(ctx, `UPDATE cloud_users SET disabled_at = NOW() WHERE id = $1::bigint`, userID)
	if err != nil {
		return fmt.Errorf("cloudstore: deactivate user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit deactivate user: %w", err)
	}
	tx = nil
	return nil
}
