package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeAdminDashboardStore backs the OBL-13 admin dashboard endpoints. It embeds
// the RBAC fake (ChunkStore + membershipStore + membershipManager) and adds the
// adminDashboardStore + managedTokenAdminStore seams so the admin routes register.
type fakeAdminDashboardStore struct {
	*fakeMembershipStore
	users    []cloudstore.AdminUser
	tokens   map[string][]cloudstore.ManagedTokenView
	revoked  []string
	disabled map[string]bool
}

func newFakeAdminDashboardStore() *fakeAdminDashboardStore {
	return &fakeAdminDashboardStore{
		fakeMembershipStore: newFakeMembershipStore(),
		tokens:              map[string][]cloudstore.ManagedTokenView{},
		disabled:            map[string]bool{},
	}
}

func (s *fakeAdminDashboardStore) ListUsers(context.Context) ([]cloudstore.AdminUser, error) {
	return s.users, nil
}

func (s *fakeAdminDashboardStore) ListMembershipsForUser(_ context.Context, accountID string) ([]cloudstore.Membership, error) {
	var out []cloudstore.Membership
	for _, m := range s.memberships {
		if m.AccountID == accountID {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (s *fakeAdminDashboardStore) UpsertMembership(_ context.Context, accountID, project string, perms int, role string) error {
	s.grant(accountID, project, perms, role)
	return nil
}

func (s *fakeAdminDashboardStore) DeleteMembership(_ context.Context, accountID, project string) error {
	delete(s.memberships, membershipKey(accountID, project))
	return nil
}

func (s *fakeAdminDashboardStore) ListManagedTokensForUser(_ context.Context, userID string) ([]cloudstore.ManagedTokenView, error) {
	return s.tokens[userID], nil
}

func (s *fakeAdminDashboardStore) RevokeManagedToken(_ context.Context, id string) error {
	s.revoked = append(s.revoked, id)
	return nil
}

func (s *fakeAdminDashboardStore) SetUserDisabled(_ context.Context, userID string, disabled bool) error {
	s.disabled[userID] = disabled
	return nil
}

func newAdminDashboardTestServer(t *testing.T) (*CloudServer, *fakeAdminDashboardStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeAdminDashboardStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	if srv.accountProjectAuth == nil {
		t.Fatal("setup: membership store must activate multi-tenant RBAC")
	}
	return srv, store, authSvc
}

func operatorCookie(t *testing.T, authSvc *cloudauth.Service) *http.Cookie {
	t.Helper()
	sess, err := authSvc.MintDashboardSession("admin-token")
	if err != nil {
		t.Fatalf("mint operator session: %v", err)
	}
	return &http.Cookie{Name: dashboardSessionCookieName, Value: sess}
}

func accountCookie(t *testing.T, authSvc *cloudauth.Service, accountID, username string) *http.Cookie {
	t.Helper()
	tok, err := authSvc.MintAccountToken(accountID, username)
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	sess, err := authSvc.MintDashboardSession(tok)
	if err != nil {
		t.Fatalf("mint account session: %v", err)
	}
	return &http.Cookie{Name: dashboardSessionCookieName, Value: sess}
}

func cookieRequest(method, url string, cookie *http.Cookie, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

// TestAdminUsersPageOperatorOnly verifies the Users page renders for the operator
// and is forbidden for a non-operator account session.
func TestAdminUsersPageOperatorOnly(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "a@x.io"}}

	// Operator → 200, page shows the user.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("expected Users page to list alice, body=%q", rec.Body.String())
	}

	// Non-operator account → 403.
	recAcct := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recAcct, cookieRequest(http.MethodGet, "/admin", accountCookie(t, authSvc, "1", "alice"), ""))
	if recAcct.Code != http.StatusForbidden {
		t.Fatalf("account GET /admin: expected 403, got %d", recAcct.Code)
	}
}

// TestAdminAccessPageRendersForOperator exercises the Access page templ end to end
// (user selector, membership rows with perm checkboxes + role select, add form).
func TestAdminAccessPageRendersForOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	store.grant("1", "lab", int(cloudauth.PermRead|cloudauth.PermInsert), "member")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/access?user=1", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/access: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "lab") || !strings.Contains(body, "ADD / UPDATE MEMBERSHIP") {
		t.Fatalf("Access page missing membership row or add form, body=%q", body)
	}
	// The revoke control must target the pre-encoded delete path for this membership.
	if !strings.Contains(body, `hx-delete="/admin/memberships/1/lab"`) {
		t.Fatalf("Access page missing revoke control for lab, body=%q", body)
	}
}

// TestAdminNavGatedToOperator verifies the Admin nav entry appears on the dashboard
// for the operator and is absent for a non-operator account (task 3 + acceptance).
func TestAdminNavGatedToOperator(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	opRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(opRec, cookieRequest(http.MethodGet, "/", operatorCookie(t, authSvc), ""))
	if opRec.Code != http.StatusOK {
		t.Fatalf("operator GET /: expected 200, got %d body=%q", opRec.Code, opRec.Body.String())
	}
	if !strings.Contains(opRec.Body.String(), `data-nav="/admin"`) {
		t.Fatalf("operator dashboard must show the Admin nav entry, body=%q", opRec.Body.String())
	}

	acctRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(acctRec, cookieRequest(http.MethodGet, "/", accountCookie(t, authSvc, "1", "alice"), ""))
	if acctRec.Code != http.StatusOK {
		t.Fatalf("account GET /: expected 200, got %d body=%q", acctRec.Code, acctRec.Body.String())
	}
	if strings.Contains(acctRec.Body.String(), `data-nav="/admin"`) {
		t.Fatalf("non-operator dashboard must NOT show the Admin nav entry, body=%q", acctRec.Body.String())
	}
}

// TestAdminListUsersGate verifies GET /admin/users honors the operator session
// (cookie) and the admin Bearer (API), and forbids everyone else.
func TestAdminListUsersGate(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "7", Username: "bob", Email: "b@x.io", TokenCount: 2}}

	// Operator cookie → 200 with JSON list.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/users", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/users: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var users []adminUserJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode users: %v body=%q", err, rec.Body.String())
	}
	if len(users) != 1 || users[0].Username != "bob" || users[0].TokenCount != 2 {
		t.Fatalf("unexpected users payload: %+v", users)
	}

	// Admin Bearer (API path) → 200.
	apiRec := httptest.NewRecorder()
	apiReq := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	apiReq.Header.Set("Authorization", "Bearer admin-token")
	srv.Handler().ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusOK {
		t.Fatalf("admin bearer GET /admin/users: expected 200, got %d", apiRec.Code)
	}

	// Sync bearer → 403 (not the operator).
	syncRec := httptest.NewRecorder()
	syncReq := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	syncReq.Header.Set("Authorization", "Bearer sync-token")
	srv.Handler().ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusForbidden {
		t.Fatalf("sync bearer GET /admin/users: expected 403, got %d", syncRec.Code)
	}

	// Account cookie → 403.
	acctRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(acctRec, cookieRequest(http.MethodGet, "/admin/users", accountCookie(t, authSvc, "7", "bob"), ""))
	if acctRec.Code != http.StatusForbidden {
		t.Fatalf("account GET /admin/users: expected 403, got %d", acctRec.Code)
	}
}

// TestAdminUpsertMembershipRoundTripsPerms verifies the operator can set a user's
// permission on a project and that the perms bitfield round-trips (read-only →
// read+write), with out-of-range bits masked.
func TestAdminUpsertMembershipRoundTripsPerms(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	// read-only (perms=1) via JSON.
	body := `{"account_id":"1","project":"lab","perms":1,"role":"member"}`
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/memberships", operatorCookie(t, authSvc), body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert read-only: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m, _ := store.GetMembership("1", "lab"); m == nil || m.Perms != int(cloudauth.PermRead) {
		t.Fatalf("expected perms=read-only(1), got %+v", m)
	}

	// upgrade to read+write (perms=3).
	rec2 := httptest.NewRecorder()
	req2 := cookieRequest(http.MethodPut, "/admin/memberships", operatorCookie(t, authSvc), `{"account_id":"1","project":"lab","perms":3,"role":"member"}`)
	req2.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("upsert read+write: expected 200, got %d", rec2.Code)
	}
	if m, _ := store.GetMembership("1", "lab"); m == nil || m.Perms != int(cloudauth.PermRead|cloudauth.PermInsert) {
		t.Fatalf("expected perms=read+insert(3), got %+v", m)
	}

	// out-of-range bits masked to PermAll.
	rec3 := httptest.NewRecorder()
	req3 := cookieRequest(http.MethodPut, "/admin/memberships", operatorCookie(t, authSvc), `{"account_id":"1","project":"lab","perms":255,"role":"member"}`)
	req3.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec3, req3)
	if m, _ := store.GetMembership("1", "lab"); m == nil || m.Perms != int(cloudauth.PermAll) {
		t.Fatalf("expected perms masked to PermAll(15), got %+v", m)
	}
}

// TestAdminUpsertMembershipHTMXForm verifies the HTMX form path (checkbox perms)
// composes the bitfield and triggers a client refresh.
func TestAdminUpsertMembershipHTMXForm(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)

	form := "account_id=1&project=lab&read=on&insert=on&role=moderator"
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/memberships", operatorCookie(t, authSvc), form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx upsert: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh header on htmx mutation, got %q", rec.Header().Get("HX-Refresh"))
	}
	m, _ := store.GetMembership("1", "lab")
	if m == nil || m.Perms != int(cloudauth.PermRead|cloudauth.PermInsert) || m.Role != "moderator" {
		t.Fatalf("expected read+insert / moderator from form, got %+v", m)
	}
}

// TestAdminDeleteMembership verifies the operator can revoke a membership and a
// non-operator cannot.
func TestAdminDeleteMembership(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.grant("1", "lab", int(cloudauth.PermAll), "owner")

	// Non-operator delete → 403, membership untouched.
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, cookieRequest(http.MethodDelete, "/admin/memberships/1/lab", accountCookie(t, authSvc, "1", "alice"), ""))
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("account DELETE membership: expected 403, got %d", badRec.Code)
	}
	if m, _ := store.GetMembership("1", "lab"); m == nil {
		t.Fatalf("membership must survive a forbidden delete")
	}

	// Operator delete → 200, membership gone.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodDelete, "/admin/memberships/1/lab", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator DELETE membership: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m, _ := store.GetMembership("1", "lab"); m != nil {
		t.Fatalf("expected membership deleted, still present: %+v", m)
	}
}

// TestAdminListUserMemberships verifies the per-user memberships endpoint returns
// project/perms/role for the operator.
func TestAdminListUserMemberships(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.grant("1", "lab", int(cloudauth.PermRead), "member")
	store.grant("1", "prod", int(cloudauth.PermAll), "owner")
	store.grant("2", "other", int(cloudauth.PermRead), "member")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/users/1/memberships", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET memberships: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var mems []adminMembershipJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &mems); err != nil {
		t.Fatalf("decode memberships: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("expected 2 memberships for user 1, got %d: %+v", len(mems), mems)
	}
	seen := map[string]int{}
	for _, m := range mems {
		seen[m.Project] = m.Perms
	}
	if seen["lab"] != int(cloudauth.PermRead) || seen["prod"] != int(cloudauth.PermAll) {
		t.Fatalf("unexpected membership perms: %+v", seen)
	}
}

// TestAdminEndpointsAllForbiddenForNonOperator is the acceptance guard: EVERY
// /admin/* call from a non-operator account returns 403.
func TestAdminEndpointsAllForbiddenForNonOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	store.grant("1", "lab", int(cloudauth.PermRead), "member")

	cases := []struct {
		method, url, body, ctype string
	}{
		{http.MethodGet, "/admin", "", ""},
		{http.MethodGet, "/admin/access", "", ""},
		{http.MethodGet, "/admin/users", "", ""},
		{http.MethodGet, "/admin/users/1/memberships", "", ""},
		{http.MethodPut, "/admin/memberships", `{"account_id":"1","project":"lab","perms":1,"role":"member"}`, "application/json"},
		{http.MethodDelete, "/admin/memberships/1/lab", "", ""},
		{http.MethodPost, "/admin/tokens", `{"user_id":"1"}`, "application/json"},
		{http.MethodPost, "/admin/users/1/disable", "", ""},
		{http.MethodPost, "/admin/users/1/enable", "", ""},
		{http.MethodPost, "/admin/tokens/9/revoke", "", ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := cookieRequest(c.method, c.url, accountCookie(t, authSvc, "1", "alice"), c.body)
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-operator %s %s: expected 403, got %d body=%q", c.method, c.url, rec.Code, rec.Body.String())
		}
	}
	// The forbidden delete must not have touched the membership.
	if m, _ := store.GetMembership("1", "lab"); m == nil {
		t.Fatalf("membership must survive forbidden admin calls")
	}
}
