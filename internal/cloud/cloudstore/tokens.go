package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Managed per-user tokens (OBL-01, Engram Cloud v1.18.0 parity).
//
// Unlike the stateless account token (a self-verifying HMAC blob) and the legacy
// single shared secret, a managed token is a high-entropy random credential whose
// HMAC-SHA256(pepper, raw) hash — and ONLY the hash — is persisted here. The raw
// token is returned to the operator exactly once at issuance and is never
// recoverable from the stored hash. Every sync request presenting a managed token
// is validated against this table so the credential can be revoked, and so a
// disabled owner is rejected at runtime.

// AuditActionTokenIssue is the audit `action` recorded when a managed token is
// minted. Issuance and this audit row are written in the SAME transaction so a
// live token can never exist without its audit trail ("no unaudited credential").
const AuditActionTokenIssue = "token_issue"

// AuditOutcomeTokenIssued is the audit `outcome` for a successful token mint.
const AuditOutcomeTokenIssued = "issued"

// tokenAuditProjectSentinel is stored in the NOT NULL audit `project` column for
// token lifecycle events, which are account-scoped rather than project-scoped.
const tokenAuditProjectSentinel = "*"

var (
	// ErrManagedTokenUserNotFound is returned when a token operation references
	// a user id that does not exist.
	ErrManagedTokenUserNotFound = errors.New("cloudstore: managed token user not found")
	// ErrManagedTokenUserDisabled is returned by IssueManagedToken when the
	// target user is disabled — a disabled user can never receive a new token.
	ErrManagedTokenUserDisabled = errors.New("cloudstore: managed token user is disabled")
)

// ManagedToken is a persisted managed-token record. The raw token value is never
// stored — only TokenHash. It is returned from IssueManagedToken carrying the
// generated id so the caller can echo the raw token (held separately) exactly once.
type ManagedToken struct {
	ID         string
	UserID     string
	Label      string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// ManagedTokenResolution is the per-request validation view of a presented token
// hash, joined against its owning user. Revoked/UserDisabled are the two runtime
// rejection signals enforced on every sync request.
type ManagedTokenResolution struct {
	TokenID      string
	UserID       string
	Username     string
	Revoked      bool
	UserDisabled bool
}

// IssueManagedToken persists a managed token (hash only) for userID and, in the
// SAME transaction, writes an audit row. If the user does not exist or is
// disabled, no token is created. If the audit insert fails the whole thing rolls
// back, guaranteeing no live token lacks an audit trail.
func (cs *CloudStore) IssueManagedToken(ctx context.Context, userID, tokenHash, label string, audit AuditEntry) (*ManagedToken, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	tokenHash = strings.TrimSpace(tokenHash)
	if userID == "" {
		return nil, fmt.Errorf("cloudstore: user_id is required")
	}
	if tokenHash == "" {
		return nil, fmt.Errorf("cloudstore: token hash is required")
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: begin issue token tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Guard: the owning user must exist and must not be disabled.
	var disabledAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT disabled_at FROM cloud_users WHERE id = $1::bigint`, userID).Scan(&disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrManagedTokenUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lookup token user: %w", err)
	}
	if disabledAt.Valid {
		return nil, ErrManagedTokenUserDisabled
	}

	var labelArg any
	if l := strings.TrimSpace(label); l != "" {
		labelArg = l
	}
	var mt ManagedToken
	err = tx.QueryRowContext(ctx, `
		INSERT INTO cloud_tokens (user_id, token_hash, label)
		VALUES ($1::bigint, $2, $3)
		RETURNING id::text, user_id::text, created_at`,
		userID, tokenHash, labelArg,
	).Scan(&mt.ID, &mt.UserID, &mt.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: insert managed token: %w", err)
	}
	mt.Label = strings.TrimSpace(label)

	// Atomic audit write — a failed audit insert rolls the token back.
	audit.Action = AuditActionTokenIssue
	audit.Outcome = AuditOutcomeTokenIssued
	if strings.TrimSpace(audit.Project) == "" {
		audit.Project = tokenAuditProjectSentinel
	}
	if strings.TrimSpace(audit.Contributor) == "" {
		audit.Contributor = "operator"
	}
	if audit.Metadata == nil {
		audit.Metadata = map[string]any{}
	}
	audit.Metadata["token_id"] = mt.ID
	audit.Metadata["user_id"] = mt.UserID
	if err := insertAuditEntryTx(ctx, tx, audit); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cloudstore: commit issue token: %w", err)
	}
	tx = nil
	return &mt, nil
}

// ResolveManagedToken returns the runtime validation view for the presented token
// hash, or (nil, nil) when no such token exists. It joins the owning user so the
// caller can enforce both revocation and user-disabled in one round trip.
func (cs *CloudStore) ResolveManagedToken(ctx context.Context, tokenHash string) (*ManagedTokenResolution, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, nil
	}
	const q = `
		SELECT t.id::text, t.user_id::text, u.username,
		       (t.revoked_at IS NOT NULL) AS revoked,
		       (u.disabled_at IS NOT NULL) AS user_disabled
		FROM cloud_tokens t
		JOIN cloud_users u ON u.id = t.user_id
		WHERE t.token_hash = $1`
	var res ManagedTokenResolution
	err := cs.db.QueryRowContext(ctx, q, tokenHash).
		Scan(&res.TokenID, &res.UserID, &res.Username, &res.Revoked, &res.UserDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: resolve managed token: %w", err)
	}
	return &res, nil
}

// TouchManagedToken updates last_used_at for a token. It is best-effort: callers
// typically ignore the returned error so a stats write never fails a request.
func (cs *CloudStore) TouchManagedToken(ctx context.Context, tokenID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return fmt.Errorf("cloudstore: token id is required")
	}
	_, err := cs.db.ExecContext(ctx, `UPDATE cloud_tokens SET last_used_at = NOW() WHERE id = $1::bigint`, tokenID)
	if err != nil {
		return fmt.Errorf("cloudstore: touch managed token: %w", err)
	}
	return nil
}

// RevokeManagedToken marks a token revoked. It is idempotent: revoking an
// already-revoked or unknown token is a no-op that returns nil.
func (cs *CloudStore) RevokeManagedToken(ctx context.Context, tokenID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return fmt.Errorf("cloudstore: token id is required")
	}
	_, err := cs.db.ExecContext(ctx,
		`UPDATE cloud_tokens SET revoked_at = NOW() WHERE id = $1::bigint AND revoked_at IS NULL`, tokenID)
	if err != nil {
		return fmt.Errorf("cloudstore: revoke managed token: %w", err)
	}
	return nil
}

// SetUserDisabled toggles a user's disabled state. Disabling stamps disabled_at
// with NOW(); enabling clears it. Returns ErrManagedTokenUserNotFound when the
// user id does not exist.
func (cs *CloudStore) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("cloudstore: user_id is required")
	}
	res, err := cs.db.ExecContext(ctx,
		`UPDATE cloud_users SET disabled_at = CASE WHEN $2 THEN NOW() ELSE NULL END WHERE id = $1::bigint`,
		userID, disabled)
	if err != nil {
		return fmt.Errorf("cloudstore: set user disabled: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagedTokenUserNotFound
	}
	return nil
}

// insertAuditEntryTx writes an audit row on an open transaction. It mirrors
// InsertAuditEntry (audit_log.go) but takes a *sql.Tx so token issuance and its
// audit row commit atomically.
func insertAuditEntryTx(ctx context.Context, tx *sql.Tx, entry AuditEntry) error {
	var reasonCode *string
	if r := strings.TrimSpace(entry.ReasonCode); r != "" {
		reasonCode = &r
	}
	var metadataJSON []byte
	if len(entry.Metadata) > 0 {
		var merr error
		metadataJSON, merr = json.Marshal(entry.Metadata)
		if merr != nil {
			return fmt.Errorf("cloudstore: insert audit (tx): marshal metadata: %w", merr)
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO cloud_sync_audit_log
			(contributor, project, action, outcome, entry_count, reason_code, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.Contributor, entry.Project, entry.Action, entry.Outcome, entry.EntryCount, reasonCode, metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: insert audit (tx): %w", err)
	}
	return nil
}
