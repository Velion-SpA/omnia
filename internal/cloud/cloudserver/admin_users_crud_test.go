package cloudserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── POST /admin/users (create) ──────────────────────────────────────────────

// TestAdminCreateUserHappyPath verifies the operator can create a new account
// and receives the generated password EXACTLY ONCE in the response.
func TestAdminCreateUserHappyPath(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"newguy","email":"newguy@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", rec.Code, rec.Body.String())
	}

	var resp adminCreateUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, rec.Body.String())
	}
	if resp.Username != "newguy" || resp.ID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.GeneratedPassword) != generatedPasswordLength {
		t.Fatalf("expected a %d-char generated password, got %q", generatedPasswordLength, resp.GeneratedPassword)
	}

	// The store only ever received the HASH — never the plaintext — and the
	// hash verifies against the returned plaintext.
	hash := store.passwordHashes[resp.ID]
	if hash == "" || hash == resp.GeneratedPassword {
		t.Fatalf("expected a bcrypt hash distinct from the plaintext, got %q", hash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(resp.GeneratedPassword)); err != nil {
		t.Fatalf("stored hash does not match the returned password: %v", err)
	}

	found := false
	for _, u := range store.users {
		if u.ID == resp.ID && u.Username == "newguy" && u.Email == "newguy@example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected newguy to be persisted, got %+v", store.users)
	}
}

// TestAdminCreateUserDuplicateUsername verifies a conflicting username fails
// with a clean 409, not a 500, and does not create a second row.
func TestAdminCreateUserDuplicateUsername(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@example.com"}}

	body := `{"username":"alice","email":"someone-else@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 {
		t.Fatalf("expected no new row on conflict, got %+v", store.users)
	}
}

// TestAdminCreateUserDuplicateEmail verifies a conflicting email (different
// username) also fails with 409.
func TestAdminCreateUserDuplicateEmail(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "shared@example.com"}}

	body := `{"username":"bob","email":"shared@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 {
		t.Fatalf("expected no new row on conflict, got %+v", store.users)
	}
}

// TestAdminCreateUserRejectsBlankUsername verifies a whitespace-only username
// is rejected with 400 and never reaches the store.
func TestAdminCreateUserRejectsBlankUsername(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"   ","email":"valid@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("expected no user created for a blank username, got %+v", store.users)
	}
}

// TestAdminCreateUserRejectsInvalidEmail verifies a malformed email is
// rejected with 400 and never reaches the store.
func TestAdminCreateUserRejectsInvalidEmail(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"newguy","email":"not-an-email"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("expected no user created for an invalid email, got %+v", store.users)
	}
}

// TestAdminCreateUserRejectsBlankEmail verifies an empty email is rejected
// with 400 (a blank email would pass the DB's NOT NULL constraint but
// collide on the UNIQUE constraint for a second blank row).
func TestAdminCreateUserRejectsBlankEmail(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"newguy","email":""}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("expected no user created for a blank email, got %+v", store.users)
	}
}

// TestAdminUpdateUserRejectsInvalidEmail verifies the same email validation
// applies to edit.
func TestAdminUpdateUserRejectsInvalidEmail(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@example.com"}}

	body := `{"username":"alice","email":"not-an-email"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/1", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.users[0].Email != "alice@example.com" {
		t.Fatalf("expected email untouched after a rejected update, got %+v", store.users[0])
	}
}

// TestAdminUpdateUserRejectsBlankUsername verifies a whitespace-only username
// is rejected with 400 on edit too.
func TestAdminUpdateUserRejectsBlankUsername(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@example.com"}}

	body := `{"username":"   ","email":"alice@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/1", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.users[0].Username != "alice" {
		t.Fatalf("expected username untouched after a rejected update, got %+v", store.users[0])
	}
}

// TestAdminCreateUserRequiresOperator verifies both a non-operator account
// session AND the legacy shared (sync) bearer are forbidden, and neither
// touches the store.
func TestAdminCreateUserRequiresOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	body := `{"username":"newguy","email":"newguy@example.com"}`

	acctRec := httptest.NewRecorder()
	acctReq := cookieRequest(http.MethodPost, "/admin/users", accountCookie(t, authSvc, "1", "alice"), body)
	acctReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(acctRec, acctReq)
	if acctRec.Code != http.StatusForbidden {
		t.Fatalf("account session: expected 403, got %d", acctRec.Code)
	}

	syncRec := httptest.NewRecorder()
	syncReq := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(body))
	syncReq.Header.Set("Content-Type", "application/json")
	syncReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusForbidden {
		t.Fatalf("legacy shared (sync) token: expected 403, got %d", syncRec.Code)
	}

	if len(store.users) != 0 {
		t.Fatalf("expected no user created by a forbidden request, got %+v", store.users)
	}
}

// TestAdminCreateUserEmitsAuditWithoutPassword verifies OBL-05 parity: a
// successful create emits an audit row recording the CREATE action, and the
// generated plaintext password appears NOWHERE in the audit metadata.
func TestAdminCreateUserEmitsAuditWithoutPassword(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"newguy","email":"newguy@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp adminCreateUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(store.auditEntries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d: %+v", len(store.auditEntries), store.auditEntries)
	}
	entry := store.auditEntries[0]
	if entry.Action != cloudstore.AuditActionUserCreate || entry.Outcome != cloudstore.AuditOutcomeUserCreated {
		t.Fatalf("unexpected create audit: %+v", entry)
	}
	for k, v := range entry.Metadata {
		if s, ok := v.(string); ok && s == resp.GeneratedPassword {
			t.Fatalf("audit metadata key %q leaked the generated password: %+v", k, entry)
		}
	}
	// NOTE: deliberately NOT asserting via strings.Contains(rec.Body.String(),
	// resp.GeneratedPassword) — encoding/json's default HTML-escaping turns a
	// literal '&' in the password into the byte sequence & in the RAW
	// response body, so a plain substring check on undecoded bytes is flaky
	// whenever the random password happens to contain '&' (~1-in-4 chance
	// given the charset). Asserting on the properly JSON-decoded resp value
	// (as done above and in TestAdminCreateUserHappyPath) is the correct,
	// escaping-agnostic way to verify the one-time password round-trips.
	if resp.GeneratedPassword == "" {
		t.Fatalf("expected the one-time response to carry a non-empty generated password")
	}
}

// ─── PUT /admin/users/{id} (edit) ────────────────────────────────────────────

// TestAdminUpdateUserHappyPath verifies the operator can edit username/email.
func TestAdminUpdateUserHappyPath(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "oldname", Email: "old@example.com"}}

	body := `{"username":"newname","email":"new@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/1", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.users[0].Username != "newname" || store.users[0].Email != "new@example.com" {
		t.Fatalf("expected user updated, got %+v", store.users[0])
	}
}

// TestAdminUpdateUserConflict verifies renaming to another account's username
// fails with 409 and leaves both rows untouched.
func TestAdminUpdateUserConflict(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{
		{ID: "1", Username: "alice", Email: "alice@example.com"},
		{ID: "2", Username: "bob", Email: "bob@example.com"},
	}

	body := `{"username":"alice","email":"bob@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/2", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.users[1].Username != "bob" {
		t.Fatalf("bob's row must be untouched after a failed rename, got %+v", store.users[1])
	}
}

// TestAdminUpdateUserUnknownID verifies editing an unknown id returns 404.
func TestAdminUpdateUserUnknownID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"ghost","email":"ghost@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/999", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminUpdateUserNonNumericID verifies a non-numeric {id} path segment is
// rejected with a clean 400, not a raw store-error 500 (the real store casts
// id to ::bigint; a non-numeric id must never reach that cast).
func TestAdminUpdateUserNonNumericID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	body := `{"username":"ghost","email":"ghost@example.com"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/users/not-a-number", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminUpdateUserRequiresOperator verifies a non-operator account and the
// legacy shared token are both forbidden.
func TestAdminUpdateUserRequiresOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@example.com"}}
	body := `{"username":"renamed","email":"renamed@example.com"}`

	acctRec := httptest.NewRecorder()
	acctReq := cookieRequest(http.MethodPut, "/admin/users/1", accountCookie(t, authSvc, "1", "alice"), body)
	acctReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(acctRec, acctReq)
	if acctRec.Code != http.StatusForbidden {
		t.Fatalf("account session: expected 403, got %d", acctRec.Code)
	}

	syncRec := httptest.NewRecorder()
	syncReq := httptest.NewRequest(http.MethodPut, "/admin/users/1", strings.NewReader(body))
	syncReq.Header.Set("Content-Type", "application/json")
	syncReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusForbidden {
		t.Fatalf("legacy shared (sync) token: expected 403, got %d", syncRec.Code)
	}

	if store.users[0].Username != "alice" {
		t.Fatalf("expected update rejected, but username changed: %+v", store.users[0])
	}
}

// ─── POST /admin/users/{id}/password (reset) ─────────────────────────────────

// TestAdminResetPasswordAdminProvided verifies an operator-supplied password
// is hashed and stored, and is NOT echoed back in the response.
func TestAdminResetPasswordAdminProvided(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	body := `{"password":"aVeryStrongPassw0rd!"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/password", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "aVeryStrongPassw0rd!") {
		t.Fatalf("admin-provided password must not be echoed back, body=%q", rec.Body.String())
	}
	if err := bcrypt.CompareHashAndPassword([]byte(store.passwordHashes["1"]), []byte("aVeryStrongPassw0rd!")); err != nil {
		t.Fatalf("stored hash does not match the provided password: %v", err)
	}
}

// TestAdminResetPasswordGenerated verifies an empty body triggers a generated
// password, returned exactly once.
func TestAdminResetPasswordGenerated(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/password", operatorCookie(t, authSvc), `{}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp adminResetPasswordResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, rec.Body.String())
	}
	if len(resp.GeneratedPassword) != generatedPasswordLength {
		t.Fatalf("expected a %d-char generated password, got %q", generatedPasswordLength, resp.GeneratedPassword)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(store.passwordHashes["1"]), []byte(resp.GeneratedPassword)); err != nil {
		t.Fatalf("stored hash does not match the generated password: %v", err)
	}
}

// TestAdminResetPasswordTooShort verifies an admin-provided password below
// the minimum length is rejected with 400 and never reaches the store.
func TestAdminResetPasswordTooShort(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	store.passwordHashes["1"] = "original-hash"

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/password", operatorCookie(t, authSvc), `{"password":"short"}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.passwordHashes["1"] != "original-hash" {
		t.Fatalf("expected hash untouched on rejected reset, got %q", store.passwordHashes["1"])
	}
}

// TestAdminResetPasswordUnknownID verifies resetting an unknown id returns 404.
func TestAdminResetPasswordUnknownID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/999/password", operatorCookie(t, authSvc), `{}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminResetPasswordNonNumericID verifies a non-numeric {id} is rejected
// with 400, not a raw store-error 500.
func TestAdminResetPasswordNonNumericID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/not-a-number/password", operatorCookie(t, authSvc), `{}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminResetPasswordRequiresOperator verifies non-operator and the legacy
// shared token are both forbidden.
func TestAdminResetPasswordRequiresOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	acctRec := httptest.NewRecorder()
	acctReq := cookieRequest(http.MethodPost, "/admin/users/1/password", accountCookie(t, authSvc, "1", "alice"), `{}`)
	acctReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(acctRec, acctReq)
	if acctRec.Code != http.StatusForbidden {
		t.Fatalf("account session: expected 403, got %d", acctRec.Code)
	}

	syncRec := httptest.NewRecorder()
	syncReq := httptest.NewRequest(http.MethodPost, "/admin/users/1/password", strings.NewReader(`{}`))
	syncReq.Header.Set("Content-Type", "application/json")
	syncReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusForbidden {
		t.Fatalf("legacy shared (sync) token: expected 403, got %d", syncRec.Code)
	}
}

// TestAdminResetPasswordEmitsAuditWithoutPassword verifies the reset audit row
// never contains the plaintext, whether admin-provided or generated.
func TestAdminResetPasswordEmitsAuditWithoutPassword(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/password", operatorCookie(t, authSvc), `{"password":"aVeryStrongPassw0rd!"}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	if len(store.auditEntries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d: %+v", len(store.auditEntries), store.auditEntries)
	}
	entry := store.auditEntries[0]
	if entry.Action != cloudstore.AuditActionUserPasswordReset || entry.Outcome != cloudstore.AuditOutcomeUserPasswordReset {
		t.Fatalf("unexpected reset audit: %+v", entry)
	}
	for k, v := range entry.Metadata {
		if s, ok := v.(string); ok && s == "aVeryStrongPassw0rd!" {
			t.Fatalf("audit metadata key %q leaked the password: %+v", k, entry)
		}
	}
}

// ─── POST /admin/users/{id}/delete (hard delete) ─────────────────────────────

// TestAdminHardDeleteUserHappyPath verifies a confirmed delete removes the
// user and emits an audit row.
func TestAdminHardDeleteUserHappyPath(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "doomed"}}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/delete?confirm=1", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("expected user removed, got %+v", store.users)
	}
	if len(store.auditEntries) != 1 || store.auditEntries[0].Action != cloudstore.AuditActionUserHardDelete {
		t.Fatalf("expected a hard-delete audit entry, got %+v", store.auditEntries)
	}
}

// TestAdminHardDeleteUserJSONBodyConfirm verifies the JSON body confirm:true
// signal works as an alternative to the query parameter.
func TestAdminHardDeleteUserJSONBodyConfirm(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "doomed"}}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/delete", operatorCookie(t, authSvc), `{"confirm":true}`)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("expected user removed, got %+v", store.users)
	}
}

// TestAdminHardDeleteUserRequiresConfirm verifies an unconfirmed request is
// rejected with 400 and the user survives.
func TestAdminHardDeleteUserRequiresConfirm(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "safe"}}

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/delete", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 {
		t.Fatalf("expected user to survive an unconfirmed delete, got %+v", store.users)
	}
}

// TestAdminHardDeleteUserRequiresOperator verifies non-operator and the
// legacy shared token are both forbidden, and the target survives.
func TestAdminHardDeleteUserRequiresOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "safe"}}

	acctRec := httptest.NewRecorder()
	acctReq := cookieRequest(http.MethodPost, "/admin/users/1/delete?confirm=1", accountCookie(t, authSvc, "1", "safe"), "")
	srv.Handler().ServeHTTP(acctRec, acctReq)
	if acctRec.Code != http.StatusForbidden {
		t.Fatalf("account session: expected 403, got %d", acctRec.Code)
	}

	syncRec := httptest.NewRecorder()
	syncReq := httptest.NewRequest(http.MethodPost, "/admin/users/1/delete?confirm=1", nil)
	syncReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusForbidden {
		t.Fatalf("legacy shared (sync) token: expected 403, got %d", syncRec.Code)
	}

	if len(store.users) != 1 {
		t.Fatalf("expected the target to survive forbidden delete attempts, got %+v", store.users)
	}
}

// TestAdminHardDeleteUserUnknownID verifies deleting an unknown (but
// confirmed) id returns 404.
func TestAdminHardDeleteUserUnknownID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/999/delete?confirm=1", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminHardDeleteUserNonNumericID verifies a non-numeric {id} is rejected
// with 400, not a raw store-error 500, even when confirmed.
func TestAdminHardDeleteUserNonNumericID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/not-a-number/delete?confirm=1", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminHardDeleteUserLastAdminRefused verifies the last-admin guard
// refuses to hard-delete the only remaining admin (409), mirroring the
// existing demote guard.
func TestAdminHardDeleteUserLastAdminRefused(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "labadmin", IsAdmin: true}}
	store.admins["1"] = true // the ONLY admin

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/delete?confirm=1", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 {
		t.Fatalf("last admin must survive a refused hard delete, got %+v", store.users)
	}

	// With a second admin present, hard-deleting one is allowed again.
	store.admins["2"] = true
	rec2 := httptest.NewRecorder()
	req2 := cookieRequest(http.MethodPost, "/admin/users/1/delete?confirm=1", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with two admins present, got %d body=%q", rec2.Code, rec2.Body.String())
	}
}

// ─── Deactivate (disable) last-admin guard retrofit ──────────────────────────

// TestDeactivateLastAdminRefused verifies the last-admin guard is also
// enforced on the existing soft-deactivate (disable) endpoint, which
// previously had NO such guard (unlike demote, which already had it).
func TestDeactivateLastAdminRefused(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true // the ONLY admin

	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPost, "/admin/users/1/disable", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.disabled["1"] {
		t.Fatalf("last admin must remain active after a refused deactivate")
	}

	// With a second admin present, deactivating one is allowed again.
	store.admins["2"] = true
	rec2 := httptest.NewRecorder()
	req2 := cookieRequest(http.MethodPost, "/admin/users/1/disable", operatorCookie(t, authSvc), "")
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with two admins present, got %d body=%q", rec2.Code, rec2.Body.String())
	}
	if !store.disabled["1"] {
		t.Fatalf("user 1 should be disabled when another admin remains")
	}
}

// TestDeactivateStoreWithoutAdminLookupIsUnguarded is the regression guard
// proving the last-admin check on deactivate DEGRADES gracefully — rather
// than blocking — when the concrete store doesn't support the admin-flag
// lookup at all (fakeAdminStore, the pre-existing managed-token-only fake
// used by TestRevokeAndDisableEndpointsRequireAdmin), preserving prior
// behavior exactly.
func TestDeactivateStoreWithoutAdminLookupIsUnguarded(t *testing.T) {
	store := &fakeAdminStore{}
	srv := newAdminTestServer(&fakeAdminAuth{issuedRaw: "omct_x"}, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/users/7/disable", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (no guard capability on this store), got %d body=%q", rec.Code, rec.Body.String())
	}
	if !store.disabled["7"] {
		t.Fatalf("expected user 7 disabled, got %v", store.disabled)
	}
}
