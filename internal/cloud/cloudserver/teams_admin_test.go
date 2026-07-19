package cloudserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── Fake teamsAdminStore ─────────────────────────────────────────────────────
// fakeTeamsStore embeds the admin-dashboard fake (ChunkStore + membership +
// adminDashboardStore seams) and adds the OBL-14 teams/profiles seam so the teams
// routes register. In-memory; seeds the default profiles like migrate() does.
type fakeTeamsStore struct {
	*fakeAdminDashboardStore
	profiles     map[string]*cloudstore.Profile
	teams        map[string]*cloudstore.Team
	teamProjects map[string]map[string]bool
	teamMembers  map[string]map[string]string // teamID → account → profileID
	projectMeta  map[string]*cloudstore.ProjectMeta
	nextProfile  int
	nextTeam     int
}

func newFakeTeamsStore() *fakeTeamsStore {
	s := &fakeTeamsStore{
		fakeAdminDashboardStore: newFakeAdminDashboardStore(),
		profiles:                map[string]*cloudstore.Profile{},
		teams:                   map[string]*cloudstore.Team{},
		teamProjects:            map[string]map[string]bool{},
		teamMembers:             map[string]map[string]string{},
		projectMeta:             map[string]*cloudstore.ProjectMeta{},
	}
	// Seed defaults (Moderator=15, Editor=7, Member=1), mirroring migrate().
	_, _ = s.CreateProfile(context.Background(), "Moderator", 15)
	_, _ = s.CreateProfile(context.Background(), "Editor", 7)
	_, _ = s.CreateProfile(context.Background(), "Member", 1)
	return s
}

func (s *fakeTeamsStore) CreateProfile(_ context.Context, name string, perms int) (*cloudstore.Profile, error) {
	for _, p := range s.profiles {
		if strings.EqualFold(p.Name, name) {
			return nil, cloudstore.ErrProfileNameTaken
		}
	}
	s.nextProfile++
	p := &cloudstore.Profile{ID: fmt.Sprintf("p%d", s.nextProfile), Name: name, Perms: perms}
	s.profiles[p.ID] = p
	return p, nil
}

func (s *fakeTeamsStore) ListProfiles(context.Context) ([]cloudstore.Profile, error) {
	out := make([]cloudstore.Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *fakeTeamsStore) GetProfile(_ context.Context, id string) (*cloudstore.Profile, error) {
	if p, ok := s.profiles[id]; ok {
		return p, nil
	}
	return nil, nil
}

func (s *fakeTeamsStore) UpdateProfile(_ context.Context, id, name string, perms int) error {
	p, ok := s.profiles[id]
	if !ok {
		return cloudstore.ErrProfileNotFound
	}
	for oid, other := range s.profiles {
		if oid != id && strings.EqualFold(other.Name, name) {
			return cloudstore.ErrProfileNameTaken
		}
	}
	p.Name, p.Perms = name, perms
	return nil
}

func (s *fakeTeamsStore) DeleteProfile(_ context.Context, id string) error {
	delete(s.profiles, id)
	return nil
}

func (s *fakeTeamsStore) CreateTeam(_ context.Context, name, kind string) (*cloudstore.Team, error) {
	for _, t := range s.teams {
		if strings.EqualFold(t.Name, name) {
			return nil, cloudstore.ErrTeamNameTaken
		}
	}
	s.nextTeam++
	t := &cloudstore.Team{ID: fmt.Sprintf("t%d", s.nextTeam), Name: name, Kind: normalizeKind(kind)}
	s.teams[t.ID] = t
	return t, nil
}

func (s *fakeTeamsStore) ListTeams(context.Context) ([]cloudstore.Team, error) {
	out := make([]cloudstore.Team, 0, len(s.teams))
	for _, t := range s.teams {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *fakeTeamsStore) GetTeam(_ context.Context, id string) (*cloudstore.Team, error) {
	if t, ok := s.teams[id]; ok {
		return t, nil
	}
	return nil, nil
}

func (s *fakeTeamsStore) UpdateTeam(_ context.Context, id, name, kind string) error {
	t, ok := s.teams[id]
	if !ok {
		return cloudstore.ErrTeamNotFound
	}
	t.Name, t.Kind = name, normalizeKind(kind)
	return nil
}

func (s *fakeTeamsStore) DeleteTeam(_ context.Context, id string) error {
	delete(s.teams, id)
	delete(s.teamProjects, id)
	delete(s.teamMembers, id)
	return nil
}

func (s *fakeTeamsStore) AddTeamProject(_ context.Context, teamID, project string) error {
	if s.teamProjects[teamID] == nil {
		s.teamProjects[teamID] = map[string]bool{}
	}
	s.teamProjects[teamID][project] = true
	return nil
}

func (s *fakeTeamsStore) RemoveTeamProject(_ context.Context, teamID, project string) error {
	delete(s.teamProjects[teamID], project)
	return nil
}

func (s *fakeTeamsStore) ListProjectsForTeam(_ context.Context, teamID string) ([]string, error) {
	var out []string
	for p := range s.teamProjects[teamID] {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (s *fakeTeamsStore) AddTeamMember(_ context.Context, teamID, accountID, profileID string) error {
	if s.teamMembers[teamID] == nil {
		s.teamMembers[teamID] = map[string]string{}
	}
	s.teamMembers[teamID][accountID] = profileID
	return nil
}

func (s *fakeTeamsStore) RemoveTeamMember(_ context.Context, teamID, accountID string) error {
	delete(s.teamMembers[teamID], accountID)
	return nil
}

func (s *fakeTeamsStore) ListMembersOfTeam(_ context.Context, teamID string) ([]cloudstore.TeamMember, error) {
	var out []cloudstore.TeamMember
	for account, profileID := range s.teamMembers[teamID] {
		m := cloudstore.TeamMember{AccountID: account, ProfileID: profileID}
		if p, ok := s.profiles[profileID]; ok {
			m.ProfileName = p.Name
			m.Perms = p.Perms
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

// ListTeamDerivedGrantsForAccount is the fake's account-keyed equivalent of
// scanning s.teamMembers for every team, mirroring the real batched SQL join
// (WHERE tm.account_id = $1) ordered the same way (project ASC, team ASC) so
// tests asserting on Sources order match the production query.
func (s *fakeTeamsStore) ListTeamDerivedGrantsForAccount(_ context.Context, accountID string) ([]cloudstore.TeamDerivedGrant, error) {
	var out []cloudstore.TeamDerivedGrant
	for teamID, members := range s.teamMembers {
		profileID, isMember := members[accountID]
		if !isMember {
			continue
		}
		team, ok := s.teams[teamID]
		if !ok {
			continue
		}
		var perms int
		var profileName string
		if p, ok := s.profiles[profileID]; ok {
			perms = p.Perms
			profileName = p.Name
		}
		for project := range s.teamProjects[teamID] {
			out = append(out, cloudstore.TeamDerivedGrant{
				Team:    team.Name,
				Project: project,
				Profile: profileName,
				Perms:   perms,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Team < out[j].Team
	})
	return out, nil
}

func (s *fakeTeamsStore) UpsertProjectMeta(_ context.Context, project, kind, displayName string) error {
	s.projectMeta[project] = &cloudstore.ProjectMeta{Project: project, Kind: normalizeKind(kind), DisplayName: displayName}
	return nil
}

func (s *fakeTeamsStore) GetProjectMeta(_ context.Context, project string) (*cloudstore.ProjectMeta, error) {
	if m, ok := s.projectMeta[project]; ok {
		return m, nil
	}
	return nil, nil
}

func (s *fakeTeamsStore) KnownProjects(context.Context) ([]cloudstore.KnownProject, error) {
	seen := map[string]bool{}
	var out []cloudstore.KnownProject
	add := func(project string) {
		if project == "" || seen[project] {
			return
		}
		seen[project] = true
		kp := cloudstore.KnownProject{Project: project}
		if m, ok := s.projectMeta[project]; ok {
			kp.Kind = m.Kind
			kp.DisplayName = m.DisplayName
		}
		out = append(out, kp)
	}
	for _, m := range s.memberships {
		add(m.Project)
	}
	for _, set := range s.teamProjects {
		for p := range set {
			add(p)
		}
	}
	for p := range s.projectMeta {
		add(p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

func newTeamsAdminTestServer(t *testing.T) (*CloudServer, *fakeTeamsStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeTeamsStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

// ─── Endpoint tests ───────────────────────────────────────────────────────────

func TestAdminProfilesGateAndRoundTrip(t *testing.T) {
	srv, _, authSvc := newTeamsAdminTestServer(t)

	// Non-operator account → 403.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/profiles", accountCookie(t, authSvc, "1", "alice"), ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("account GET /admin/profiles: expected 403, got %d", rec.Code)
	}

	// Operator create with over-wide perms → masked to PermAll(15).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/profiles", operatorCookie(t, authSvc), `{"name":"Auditor","perms":255}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create profile: expected 201, got %d body=%q", rec.Code, rec.Body.String())
	}
	var created profileJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Perms != 15 {
		t.Fatalf("perms not masked to PermAll: %+v", created)
	}

	// List includes the seeded defaults + the new one.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/profiles", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list profiles: expected 200, got %d", rec.Code)
	}
	var list []profileJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	names := map[string]int{}
	for _, p := range list {
		names[p.Name] = p.Perms
	}
	if names["Moderator"] != 15 || names["Member"] != 1 || names["Editor"] != 7 || names["Auditor"] != 15 {
		t.Fatalf("unexpected profile list: %+v", names)
	}
}

func TestAdminTeamsFullFlow(t *testing.T) {
	srv, _, authSvc := newTeamsAdminTestServer(t)
	op := func() *http.Cookie { return operatorCookie(t, authSvc) }

	// Create a team.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/teams", op(), `{"name":"Migración","kind":"work"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team: expected 201, got %d body=%q", rec.Code, rec.Body.String())
	}
	var team teamJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &team)
	if team.ID == "" || team.Kind != "work" {
		t.Fatalf("unexpected team: %+v", team)
	}

	// Attach a project.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/teams/"+team.ID+"/projects/mig-proj", op(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("attach project: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Add a member by profile NAME (resolved to id).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/teams/"+team.ID+"/members/worker", op(), `{"profile":"Moderator"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("add member: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// GET the team detail: project attached + member with Moderator perms(15).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/teams/"+team.ID, op(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("get team: expected 200, got %d", rec.Code)
	}
	var detail teamDetailJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if len(detail.Projects) != 1 || detail.Projects[0] != "mig-proj" {
		t.Fatalf("team projects wrong: %+v", detail.Projects)
	}
	if len(detail.Members) != 1 || detail.Members[0].AccountID != "worker" || detail.Members[0].Perms != 15 {
		t.Fatalf("team members wrong: %+v", detail.Members)
	}

	// Unknown profile name → 400.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/teams/"+team.ID+"/members/x", op(), `{"profile":"Nope"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad profile: expected 400, got %d", rec.Code)
	}

	// Delete the team.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodDelete, "/admin/teams/"+team.ID, op(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete team: expected 200, got %d", rec.Code)
	}
}

func TestAdminProjectClassificationAndKnownProjects(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	op := func() *http.Cookie { return operatorCookie(t, authSvc) }

	// Seed a membership + a team project so KnownProjects has union content.
	store.grant("1", "homelab", 1, "member")
	tm, _ := store.CreateTeam(context.Background(), "T", "work")
	_ = store.AddTeamProject(context.Background(), tm.ID, "workly")

	// Classify a project.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPut, "/admin/projects/homelab/meta", op(), `{"kind":"personal","display_name":"Home Lab"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("classify: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// GET /admin/projects: union includes homelab (classified) + workly (team).
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/projects", op(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list projects: expected 200, got %d", rec.Code)
	}
	var known []knownProjectJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &known)
	byName := map[string]knownProjectJSON{}
	for _, k := range known {
		byName[k.Project] = k
	}
	if byName["homelab"].Kind != "personal" {
		t.Fatalf("homelab should be personal: %+v", byName["homelab"])
	}
	if _, ok := byName["workly"]; !ok {
		t.Fatalf("workly (team project) missing from known projects: %+v", known)
	}
}

// ─── Resolver wiring (hot path) ───────────────────────────────────────────────

// stubResolver returns a fixed perms value and records that it was consulted, so we
// can prove AuthorizeAccountProject routes through the OBL-14 resolver rather than
// the flat membership lookup.
type stubResolver struct {
	perms  int
	called bool
}

func (s *stubResolver) EffectivePerms(_ context.Context, _, _ string) (int, error) {
	s.called = true
	return s.perms, nil
}

func TestRBACAuthorizerUsesEffectivePermsResolver(t *testing.T) {
	ms := newFakeMembershipStore()
	// A flat membership that would DENY insert (read-only). The resolver must win.
	ms.grant("acct", "proj", 1, "member")

	res := &stubResolver{perms: int(cloudauth.PermAll)}
	ra := &rbacAuthorizer{store: ms, resolver: res}
	claims := &cloudauth.AccountClaims{AccountID: "acct"}

	if err := ra.AuthorizeAccountProject(context.Background(), claims, "proj", cloudauth.PermInsert); err != nil {
		t.Fatalf("resolver grants insert but authorize denied: %v", err)
	}
	if !res.called {
		t.Fatal("resolver was not consulted — hot path not wired to EffectivePerms")
	}

	// Resolver denies (0) → deny even though a flat membership grants read.
	res2 := &stubResolver{perms: 0}
	ra2 := &rbacAuthorizer{store: ms, resolver: res2}
	if err := ra2.AuthorizeAccountProject(context.Background(), claims, "proj", cloudauth.PermRead); err == nil {
		t.Fatal("resolver denies but authorize allowed — deny-by-default broken")
	}
}

// TestRBACAuthorizerFallsBackToMembership verifies that when no resolver is present
// (a store that does not implement it), the authorizer preserves the flat
// membership-only, deny-by-default behavior.
func TestRBACAuthorizerFallsBackToMembership(t *testing.T) {
	ms := newFakeMembershipStore()
	ms.grant("acct", "proj", int(cloudauth.PermRead), "member")
	ra := &rbacAuthorizer{store: ms} // resolver nil
	claims := &cloudauth.AccountClaims{AccountID: "acct"}

	if err := ra.AuthorizeAccountProject(context.Background(), claims, "proj", cloudauth.PermRead); err != nil {
		t.Fatalf("membership grants read but denied: %v", err)
	}
	if err := ra.AuthorizeAccountProject(context.Background(), claims, "proj", cloudauth.PermInsert); err == nil {
		t.Fatal("membership is read-only but insert allowed")
	}
	if err := ra.AuthorizeAccountProject(context.Background(), claims, "other", cloudauth.PermRead); err == nil {
		t.Fatal("no membership on 'other' but read allowed — deny-by-default broken")
	}
}
