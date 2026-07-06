package cloudserver

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Managed-token administration endpoints (OBL-01). These are gated on the
// operator/admin credential (OMNIA_CLOUD_ADMIN), NOT the sync bearer, and are
// registered only when both the account service can issue managed tokens and the
// store supports token/user lifecycle.

// managedTokenIssuer is the optional capability (auth.Service) that mints a
// managed token and returns the raw value exactly once. Detected on s.account.
type managedTokenIssuer interface {
	IssueManagedToken(ctx context.Context, userID, label string) (rawToken, tokenID string, err error)
}

// managedTokenAdminStore is the optional store capability for token revocation
// and user disable/enable. Detected on s.store.
type managedTokenAdminStore interface {
	RevokeManagedToken(ctx context.Context, tokenID string) error
	SetUserDisabled(ctx context.Context, userID string, disabled bool) error
}

// Compile-time assertions: the concrete types must satisfy the seams.
var (
	_ managedTokenAdminStore = (*cloudstore.CloudStore)(nil)
	_ managedTokenIssuer     = (*auth.Service)(nil)
)

// requireAdminBearer authorizes a managed-token admin request. It requires the
// configured operator credential (dashboardAdmin / OMNIA_CLOUD_ADMIN) presented
// as the Bearer token, compared in constant time. The sync bearer never passes.
// On failure it writes the HTTP error and returns false.
func (s *CloudServer) requireAdminBearer(w http.ResponseWriter, r *http.Request) bool {
	admin := strings.TrimSpace(s.dashboardAdmin)
	if admin == "" {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "managed-token administration requires an operator credential"})
		return false
	}
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		jsonResponse(w, http.StatusUnauthorized, map[string]string{"error": "bearer token required"})
		return false
	}
	if !hmac.Equal([]byte(strings.TrimSpace(parts[1])), []byte(admin)) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "operator credential required"})
		return false
	}
	return true
}

type issueTokenRequest struct {
	UserID string `json:"user_id"`
	Label  string `json:"label"`
}

// handleIssueManagedToken handles POST /admin/tokens. On success it returns the
// RAW token EXACTLY ONCE — it is never retrievable again.
func (s *CloudServer) handleIssueManagedToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminBearer(w, r) {
		return
	}
	issuer, ok := s.account.(managedTokenIssuer)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "managed tokens unavailable"})
		return
	}
	var req issueTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	raw, tokenID, err := issuer.IssueManagedToken(r.Context(), req.UserID, strings.TrimSpace(req.Label))
	if err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrManagedTokenUserNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		case errors.Is(err, cloudstore.ErrManagedTokenUserDisabled):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "user is disabled"})
		case errors.Is(err, auth.ErrManagedTokensNotEnabled):
			jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "managed tokens are not enabled"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not issue token"})
		}
		return
	}
	// The raw token is shown ONCE here and never persisted or logged.
	jsonResponse(w, http.StatusCreated, map[string]string{
		"id":      tokenID,
		"user_id": req.UserID,
		"token":   raw,
		"label":   strings.TrimSpace(req.Label),
	})
}

// handleRevokeManagedToken handles POST /admin/tokens/{id}/revoke. Idempotent.
func (s *CloudServer) handleRevokeManagedToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminBearer(w, r) {
		return
	}
	adminStore, ok := s.store.(managedTokenAdminStore)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "managed tokens unavailable"})
		return
	}
	tokenID := strings.TrimSpace(r.PathValue("id"))
	if tokenID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "token id is required"})
		return
	}
	if err := adminStore.RevokeManagedToken(r.Context(), tokenID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke token"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "revoked", "id": tokenID})
}

// handleDisableUser handles POST /admin/users/{id}/disable.
func (s *CloudServer) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	s.setUserDisabled(w, r, true)
}

// handleEnableUser handles POST /admin/users/{id}/enable.
func (s *CloudServer) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	s.setUserDisabled(w, r, false)
}

func (s *CloudServer) setUserDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	if !s.requireAdminBearer(w, r) {
		return
	}
	adminStore, ok := s.store.(managedTokenAdminStore)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "managed tokens unavailable"})
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	if err := adminStore.SetUserDisabled(r.Context(), userID, disabled); err != nil {
		if errors.Is(err, cloudstore.ErrManagedTokenUserNotFound) {
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update user"})
		return
	}
	status := "enabled"
	if disabled {
		status = "disabled"
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": status, "user_id": userID})
}
