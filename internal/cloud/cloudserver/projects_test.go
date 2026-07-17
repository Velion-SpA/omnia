package cloudserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
)

// decodeProjectsResponse decodes the {"projects": [...]} body from GET /projects.
func decodeProjectsResponse(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Projects []string `json:"projects"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode projects response: %v body=%q", err, string(body))
	}
	return out.Projects
}

// TestListProjectsReturnsReadableProjects verifies GET /projects returns
// exactly the projects the caller has at least read access to (via the
// ListMembershipsForAccount fallback, since fakeMembershipStore predates the
// teams model and does not implement ListReadableProjectsForAccount).
func TestListProjectsReturnsReadableProjects(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermRead), cloudauth.RoleMember)
	ms.grant("alice", "proj-b", int(cloudauth.PermAll), cloudauth.RoleOwner)
	// A project alice has no membership on at all must not leak in.
	ms.grant("bob", "proj-c", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/projects", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /projects, got %d body=%q", rec.Code, rec.Body.String())
	}
	projects := decodeProjectsResponse(t, rec.Body.Bytes())
	sort.Strings(projects)
	if len(projects) != 2 || projects[0] != "proj-a" || projects[1] != "proj-b" {
		t.Fatalf("expected [proj-a proj-b], got %+v", projects)
	}
}

// TestListProjectsRejectsLegacyToken verifies the legacy shared token (no
// per-account identity) is rejected with 403, mirroring
// TestLegacyTokenOnManagementBlocked for the member-management endpoints.
func TestListProjectsRejectsLegacyToken(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		legacy:   "shared-secret",
		accounts: map[string]*cloudauth.AccountClaims{},
	}
	ms.grant("owner", "proj", int(cloudauth.PermAll), cloudauth.RoleOwner)
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/projects", "shared-secret", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for legacy token GET /projects, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── Primary-path (teams-layered) fake ─────────────────────────────────────

// fakeTeamReaderStore embeds fakeMembershipStore and additionally implements
// ListReadableProjectsForAccount, so it satisfies the PRIMARY resolution seam
// handleListProjects checks first (the layered teams ∪ per-project-override
// model), unlike plain fakeMembershipStore which only implements the
// ListMembershipsForAccount fallback used by the other tests in this file.
// (Named distinctly from the unrelated fakeTeamsStore in teams_admin_test.go,
// which backs the OBL-14 teams/profiles admin routes, not this seam.)
type fakeTeamReaderStore struct {
	*fakeMembershipStore
	readableProjects map[string][]string // accountID -> canned readable-projects result
}

func newFakeTeamReaderStore() *fakeTeamReaderStore {
	return &fakeTeamReaderStore{
		fakeMembershipStore: newFakeMembershipStore(),
		readableProjects:    make(map[string][]string),
	}
}

func (s *fakeTeamReaderStore) ListReadableProjectsForAccount(accountID string) ([]string, error) {
	return s.readableProjects[accountID], nil
}

// TestListProjectsUsesReadableProjectsPrimaryPath verifies that when the
// store implements ListReadableProjectsForAccount, GET /projects returns
// exactly that method's result and PREFERS it over the membership fallback.
// alice is given a plain membership grant AND a canned (disjoint) primary-path
// result; if the handler ever fell through to ListMembershipsForAccount
// instead of using the primary seam, this test would observe the membership
// grant's project ("member-only-proj") instead of the canned set.
func TestListProjectsUsesReadableProjectsPrimaryPath(t *testing.T) {
	ts := newFakeTeamReaderStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ts.grant("alice", "member-only-proj", int(cloudauth.PermRead), cloudauth.RoleMember)
	ts.readableProjects["alice"] = []string{"team-proj-x", "team-proj-y"}
	srv := New(ts, accountCapableAuth{fakeRBACAuth: authSvc}, 0)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/projects", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /projects, got %d body=%q", rec.Code, rec.Body.String())
	}
	projects := decodeProjectsResponse(t, rec.Body.Bytes())
	sort.Strings(projects)
	want := []string{"team-proj-x", "team-proj-y"}
	if len(projects) != len(want) || projects[0] != want[0] || projects[1] != want[1] {
		t.Fatalf("expected primary-path result %+v (not the membership fallback), got %+v", want, projects)
	}
}
