package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Velion-SpA/omnia/internal/store"
)

// Device represents a registered device for an account.
type Device struct {
	ID            string     `json:"id"`
	AccountID     string     `json:"account_id"`
	Name          string     `json:"name"`
	ScopeProjects []string   `json:"scope_projects"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
}

// scanNullTime converts a sql.NullTime into an optional UTC *time.Time.
func scanNullTime(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time.UTC()
	return &t
}

// GetOrCreateDevice upserts a device by (account_id, name). If the device
// already exists, it is returned unchanged. If not, a new row is inserted with
// an empty scope_projects list. Returns the device (existing or new).
func (cs *CloudStore) GetOrCreateDevice(accountID, name string) (*Device, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	name = strings.TrimSpace(name)
	if accountID == "" {
		return nil, fmt.Errorf("cloudstore: account_id is required")
	}
	if name == "" {
		return nil, fmt.Errorf("cloudstore: device name is required")
	}

	// (xmax = 0) is the standard Postgres upsert idiom to detect whether the
	// RETURNING row came from the INSERT branch (xmax = 0) or the ON CONFLICT
	// UPDATE branch (xmax != 0) — used below to audit ONLY a brand-new device.
	const q = `
        INSERT INTO cloud_devices (account_id, name, scope_projects)
        VALUES ($1, $2, '[]'::jsonb)
        ON CONFLICT (account_id, name) DO UPDATE
            SET account_id = EXCLUDED.account_id
        RETURNING id::text, account_id, name, scope_projects, last_seen_at, (xmax = 0) AS inserted`

	var d Device
	var scopeRaw []byte
	var lastSeen sql.NullTime
	var inserted bool
	ctx := context.Background()
	err := cs.db.QueryRowContext(ctx, q, accountID, name).
		Scan(&d.ID, &d.AccountID, &d.Name, &scopeRaw, &lastSeen, &inserted)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get or create device: %w", err)
	}
	if err := json.Unmarshal(scopeRaw, &d.ScopeProjects); err != nil {
		d.ScopeProjects = nil
	}
	d.LastSeenAt = scanNullTime(lastSeen)
	if inserted {
		// OBL-05: audit a NEW device registration. Best-effort — a failed audit
		// write only logs a warning and never fails device creation itself,
		// matching the non-blocking contract for every non-credential event.
		if aerr := cs.InsertAuditEntry(ctx, AuditEntry{
			Contributor: accountID,
			Project:     AuditProjectSentinel,
			Action:      AuditActionDeviceCreate,
			Outcome:     AuditOutcomeDeviceCreated,
			Metadata:    map[string]any{"device_id": d.ID, "device_name": d.Name},
		}); aerr != nil {
			log.Printf("cloudstore: audit insert failed (device create): %v", aerr)
		}
	}
	return &d, nil
}

// GetDevice returns the device with the given id, or nil if not found.
func (cs *CloudStore) GetDevice(id string) (*Device, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("cloudstore: device id is required")
	}
	const q = `SELECT id::text, account_id, name, scope_projects, last_seen_at FROM cloud_devices WHERE id::text = $1`
	var d Device
	var scopeRaw []byte
	var lastSeen sql.NullTime
	err := cs.db.QueryRowContext(context.Background(), q, id).
		Scan(&d.ID, &d.AccountID, &d.Name, &scopeRaw, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get device: %w", err)
	}
	if err := json.Unmarshal(scopeRaw, &d.ScopeProjects); err != nil {
		d.ScopeProjects = nil
	}
	d.LastSeenAt = scanNullTime(lastSeen)
	return &d, nil
}

// ListDevicesForAccount returns all devices owned by the given account.
func (cs *CloudStore) ListDevicesForAccount(accountID string) ([]Device, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("cloudstore: account_id is required")
	}
	const q = `SELECT id::text, account_id, name, scope_projects, last_seen_at FROM cloud_devices WHERE account_id = $1 ORDER BY id`
	rows, err := cs.db.QueryContext(context.Background(), q, accountID)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var scopeRaw []byte
		var lastSeen sql.NullTime
		if err := rows.Scan(&d.ID, &d.AccountID, &d.Name, &scopeRaw, &lastSeen); err != nil {
			return nil, fmt.Errorf("cloudstore: scan device: %w", err)
		}
		if err := json.Unmarshal(scopeRaw, &d.ScopeProjects); err != nil {
			d.ScopeProjects = nil
		}
		d.LastSeenAt = scanNullTime(lastSeen)
		out = append(out, d)
	}
	return out, rows.Err()
}

// TouchDevice stamps last_seen_at = NOW() for the given device id. It is
// best-effort: the caller (the authorize path) ignores the returned error so a
// stats write never fails an authenticated request. A missing device is a no-op.
func (cs *CloudStore) TouchDevice(ctx context.Context, id string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("cloudstore: device id is required")
	}
	_, err := cs.db.ExecContext(ctx, `UPDATE cloud_devices SET last_seen_at = NOW() WHERE id::text = $1`, id)
	if err != nil {
		return fmt.Errorf("cloudstore: touch device: %w", err)
	}
	return nil
}

// SetDeviceScope replaces the scope_projects for a device. Projects are
// normalized via store.NormalizeProject before storing.
func (cs *CloudStore) SetDeviceScope(id string, projects []string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("cloudstore: device id is required")
	}
	normalized := make([]string, 0, len(projects))
	for _, p := range projects {
		p, _ = store.NormalizeProject(p)
		p = strings.TrimSpace(p)
		if p != "" {
			normalized = append(normalized, p)
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("cloudstore: marshal scope: %w", err)
	}
	res, err := cs.db.ExecContext(context.Background(),
		`UPDATE cloud_devices SET scope_projects = $1 WHERE id::text = $2`,
		raw, id)
	if err != nil {
		return fmt.Errorf("cloudstore: set device scope: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("cloudstore: device not found: %s", id)
	}
	return nil
}

// DeleteDevice deletes a device by id. No-op if not found.
func (cs *CloudStore) DeleteDevice(id string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("cloudstore: device id is required")
	}
	_, err := cs.db.ExecContext(context.Background(),
		`DELETE FROM cloud_devices WHERE id::text = $1`, id)
	if err != nil {
		return fmt.Errorf("cloudstore: delete device: %w", err)
	}
	return nil
}
