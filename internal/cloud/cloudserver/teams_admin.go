package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Velion-SpA/omnia/internal/cloud/auth"
	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
)

// Operator-only teams + profiles + project-classification endpoints (OBL-14).
// These extend the OBL-13 Admin section: every handler gates on requireOperator
// (the UI is never trusted) and the routes register only when the store supports
// team administration (teamsAdminStore), detected via type assertion the same way
// adminDashboardStore is. They are the data plane the OBL-15 UI consumes; no HTML
// is rendered here. cloud_memberships remains the per-project OVERRIDE layer and is
// managed by the existing OBL-13 membership endpoints — untouched here.

// teamsAdminStore is the teams/profiles slice of the store. *cloudstore.CloudStore
// satisfies it; the compile-time assertion below keeps it honest.
type teamsAdminStore interface {
	// Profiles
	CreateProfile(ctx context.Context, name string, perms int) (*cloudstore.Profile, error)
	ListProfiles(ctx context.Context) ([]cloudstore.Profile, error)
	GetProfile(ctx context.Context, id string) (*cloudstore.Profile, error)
	UpdateProfile(ctx context.Context, id, name string, perms int) error
	DeleteProfile(ctx context.Context, id string) error
	// Teams
	CreateTeam(ctx context.Context, name, kind string) (*cloudstore.Team, error)
	ListTeams(ctx context.Context) ([]cloudstore.Team, error)
	GetTeam(ctx context.Context, id string) (*cloudstore.Team, error)
	UpdateTeam(ctx context.Context, id, name, kind string) error
	DeleteTeam(ctx context.Context, id string) error
	// Team ↔ projects
	AddTeamProject(ctx context.Context, teamID, project string) error
	RemoveTeamProject(ctx context.Context, teamID, project string) error
	ListProjectsForTeam(ctx context.Context, teamID string) ([]string, error)
	// Team ↔ members
	AddTeamMember(ctx context.Context, teamID, accountID, profileID string) error
	RemoveTeamMember(ctx context.Context, teamID, accountID string) error
	ListMembersOfTeam(ctx context.Context, teamID string) ([]cloudstore.TeamMember, error)
	// Project classification + known projects
	UpsertProjectMeta(ctx context.Context, project, kind, displayName string) error
	GetProjectMeta(ctx context.Context, project string) (*cloudstore.ProjectMeta, error)
	KnownProjects(ctx context.Context) ([]cloudstore.KnownProject, error)
}

// Compile-time assertion: the concrete store must satisfy the teams admin seam.
var _ teamsAdminStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) teamsStore() (teamsAdminStore, bool) {
	ts, ok := s.store.(teamsAdminStore)
	return ts, ok
}

// requireTeamsAdmin combines the operator gate with the store-capability check so
// every teams handler starts identically. On failure it has written the response
// and returns (nil, false).
func (s *CloudServer) requireTeamsAdmin(w http.ResponseWriter, r *http.Request) (teamsAdminStore, bool) {
	if !s.requireOperator(w, r) {
		return nil, false
	}
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return nil, false
	}
	return ts, true
}

// ─── JSON shapes ─────────────────────────────────────────────────────────────

type profileJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Perms int    `json:"perms"`
}

type teamJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type teamMemberJSON struct {
	AccountID   string `json:"account_id"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile,omitempty"`
	Perms       int    `json:"perms"`
}

type teamDetailJSON struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Kind     string           `json:"kind"`
	Projects []string         `json:"projects"`
	Members  []teamMemberJSON `json:"members"`
}

type knownProjectJSON struct {
	Project     string `json:"project"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
}

func toProfileJSON(p cloudstore.Profile) profileJSON {
	return profileJSON{ID: p.ID, Name: p.Name, Perms: p.Perms}
}

func toTeamJSON(t cloudstore.Team) teamJSON {
	return teamJSON{ID: t.ID, Name: t.Name, Kind: t.Kind}
}

// ─── Profiles ────────────────────────────────────────────────────────────────

// handleAdminListProfiles handles GET /admin/profiles.
func (s *CloudServer) handleAdminListProfiles(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	profiles, err := ts.ListProfiles(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list profiles"})
		return
	}
	out := make([]profileJSON, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, toProfileJSON(p))
	}
	jsonResponse(w, http.StatusOK, out)
}

type profileInput struct {
	Name  string `json:"name"`
	Perms int    `json:"perms"`
}

// handleAdminCreateProfile handles PUT /admin/profiles — creates a new preset.
func (s *CloudServer) handleAdminCreateProfile(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	var in profileInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	perms := in.Perms & int(auth.PermAll)
	p, err := ts.CreateProfile(r.Context(), name, perms)
	if err != nil {
		if errors.Is(err, cloudstore.ErrProfileNameTaken) {
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "profile name already exists"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not create profile"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusCreated, toProfileJSON(*p))
}

// handleAdminUpdateProfile handles PUT /admin/profiles/{id} — renames/re-scopes.
func (s *CloudServer) handleAdminUpdateProfile(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "profile id is required"})
		return
	}
	var in profileInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	perms := in.Perms & int(auth.PermAll)
	if err := ts.UpdateProfile(r.Context(), id, name, perms); err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrProfileNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "profile not found"})
		case errors.Is(err, cloudstore.ErrProfileNameTaken):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "profile name already exists"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update profile"})
		}
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, profileJSON{ID: id, Name: name, Perms: perms})
}

// handleAdminDeleteProfile handles DELETE /admin/profiles/{id}.
func (s *CloudServer) handleAdminDeleteProfile(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "profile id is required"})
		return
	}
	if err := ts.DeleteProfile(r.Context(), id); err != nil {
		// A profile still referenced by a team member cannot be dropped (FK); surface
		// a clean 409 so the operator reassigns members first.
		jsonResponse(w, http.StatusConflict, map[string]string{"error": "could not delete profile (still in use?)"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// ─── Teams ───────────────────────────────────────────────────────────────────

// handleAdminListTeams handles GET /admin/teams.
func (s *CloudServer) handleAdminListTeams(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	teams, err := ts.ListTeams(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list teams"})
		return
	}
	out := make([]teamJSON, 0, len(teams))
	for _, t := range teams {
		out = append(out, toTeamJSON(t))
	}
	jsonResponse(w, http.StatusOK, out)
}

// handleAdminGetTeam handles GET /admin/teams/{id} — a team with its projects and
// members (each member joined with its profile name + perms).
func (s *CloudServer) handleAdminGetTeam(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id is required"})
		return
	}
	team, err := ts.GetTeam(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not load team"})
		return
	}
	if team == nil {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "team not found"})
		return
	}
	projects, err := ts.ListProjectsForTeam(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list team projects"})
		return
	}
	members, err := ts.ListMembersOfTeam(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list team members"})
		return
	}
	memOut := make([]teamMemberJSON, 0, len(members))
	for _, m := range members {
		memOut = append(memOut, teamMemberJSON{AccountID: m.AccountID, ProfileID: m.ProfileID, ProfileName: m.ProfileName, Perms: m.Perms})
	}
	if projects == nil {
		projects = []string{}
	}
	jsonResponse(w, http.StatusOK, teamDetailJSON{
		ID:       team.ID,
		Name:     team.Name,
		Kind:     team.Kind,
		Projects: projects,
		Members:  memOut,
	})
}

type teamInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// handleAdminCreateTeam handles PUT /admin/teams — creates a team.
func (s *CloudServer) handleAdminCreateTeam(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	var in teamInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	team, err := ts.CreateTeam(r.Context(), name, in.Kind)
	if err != nil {
		if errors.Is(err, cloudstore.ErrTeamNameTaken) {
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "team name already exists"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not create team"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusCreated, toTeamJSON(*team))
}

// handleAdminUpdateTeam handles PUT /admin/teams/{id} — renames/re-classifies.
func (s *CloudServer) handleAdminUpdateTeam(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id is required"})
		return
	}
	var in teamInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := ts.UpdateTeam(r.Context(), id, name, in.Kind); err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrTeamNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "team not found"})
		case errors.Is(err, cloudstore.ErrTeamNameTaken):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "team name already exists"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update team"})
		}
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, teamJSON{ID: id, Name: name, Kind: normalizeKind(in.Kind)})
}

// handleAdminDeleteTeam handles DELETE /admin/teams/{id} — cascades projects+members.
func (s *CloudServer) handleAdminDeleteTeam(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id is required"})
		return
	}
	if err := ts.DeleteTeam(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not delete team"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// ─── Team ↔ projects ─────────────────────────────────────────────────────────

// handleAdminAddTeamProject handles PUT /admin/teams/{id}/projects/{project}.
func (s *CloudServer) handleAdminAddTeamProject(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	teamID := strings.TrimSpace(r.PathValue("id"))
	project := strings.TrimSpace(r.PathValue("project"))
	if teamID == "" || project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id and project are required"})
		return
	}
	if err := ts.AddTeamProject(r.Context(), teamID, project); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not attach project"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "attached", "team_id": teamID, "project": project})
}

// handleAdminRemoveTeamProject handles DELETE /admin/teams/{id}/projects/{project}.
func (s *CloudServer) handleAdminRemoveTeamProject(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	teamID := strings.TrimSpace(r.PathValue("id"))
	project := strings.TrimSpace(r.PathValue("project"))
	if teamID == "" || project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id and project are required"})
		return
	}
	if err := ts.RemoveTeamProject(r.Context(), teamID, project); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not detach project"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "detached", "team_id": teamID, "project": project})
}

// ─── Team ↔ members ──────────────────────────────────────────────────────────

type teamMemberInput struct {
	ProfileID string `json:"profile_id"`
	Profile   string `json:"profile"` // profile name; resolved when profile_id is absent
}

// handleAdminAddTeamMember handles PUT /admin/teams/{id}/members/{account_id}. The
// body carries the profile (by id or name) that sets the member's permission level.
func (s *CloudServer) handleAdminAddTeamMember(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	teamID := strings.TrimSpace(r.PathValue("id"))
	accountID := strings.TrimSpace(r.PathValue("account_id"))
	if teamID == "" || accountID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id and account_id are required"})
		return
	}
	var in teamMemberInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	profileID, err := s.resolveProfileID(r.Context(), ts, in)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ts.AddTeamMember(r.Context(), teamID, accountID, profileID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not add member"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, teamMemberJSON{AccountID: accountID, ProfileID: profileID})
}

// handleAdminRemoveTeamMember handles DELETE /admin/teams/{id}/members/{account_id}.
func (s *CloudServer) handleAdminRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	teamID := strings.TrimSpace(r.PathValue("id"))
	accountID := strings.TrimSpace(r.PathValue("account_id"))
	if teamID == "" || accountID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "team id and account_id are required"})
		return
	}
	if err := ts.RemoveTeamMember(r.Context(), teamID, accountID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not remove member"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "removed", "team_id": teamID, "account_id": accountID})
}

// resolveProfileID maps a member input to a concrete profile id. profile_id wins;
// otherwise a profile name is resolved against the profile table. An empty input is
// rejected — a team member without a profile grants no perms, so the operator must
// pick one explicitly.
func (s *CloudServer) resolveProfileID(ctx context.Context, ts teamsAdminStore, in teamMemberInput) (string, error) {
	if id := strings.TrimSpace(in.ProfileID); id != "" {
		p, err := ts.GetProfile(ctx, id)
		if err != nil {
			return "", errors.New("could not resolve profile")
		}
		if p == nil {
			return "", errors.New("profile not found")
		}
		return p.ID, nil
	}
	name := strings.TrimSpace(in.Profile)
	if name == "" {
		return "", errors.New("profile is required")
	}
	profiles, err := ts.ListProfiles(ctx)
	if err != nil {
		return "", errors.New("could not resolve profile")
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Name, name) {
			return p.ID, nil
		}
	}
	return "", errors.New("profile not found")
}

// ─── Project classification + known projects ─────────────────────────────────

type projectMetaInput struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
}

// handleAdminSetProjectMeta handles PUT /admin/projects/{project}/meta — classifies
// a project personal/work with an optional display name.
func (s *CloudServer) handleAdminSetProjectMeta(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}
	var in projectMetaInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := ts.UpsertProjectMeta(r.Context(), project, in.Kind, in.DisplayName); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not classify project"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, knownProjectJSON{
		Project:     project,
		Kind:        normalizeKind(in.Kind),
		DisplayName: strings.TrimSpace(in.DisplayName),
	})
}

// handleAdminListProjects handles GET /admin/projects — the known-projects list
// (distinct across chunks ∪ memberships ∪ team projects ∪ classification) with each
// project's classification, backing the OBL-15 searchable selector.
func (s *CloudServer) handleAdminListProjects(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.requireTeamsAdmin(w, r)
	if !ok {
		return
	}
	projects, err := ts.KnownProjects(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list projects"})
		return
	}
	out := make([]knownProjectJSON, 0, len(projects))
	for _, p := range projects {
		out = append(out, knownProjectJSON{Project: p.Project, Kind: p.Kind, DisplayName: p.DisplayName})
	}
	jsonResponse(w, http.StatusOK, out)
}

// ─── shared helpers ──────────────────────────────────────────────────────────

// decodeJSONBody reads a JSON body under the shared auth body-size cap. An empty
// body decodes to the zero value (so a body-less PUT with all path params is fine).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// normalizeKind mirrors the store's personal/work normalization for echoing the
// classification back in a mutation response (the store is the source of truth).
func normalizeKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "personal") {
		return "personal"
	}
	return "work"
}
