package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// This file proves the two N+1 findings from the 2026-07-19 performance audit
// are actually fixed — not just that the batched store methods exist (already
// covered by cloudstore integration tests), but that the CALLERS
// (handleAdminProjectsPage, teamDerivedForAccount) were rewired to use them
// INSTEAD OF the old per-item scans. Each test instruments a counting wrapper
// around the existing fakes and asserts the old per-item methods are called
// ZERO times, while the new batched method is called exactly ONCE, regardless
// of how many projects/teams exist.

// ─── Bug 1: Admin Projects page sync-control N+1 ─────────────────────────────

// countingProjectSyncStore wraps a *fakeProjectSyncAdminStore and counts calls
// to GetProjectSyncControl (the old per-project call) vs
// ListProjectSyncControlsMap (the new batched call).
type countingProjectSyncStore struct {
	*fakeProjectSyncAdminStore
	getProjectSyncControlCalls   int
	listProjectSyncControlsCalls int
}

func (s *countingProjectSyncStore) GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error) {
	s.getProjectSyncControlCalls++
	return s.fakeProjectSyncAdminStore.GetProjectSyncControl(project)
}

func (s *countingProjectSyncStore) ListProjectSyncControlsMap(ctx context.Context) (map[string]cloudstore.ProjectSyncControl, error) {
	s.listProjectSyncControlsCalls++
	return s.fakeProjectSyncAdminStore.ListProjectSyncControlsMap(ctx)
}

// TestAdminProjectsPageSyncControlsBatchedNotPerProject renders GET
// /admin/projects with several known projects and asserts the handler issues
// ONE ListProjectSyncControlsMap call and ZERO GetProjectSyncControl calls —
// the fix for the N+1 where a per-project GetProjectSyncControl was issued
// inside the projects loop.
func TestAdminProjectsPageSyncControlsBatchedNotPerProject(t *testing.T) {
	base := newFakeProjectSyncAdminStore()
	const knownProjectCount = 6
	for i := 0; i < knownProjectCount; i++ {
		base.grant(fmt.Sprintf("acct-%d", i), fmt.Sprintf("nplus1-proj-%d", i), int(cloudauth.PermRead), cloudauth.RoleMember)
	}
	// One project explicitly paused, to prove the batched path still surfaces
	// pause state correctly (not just that it's called once).
	if err := base.SetProjectSyncEnabled("nplus1-proj-2", false, "operator", "incident"); err != nil {
		t.Fatalf("seed pause: %v", err)
	}

	counting := &countingProjectSyncStore{fakeProjectSyncAdminStore: base}
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(counting, authSvc, 0, WithDashboardAdminToken("admin-token"))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/projects: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	if counting.listProjectSyncControlsCalls != 1 {
		t.Fatalf("expected ListProjectSyncControlsMap called exactly once, got %d", counting.listProjectSyncControlsCalls)
	}
	if counting.getProjectSyncControlCalls != 0 {
		t.Fatalf("expected GetProjectSyncControl called ZERO times (batched), got %d calls for %d known projects — N+1 regression",
			counting.getProjectSyncControlCalls, knownProjectCount)
	}

	// Sanity: rendering is still correct — the paused project shows its state.
	body := rec.Body.String()
	for _, want := range []string{"nplus1-proj-2", "incident"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Projects page missing %q after batching fix, body=%q", want, body)
		}
	}
}

// ─── Bug 2: Admin Access page teamDerivedForAccount N+1 ──────────────────────

// countingTeamsStore wraps a *fakeTeamsStore and counts calls to the OLD
// per-team scan trio (ListTeams, ListMembersOfTeam, ListProjectsForTeam) vs
// the NEW account-keyed ListTeamDerivedGrantsForAccount.
type countingTeamsStore struct {
	*fakeTeamsStore
	listTeamsCalls                       int
	listMembersOfTeamCalls               int
	listProjectsForTeamCalls             int
	listTeamDerivedGrantsForAccountCalls int
}

func (s *countingTeamsStore) ListTeams(ctx context.Context) ([]cloudstore.Team, error) {
	s.listTeamsCalls++
	return s.fakeTeamsStore.ListTeams(ctx)
}

func (s *countingTeamsStore) ListMembersOfTeam(ctx context.Context, teamID string) ([]cloudstore.TeamMember, error) {
	s.listMembersOfTeamCalls++
	return s.fakeTeamsStore.ListMembersOfTeam(ctx, teamID)
}

func (s *countingTeamsStore) ListProjectsForTeam(ctx context.Context, teamID string) ([]string, error) {
	s.listProjectsForTeamCalls++
	return s.fakeTeamsStore.ListProjectsForTeam(ctx, teamID)
}

func (s *countingTeamsStore) ListTeamDerivedGrantsForAccount(ctx context.Context, accountID string) ([]cloudstore.TeamDerivedGrant, error) {
	s.listTeamDerivedGrantsForAccountCalls++
	return s.fakeTeamsStore.ListTeamDerivedGrantsForAccount(ctx, accountID)
}

// TestTeamDerivedForAccountUsesSingleAccountKeyedQuery is the direct proof of
// the audit's exact complaint: "scales with total org team count, not the
// account's memberships". Five teams exist in the org; the account belongs to
// only ONE of them. The fix must cost exactly one query regardless of the
// other four teams' existence.
func TestTeamDerivedForAccountUsesSingleAccountKeyedQuery(t *testing.T) {
	srv, base, _ := newTeamsAdminTestServer(t)
	ctx := context.Background()
	memberProfile := profileIDByName(t, base, "Member")

	const orgTeamCount = 5
	const ownProject = "proj2"
	for i := 0; i < orgTeamCount; i++ {
		team, err := base.CreateTeam(ctx, fmt.Sprintf("Team%d", i), "work")
		if err != nil {
			t.Fatalf("create team %d: %v", i, err)
		}
		project := fmt.Sprintf("proj%d", i)
		if err := base.AddTeamProject(ctx, team.ID, project); err != nil {
			t.Fatalf("add project for team %d: %v", i, err)
		}
		if err := base.UpsertProjectMeta(ctx, project, "work", ""); err != nil {
			t.Fatalf("classify project %d: %v", i, err)
		}
		if project == ownProject {
			if err := base.AddTeamMember(ctx, team.ID, "worker", memberProfile); err != nil {
				t.Fatalf("add worker to its own team: %v", err)
			}
		} else {
			if err := base.AddTeamMember(ctx, team.ID, "someone-else", memberProfile); err != nil {
				t.Fatalf("add unrelated member to team %d: %v", i, err)
			}
		}
	}

	wrapper := &countingTeamsStore{fakeTeamsStore: base}
	_, work, _ := srv.teamDerivedForAccount(ctx, wrapper, "worker", nil)

	if wrapper.listTeamDerivedGrantsForAccountCalls != 1 {
		t.Fatalf("expected ListTeamDerivedGrantsForAccount called exactly once, got %d", wrapper.listTeamDerivedGrantsForAccountCalls)
	}
	if wrapper.listTeamsCalls != 0 || wrapper.listMembersOfTeamCalls != 0 || wrapper.listProjectsForTeamCalls != 0 {
		t.Fatalf("expected teamDerivedForAccount to use ONLY the batched account-keyed query (0 ListTeams/ListMembersOfTeam/ListProjectsForTeam calls) — got ListTeams=%d ListMembersOfTeam=%d ListProjectsForTeam=%d across %d org teams — N+1 regression",
			wrapper.listTeamsCalls, wrapper.listMembersOfTeamCalls, wrapper.listProjectsForTeamCalls, orgTeamCount)
	}

	// Sanity: the result is still correct — worker's own team grants exactly
	// one project (its own), untouched by the other 4 teams in the org.
	if len(work) != 1 || work[0].Project != ownProject {
		t.Fatalf("expected exactly one work-project row (%q) for worker's own team, got %+v", ownProject, work)
	}
}
