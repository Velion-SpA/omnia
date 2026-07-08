package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Velion-SpA/omnia/internal/cloud/auth"
	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
)

// TestSyncBearerCannotSeeAllProjectsInMultiTenantMode is the OBL-03 guard: in a
// multi-tenant deployment (an account/membership store is configured), presenting
// the sync bearer to the dashboard must NOT grant operator visibility over every
// account's projects. Only the designated admin credential is an operator.
func TestSyncBearerCannotSeeAllProjectsInMultiTenantMode(t *testing.T) {
	ms := newFakeMembershipStore()
	// A foreign account owns a project the sync bearer must never see.
	ms.grant("alice", "secret-proj", int(cloudauth.PermRead), "owner")

	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})

	srv := New(ms, authSvc, 0, WithDashboardAdminToken("admin-token"))
	if srv.accountProjectAuth == nil {
		t.Fatal("setup: membership store must activate multi-tenant RBAC")
	}

	// The sync bearer, presented as a dashboard session, is NOT an operator and
	// sees no foreign projects.
	syncSession, err := authSvc.MintDashboardSession("sync-token")
	if err != nil {
		t.Fatalf("mint sync session: %v", err)
	}
	syncReq := httptest.NewRequest(http.MethodGet, "/", nil)
	syncReq.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: syncSession})
	projects, operator := srv.dashboardVisibleProjects(syncReq)
	if operator {
		t.Fatal("sync bearer must NOT be treated as dashboard operator in multi-tenant mode")
	}
	for _, p := range projects {
		if p == "secret-proj" {
			t.Fatalf("sync bearer must not see foreign project, got %v", projects)
		}
	}
	if len(projects) != 0 {
		t.Fatalf("sync bearer (no account) must see no projects, got %v", projects)
	}

	// The admin credential still grants full operator visibility.
	adminSession, err := authSvc.MintDashboardSession("admin-token")
	if err != nil {
		t.Fatalf("mint admin session: %v", err)
	}
	adminReq := httptest.NewRequest(http.MethodGet, "/", nil)
	adminReq.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: adminSession})
	if _, operator := srv.dashboardVisibleProjects(adminReq); !operator {
		t.Fatal("admin credential must grant operator visibility")
	}

	// An account session sees exactly its own memberships (scope isolation intact).
	acctToken, err := authSvc.MintAccountToken("alice", "alice")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	aliceSession, err := authSvc.MintDashboardSession(acctToken)
	if err != nil {
		t.Fatalf("mint alice session: %v", err)
	}
	aliceReq := httptest.NewRequest(http.MethodGet, "/", nil)
	aliceReq.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: aliceSession})
	aliceProjects, aliceOperator := srv.dashboardVisibleProjects(aliceReq)
	if aliceOperator {
		t.Fatal("an account must never be an operator")
	}
	if len(aliceProjects) != 1 || aliceProjects[0] != "secret-proj" {
		t.Fatalf("alice must see exactly her project, got %v", aliceProjects)
	}
}

// TestSyncBearerRemainsOperatorInLegacySingleTenantMode pins the compatibility
// switch: with NO account/membership store configured (pure legacy single-tenant),
// the sync bearer still resolves to the server operator, preserving existing
// single-token deployments (OBL-03 do_not_touch on legacy behaviour).
func TestSyncBearerRemainsOperatorInLegacySingleTenantMode(t *testing.T) {
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")

	// fakeStore does NOT implement membershipStore → legacy single-tenant mode.
	srv := New(&fakeStore{}, authSvc, 0, WithDashboardAdminToken("admin-token"))
	if srv.accountProjectAuth != nil {
		t.Fatal("setup: fakeStore must NOT activate multi-tenant RBAC")
	}

	syncSession, err := authSvc.MintDashboardSession("sync-token")
	if err != nil {
		t.Fatalf("mint sync session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: syncSession})
	if _, operator := srv.dashboardVisibleProjects(req); !operator {
		t.Fatal("legacy single-tenant sync bearer must remain the server operator")
	}
}
