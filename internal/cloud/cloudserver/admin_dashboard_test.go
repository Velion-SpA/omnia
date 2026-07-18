package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	admins   map[string]bool // OBL-16: account id → is_admin

	// Command Center v2, Slice 1: user CRUD fake state. passwordHashes never
	// stores plaintext — handlers only ever pass a bcrypt hash down to the
	// store, mirroring the real CloudStore.
	passwordHashes map[string]string
	nextUserID     int
}

func newFakeAdminDashboardStore() *fakeAdminDashboardStore {
	return &fakeAdminDashboardStore{
		fakeMembershipStore: newFakeMembershipStore(),
		tokens:              map[string][]cloudstore.ManagedTokenView{},
		disabled:            map[string]bool{},
		admins:              map[string]bool{},
		passwordHashes:      map[string]string{},
	}
}

// findFakeUserIndex returns the index of the user with the given id, or -1.
func (s *fakeAdminDashboardStore) findFakeUserIndex(id string) int {
	for i, u := range s.users {
		if u.ID == id {
			return i
		}
	}
	return -1
}

// AdminCreateUser fakes the store-level uniqueness check (username OR email
// already used by ANOTHER row) that the real CloudStore enforces via Postgres
// UNIQUE constraints.
func (s *fakeAdminDashboardStore) AdminCreateUser(_ context.Context, username, email, passwordHash string) (string, error) {
	for _, u := range s.users {
		if u.Username == username || (email != "" && u.Email == email) {
			return "", cloudstore.ErrUserExists
		}
	}
	s.nextUserID++
	id := strconv.Itoa(s.nextUserID)
	s.users = append(s.users, cloudstore.AdminUser{ID: id, Username: username, Email: email})
	s.passwordHashes[id] = passwordHash
	return id, nil
}

// AdminUpdateUser fakes the same uniqueness check against every OTHER row (a
// self-rename to the current value never conflicts).
func (s *fakeAdminDashboardStore) AdminUpdateUser(_ context.Context, id, username, email string) error {
	idx := s.findFakeUserIndex(id)
	if idx == -1 {
		return cloudstore.ErrManagedTokenUserNotFound
	}
	for i, u := range s.users {
		if i == idx {
			continue
		}
		if u.Username == username || (email != "" && u.Email == email) {
			return cloudstore.ErrUserExists
		}
	}
	s.users[idx].Username = username
	s.users[idx].Email = email
	return nil
}

// AdminSetUserPassword fakes replacing the stored hash.
func (s *fakeAdminDashboardStore) AdminSetUserPassword(_ context.Context, id, passwordHash string) error {
	if s.findFakeUserIndex(id) == -1 {
		return cloudstore.ErrManagedTokenUserNotFound
	}
	s.passwordHashes[id] = passwordHash
	return nil
}

// AdminHardDeleteUser fakes the cascading delete: the user row, its
// memberships, its admin flag, and its disabled flag.
func (s *fakeAdminDashboardStore) AdminHardDeleteUser(_ context.Context, id string) error {
	idx := s.findFakeUserIndex(id)
	if idx == -1 {
		return cloudstore.ErrManagedTokenUserNotFound
	}
	// Last-admin guard, fake-mirroring the real cloudstore.AdminHardDeleteUser's
	// atomic in-transaction check (lockAdminIDsForUpdate).
	if s.admins[id] {
		n := 0
		for _, v := range s.admins {
			if v {
				n++
			}
		}
		if n <= 1 {
			return cloudstore.ErrLastAdmin
		}
	}
	s.users = append(s.users[:idx], s.users[idx+1:]...)
	delete(s.passwordHashes, id)
	delete(s.admins, id)
	delete(s.disabled, id)
	for k, m := range s.memberships {
		if m.AccountID == id {
			delete(s.memberships, k)
		}
	}
	return nil
}

// IsUserAdmin backs both the operator-promotion lookup (OBL-16) and the last-admin
// guard on demote.
func (s *fakeAdminDashboardStore) IsUserAdmin(_ context.Context, accountID string) (bool, error) {
	return s.admins[accountID], nil
}

func (s *fakeAdminDashboardStore) SetUserAdmin(_ context.Context, accountID string, admin bool) error {
	if s.admins == nil {
		s.admins = map[string]bool{}
	}
	s.admins[accountID] = admin
	return nil
}

func (s *fakeAdminDashboardStore) CountAdmins(context.Context) (int, error) {
	n := 0
	for _, v := range s.admins {
		if v {
			n++
		}
	}
	return n, nil
}

// DemoteUserAdminGuarded fakes the atomic guard: refuses with
// cloudstore.ErrLastAdmin when accountID is the only remaining admin. The
// fake has no concurrency to protect against (Go test goroutines aside, each
// test call is sequential), so this simply replicates the SAME observable
// check-then-act semantics the real cloudstore.DemoteUserAdminGuarded
// guarantees atomically against a live Postgres.
func (s *fakeAdminDashboardStore) DemoteUserAdminGuarded(ctx context.Context, accountID string) error {
	if s.admins[accountID] {
		n := 0
		for _, v := range s.admins {
			if v {
				n++
			}
		}
		if n <= 1 {
			return cloudstore.ErrLastAdmin
		}
	}
	return s.SetUserAdmin(ctx, accountID, false)
}

// DeactivateUserGuarded fakes the atomic deactivate guard, mirroring
// DemoteUserAdminGuarded above.
func (s *fakeAdminDashboardStore) DeactivateUserGuarded(ctx context.Context, userID string) error {
	if s.admins[userID] {
		n := 0
		for _, v := range s.admins {
			if v {
				n++
			}
		}
		if n <= 1 {
			return cloudstore.ErrLastAdmin
		}
	}
	return s.SetUserDisabled(ctx, userID, true)
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

// TestAdminUsersPageRendersToolbarAndMenu exercises the Command Center v2,
// Slice 2 Users page markup: the search toolbar + count, the collapsed
// per-row kebab menu (referencing every CRUD route from Slice 1), and the
// three modals (create / edit / reset password).
func TestAdminUsersPageRendersToolbarAndMenu(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@x.io", IsAdmin: true}}
	store.admins["1"] = true

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Toolbar: search input + count + the "New user" trigger.
	for _, want := range []string{
		`id="admin-user-search"`,
		`1 account · 1 admin`,
		`data-modal="admin-create-user-modal"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Users page missing toolbar element %q, body=%q", want, body)
		}
	}

	// Row: one primary Access link + the kebab menu referencing every
	// Slice-1 route plus the pre-existing promote/disable/token actions.
	for _, want := range []string{
		`href="/admin/access?user=1"`,
		`data-action="edit-user"`,
		`data-action="reset-password"`,
		`data-action="toggle-token-form"`,
		`hx-post="/admin/users/1/demote"`, // alice is seeded as admin above
		`hx-post="/admin/users/1/disable"`,
		`hx-confirm="Disable this user?`,
		`hx-get="/admin/users/1/delete-confirm"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Users row menu missing %q, body=%q", want, body)
		}
	}

	// The three modals: create (JSON-composed create+promote), edit (generic
	// data-admin-form JSON PUT), reset password.
	for _, want := range []string{
		`id="admin-create-user-modal"`,
		`value="member" checked`,
		`id="admin-edit-user-modal"`,
		`data-url-template="/admin/users/{id}"`,
		`id="admin-reset-password-modal"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Users page missing modal element %q, body=%q", want, body)
		}
	}

	// The page-level error banner (fixes the previous hx-swap=none silent
	// failure) and the admin.js script tag must both be present.
	if !strings.Contains(body, `id="admin-error-banner"`) {
		t.Fatalf("Users page missing the error banner, body=%q", body)
	}
	if !strings.Contains(body, `/static/admin.js`) {
		t.Fatalf("Users page must load admin.js for the new modal/menu behavior, body=%q", body)
	}
}

// TestAdminUserDeleteConfirmCancelRoundTrip exercises the swap-in-place
// hard-delete confirm step: GET delete-confirm renders the reused
// ui.ConfirmDialog (referencing the real Slice-1 delete route), and GET
// delete-cancel restores the original trigger. Neither handler touches
// anything but ListUsers (a read, for the username lookup) — no mutation.
func TestAdminUserDeleteConfirmCancelRoundTrip(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "7", Username: "bob"}}

	confirmRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(confirmRec, cookieRequest(http.MethodGet, "/admin/users/7/delete-confirm", operatorCookie(t, authSvc), ""))
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("GET delete-confirm: expected 200, got %d body=%q", confirmRec.Code, confirmRec.Body.String())
	}
	confirmBody := confirmRec.Body.String()
	for _, want := range []string{
		"bob",
		`hx-post="/admin/users/7/delete?confirm=1"`,
		`hx-get="/admin/users/7/delete-cancel"`,
	} {
		if !strings.Contains(confirmBody, want) {
			t.Fatalf("delete-confirm fragment missing %q, body=%q", want, confirmBody)
		}
	}

	cancelRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(cancelRec, cookieRequest(http.MethodGet, "/admin/users/7/delete-cancel", operatorCookie(t, authSvc), ""))
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("GET delete-cancel: expected 200, got %d body=%q", cancelRec.Code, cancelRec.Body.String())
	}
	cancelBody := cancelRec.Body.String()
	if !strings.Contains(cancelBody, `/admin/users/7/delete-confirm`) {
		t.Fatalf("delete-cancel must restore the original trigger, body=%q", cancelBody)
	}
	if strings.Contains(cancelBody, "Delete permanently") {
		t.Fatalf("delete-cancel must NOT still show the confirm dialog, body=%q", cancelBody)
	}

	// Non-numeric id is rejected before any render (defense in depth, same
	// convention as every other /admin/users/{id}/* route).
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, cookieRequest(http.MethodGet, "/admin/users/abc/delete-confirm", operatorCookie(t, authSvc), ""))
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("GET delete-confirm non-numeric id: expected 400, got %d", badRec.Code)
	}
}

// TestAdminUserDeleteConfirmIgnoresSpoofedUsername is the SECURITY FIX
// regression guard (Slice 2 review, MUST-FIX 3): the confirm/cancel
// fragments used to render whatever `?username=` said, letting a caller
// spoof the displayed name independently of `id`. They must now ALWAYS show
// the real username looked up from the store, and any `?username=` query
// value — matching or not — must be silently ignored.
func TestAdminUserDeleteConfirmIgnoresSpoofedUsername(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{
		{ID: "7", Username: "bob"},
		{ID: "9", Username: "mallory"},
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/users/7/delete-confirm?username=mallory", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delete-confirm: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "bob") {
		t.Fatalf("delete-confirm must show the REAL username (bob) looked up by id, body=%q", body)
	}
	if strings.Contains(body, "mallory") {
		t.Fatalf("delete-confirm must NOT trust the spoofed ?username= query value, body=%q", body)
	}

	// An id with no matching user (e.g. already deleted) is a clean 404, not
	// a render with an empty/spoofed name.
	missingRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingRec, cookieRequest(http.MethodGet, "/admin/users/404/delete-confirm?username=mallory", operatorCookie(t, authSvc), ""))
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("GET delete-confirm unknown id: expected 404, got %d body=%q", missingRec.Code, missingRec.Body.String())
	}
}

// TestAdminUserDeleteConfirmRequiresOperator guards the two new fragment
// routes with the SAME operator gate as the rest of the Admin section.
func TestAdminUserDeleteConfirmRequiresOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}

	for _, url := range []string{
		"/admin/users/1/delete-confirm",
		"/admin/users/1/delete-cancel",
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, url, accountCookie(t, authSvc, "1", "alice"), ""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-operator GET %s: expected 403, got %d", url, rec.Code)
		}
	}
}

// TestAdminAccessPageRendersForOperator exercises the Command Center v2,
// Slice 3 unified Access page end to end: the searchable account picker, and
// ONE merged row per project showing its effective label + R/W/U/D chips,
// its Override source badge, and the edit-in-place triggers (Edit/Revoke).
// The actual mutation still lands on the pre-existing PUT/DELETE
// /admin/memberships routes — only the Slice 3 view-only row fragments are
// new (asserted separately in TestAdminAccessRowEditRevokeRoundTrip).
func TestAdminAccessPageRendersForOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice", Email: "alice@x.io"}}
	store.grant("1", "lab", int(cloudauth.PermRead|cloudauth.PermInsert), "member")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/access?user=1", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/access: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"lab",
		"badge-warn",
		">Override<",
		"Partial", // read+insert, no update/delete
		`data-acct-select`,
		`hx-get="/admin/access/rows/1/lab/edit"`,
		`hx-get="/admin/access/rows/1/lab/revoke"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Access page missing %q, body=%q", want, body)
		}
	}
}

// TestAdminAccessRowEditRevokeRoundTrip exercises the Slice 3 per-row
// edit-in-place fragments: the default view shows the Override source, the
// edit fragment PUTs to the pre-existing /admin/memberships route with a
// Cancel back to the view fragment, and the revoke-confirm fragment reuses
// ui.ConfirmDialog wired to the pre-existing DELETE route. An unknown project
// for the account is a clean 404, and every route is operator-gated.
func TestAdminAccessRowEditRevokeRoundTrip(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	store.grant("1", "lab", int(cloudauth.PermRead), "member")

	viewRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(viewRec, cookieRequest(http.MethodGet, "/admin/access/rows/1/lab", operatorCookie(t, authSvc), ""))
	if viewRec.Code != http.StatusOK {
		t.Fatalf("row view: expected 200, got %d body=%q", viewRec.Code, viewRec.Body.String())
	}
	if !strings.Contains(viewRec.Body.String(), ">Override<") {
		t.Fatalf("row view must show the Override source, body=%q", viewRec.Body.String())
	}

	editRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(editRec, cookieRequest(http.MethodGet, "/admin/access/rows/1/lab/edit", operatorCookie(t, authSvc), ""))
	if editRec.Code != http.StatusOK {
		t.Fatalf("row edit: expected 200, got %d body=%q", editRec.Code, editRec.Body.String())
	}
	editBody := editRec.Body.String()
	if !strings.Contains(editBody, `hx-put="/admin/memberships"`) {
		t.Fatalf("row edit must PUT to the existing membership route, body=%q", editBody)
	}
	if !strings.Contains(editBody, `hx-get="/admin/access/rows/1/lab"`) {
		t.Fatalf("row edit must have a Cancel back to the view fragment, body=%q", editBody)
	}

	confirmRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(confirmRec, cookieRequest(http.MethodGet, "/admin/access/rows/1/lab/revoke", operatorCookie(t, authSvc), ""))
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("row revoke-confirm: expected 200, got %d body=%q", confirmRec.Code, confirmRec.Body.String())
	}
	confirmBody := confirmRec.Body.String()
	if !strings.Contains(confirmBody, `hx-delete="/admin/memberships/1/lab"`) {
		t.Fatalf("revoke-confirm must wire the existing DELETE route, body=%q", confirmBody)
	}
	if !strings.Contains(confirmBody, `hx-get="/admin/access/rows/1/lab"`) {
		t.Fatalf("revoke-confirm must have a Cancel back to the view fragment, body=%q", confirmBody)
	}

	missingRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingRec, cookieRequest(http.MethodGet, "/admin/access/rows/1/ghost", operatorCookie(t, authSvc), ""))
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("unknown project row: expected 404, got %d", missingRec.Code)
	}

	for _, url := range []string{
		"/admin/access/rows/1/lab",
		"/admin/access/rows/1/lab/edit",
		"/admin/access/rows/1/lab/revoke",
	} {
		forbiddenRec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(forbiddenRec, cookieRequest(http.MethodGet, url, accountCookie(t, authSvc, "1", "alice"), ""))
		if forbiddenRec.Code != http.StatusForbidden {
			t.Fatalf("non-operator GET %s: expected 403, got %d", url, forbiddenRec.Code)
		}
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

// TestAdminAccountIsOperator is the OBL-16 acceptance guard: an account carrying
// cloud_users.is_admin=true logs in with username/password (no operator token) and
// is treated as operator — it sees the Admin nav on the dashboard and every
// /admin/* route returns 200. A non-admin account still sees no Admin nav and 403.
func TestAdminAccountIsOperator(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "labadmin", IsAdmin: true}}
	store.admins["1"] = true // labadmin is an account-level admin

	adminSession := accountCookie(t, authSvc, "1", "labadmin")

	// Dashboard root: Admin nav renders for the admin account.
	navRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(navRec, cookieRequest(http.MethodGet, "/", adminSession, ""))
	if navRec.Code != http.StatusOK {
		t.Fatalf("admin account GET /: expected 200, got %d", navRec.Code)
	}
	if !strings.Contains(navRec.Body.String(), `data-nav="/admin"`) {
		t.Fatalf("admin account dashboard must show the Admin nav entry, body=%q", navRec.Body.String())
	}

	// GET /admin → 200 for the admin account.
	pageRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(pageRec, cookieRequest(http.MethodGet, "/admin", adminSession, ""))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("admin account GET /admin: expected 200, got %d body=%q", pageRec.Code, pageRec.Body.String())
	}

	// GET /admin/users → 200 for the admin account.
	usersRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(usersRec, cookieRequest(http.MethodGet, "/admin/users", adminSession, ""))
	if usersRec.Code != http.StatusOK {
		t.Fatalf("admin account GET /admin/users: expected 200, got %d", usersRec.Code)
	}

	// A non-admin account (is_admin=false) is still forbidden and sees no Admin nav.
	nonAdmin := accountCookie(t, authSvc, "2", "personal")
	forbiddenRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forbiddenRec, cookieRequest(http.MethodGet, "/admin", nonAdmin, ""))
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("non-admin account GET /admin: expected 403, got %d", forbiddenRec.Code)
	}
	navRec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(navRec2, cookieRequest(http.MethodGet, "/", nonAdmin, ""))
	if strings.Contains(navRec2.Body.String(), `data-nav="/admin"`) {
		t.Fatalf("non-admin dashboard must NOT show the Admin nav entry, body=%q", navRec2.Body.String())
	}
}

// TestAdminSyncBearerNeverOperatorWithAdminFlag pins OBL-03 under OBL-16: even with
// the account-admin lookup wired in, the plain SYNC bearer never resolves to
// operator (its session carries no account identity, so it can't be promoted).
func TestAdminSyncBearerNeverOperatorWithAdminFlag(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true // an admin account exists, but the sync bearer isn't it

	// Sync bearer as a dashboard SESSION cookie → not operator.
	syncSession, err := authSvc.MintDashboardSession("sync-token")
	if err != nil {
		t.Fatalf("mint sync session: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/users", &http.Cookie{Name: dashboardSessionCookieName, Value: syncSession}, ""))
	if rec.Code == http.StatusOK {
		t.Fatalf("sync bearer session must NOT be operator (OBL-03), got 200")
	}
}

// TestAdminPromoteDemoteRoundTrip verifies the operator can grant and revoke an
// account's admin flag from the Users page endpoints.
func TestAdminPromoteDemoteRoundTrip(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true // keep a standing admin so demotes below aren't "last admin"

	// Promote user 5.
	promoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(promoteRec, cookieRequest(http.MethodPost, "/admin/users/5/promote", operatorCookie(t, authSvc), ""))
	if promoteRec.Code != http.StatusOK {
		t.Fatalf("promote: expected 200, got %d body=%q", promoteRec.Code, promoteRec.Body.String())
	}
	if !store.admins["5"] {
		t.Fatalf("expected user 5 to be admin after promote, got %v", store.admins["5"])
	}

	// Demote user 5 (user 1 remains admin, so not the last).
	demoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(demoteRec, cookieRequest(http.MethodPost, "/admin/users/5/demote", operatorCookie(t, authSvc), ""))
	if demoteRec.Code != http.StatusOK {
		t.Fatalf("demote: expected 200, got %d body=%q", demoteRec.Code, demoteRec.Body.String())
	}
	if store.admins["5"] {
		t.Fatalf("expected user 5 to be non-admin after demote")
	}

	// A non-operator account cannot promote/demote.
	forbidden := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forbidden, cookieRequest(http.MethodPost, "/admin/users/5/promote", accountCookie(t, authSvc, "9", "nope"), ""))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("non-operator promote: expected 403, got %d", forbidden.Code)
	}
}

// TestPromoteDemoteEmitAudit verifies OBL-05: a successful promote/demote each
// emit a best-effort audit row with the operator actor and the target user id.
// The rejected non-operator attempt (403) must NOT audit.
func TestPromoteDemoteEmitAudit(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true // standing admin so the demote below isn't "last admin"

	promoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(promoteRec, cookieRequest(http.MethodPost, "/admin/users/5/promote", operatorCookie(t, authSvc), ""))
	if promoteRec.Code != http.StatusOK {
		t.Fatalf("promote: expected 200, got %d body=%q", promoteRec.Code, promoteRec.Body.String())
	}

	demoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(demoteRec, cookieRequest(http.MethodPost, "/admin/users/5/demote", operatorCookie(t, authSvc), ""))
	if demoteRec.Code != http.StatusOK {
		t.Fatalf("demote: expected 200, got %d body=%q", demoteRec.Code, demoteRec.Body.String())
	}

	if len(store.auditEntries) != 2 {
		t.Fatalf("expected 2 audit entries (promote + demote), got %d: %+v", len(store.auditEntries), store.auditEntries)
	}
	promote := store.auditEntries[0]
	if promote.Action != cloudstore.AuditActionAdminPromote || promote.Outcome != cloudstore.AuditOutcomeAdminPromoted {
		t.Fatalf("unexpected promote audit: %+v", promote)
	}
	if promote.Metadata["user_id"] != "5" || promote.Project != cloudstore.AuditProjectSentinel {
		t.Fatalf("unexpected promote audit metadata/project: %+v", promote)
	}
	demote := store.auditEntries[1]
	if demote.Action != cloudstore.AuditActionAdminDemote || demote.Outcome != cloudstore.AuditOutcomeAdminDemoted {
		t.Fatalf("unexpected demote audit: %+v", demote)
	}
	if demote.Metadata["user_id"] != "5" {
		t.Fatalf("unexpected demote audit metadata: %+v", demote)
	}

	// A rejected (non-operator) promote attempt must not audit.
	forbidden := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forbidden, cookieRequest(http.MethodPost, "/admin/users/5/promote", accountCookie(t, authSvc, "9", "nope"), ""))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("non-operator promote: expected 403, got %d", forbidden.Code)
	}
	if len(store.auditEntries) != 2 {
		t.Fatalf("rejected promote must not audit, got %d entries", len(store.auditEntries))
	}
}

// TestDemoteLastAdminRefused verifies the last-admin guard: the only remaining
// admin cannot be demoted (409), preventing an Admin-section lockout.
func TestDemoteLastAdminRefused(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true // the ONLY admin

	// Demoting the last admin is refused.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/users/1/demote", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusConflict {
		t.Fatalf("demote last admin: expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !store.admins["1"] {
		t.Fatalf("last admin must remain admin after a refused demote")
	}

	// With a second admin present, demoting one is allowed again.
	store.admins["2"] = true
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, cookieRequest(http.MethodPost, "/admin/users/1/demote", operatorCookie(t, authSvc), ""))
	if rec2.Code != http.StatusOK {
		t.Fatalf("demote with two admins: expected 200, got %d", rec2.Code)
	}
	if store.admins["1"] {
		t.Fatalf("user 1 should be demoted when another admin remains")
	}
}

// TestPromoteDemoteNonNumericID verifies a non-numeric {id} on promote/demote
// is rejected with 400, not a raw store-error 500 (SHOULD-FIX 4, Slice 1
// security review — applied consistently across every /admin/users/{id}/*
// route, not just the new Slice 1 ones).
func TestPromoteDemoteNonNumericID(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	promoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(promoteRec, cookieRequest(http.MethodPost, "/admin/users/not-a-number/promote", operatorCookie(t, authSvc), ""))
	if promoteRec.Code != http.StatusBadRequest {
		t.Fatalf("promote: expected 400, got %d body=%q", promoteRec.Code, promoteRec.Body.String())
	}

	demoteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(demoteRec, cookieRequest(http.MethodPost, "/admin/users/not-a-number/demote", operatorCookie(t, authSvc), ""))
	if demoteRec.Code != http.StatusBadRequest {
		t.Fatalf("demote: expected 400, got %d body=%q", demoteRec.Code, demoteRec.Body.String())
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
		// Command Center v2, Slice 1: operator-facing user CRUD.
		{http.MethodPost, "/admin/users", `{"username":"x","email":"x@example.com"}`, "application/json"},
		{http.MethodPut, "/admin/users/1", `{"username":"x","email":"x@example.com"}`, "application/json"},
		{http.MethodPost, "/admin/users/1/password", `{}`, "application/json"},
		{http.MethodPost, "/admin/users/1/delete?confirm=1", "", ""},
		// Command Center v2, Slice 2: the Users page delete-confirm/cancel
		// swap-in-place fragments.
		{http.MethodGet, "/admin/users/1/delete-confirm", "", ""},
		{http.MethodGet, "/admin/users/1/delete-cancel", "", ""},
		// Command Center v2, Slice 3: the unified Access page's per-row
		// edit-in-place fragments.
		{http.MethodGet, "/admin/access/rows/1/lab", "", ""},
		{http.MethodGet, "/admin/access/rows/1/lab/edit", "", ""},
		{http.MethodGet, "/admin/access/rows/1/lab/revoke", "", ""},
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
