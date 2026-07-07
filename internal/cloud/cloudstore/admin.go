package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Operator-facing administration reads/writes backing the dashboard Admin section
// (OBL-13). These are additive over the existing cloud_users / cloud_tokens /
// cloud_memberships tables — no schema change. The HTTP layer gates every call on
// the operator session, so these methods perform no authorization themselves.

// AdminUser is the operator-facing view of an account for the Admin Users page. It
// augments the base user record with the lifecycle + token-usage columns an
// operator needs to triage an account (status, when it was created, how many live
// tokens it holds, when a token was last used).
type AdminUser struct {
	ID           string
	Username     string
	Email        string
	CreatedAt    time.Time
	DisabledAt   *time.Time // nil ⇒ active; non-nil ⇒ disabled at that time
	TokenCount   int        // count of non-revoked managed tokens
	LastTokenUse *time.Time // most recent last_used_at across the account's tokens
}

// Disabled reports whether the account is currently disabled.
func (u AdminUser) Disabled() bool { return u.DisabledAt != nil }

// ManagedTokenView is the operator-facing view of a managed token for the token
// list. It never carries the token hash and never the raw value (which is shown
// exactly once at issuance and is unrecoverable).
type ManagedTokenView struct {
	ID         string
	Label      string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// Revoked reports whether the token has been revoked.
func (t ManagedTokenView) Revoked() bool { return t.RevokedAt != nil }

// ListUsers returns every account with lifecycle + token-usage aggregates,
// ordered by username. It is operator-only (the caller gates on the operator
// session); it applies no per-account scope.
func (cs *CloudStore) ListUsers(ctx context.Context) ([]AdminUser, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT u.id::text, u.username, u.email, u.created_at, u.disabled_at,
		       COUNT(t.id) FILTER (WHERE t.revoked_at IS NULL) AS active_tokens,
		       MAX(t.last_used_at) AS last_token_use
		FROM cloud_users u
		LEFT JOIN cloud_tokens t ON t.user_id = u.id
		GROUP BY u.id, u.username, u.email, u.created_at, u.disabled_at
		ORDER BY u.username ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list users: %w", err)
	}
	defer rows.Close()

	var out []AdminUser
	for rows.Next() {
		var (
			u          AdminUser
			disabledAt sql.NullTime
			lastUse    sql.NullTime
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.CreatedAt, &disabledAt, &u.TokenCount, &lastUse); err != nil {
			return nil, fmt.Errorf("cloudstore: scan user: %w", err)
		}
		if disabledAt.Valid {
			t := disabledAt.Time
			u.DisabledAt = &t
		}
		if lastUse.Valid {
			t := lastUse.Time
			u.LastTokenUse = &t
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListMembershipsForUser returns every membership held by accountID, ordered by
// project. It mirrors ListMembershipsForAccount but is context-aware and is the
// read behind GET /admin/users/{id}/memberships.
func (cs *CloudStore) ListMembershipsForUser(ctx context.Context, accountID string) ([]Membership, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT account_id, project, perms, role FROM cloud_memberships WHERE account_id = $1 ORDER BY project ASC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(accountID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list memberships for user: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.AccountID, &m.Project, &m.Perms, &m.Role); err != nil {
			return nil, fmt.Errorf("cloudstore: scan membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertMembership grants or updates a membership. It mirrors the psql seed upsert
// (ON CONFLICT (account_id, project) DO UPDATE), so the operator can set any
// account's permission on any project without being a per-project owner. Perms
// masking (to the defined bit range) is the caller's responsibility.
func (cs *CloudStore) UpsertMembership(ctx context.Context, accountID, project string, perms int, role string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	project = strings.TrimSpace(project)
	if accountID == "" {
		return fmt.Errorf("cloudstore: account_id is required")
	}
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = "member"
	}
	const q = `
		INSERT INTO cloud_memberships (account_id, project, perms, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, project) DO UPDATE
		SET perms = EXCLUDED.perms, role = EXCLUDED.role`
	if _, err := cs.db.ExecContext(ctx, q, accountID, project, perms, role); err != nil {
		return fmt.Errorf("cloudstore: upsert membership: %w", err)
	}
	return nil
}

// DeleteMembership removes a membership. It is idempotent: deleting a
// non-existent membership is a no-op that returns nil.
func (cs *CloudStore) DeleteMembership(ctx context.Context, accountID, project string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	const q = `DELETE FROM cloud_memberships WHERE account_id = $1 AND project = $2`
	if _, err := cs.db.ExecContext(ctx, q, strings.TrimSpace(accountID), strings.TrimSpace(project)); err != nil {
		return fmt.Errorf("cloudstore: delete membership: %w", err)
	}
	return nil
}

// ListManagedTokensForUser returns the operator-facing token list for a user,
// newest first. The token hash and raw value are never exposed.
func (cs *CloudStore) ListManagedTokensForUser(ctx context.Context, userID string) ([]ManagedTokenView, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT id::text, COALESCE(label, ''), created_at, revoked_at, last_used_at
		FROM cloud_tokens
		WHERE user_id = $1::bigint
		ORDER BY created_at DESC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list managed tokens: %w", err)
	}
	defer rows.Close()
	var out []ManagedTokenView
	for rows.Next() {
		var (
			t         ManagedTokenView
			revokedAt sql.NullTime
			lastUsed  sql.NullTime
		)
		if err := rows.Scan(&t.ID, &t.Label, &t.CreatedAt, &revokedAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("cloudstore: scan managed token: %w", err)
		}
		if revokedAt.Valid {
			ts := revokedAt.Time
			t.RevokedAt = &ts
		}
		if lastUsed.Valid {
			ts := lastUsed.Time
			t.LastUsedAt = &ts
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
