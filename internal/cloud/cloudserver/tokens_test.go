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
	revoked      []string
	disabled     map[string]bool
	auditEntries []cloudstore.AuditEntry // OBL-05: captures InsertAuditEntry calls
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

// InsertAuditEntry records the call for OBL-05 audit-emission assertions
// (token revoke, user disable/enable).
func (s *fakeAdminStore) InsertAuditEntry(_ context.Context, entry cloudstore.AuditEntry) error {
	s.auditEntries = append(s.auditEntries, entry)
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

// TestRevokeDisableEnableEmitAudit verifies OBL-05: revoke, disable, and
// enable each emit a best-effort audit row with the operator actor and the
// affected token/user id in metadata.
func TestRevokeDisableEnableEmitAudit(t *testing.T) {
	store := &fakeAdminStore{}
	srv := newAdminTestServer(&fakeAdminAuth{issuedRaw: "omct_x"}, store)

	post := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer admin-token")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d body=%q", path, rec.Code, rec.Body.String())
		}
	}

	post("/admin/tokens/99/revoke")
	post("/admin/users/7/disable")
	post("/admin/users/7/enable")

	if len(store.auditEntries) != 3 {
		t.Fatalf("expected 3 audit entries, got %d: %+v", len(store.auditEntries), store.auditEntries)
	}

	revoke := store.auditEntries[0]
	if revoke.Action != cloudstore.AuditActionTokenRevoke || revoke.Outcome != cloudstore.AuditOutcomeTokenRevoked {
		t.Fatalf("unexpected revoke audit: %+v", revoke)
	}
	if revoke.Project != cloudstore.AuditProjectSentinel || revoke.Metadata["token_id"] != "99" {
		t.Fatalf("unexpected revoke audit project/metadata: %+v", revoke)
	}

	disable := store.auditEntries[1]
	if disable.Action != cloudstore.AuditActionUserDisable || disable.Outcome != cloudstore.AuditOutcomeUserDisabled {
		t.Fatalf("unexpected disable audit: %+v", disable)
	}
	if disable.Metadata["user_id"] != "7" {
		t.Fatalf("unexpected disable audit metadata: %+v", disable)
	}

	enable := store.auditEntries[2]
	if enable.Action != cloudstore.AuditActionUserEnable || enable.Outcome != cloudstore.AuditOutcomeUserEnabled {
		t.Fatalf("unexpected enable audit: %+v", enable)
	}
	if enable.Metadata["user_id"] != "7" {
		t.Fatalf("unexpected enable audit metadata: %+v", enable)
	}

	// Contributor is the operator actor — the admin Bearer path resolves to the
	// generic "operator" label (no per-request account identity on that path).
	for _, e := range store.auditEntries {
		if e.Contributor != "operator" {
			t.Fatalf("expected contributor=operator, got %+v", e)
		}
	}
}
