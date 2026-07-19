package cloudstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── Outcome and Action Constants ─────────────────────────────────────────────

// AuditOutcomeRejectedProjectPaused is the single outcome constant for v1.
// Used as the `outcome` column value when a push is rejected because the project sync is paused.
const AuditOutcomeRejectedProjectPaused = "rejected_project_paused"

// AuditActionMutationPush discriminates mutation push rejections.
const AuditActionMutationPush = "mutation_push"

// AuditActionChunkPush discriminates chunk push rejections.
const AuditActionChunkPush = "chunk_push"

// AuditActionProjectPause discriminates an operator pausing a project's sync
// (OBL-04 — the control side of the pause/resume enforcement above).
const AuditActionProjectPause = "project_pause"

// AuditActionProjectResume discriminates an operator resuming a project's sync.
const AuditActionProjectResume = "project_resume"

// AuditOutcomeProjectPaused is the outcome for a successful operator pause action.
// Distinct from AuditOutcomeRejectedProjectPaused, which records a PUSH rejected
// because the project was already paused.
const AuditOutcomeProjectPaused = "project_paused"

// AuditOutcomeProjectResumed is the outcome for a successful operator resume action.
const AuditOutcomeProjectResumed = "project_resumed"

// ─── OBL-05: expanded security-relevant event constants ──────────────────────
//
// AuditProjectSentinel is stored in the NOT NULL audit `project` column for
// account-scoped events that are not tied to a single project: token and user
// lifecycle, login/signup, device lifecycle, and account-admin promote/demote.
// (Membership grant/revoke IS project-scoped and uses the real project name.)
const AuditProjectSentinel = "*"

// AuditActionTokenRevoke discriminates a managed-token revocation. Unlike
// issuance (tokens.go, atomic with its audit row), revocation is best-effort:
// the token is already revoked in the DB by the time the audit write happens,
// so a failed audit insert only logs a warning (it never un-revokes the token).
const AuditActionTokenRevoke = "token_revoke"

// AuditOutcomeTokenRevoked is the outcome for a successful token revocation.
const AuditOutcomeTokenRevoked = "revoked"

// AuditActionUserDisable discriminates an operator disabling an account.
const AuditActionUserDisable = "user_disable"

// AuditOutcomeUserDisabled is the outcome for a successful user disable.
const AuditOutcomeUserDisabled = "disabled"

// AuditActionUserEnable discriminates an operator re-enabling a disabled account.
const AuditActionUserEnable = "user_enable"

// AuditOutcomeUserEnabled is the outcome for a successful user enable.
const AuditOutcomeUserEnabled = "enabled"

// AuditActionLogin discriminates a login attempt (device-bound or plain). Both
// AuditOutcomeLoginSuccess and AuditOutcomeLoginFailed use this same action so
// the two are trivially correlated by (action, contributor) in the audit view.
const AuditActionLogin = "login"

// AuditOutcomeLoginSuccess is the outcome for a successful login.
const AuditOutcomeLoginSuccess = "login_success"

// AuditOutcomeLoginFailed is the outcome for a login rejected on bad credentials.
const AuditOutcomeLoginFailed = "login_failed"

// AuditActionSignup discriminates a new-account signup.
const AuditActionSignup = "signup"

// AuditOutcomeSignupSucceeded is the outcome for a successful signup.
const AuditOutcomeSignupSucceeded = "succeeded"

// AuditActionMembershipGrant discriminates granting or updating a project
// membership (project-scoped: Project carries the real project name).
const AuditActionMembershipGrant = "membership_grant"

// AuditOutcomeMembershipGranted is the outcome for a successful membership grant.
const AuditOutcomeMembershipGranted = "granted"

// AuditActionMembershipRevoke discriminates removing a project membership
// (project-scoped: Project carries the real project name).
const AuditActionMembershipRevoke = "membership_revoke"

// AuditOutcomeMembershipRevoked is the outcome for a successful membership revoke.
const AuditOutcomeMembershipRevoked = "revoked"

// AuditActionDeviceCreate discriminates a NEW device being registered (first
// GetOrCreateDevice for an (account, name) pair). Re-authenticating an already
// known device does not re-emit this event.
const AuditActionDeviceCreate = "device_create"

// AuditOutcomeDeviceCreated is the outcome for a new device registration.
const AuditOutcomeDeviceCreated = "created"

// AuditActionDeviceRevoke discriminates an account removing one of its devices.
const AuditActionDeviceRevoke = "device_revoke"

// AuditOutcomeDeviceRevoked is the outcome for a successful device removal.
const AuditOutcomeDeviceRevoked = "revoked"

// AuditActionAdminPromote discriminates an operator granting the account-level
// admin flag (OBL-16).
const AuditActionAdminPromote = "admin_promote"

// AuditOutcomeAdminPromoted is the outcome for a successful promote.
const AuditOutcomeAdminPromoted = "promoted"

// AuditActionAdminDemote discriminates an operator revoking the account-level
// admin flag (OBL-16).
const AuditActionAdminDemote = "admin_demote"

// AuditOutcomeAdminDemoted is the outcome for a successful demote.
const AuditOutcomeAdminDemoted = "demoted"

// ─── Command Center v2, Slice 1: user CRUD event constants ───────────────────
//
// AuditActionUserCreate discriminates an operator creating a new account. The
// generated one-time password is NEVER included in this event's metadata.
const AuditActionUserCreate = "user_create"

// AuditOutcomeUserCreated is the outcome for a successful create.
const AuditOutcomeUserCreated = "created"

// AuditActionUserUpdate discriminates an operator editing an account's
// username/email.
const AuditActionUserUpdate = "user_update"

// AuditOutcomeUserUpdated is the outcome for a successful edit.
const AuditOutcomeUserUpdated = "updated"

// AuditActionUserPasswordReset discriminates an operator resetting an
// account's password (admin-provided or generated). The password value is
// NEVER included in this event's metadata.
const AuditActionUserPasswordReset = "user_password_reset"

// AuditOutcomeUserPasswordReset is the outcome for a successful reset.
const AuditOutcomeUserPasswordReset = "reset"

// AuditActionUserHardDelete discriminates an operator permanently deleting an
// account (distinct from AuditActionUserDisable, which is the reversible
// soft-deactivate).
const AuditActionUserHardDelete = "user_hard_delete"

// AuditOutcomeUserHardDeleted is the outcome for a successful hard delete.
const AuditOutcomeUserHardDeleted = "hard_deleted"

// ─── Types ────────────────────────────────────────────────────────────────────

// AuditEntry is the write-side struct for inserting an audit log row.
type AuditEntry struct {
	Contributor string
	Project     string
	Action      string // use AuditAction* constants
	Outcome     string // use AuditOutcome* constants
	EntryCount  int
	ReasonCode  string
	Metadata    map[string]any // reserved for future use; nil is fine (stored as NULL)
}

// AuditFilter holds optional filter fields for ListAuditEntriesPaginated.
// All fields are independently optional; zero values mean "no filter".
type AuditFilter struct {
	Contributor    string
	Project        string
	Outcome        string
	OccurredAtFrom time.Time // zero value = no lower bound
	OccurredAtTo   time.Time // zero value = no upper bound
}

// DashboardAuditRow is the read-side struct returned from ListAuditEntriesPaginated.
// N6: Metadata is now included so callers have the full audit row.
// In v1 UI the field is present but not rendered; the API contract is complete.
type DashboardAuditRow struct {
	ID          int64
	OccurredAt  string // RFC3339 UTC
	Contributor string
	Project     string
	Action      string
	Outcome     string
	EntryCount  int
	ReasonCode  string
	Metadata    map[string]any // nil when NULL in DB
}

// ─── CloudStore Methods ───────────────────────────────────────────────────────

// InsertAuditEntry synchronously inserts one audit log row.
// On DB error the error is returned to the caller; do NOT suppress it.
// The caller is responsible for logging at WARN and deciding HTTP response.
// JW5: Metadata field is included in the INSERT via json.Marshal so that
// future-proofing data is not silently dropped.
// N5: nil or empty Metadata map is stored as NULL in the DB (not as "{}").
func (cs *CloudStore) InsertAuditEntry(ctx context.Context, entry AuditEntry) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: InsertAuditEntry: not initialized")
	}
	var reasonCode *string
	if r := strings.TrimSpace(entry.ReasonCode); r != "" {
		reasonCode = &r
	}
	var metadataJSON []byte
	if len(entry.Metadata) > 0 {
		var merr error
		metadataJSON, merr = json.Marshal(entry.Metadata)
		if merr != nil {
			return fmt.Errorf("cloudstore: InsertAuditEntry: marshal metadata: %w", merr)
		}
	}
	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_sync_audit_log
			(contributor, project, action, outcome, entry_count, reason_code, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.Contributor,
		entry.Project,
		entry.Action,
		entry.Outcome,
		entry.EntryCount,
		reasonCode,
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: InsertAuditEntry: %w", err)
	}
	return nil
}

// ListAuditEntriesPaginated returns a page of audit rows matching the filter,
// sorted by occurred_at DESC, plus the total matching count.
// limit and offset are SQL LIMIT/OFFSET values.
func (cs *CloudStore) ListAuditEntriesPaginated(ctx context.Context, filter AuditFilter, limit, offset int) ([]DashboardAuditRow, int, error) {
	if cs == nil || cs.db == nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated: not initialized")
	}

	var conditions []string
	var args []any
	argIdx := 1

	if c := strings.TrimSpace(filter.Contributor); c != "" {
		conditions = append(conditions, fmt.Sprintf("contributor = $%d", argIdx))
		args = append(args, c)
		argIdx++
	}
	if p := strings.TrimSpace(filter.Project); p != "" {
		conditions = append(conditions, fmt.Sprintf("project = $%d", argIdx))
		args = append(args, p)
		argIdx++
	}
	if o := strings.TrimSpace(filter.Outcome); o != "" {
		conditions = append(conditions, fmt.Sprintf("outcome = $%d", argIdx))
		args = append(args, o)
		argIdx++
	}
	if !filter.OccurredAtFrom.IsZero() {
		conditions = append(conditions, fmt.Sprintf("occurred_at >= $%d", argIdx))
		args = append(args, filter.OccurredAtFrom.UTC())
		argIdx++
	}
	if !filter.OccurredAtTo.IsZero() {
		conditions = append(conditions, fmt.Sprintf("occurred_at <= $%d", argIdx))
		args = append(args, filter.OccurredAtTo.UTC())
		argIdx++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// COUNT query for total.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM cloud_sync_audit_log %s", where)
	var total int
	if err := cs.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated count: %w", err)
	}

	// Data query with LIMIT/OFFSET.
	// N6: metadata is now included in SELECT so callers receive the full audit row.
	dataQuery := fmt.Sprintf(`
		SELECT id, occurred_at, contributor, project, action, outcome, entry_count,
		       COALESCE(reason_code, ''), metadata
		FROM cloud_sync_audit_log
		%s
		ORDER BY occurred_at DESC
		LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)
	dataArgs := make([]any, len(args)+2)
	copy(dataArgs, args)
	dataArgs[len(args)] = limit
	dataArgs[len(args)+1] = offset

	dbRows, err := cs.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated query: %w", err)
	}
	defer dbRows.Close()

	var result []DashboardAuditRow
	for dbRows.Next() {
		var row DashboardAuditRow
		var occurredAt time.Time
		var metaBytes []byte
		if err := dbRows.Scan(
			&row.ID,
			&occurredAt,
			&row.Contributor,
			&row.Project,
			&row.Action,
			&row.Outcome,
			&row.EntryCount,
			&row.ReasonCode,
			&metaBytes,
		); err != nil {
			return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated scan: %w", err)
		}
		row.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		if metaBytes != nil {
			if err := json.Unmarshal(metaBytes, &row.Metadata); err != nil {
				return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated unmarshal metadata: %w", err)
			}
		}
		result = append(result, row)
	}
	if err := dbRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated iterate: %w", err)
	}

	if result == nil {
		result = []DashboardAuditRow{}
	}
	return result, total, nil
}
