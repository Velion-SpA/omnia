package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Operator-facing user CRUD (Command Center v2, Slice 1 — backend only, no
// UI/templ yet). Every handler re-checks the operator session via
// requireOperator, exactly like the rest of the OBL-13 Admin section, and
// every mutation writes a best-effort audit row via emitAudit. Routes are
// registered in the SAME s.store.(adminDashboardStore) gated block as the
// existing /admin section (cloudserver.go).

// minAdminSetPasswordLength mirrors auth.minPasswordLength (unexported in the
// auth package, so re-declared here) — the same floor Signup enforces applies
// to an operator directly setting a password.
const minAdminSetPasswordLength = 8

// atomicDeactivateGuard is the optional RACE-SAFE deactivate capability
// (cloudstore.DeactivateUserGuarded). setUserDisabled (tokens.go) type-
// asserts s.store against this to opt into the atomic guard when the
// concrete store supports it, falling back to the plain unconditional
// SetUserDisabled when it does not (e.g. the older managed-token-only fake
// with no admin-flag concept at all).
//
// SECURITY FIX (Slice 1 review): this REPLACES the former lastAdminGuardStore
// / refuseIfLastAdmin pair, which performed the last-admin CHECK (IsUserAdmin
// + CountAdmins) as a separate read BEFORE a separate mutating call — a
// classic check-then-act TOCTOU where two concurrent requests targeting
// DIFFERENT admin accounts could each pass the check before either mutation
// committed, leaving zero admins. The guard now lives ENTIRELY inside the
// store method's own transaction (cloudstore.lockAdminIDsForUpdate row-locks
// the admin set before checking), so there is no longer a separate
// handler-layer check to race against.
type atomicDeactivateGuard interface {
	DeactivateUserGuarded(ctx context.Context, userID string) error
}

var _ atomicDeactivateGuard = (*cloudstore.CloudStore)(nil)

// ─── POST /admin/users (create) ──────────────────────────────────────────────

type adminCreateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	// Role is accepted for forward compatibility with the outline but is
	// intentionally NOT wired to any behavior in this slice: there is no
	// per-account "role" concept beyond the is_admin flag, and auto-granting
	// admin at create time would bypass the audit/step-up path the dedicated
	// promote/demote endpoints already established. See apply-progress
	// deviations for the full rationale.
	Role string `json:"role,omitempty"`
}

type adminCreateUserResponse struct {
	ID                string `json:"id"`
	Username          string `json:"username"`
	GeneratedPassword string `json:"generated_password"`
}

// handleAdminCreateUser handles POST /admin/users. It generates a random
// strong password server-side, hashes it, and returns the PLAINTEXT exactly
// once in this response — it is never logged, audited, or retrievable again.
func (s *CloudServer) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req adminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	username := strings.TrimSpace(req.Username)
	email := strings.TrimSpace(req.Email)
	if username == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "username is required"})
		return
	}
	if !isValidEmail(email) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "a valid email is required"})
		return
	}

	rawPassword, err := generateStrongPassword()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not generate password"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not create user"})
		return
	}

	id, err := as.AdminCreateUser(r.Context(), username, email, string(hash))
	if err != nil {
		if errors.Is(err, cloudstore.ErrUserExists) {
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "username or email already in use"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not create user"})
		return
	}

	// OBL-05-style best-effort audit of the CREATE action. The password is
	// NEVER included in metadata — only the response body carries it, once.
	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: s.operatorActor(r),
		Project:     cloudstore.AuditProjectSentinel,
		Action:      cloudstore.AuditActionUserCreate,
		Outcome:     cloudstore.AuditOutcomeUserCreated,
		Metadata:    map[string]any{"user_id": id, "username": username},
	})
	jsonResponse(w, http.StatusCreated, adminCreateUserResponse{ID: id, Username: username, GeneratedPassword: rawPassword})
}

// ─── PUT /admin/users/{id} (edit) ────────────────────────────────────────────

type adminUpdateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

// handleAdminUpdateUser handles PUT /admin/users/{id} — edits username/email.
func (s *CloudServer) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	if !isNumericID(userID) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id must be numeric"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req adminUpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	username := strings.TrimSpace(req.Username)
	email := strings.TrimSpace(req.Email)
	if username == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "username is required"})
		return
	}
	if !isValidEmail(email) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "a valid email is required"})
		return
	}

	if err := as.AdminUpdateUser(r.Context(), userID, username, email); err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrUserExists):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "username or email already in use"})
		case errors.Is(err, cloudstore.ErrManagedTokenUserNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update user"})
		}
		return
	}

	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: s.operatorActor(r),
		Project:     cloudstore.AuditProjectSentinel,
		Action:      cloudstore.AuditActionUserUpdate,
		Outcome:     cloudstore.AuditOutcomeUserUpdated,
		Metadata:    map[string]any{"user_id": userID, "username": username},
	})
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "updated", "id": userID, "username": username, "email": email})
}

// ─── POST /admin/users/{id}/password (reset) ─────────────────────────────────

type adminResetPasswordRequest struct {
	Password string `json:"password,omitempty"`
}

type adminResetPasswordResponse struct {
	Status            string `json:"status"`
	ID                string `json:"id"`
	GeneratedPassword string `json:"generated_password,omitempty"`
}

// handleAdminResetUserPassword handles POST /admin/users/{id}/password. If
// the body supplies a password, the operator's chosen value is hashed and
// stored (never echoed back). If the body omits it, a random strong password
// is generated and returned EXACTLY ONCE, exactly like create.
func (s *CloudServer) handleAdminResetUserPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	if !isNumericID(userID) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id must be numeric"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req adminResetPasswordRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}

	resp := adminResetPasswordResponse{Status: "reset", ID: userID}
	var rawPassword string
	if strings.TrimSpace(req.Password) != "" {
		rawPassword = req.Password
		if len(rawPassword) < minAdminSetPasswordLength {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 characters"})
			return
		}
	} else {
		generated, err := generateStrongPassword()
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not generate password"})
			return
		}
		rawPassword = generated
		resp.GeneratedPassword = generated
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not set password"})
		return
	}
	if err := as.AdminSetUserPassword(r.Context(), userID, string(hash)); err != nil {
		if errors.Is(err, cloudstore.ErrManagedTokenUserNotFound) {
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not set password"})
		return
	}

	// The password value is NEVER included in audit metadata, whether
	// admin-provided or generated.
	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: s.operatorActor(r),
		Project:     cloudstore.AuditProjectSentinel,
		Action:      cloudstore.AuditActionUserPasswordReset,
		Outcome:     cloudstore.AuditOutcomeUserPasswordReset,
		Metadata:    map[string]any{"user_id": userID},
	})
	jsonResponse(w, http.StatusOK, resp)
}

// ─── POST /admin/users/{id}/delete (hard delete) ─────────────────────────────

// handleAdminHardDeleteUser handles POST /admin/users/{id}/delete. It requires
// an EXPLICIT confirmation signal (?confirm=1 or JSON body {"confirm":true})
// and refuses to delete the last remaining admin, mirroring the existing
// demote guard.
func (s *CloudServer) handleAdminHardDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	if !isNumericID(userID) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id must be numeric"})
		return
	}

	confirmed := isConfirmQueryFlag(r.URL.Query().Get("confirm"))
	if !confirmed {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
		var body struct {
			Confirm bool `json:"confirm"`
		}
		if r.ContentLength != 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		confirmed = body.Confirm
	}
	if !confirmed {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": `hard delete requires explicit confirmation (?confirm=1 or {"confirm":true})`})
		return
	}

	// Last-admin guard: AdminHardDeleteUser now performs this check ATOMICALLY
	// inside its own transaction (cloudstore.lockAdminIDsForUpdate row-locks
	// the admin set before checking), so there is no separate handler-layer
	// pre-check to race against — see the security-fix note on
	// atomicDeactivateGuard above for the full TOCTOU rationale this closes.
	if err := as.AdminHardDeleteUser(r.Context(), userID); err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrLastAdmin):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "cannot hard delete the last remaining admin"})
		case errors.Is(err, cloudstore.ErrManagedTokenUserNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not delete user"})
		}
		return
	}

	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: s.operatorActor(r),
		Project:     cloudstore.AuditProjectSentinel,
		Action:      cloudstore.AuditActionUserHardDelete,
		Outcome:     cloudstore.AuditOutcomeUserHardDeleted,
		Metadata:    map[string]any{"user_id": userID},
	})
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "deleted", "id": userID})
}

// isConfirmQueryFlag reports whether a `?confirm=` query value is a truthy
// confirmation signal ("1" or "true", case-insensitive).
func isConfirmQueryFlag(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true")
}

// isValidEmail reports whether email is a parseable RFC 5322 address.
// net/mail.ParseAddress rejects the empty string outright, so this single
// check also closes the "blank email collides on UNIQUE for a second blank
// row" gap (SHOULD-FIX 3, Slice 1 security review) without a separate
// empty-string check.
func isValidEmail(email string) bool {
	_, err := mail.ParseAddress(email)
	return err == nil
}

// isNumericID reports whether id is a non-empty string of ASCII digits — the
// only shape cloud_users.id (BIGSERIAL) can ever legitimately take. Every
// store method backing a {id} route casts the string to ::bigint; without
// this pre-check a non-numeric id reaches that cast and surfaces as a raw,
// unmapped Postgres syntax error (500) instead of a clean 400 (SHOULD-FIX 4,
// Slice 1 security review).
func isNumericID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
