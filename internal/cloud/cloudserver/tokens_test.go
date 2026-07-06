package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeAdminAuth satisfies Authenticator + AccountService + managedTokenIssuer so
// the managed-token admin endpoints register and can be exercised without a DB.
type fakeAdminAuth struct {
	issuedRaw  string
	issueErr   error
	lastUserID string
}

func (a *fakeAdminAuth) Authorize(r *http.Request) error {
	_, err := a.AuthorizeAccount(r)
	return err
}
func (a *fakeAdminAuth) AuthorizeAccount(*http.Request) (*cloudauth.AccountClaims, error) {
	return nil, nil
}
func (a *fakeAdminAuth) Signup(string, string, string) (*cloudstore.User, error) { return nil, nil }
func (a *fakeAdminAuth) Login(string, string) (string, *cloudstore.User, error) {
	return "", nil, nil
}
func (a *fakeAdminAuth) IssueManagedToken(_ context.Context, userID, _ string) (string, string, error) {
	if a.issueErr != nil {
		return "", "", a.issueErr
	}
	a.lastUserID = userID
	return a.issuedRaw, "tok-1", nil
}

// fakeAdminStore satisfies ChunkStore (via embedded fakeStore) + managedTokenAdminStore.
type fakeAdminStore struct {
	fakeStore
	revoked  []string
	disabled map[string]bool
}

func (s *fakeAdminStore) RevokeManagedToken(_ context.Context, id string) error {
	s.revoked = append(s.revoked, id)
	return nil
}
func (s *fakeAdminStore) SetUserDisabled(_ context.Context, userID string, disabled bool) error {
	if s.disabled == nil {
		s.disabled = make(map[string]bool)
	}
	s.disabled[userID] = disabled
	return nil
}

func newAdminTestServer(auth *fakeAdminAuth, store *fakeAdminStore) *CloudServer {
	return New(store, auth, 0, WithDashboardAdminToken("admin-token"))
}

func TestIssueManagedTokenEndpointShowsRawOnceAndGatesOnAdmin(t *testing.T) {
	auth := &fakeAdminAuth{issuedRaw: "omct_rawsecret"}
	srv := newAdminTestServer(auth, &fakeAdminStore{})

	body := []byte(`{"user_id":"42","label":"ci"}`)

	// Wrong (sync) credential must be rejected — the sync bearer is not an operator.
	badRec := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/admin/tokens", bytes.NewReader(body))
	badReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("non-admin credential must be forbidden, got %d body=%q", badRec.Code, badRec.Body.String())
	}

	// Missing credential must be 401.
	noAuthRec := httptest.NewRecorder()
	noAuthReq := httptest.NewRequest(http.MethodPost, "/admin/tokens", bytes.NewReader(body))
	srv.Handler().ServeHTTP(noAuthRec, noAuthReq)
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential must be 401, got %d", noAuthRec.Code)
	}

	// Admin credential issues the token and returns the raw value ONCE.
	okRec := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodPost, "/admin/tokens", bytes.NewReader(body))
	okReq.Header.Set("Authorization", "Bearer admin-token")
	srv.Handler().ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusCreated {
		t.Fatalf("admin issuance must succeed, got %d body=%q", okRec.Code, okRec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(okRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if resp["token"] != "omct_rawsecret" {
		t.Fatalf("expected raw token in response, got %q", resp["token"])
	}
	if auth.lastUserID != "42" {
		t.Fatalf("issuer got user_id %q, want 42", auth.lastUserID)
	}
}

func TestRevokeAndDisableEndpointsRequireAdmin(t *testing.T) {
	store := &fakeAdminStore{}
	srv := newAdminTestServer(&fakeAdminAuth{issuedRaw: "omct_x"}, store)

	// Revoke — admin.
	revRec := httptest.NewRecorder()
	revReq := httptest.NewRequest(http.MethodPost, "/admin/tokens/99/revoke", nil)
	revReq.Header.Set("Authorization", "Bearer admin-token")
	srv.Handler().ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusOK {
		t.Fatalf("admin revoke must succeed, got %d body=%q", revRec.Code, revRec.Body.String())
	}
	if len(store.revoked) != 1 || store.revoked[0] != "99" {
		t.Fatalf("expected token 99 revoked, got %v", store.revoked)
	}

	// Revoke — non-admin rejected, store untouched.
	revBadRec := httptest.NewRecorder()
	revBadReq := httptest.NewRequest(http.MethodPost, "/admin/tokens/100/revoke", nil)
	revBadReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(revBadRec, revBadReq)
	if revBadRec.Code != http.StatusForbidden {
		t.Fatalf("non-admin revoke must be forbidden, got %d", revBadRec.Code)
	}
	if len(store.revoked) != 1 {
		t.Fatalf("non-admin revoke must not touch store, got %v", store.revoked)
	}

	// Disable user — admin.
	disRec := httptest.NewRecorder()
	disReq := httptest.NewRequest(http.MethodPost, "/admin/users/7/disable", nil)
	disReq.Header.Set("Authorization", "Bearer admin-token")
	srv.Handler().ServeHTTP(disRec, disReq)
	if disRec.Code != http.StatusOK {
		t.Fatalf("admin disable must succeed, got %d body=%q", disRec.Code, disRec.Body.String())
	}
	if !store.disabled["7"] {
		t.Fatalf("expected user 7 disabled, got %v", store.disabled)
	}

	// Enable user — admin.
	enRec := httptest.NewRecorder()
	enReq := httptest.NewRequest(http.MethodPost, "/admin/users/7/enable", nil)
	enReq.Header.Set("Authorization", "Bearer admin-token")
	srv.Handler().ServeHTTP(enRec, enReq)
	if enRec.Code != http.StatusOK {
		t.Fatalf("admin enable must succeed, got %d", enRec.Code)
	}
	if store.disabled["7"] {
		t.Fatalf("expected user 7 enabled, got %v", store.disabled)
	}
}
