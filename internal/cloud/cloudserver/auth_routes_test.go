package cloudserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// newTestAuthService builds a real *auth.Service backed by a nil cloudstore
// (sufficient for token-only operations like MintAccountToken / Refresh).
func newTestAuthService(t *testing.T) *auth.Service {
	t.Helper()
	svc, err := auth.NewService(nil, strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	return svc
}

// fakeAccountAuth implements both Authenticator and AccountService so New
// registers the public /auth routes. It is an in-memory stand-in for
// *auth.Service that avoids a live Postgres dependency.
type fakeAccountAuth struct {
	users map[string]string // username -> password
}

func (a *fakeAccountAuth) Authorize(*http.Request) error { return nil }

func (a *fakeAccountAuth) Signup(username, email, password string) (*cloudstore.User, error) {
	if strings.TrimSpace(username) == "" {
		return nil, auth.ErrUsernameRequired
	}
	if len(password) < 8 {
		return nil, auth.ErrPasswordTooShort
	}
	if a.users == nil {
		a.users = map[string]string{}
	}
	if _, ok := a.users[username]; ok {
		return nil, auth.ErrAccountExists
	}
	a.users[username] = password
	return &cloudstore.User{ID: "user-1", Username: username, Email: email}, nil
}

func (a *fakeAccountAuth) Login(username, password string) (string, *cloudstore.User, error) {
	pw, ok := a.users[username]
	if !ok || pw != password {
		return "", nil, auth.ErrInvalidCredentials
	}
	return "token-for-" + username, &cloudstore.User{ID: "user-1", Username: username}, nil
}

func TestAuthRoutesSignupThenLogin(t *testing.T) {
	srv := New(&fakeStore{}, &fakeAccountAuth{}, 0)
	handler := srv.Handler()

	signup := httptest.NewRequest("POST", "/auth/signup",
		strings.NewReader(`{"username":"alice","email":"alice@example.com","password":"supersecret"}`))
	signupRec := httptest.NewRecorder()
	handler.ServeHTTP(signupRec, signup)
	if signupRec.Code != http.StatusCreated {
		t.Fatalf("signup status = %d, want %d (body: %s)", signupRec.Code, http.StatusCreated, signupRec.Body.String())
	}
	var signupBody struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if err := json.Unmarshal(signupRec.Body.Bytes(), &signupBody); err != nil {
		t.Fatalf("decode signup body: %v", err)
	}
	if signupBody.Username != "alice" || signupBody.ID == "" {
		t.Fatalf("unexpected signup body: %+v", signupBody)
	}

	login := httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(`{"username":"alice","password":"supersecret"}`))
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d (body: %s)", loginRec.Code, http.StatusOK, loginRec.Body.String())
	}
	var loginBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginBody); err != nil {
		t.Fatalf("decode login body: %v", err)
	}
	if loginBody.Token == "" {
		t.Fatalf("expected a token, got empty (body: %s)", loginRec.Body.String())
	}
}

func TestAuthRoutesStatusCodes(t *testing.T) {
	srv := New(&fakeStore{}, &fakeAccountAuth{}, 0)
	handler := srv.Handler()

	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{name: "signup created", path: "/auth/signup", body: `{"username":"bob","email":"b@e.com","password":"supersecret"}`, want: http.StatusCreated},
		{name: "signup duplicate conflict", path: "/auth/signup", body: `{"username":"bob","email":"b2@e.com","password":"supersecret"}`, want: http.StatusConflict},
		{name: "signup short password", path: "/auth/signup", body: `{"username":"carol","email":"c@e.com","password":"short"}`, want: http.StatusBadRequest},
		{name: "signup malformed json", path: "/auth/signup", body: `{`, want: http.StatusBadRequest},
		{name: "login invalid credentials", path: "/auth/login", body: `{"username":"bob","password":"wrong"}`, want: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("%s %s status = %d, want %d (body: %s)", "POST", tt.path, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// ─── Refresh endpoint tests ───────────────────────────────────────────────────

// TestRefreshValidTokenReturnsNewToken verifies that a valid Bearer token
// results in 200 and a parseable token in the response body.
func TestRefreshValidTokenReturnsNewToken(t *testing.T) {
	svc := newTestAuthService(t)
	// Mint a token to exchange.
	original, err := svc.MintAccountToken("acc-1", "alice")
	if err != nil {
		t.Fatalf("MintAccountToken: %v", err)
	}

	srv := New(&fakeStore{}, svc, 0)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+original)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected a non-empty token in response")
	}
	// The new token must itself be parseable.
	claims, err := svc.ParseAccountToken(resp.Token)
	if err != nil {
		t.Fatalf("ParseAccountToken on refreshed token: %v", err)
	}
	if claims.AccountID != "acc-1" || claims.Username != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

// TestRefreshGarbageTokenReturns401 verifies that a malformed or tampered
// token is rejected with 401.
func TestRefreshGarbageTokenReturns401(t *testing.T) {
	svc := newTestAuthService(t)
	srv := New(&fakeStore{}, svc, 0)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer garbage.token.here")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for garbage token, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestRefreshMissingTokenReturns401 verifies that a request with no token
// (no Authorization header, no body) is rejected with 401.
func TestRefreshMissingTokenReturns401(t *testing.T) {
	svc := newTestAuthService(t)
	srv := New(&fakeStore{}, svc, 0)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAuthRoutesNotRegisteredWithoutAccountService ensures the public /auth
// endpoints exist only when the auth dependency implements AccountService.
func TestAuthRoutesNotRegisteredWithoutAccountService(t *testing.T) {
	srv := New(&fakeStore{}, fakeAuth{}, 0)
	handler := srv.Handler()

	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":"a","password":"b"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	// The /auth/login POST endpoint is NOT registered without an account service.
	// Since the unified dashboard now owns the root namespace ("GET /"), the path is
	// recognised for GET only, so an unhandled POST resolves to 405 Method Not Allowed
	// rather than 404 — either way, no login is processed.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 when AccountService absent, got %d", rec.Code)
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("auth login must not succeed without an account service")
	}
}
