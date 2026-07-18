package cloudstore

import (
	"context"
	"fmt"
	"strings"
)

// Operator-facing user CRUD (Command Center v2, Slice 1). Additive over the
// existing cloud_users / cloud_tokens / cloud_memberships tables — no schema
// change. These are distinct from the self-service Signup path (auth.Service):
// an operator can create/edit/reset/hard-delete ANY account, whereas Signup
// only ever creates the caller's own. Named with an Admin prefix (matching the
// existing AdminUser view type in this file) so they never collide with the
// plain CreateUser used by Signup. The HTTP layer gates every call on the
// operator session; these methods perform no authorization themselves.

// AdminCreateUser creates a new account on behalf of an operator. It enforces
// the SAME username/email uniqueness CreateUser (Signup path) relies on —
// a conflicting username OR email fails cleanly with ErrUserExists and never
// mutates the existing row (OBL-02 lesson: no silent overwrite via ON
// CONFLICT). The caller is responsible for hashing the password before
// calling this — the store only ever sees the hash, never the plaintext.
func (cs *CloudStore) AdminCreateUser(ctx context.Context, username, email, passwordHash string) (string, error) {
	if cs == nil || cs.db == nil {
		return "", fmt.Errorf("cloudstore: not initialized")
	}
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if username == "" {
		return "", fmt.Errorf("cloudstore: username is required")
	}
	const q = `
		INSERT INTO cloud_users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id::text`
	var id string
	if err := cs.db.QueryRowContext(ctx, q, username, email, passwordHash).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return "", ErrUserExists
		}
		return "", fmt.Errorf("cloudstore: admin create user: %w", err)
	}
	return id, nil
}

// AdminUpdateUser edits an existing account's username/email (operator-facing
// edit). Uniqueness is enforced by the SAME UNIQUE constraints CreateUser
// relies on: updating a user's OWN username/email to its current value never
// conflicts (it's the same row); only a conflict with ANOTHER existing
// account raises ErrUserExists, and Postgres guarantees the UPDATE touches
// zero rows in that case (no partial write). Returns
// ErrManagedTokenUserNotFound when the id does not exist, mirroring
// SetUserDisabled / SetUserAdmin so the HTTP layer maps it to a clean 404.
func (cs *CloudStore) AdminUpdateUser(ctx context.Context, userID, username, email string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if userID == "" {
		return fmt.Errorf("cloudstore: user_id is required")
	}
	if username == "" {
		return fmt.Errorf("cloudstore: username is required")
	}
	res, err := cs.db.ExecContext(ctx, `UPDATE cloud_users SET username = $2, email = $3 WHERE id = $1::bigint`, userID, username, email)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrUserExists
		}
		return fmt.Errorf("cloudstore: admin update user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	return nil
}

// AdminSetUserPassword replaces a user's stored password hash (operator reset
// flow). Like AdminCreateUser, the caller hashes the new password before
// calling this — the store never sees the plaintext. Returns
// ErrManagedTokenUserNotFound when the id does not exist.
func (cs *CloudStore) AdminSetUserPassword(ctx context.Context, userID, passwordHash string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("cloudstore: user_id is required")
	}
	if passwordHash == "" {
		return fmt.Errorf("cloudstore: password hash is required")
	}
	res, err := cs.db.ExecContext(ctx, `UPDATE cloud_users SET password_hash = $2 WHERE id = $1::bigint`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("cloudstore: admin set user password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	return nil
}

// AdminHardDeleteUser permanently removes an account and every row that would
// otherwise be left orphaned, in ONE transaction, in this order:
//  1. cloud_tokens — carries an FK REFERENCES cloud_users(id) with NO ON
//     DELETE CASCADE — deleting the user row first would fail with a raw
//     Postgres FK violation for any account that ever had a managed token
//     issued, so tokens are deleted FIRST.
//  2. cloud_memberships — plain TEXT account_id (no FK), but leaving it
//     behind would orphan permission rows for a deleted account.
//  3. cloud_team_members — plain TEXT account_id (no FK). An orphaned row
//     here is a SECURITY-RELEVANT leak, not just clutter: EffectivePerms /
//     ListReadableProjectsForAccount key on tm.account_id with no join back
//     to cloud_users, so a future account that reuses the same numeric id
//     would silently inherit the deleted account's team-derived perms.
//  4. cloud_devices — plain TEXT account_id (no FK), same orphan-cleanup
//     reasoning (no security implication, just data hygiene).
//  5. cloud_users — the account row itself, guarded by the last-admin check
//     below.
//
// Also enforces the last-admin guard ATOMICALLY: lockAdminIDsForUpdate locks
// every is_admin=true row (SELECT ... FOR UPDATE) inside this SAME
// transaction before the count is evaluated, so a concurrent demote /
// deactivate / hard-delete targeting a DIFFERENT admin account cannot race
// past this check — see lockAdminIDsForUpdate for the full rationale.
//
// Returns ErrLastAdmin when userID is the only remaining is_admin account,
// and ErrManagedTokenUserNotFound when the id does not exist (the delete is
// NOT idempotent-as-success like DeleteMembership — the caller needs to know
// whether an account actually existed, e.g. to map to a 404).
func (cs *CloudStore) AdminHardDeleteUser(ctx context.Context, userID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("cloudstore: user_id is required")
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin hard delete user tx: %w", err)
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_tokens WHERE user_id = $1::bigint`, userID); err != nil {
		return fmt.Errorf("cloudstore: hard delete user (tokens): %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_memberships WHERE account_id = $1`, userID); err != nil {
		return fmt.Errorf("cloudstore: hard delete user (memberships): %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_team_members WHERE account_id = $1`, userID); err != nil {
		return fmt.Errorf("cloudstore: hard delete user (team members): %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_devices WHERE account_id = $1`, userID); err != nil {
		return fmt.Errorf("cloudstore: hard delete user (devices): %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM cloud_users WHERE id = $1::bigint`, userID)
	if err != nil {
		return fmt.Errorf("cloudstore: hard delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit hard delete user: %w", err)
	}
	tx = nil
	return nil
}
