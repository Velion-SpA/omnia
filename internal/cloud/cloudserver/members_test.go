package cloudserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cloudauth "github.com/Velion-SpA/omnia/internal/cloud/auth"
	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
	"github.com/Velion-SpA/omnia/internal/store"
)

// ─── Membership-manager methods on the RBAC fake store ────────────────────────
// These complete the membershipManager interface so the fake can back the
// member-management endpoints and claim-on-first-push.

func (s *fakeMembershipStore) ListProjectMembers(project string) ([]cloudstore.Membership, error) {
	var out []cloudstore.Membership
	for _, m := range s.memberships {
		if m.Project == project {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (s *fakeMembershipStore) GrantMembership(accountID, project string, perms int, role string) error {
	s.grant(accountID, project, perms, role)
	return nil
}

func (s *fakeMembershipStore) RevokeMembership(accountID, project string) error {
	delete(s.memberships, membershipKey(accountID, project))
	return nil
}

// ─── Account-capable auth fake ────────────────────────────────────────────────
// The member-management routes register only when s.account != nil (i.e. the
// Authenticator also satisfies AccountService). accountCapableAuth embeds
// fakeRBACAuth and adds no-op Signup/Login so the routes are wired in tests.

type accountCapableAuth struct {
	*fakeRBACAuth
}

func (a accountCapableAuth) Signup(username, email, password string) (*cloudstore.User, error) {
	return &cloudstore.User{ID: username, Username: username, Email: email}, nil
}

func (a accountCapableAuth) Login(username, password string) (string, *cloudstore.User, error) {
	return "token-" + username, &cloudstore.User{ID: username, Username: username}, nil
}

// newMemberTestServer builds an RBAC server whose auth ALSO satisfies
// AccountService, so the /projects/{project}/members routes are registered.
func newMemberTestServer(ms *fakeMembershipStore, authSvc *fakeRBACAuth) *CloudServer {
	return New(ms, accountCapableAuth{fakeRBACAuth: authSvc}, 0)
}

func decodeMembers(t *testing.T, body []byte) []memberView {
	t.Helper()
	var out []memberView
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode members response: %v body=%q", err, string(body))
	}
	return out
}

// ─── Task 4 tests ─────────────────────────────────────────────────────────────

// TestClaimOnFirstPushMakesPusherOwner verifies that the first authenticated
// account to push a brand-new project becomes its owner with PermAll.
func TestClaimOnFirstPushMakesPusherOwner(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	srv := newMemberTestServer(ms, authSvc)

	// Alice pushes to a project that has no members yet.
	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, _ := coerceChunkProject(payload, "fresh-proj")
	chunkID := chunkIDFromPayload(normalized)
	pushBody, _ := json.Marshal(map[string]any{
		"chunk_id":   chunkID,
		"project":    "fresh-proj",
		"created_by": "alice",
		"data":       json.RawMessage(payload),
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/sync/push", "token-alice", pushBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for first push to fresh project, got %d body=%q", rec.Code, rec.Body.String())
	}

	m, _ := ms.GetMembership("alice", "fresh-proj")
	if m == nil {
		t.Fatalf("expected alice to have a membership on fresh-proj after claim")
	}
	if m.Role != cloudauth.RoleOwner {
		t.Fatalf("expected alice to be owner, got role %q", m.Role)
	}
	if cloudauth.Permission(m.Perms) != cloudauth.PermAll {
		t.Fatalf("expected owner to have PermAll, got %d", m.Perms)
	}
}

// TestClaimDoesNotStealExistingProject verifies a second account pushing an
// already-claimed project does NOT become owner and is denied (no membership).
func TestClaimDoesNotStealExistingProject(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
			"token-eve":   {AccountID: "eve", Username: "eve"},
		},
	}
	ms.grant("alice", "owned", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, _ := coerceChunkProject(payload, "owned")
	chunkID := chunkIDFromPayload(normalized)
	pushBody, _ := json.Marshal(map[string]any{
		"chunk_id":   chunkID,
		"project":    "owned",
		"created_by": "eve",
		"data":       json.RawMessage(payload),
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/sync/push", "token-eve", pushBody))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for eve pushing alice's project, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m, _ := ms.GetMembership("eve", "owned"); m != nil {
		t.Fatalf("eve must NOT have gained a membership on owned, got %+v", m)
	}
}

// TestOwnerAddsMember verifies an owner can add a member with PermRead+RoleMember.
func TestOwnerAddsMember(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	body, _ := json.Marshal(addMemberRequest{AccountID: "newbie", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-owner", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 when owner adds member, got %d body=%q", rec.Code, rec.Body.String())
	}
	m, _ := ms.GetMembership("newbie", "proj")
	if m == nil || m.Role != cloudauth.RoleMember || cloudauth.Permission(m.Perms) != cloudauth.PermRead {
		t.Fatalf("expected newbie to be member with PermRead, got %+v", m)
	}
}

// TestGrantMembershipEmitsAudit verifies OBL-05: a successful membership grant
// emits a membership_grant audit row scoped to the project, with the granter
// as contributor and the target account in metadata.
func TestGrantMembershipEmitsAudit(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	body, _ := json.Marshal(addMemberRequest{AccountID: "newbie", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-owner", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditEntries) != 1 {
		t.Fatalf("expected 1 audit entry after grant, got %d: %+v", len(ms.auditEntries), ms.auditEntries)
	}
	entry := ms.auditEntries[0]
	if entry.Action != cloudstore.AuditActionMembershipGrant || entry.Outcome != cloudstore.AuditOutcomeMembershipGranted {
		t.Fatalf("unexpected action/outcome: %+v", entry)
	}
	if entry.Contributor != "owner" || entry.Project != "proj" {
		t.Fatalf("expected contributor=owner project=proj, got %+v", entry)
	}
	if entry.Metadata == nil || entry.Metadata["target_account_id"] != "newbie" {
		t.Fatalf("expected target_account_id=newbie in metadata, got %+v", entry.Metadata)
	}
}

// TestRemoveMemberEmitsAudit verifies OBL-05: a successful membership revoke
// emits a membership_revoke audit row.
func TestRemoveMemberEmitsAudit(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("bob", "proj", int(cloudauth.PermRead), cloudauth.RoleMember)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodDelete, "/projects/proj/members/bob", "token-owner", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditEntries) != 1 {
		t.Fatalf("expected 1 audit entry after revoke, got %d: %+v", len(ms.auditEntries), ms.auditEntries)
	}
	entry := ms.auditEntries[0]
	if entry.Action != cloudstore.AuditActionMembershipRevoke || entry.Outcome != cloudstore.AuditOutcomeMembershipRevoked {
		t.Fatalf("unexpected action/outcome: %+v", entry)
	}
	if entry.Contributor != "owner" || entry.Project != "proj" {
		t.Fatalf("expected contributor=owner project=proj, got %+v", entry)
	}
	if entry.Metadata == nil || entry.Metadata["target_account_id"] != "bob" {
		t.Fatalf("expected target_account_id=bob in metadata, got %+v", entry.Metadata)
	}
}

// TestAdminAddAndRemoveButCannotDeleteOwner verifies an admin can add/remove
// members but DELETE on the owner returns 403.
func TestAdminAddAndRemoveButCannotDeleteOwner(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-admin": {AccountID: "admin", Username: "admin"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("admin", "proj", int(cloudauth.PermAll), cloudauth.RoleAdmin)
	srv := newMemberTestServer(ms, authSvc)

	// Admin adds a member.
	addBody, _ := json.Marshal(addMemberRequest{AccountID: "m1", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	recAdd := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recAdd, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-admin", addBody))
	if recAdd.Code != http.StatusCreated {
		t.Fatalf("expected 201 when admin adds member, got %d body=%q", recAdd.Code, recAdd.Body.String())
	}

	// Admin removes that member.
	recDel := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDel, makeAccountRequest(http.MethodDelete, "/projects/proj/members/m1", "token-admin", nil))
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 when admin removes member, got %d body=%q", recDel.Code, recDel.Body.String())
	}
	if m, _ := ms.GetMembership("m1", "proj"); m != nil {
		t.Fatalf("expected m1 removed, still present: %+v", m)
	}

	// Admin tries to DELETE the owner — must be 403, owner untouched.
	recOwner := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recOwner, makeAccountRequest(http.MethodDelete, "/projects/proj/members/owner", "token-admin", nil))
	if recOwner.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when admin deletes owner, got %d body=%q", recOwner.Code, recOwner.Body.String())
	}
	if m, _ := ms.GetMembership("owner", "proj"); m == nil {
		t.Fatalf("owner must remain after a blocked delete")
	}
}

// TestModeratorGrantingAdminBlocked verifies escalation is blocked: a moderator
// trying to grant RoleAdmin gets 403.
func TestModeratorGrantingAdminBlocked(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-mod": {AccountID: "mod", Username: "mod"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("mod", "proj", int(cloudauth.PermWrite), cloudauth.RoleModerator)
	srv := newMemberTestServer(ms, authSvc)

	body, _ := json.Marshal(addMemberRequest{AccountID: "wannabe", Perms: int(cloudauth.PermAll), Role: cloudauth.RoleAdmin})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-mod", body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (escalation blocked) when moderator grants admin, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m, _ := ms.GetMembership("wannabe", "proj"); m != nil {
		t.Fatalf("escalation leaked: wannabe gained a membership: %+v", m)
	}
}

// TestModeratorRemovingAdminBlocked verifies a moderator cannot remove an admin.
func TestModeratorRemovingAdminBlocked(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-mod": {AccountID: "mod", Username: "mod"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("mod", "proj", int(cloudauth.PermWrite), cloudauth.RoleModerator)
	ms.grant("someadmin", "proj", int(cloudauth.PermAll), cloudauth.RoleAdmin)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodDelete, "/projects/proj/members/someadmin", "token-mod", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when moderator removes admin, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m, _ := ms.GetMembership("someadmin", "proj"); m == nil {
		t.Fatalf("admin must remain after a blocked moderator delete")
	}
}

// TestMemberCannotManage verifies a plain member is rejected on every management
// endpoint (GET/POST/DELETE).
func TestMemberCannotManage(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-member": {AccountID: "member", Username: "member"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("member", "proj", int(cloudauth.PermRead), cloudauth.RoleMember)
	srv := newMemberTestServer(ms, authSvc)

	// POST add — 403.
	addBody, _ := json.Marshal(addMemberRequest{AccountID: "x", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	recAdd := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recAdd, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-member", addBody))
	if recAdd.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member POST, got %d body=%q", recAdd.Code, recAdd.Body.String())
	}

	// DELETE — 403.
	recDel := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDel, makeAccountRequest(http.MethodDelete, "/projects/proj/members/owner", "token-member", nil))
	if recDel.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member DELETE, got %d body=%q", recDel.Code, recDel.Body.String())
	}

	// GET list — a member IS allowed to read the member list (has a membership).
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, makeAccountRequest(http.MethodGet, "/projects/proj/members", "token-member", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for member GET list (member has a membership), got %d body=%q", recGet.Code, recGet.Body.String())
	}
}

// TestActorAssigningRoleAtOwnLevelBlocked verifies an actor cannot assign a role
// equal to their own (admin assigning admin → 403).
func TestActorAssigningRoleAtOwnLevelBlocked(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-admin": {AccountID: "admin", Username: "admin"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("admin", "proj", int(cloudauth.PermAll), cloudauth.RoleAdmin)
	srv := newMemberTestServer(ms, authSvc)

	body, _ := json.Marshal(addMemberRequest{AccountID: "peer", Perms: int(cloudauth.PermAll), Role: cloudauth.RoleAdmin})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-admin", body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when admin assigns admin (self-level), got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestLegacyTokenOnManagementBlocked verifies the legacy shared token (no
// per-project role / nil claims) is rejected with 403 on management endpoints.
func TestLegacyTokenOnManagementBlocked(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		legacy:   "shared-secret",
		accounts: map[string]*cloudauth.AccountClaims{},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	// GET with legacy token — 403 (no per-project role).
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, makeAccountRequest(http.MethodGet, "/projects/proj/members", "shared-secret", nil))
	if recGet.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for legacy token GET members, got %d body=%q", recGet.Code, recGet.Body.String())
	}

	// POST with legacy token — 403.
	addBody, _ := json.Marshal(addMemberRequest{AccountID: "x", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	recAdd := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recAdd, makeAccountRequest(http.MethodPost, "/projects/proj/members", "shared-secret", addBody))
	if recAdd.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for legacy token POST members, got %d body=%q", recAdd.Code, recAdd.Body.String())
	}
}

// TestGetMembersListsForAuthorizedCaller verifies an authorized caller gets the
// full member list.
func TestGetMembersListsForAuthorizedCaller(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	ms.grant("bob", "proj", int(cloudauth.PermRead), cloudauth.RoleMember)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/projects/proj/members", "token-owner", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for owner GET members, got %d body=%q", rec.Code, rec.Body.String())
	}
	members := decodeMembers(t, rec.Body.Bytes())
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d: %+v", len(members), members)
	}
	seen := map[string]string{}
	for _, m := range members {
		seen[m.AccountID] = m.Role
	}
	if seen["owner"] != cloudauth.RoleOwner || seen["bob"] != cloudauth.RoleMember {
		t.Fatalf("member list roles wrong: %+v", seen)
	}
}

// TestNonMemberCannotListMembers verifies a caller with NO membership on the
// project cannot read the member list (403).
func TestNonMemberCannotListMembers(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-outsider": {AccountID: "outsider", Username: "outsider"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/projects/proj/members", "token-outsider", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-member GET members, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAddMemberPermsAreMasked verifies that out-of-range permission bits are
// silently masked to the defined PermAll boundary before storage and response.
func TestAddMemberPermsAreMasked(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	// Send perms=255 (0xFF), which exceeds PermAll=15 (0x0F).
	body, _ := json.Marshal(addMemberRequest{AccountID: "newbie", Perms: 255, Role: cloudauth.RoleMember})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/proj/members", "token-owner", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 when adding member with masked perms, got %d body=%q", rec.Code, rec.Body.String())
	}

	expectedPerms := 255 & int(cloudauth.PermAll) // = 15
	m, _ := ms.GetMembership("newbie", "proj")
	if m == nil {
		t.Fatal("expected newbie to have a membership after add")
	}
	if m.Perms != expectedPerms {
		t.Fatalf("stored perms = %d, want %d (masked)", m.Perms, expectedPerms)
	}

	// The HTTP response body must also reflect the masked value.
	var respView memberView
	if err := json.Unmarshal(rec.Body.Bytes(), &respView); err != nil {
		t.Fatalf("decode response: %v body=%q", err, rec.Body.String())
	}
	if respView.Perms != expectedPerms {
		t.Fatalf("response perms = %d, want %d (masked)", respView.Perms, expectedPerms)
	}
}

// TestAddMemberToOrphanProjectReturns404 verifies adding a member to a project
// that has no members (orphan) returns 404 — claiming is via push, not add.
func TestAddMemberToOrphanProjectReturns404(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-owner": {AccountID: "owner", Username: "owner"},
		},
	}
	// owner has a membership elsewhere but NOT on "ghost-proj".
	ms.grant("owner", "other", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	body, _ := json.Marshal(addMemberRequest{AccountID: "x", Perms: int(cloudauth.PermRead), Role: cloudauth.RoleMember})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/projects/ghost-proj/members", "token-owner", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 adding member to orphan project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestMutationPushClaimsOwnership verifies claim-on-first-push also fires on the
// mutation-push path for a fresh project.
func TestMutationPushClaimsOwnership(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-carol": {AccountID: "carol", Username: "carol"},
		},
	}
	srv := newMemberTestServer(ms, authSvc)

	pushBody, _ := json.Marshal(map[string]any{
		"entries": []map[string]any{
			{
				"project":    "mut-proj",
				"entity":     store.SyncEntitySession,
				"entity_key": "s-1",
				"op":         store.SyncOpUpsert,
				"payload":    json.RawMessage(`{"id":"s-1"}`),
			},
		},
		"created_by": "carol",
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/sync/mutations/push", "token-carol", pushBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for first mutation push to fresh project, got %d body=%q", rec.Code, rec.Body.String())
	}
	m, _ := ms.GetMembership("carol", "mut-proj")
	if m == nil || m.Role != cloudauth.RoleOwner {
		t.Fatalf("expected carol to own mut-proj after mutation push, got %+v", m)
	}
}
