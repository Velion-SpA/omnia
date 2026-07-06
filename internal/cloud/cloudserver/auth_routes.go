package cloudserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// AccountService is the optional capability an injected Authenticator may
// implement to expose account signup/login. It is detected via type assertion
// in New (mirroring how ProjectAuthorizer is detected): when the auth
// dependency satisfies AccountService, the public /auth routes are registered.
type AccountService interface {
	Signup(username, email, password string) (*cloudstore.User, error)
	Login(username, password string) (token string, user *cloudstore.User, err error)
}

// DeviceLoginService is an optional extension detected via type assertion.
// When the account service supports it, login with a device name is possible.
type DeviceLoginService interface {
	LoginForDevice(username, password, deviceName string) (token string, user *cloudstore.User, err error)
}

const maxAuthBodyBytes int64 = 16 * 1024

type signupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Device   string `json:"device,omitempty"`
}

// handleSignup creates an account and returns {id,username,email} on success.
func (s *CloudServer) handleSignup(w http.ResponseWriter, r *http.Request) {
	if s.account == nil {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "account service unavailable"})
		return
	}
	// Open signup is closed by default (OBL-02): a LAN-reachable server must not let
	// any caller self-register. Accounts are provisioned via `omnia cloud bootstrap-admin`
	// (first admin) and the operator admin API thereafter. Set OMNIA_CLOUD_OPEN_SIGNUP=1
	// to deliberately reopen self-signup.
	if !s.openSignup {
		jsonResponse(w, http.StatusForbidden, map[string]string{
			"error": "open signup is disabled; ask an operator to provision your account (omnia cloud bootstrap-admin / admin API) or set OMNIA_CLOUD_OPEN_SIGNUP=1 to reopen self-signup",
		})
		return
	}
	var req signupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	user, err := s.account.Signup(req.Username, req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAccountExists):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "account already exists"})
		case errors.Is(err, auth.ErrUsernameRequired), errors.Is(err, auth.ErrPasswordTooShort):
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not create account"})
		}
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]string{
		"id":       user.ID,
		"username": user.Username,
		"email":    user.Email,
	})
}

// handleRefresh exchanges a valid account token for a newly-minted one.
// The current token may be supplied via an Authorization: Bearer header or a
// JSON body {"token": "..."}.  The endpoint is intentionally public (no
// withAuth wrapper) because the caller may already have an expiring token.
func (s *CloudServer) handleRefresh(w http.ResponseWriter, r *http.Request, rs RefreshService) {
	currentToken := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		currentToken = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			currentToken = body.Token
		}
	}
	currentToken = strings.TrimSpace(currentToken)
	if currentToken == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	newToken, err := rs.Refresh(currentToken)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": newToken})
}

// handleLogin verifies credentials and returns {token} on success.
func (s *CloudServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.account == nil {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "account service unavailable"})
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	// If a device name is provided AND the account service supports device login,
	// mint a device-bound token.
	req.Device = strings.TrimSpace(req.Device)
	if req.Device != "" {
		if dls, ok := s.account.(DeviceLoginService); ok {
			token, _, err := dls.LoginForDevice(req.Username, req.Password, req.Device)
			if err != nil {
				if errors.Is(err, auth.ErrInvalidCredentials) {
					jsonResponse(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
					return
				}
				jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not log in"})
				return
			}
			jsonResponse(w, http.StatusOK, map[string]string{"token": token})
			return
		}
	}
	// Fall through to normal login (no device).
	token, _, err := s.account.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			jsonResponse(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not log in"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"token": token})
}
