package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Membership represents a user's access record for a project.
type Membership struct {
	AccountID string
	Project   string
	Perms     int
	Role      string
}

// GrantMembership upserts a membership record.
func (cs *CloudStore) GrantMembership(accountID, project string, perms int, role string) error {
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
	_, err := cs.db.ExecContext(context.Background(), q, accountID, project, perms, role)
	if err != nil {
		return fmt.Errorf("cloudstore: grant membership: %w", err)
	}
	return nil
}

// RevokeMembership deletes a membership record. No-op if not found.
func (cs *CloudStore) RevokeMembership(accountID, project string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	const q = `DELETE FROM cloud_memberships WHERE account_id = $1 AND project = $2`
	_, err := cs.db.ExecContext(context.Background(), q, strings.TrimSpace(accountID), strings.TrimSpace(project))
	if err != nil {
		return fmt.Errorf("cloudstore: revoke membership: %w", err)
	}
	return nil
}

// GetMembership returns the membership for (accountID, project), or nil if not found.
func (cs *CloudStore) GetMembership(accountID, project string) (*Membership, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT account_id, project, perms, role FROM cloud_memberships WHERE account_id = $1 AND project = $2`
	var m Membership
	err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(accountID), strings.TrimSpace(project)).
		Scan(&m.AccountID, &m.Project, &m.Perms, &m.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get membership: %w", err)
	}
	return &m, nil
}

// ListMembershipsForAccount returns all memberships for an account.
func (cs *CloudStore) ListMembershipsForAccount(accountID string) ([]Membership, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT account_id, project, perms, role FROM cloud_memberships WHERE account_id = $1`
	rows, err := cs.db.QueryContext(context.Background(), q, strings.TrimSpace(accountID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list memberships for account: %w", err)
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

// ListProjectMembers returns all memberships for a project.
func (cs *CloudStore) ListProjectMembers(project string) ([]Membership, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT account_id, project, perms, role FROM cloud_memberships WHERE project = $1`
	rows, err := cs.db.QueryContext(context.Background(), q, strings.TrimSpace(project))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list project members: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.AccountID, &m.Project, &m.Perms, &m.Role); err != nil {
			return nil, fmt.Errorf("cloudstore: scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
