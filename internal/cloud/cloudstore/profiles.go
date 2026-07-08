package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Profiles are operator-creatable permission presets (OBL-14): a named perms
// bitfield (Read=1, Insert=2, Update=4, Delete=8) applied to every project of a
// team the account belongs to. Seeded defaults: Moderator=15, Editor=7, Member=1.
// cloudstore never imports internal/cloud/auth (auth imports cloudstore), so the
// bitfield masking lives in the caller; here perms is an opaque int.

// ErrProfileNotFound is returned when a profile id does not resolve.
var ErrProfileNotFound = errors.New("cloudstore: profile not found")

// ErrProfileNameTaken is returned when a create/update collides with an existing
// profile name (the UNIQUE(name) constraint).
var ErrProfileNameTaken = errors.New("cloudstore: profile name already exists")

// Profile is a permission preset row.
type Profile struct {
	ID    string
	Name  string
	Perms int
}

// CreateProfile inserts a new permission preset. A duplicate name maps to
// ErrProfileNameTaken so the HTTP layer can return a clean 409.
func (cs *CloudStore) CreateProfile(ctx context.Context, name string, perms int) (*Profile, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("cloudstore: profile name is required")
	}
	const q = `INSERT INTO cloud_profiles (name, perms) VALUES ($1, $2) RETURNING id::text, name, perms`
	var p Profile
	if err := cs.db.QueryRowContext(ctx, q, name, perms).Scan(&p.ID, &p.Name, &p.Perms); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrProfileNameTaken
		}
		return nil, fmt.Errorf("cloudstore: create profile: %w", err)
	}
	return &p, nil
}

// ListProfiles returns every profile, ordered by name.
func (cs *CloudStore) ListProfiles(ctx context.Context) ([]Profile, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, name, perms FROM cloud_profiles ORDER BY name ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list profiles: %w", err)
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.Name, &p.Perms); err != nil {
			return nil, fmt.Errorf("cloudstore: scan profile: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProfile returns the profile for id, or nil when absent.
func (cs *CloudStore) GetProfile(ctx context.Context, id string) (*Profile, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("cloudstore: profile id is required")
	}
	const q = `SELECT id::text, name, perms FROM cloud_profiles WHERE id = $1::bigint`
	var p Profile
	err := cs.db.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.Name, &p.Perms)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get profile: %w", err)
	}
	return &p, nil
}

// UpdateProfile renames and/or re-scopes an existing profile. Returns
// ErrProfileNotFound when the id does not exist and ErrProfileNameTaken on a name
// collision.
func (cs *CloudStore) UpdateProfile(ctx context.Context, id, name string, perms int) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return fmt.Errorf("cloudstore: profile id is required")
	}
	if name == "" {
		return fmt.Errorf("cloudstore: profile name is required")
	}
	res, err := cs.db.ExecContext(ctx, `UPDATE cloud_profiles SET name = $2, perms = $3 WHERE id = $1::bigint`, id, name, perms)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrProfileNameTaken
		}
		return fmt.Errorf("cloudstore: update profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// DeleteProfile removes a profile. Team members referencing it keep their
// (team, account) row with profile_id nulled by the FK's default behaviour is NOT
// automatic — the FK has no ON DELETE clause, so a profile still referenced by a
// member cannot be deleted (FK violation surfaces as an error). This is deliberate:
// deleting an in-use profile would silently strip perms from everyone using it, so
// the operator must reassign members first. Idempotent for an absent id (no-op).
func (cs *CloudStore) DeleteProfile(ctx context.Context, id string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("cloudstore: profile id is required")
	}
	if _, err := cs.db.ExecContext(ctx, `DELETE FROM cloud_profiles WHERE id = $1::bigint`, id); err != nil {
		return fmt.Errorf("cloudstore: delete profile: %w", err)
	}
	return nil
}
