package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Teams group projects and classify them personal/work (OBL-14). A team member
// (team, account, profile) is granted the profile's perms on every project in the
// team. Multiple teams UNION (bit_or of the profiles' perms) where they overlap.
// The per-project cloud_memberships row remains the OVERRIDE layer that takes
// precedence over the team-derived union — see EffectivePerms.

// ErrTeamNotFound is returned when a team id does not resolve.
var ErrTeamNotFound = errors.New("cloudstore: team not found")

// ErrTeamNameTaken is returned when a create/update collides with an existing team
// name (the UNIQUE(name) constraint).
var ErrTeamNameTaken = errors.New("cloudstore: team name already exists")

// Team is a group of projects with a personal/work classification.
type Team struct {
	ID   string
	Name string
	Kind string
}

// TeamMember is a member row joined with its profile's name + perms.
type TeamMember struct {
	AccountID   string
	ProfileID   string
	ProfileName string
	Perms       int
}

// ProjectMeta is the per-project personal/work classification, independent of any
// team so a project can be classified even with no team.
type ProjectMeta struct {
	Project     string
	Kind        string
	DisplayName string
}

// KnownProject is a distinct project name known to the server (across chunks,
// memberships, team projects and classification) plus its classification, if any.
// Kind is empty when the project has not been classified yet.
type KnownProject struct {
	Project     string
	Kind        string
	DisplayName string
}

// normalizeTeamKind constrains the kind to the two supported buckets, defaulting to
// "work". Any unrecognized value collapses to "work" so a team is never left in an
// undefined classification.
func normalizeTeamKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "personal":
		return "personal"
	default:
		return "work"
	}
}

// ─── Teams CRUD ──────────────────────────────────────────────────────────────

// CreateTeam inserts a new team. A duplicate name maps to ErrTeamNameTaken.
func (cs *CloudStore) CreateTeam(ctx context.Context, name, kind string) (*Team, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("cloudstore: team name is required")
	}
	kind = normalizeTeamKind(kind)
	const q = `INSERT INTO cloud_teams (name, kind) VALUES ($1, $2) RETURNING id::text, name, kind`
	var tm Team
	if err := cs.db.QueryRowContext(ctx, q, name, kind).Scan(&tm.ID, &tm.Name, &tm.Kind); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTeamNameTaken
		}
		return nil, fmt.Errorf("cloudstore: create team: %w", err)
	}
	return &tm, nil
}

// ListTeams returns every team, ordered by name.
func (cs *CloudStore) ListTeams(ctx context.Context) ([]Team, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, name, kind FROM cloud_teams ORDER BY name ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list teams: %w", err)
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var tm Team
		if err := rows.Scan(&tm.ID, &tm.Name, &tm.Kind); err != nil {
			return nil, fmt.Errorf("cloudstore: scan team: %w", err)
		}
		out = append(out, tm)
	}
	return out, rows.Err()
}

// GetTeam returns the team for id, or nil when absent.
func (cs *CloudStore) GetTeam(ctx context.Context, id string) (*Team, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("cloudstore: team id is required")
	}
	const q = `SELECT id::text, name, kind FROM cloud_teams WHERE id = $1::bigint`
	var tm Team
	err := cs.db.QueryRowContext(ctx, q, id).Scan(&tm.ID, &tm.Name, &tm.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get team: %w", err)
	}
	return &tm, nil
}

// UpdateTeam renames and/or re-classifies a team. Returns ErrTeamNotFound when the
// id does not exist and ErrTeamNameTaken on a name collision.
func (cs *CloudStore) UpdateTeam(ctx context.Context, id, name, kind string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return fmt.Errorf("cloudstore: team id is required")
	}
	if name == "" {
		return fmt.Errorf("cloudstore: team name is required")
	}
	kind = normalizeTeamKind(kind)
	res, err := cs.db.ExecContext(ctx, `UPDATE cloud_teams SET name = $2, kind = $3 WHERE id = $1::bigint`, id, name, kind)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrTeamNameTaken
		}
		return fmt.Errorf("cloudstore: update team: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTeamNotFound
	}
	return nil
}

// DeleteTeam removes a team; the FK ON DELETE CASCADE drops its team_projects and
// team_members rows. Idempotent for an absent id (no-op).
func (cs *CloudStore) DeleteTeam(ctx context.Context, id string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("cloudstore: team id is required")
	}
	if _, err := cs.db.ExecContext(ctx, `DELETE FROM cloud_teams WHERE id = $1::bigint`, id); err != nil {
		return fmt.Errorf("cloudstore: delete team: %w", err)
	}
	return nil
}

// ─── Team ↔ projects ─────────────────────────────────────────────────────────

// AddTeamProject attaches a project to a team. Idempotent (ON CONFLICT DO NOTHING).
func (cs *CloudStore) AddTeamProject(ctx context.Context, teamID, project string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	teamID = strings.TrimSpace(teamID)
	project = strings.TrimSpace(project)
	if teamID == "" {
		return fmt.Errorf("cloudstore: team id is required")
	}
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	const q = `INSERT INTO cloud_team_projects (team_id, project) VALUES ($1::bigint, $2) ON CONFLICT (team_id, project) DO NOTHING`
	if _, err := cs.db.ExecContext(ctx, q, teamID, project); err != nil {
		return fmt.Errorf("cloudstore: add team project: %w", err)
	}
	return nil
}

// RemoveTeamProject detaches a project from a team. Idempotent (no-op if absent).
func (cs *CloudStore) RemoveTeamProject(ctx context.Context, teamID, project string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	const q = `DELETE FROM cloud_team_projects WHERE team_id = $1::bigint AND project = $2`
	if _, err := cs.db.ExecContext(ctx, q, strings.TrimSpace(teamID), strings.TrimSpace(project)); err != nil {
		return fmt.Errorf("cloudstore: remove team project: %w", err)
	}
	return nil
}

// ListProjectsForTeam returns the projects attached to a team, ordered by name.
func (cs *CloudStore) ListProjectsForTeam(ctx context.Context, teamID string) ([]string, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT project FROM cloud_team_projects WHERE team_id = $1::bigint ORDER BY project ASC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(teamID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list projects for team: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("cloudstore: scan team project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─── Team ↔ members ──────────────────────────────────────────────────────────

// AddTeamMember adds or updates an account's membership in a team with the given
// profile. profileID may be empty (NULL) — such a member contributes 0 perms until
// a profile is assigned. Upsert on (team_id, account_id) so re-adding changes the
// profile (the "change a user's profile" acceptance path).
func (cs *CloudStore) AddTeamMember(ctx context.Context, teamID, accountID, profileID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	teamID = strings.TrimSpace(teamID)
	accountID = strings.TrimSpace(accountID)
	profileID = strings.TrimSpace(profileID)
	if teamID == "" {
		return fmt.Errorf("cloudstore: team id is required")
	}
	if accountID == "" {
		return fmt.Errorf("cloudstore: account_id is required")
	}
	var profileArg any
	if profileID == "" {
		profileArg = nil
	} else {
		profileArg = profileID
	}
	const q = `
		INSERT INTO cloud_team_members (team_id, account_id, profile_id)
		VALUES ($1::bigint, $2, $3::bigint)
		ON CONFLICT (team_id, account_id) DO UPDATE SET profile_id = EXCLUDED.profile_id`
	if _, err := cs.db.ExecContext(ctx, q, teamID, accountID, profileArg); err != nil {
		return fmt.Errorf("cloudstore: add team member: %w", err)
	}
	return nil
}

// RemoveTeamMember removes an account from a team. Idempotent (no-op if absent).
func (cs *CloudStore) RemoveTeamMember(ctx context.Context, teamID, accountID string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	const q = `DELETE FROM cloud_team_members WHERE team_id = $1::bigint AND account_id = $2`
	if _, err := cs.db.ExecContext(ctx, q, strings.TrimSpace(teamID), strings.TrimSpace(accountID)); err != nil {
		return fmt.Errorf("cloudstore: remove team member: %w", err)
	}
	return nil
}

// ListMembersOfTeam returns the accounts in a team joined with each member's
// profile name + perms, ordered by account id.
func (cs *CloudStore) ListMembersOfTeam(ctx context.Context, teamID string) ([]TeamMember, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT tm.account_id,
		       COALESCE(tm.profile_id::text, ''),
		       COALESCE(p.name, ''),
		       COALESCE(p.perms, 0)
		FROM cloud_team_members tm
		LEFT JOIN cloud_profiles p ON p.id = tm.profile_id
		WHERE tm.team_id = $1::bigint
		ORDER BY tm.account_id ASC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(teamID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list members of team: %w", err)
	}
	defer rows.Close()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.AccountID, &m.ProfileID, &m.ProfileName, &m.Perms); err != nil {
			return nil, fmt.Errorf("cloudstore: scan team member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListTeamsForAccount returns every team the account belongs to, ordered by name.
func (cs *CloudStore) ListTeamsForAccount(ctx context.Context, accountID string) ([]Team, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT t.id::text, t.name, t.kind
		FROM cloud_teams t
		JOIN cloud_team_members tm ON tm.team_id = t.id
		WHERE tm.account_id = $1
		ORDER BY t.name ASC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(accountID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list teams for account: %w", err)
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var tm Team
		if err := rows.Scan(&tm.ID, &tm.Name, &tm.Kind); err != nil {
			return nil, fmt.Errorf("cloudstore: scan account team: %w", err)
		}
		out = append(out, tm)
	}
	return out, rows.Err()
}

// ─── Project classification ──────────────────────────────────────────────────

// UpsertProjectMeta sets a project's personal/work classification and optional
// display name. Independent of teams — a project can be classified with no team.
func (cs *CloudStore) UpsertProjectMeta(ctx context.Context, project, kind, displayName string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	kind = normalizeTeamKind(kind)
	displayName = strings.TrimSpace(displayName)
	var displayArg any
	if displayName == "" {
		displayArg = nil
	} else {
		displayArg = displayName
	}
	const q = `
		INSERT INTO cloud_project_meta (project, kind, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (project) DO UPDATE SET kind = EXCLUDED.kind, display_name = EXCLUDED.display_name`
	if _, err := cs.db.ExecContext(ctx, q, project, kind, displayArg); err != nil {
		return fmt.Errorf("cloudstore: upsert project meta: %w", err)
	}
	return nil
}

// GetProjectMeta returns the classification for a project, or nil when absent.
func (cs *CloudStore) GetProjectMeta(ctx context.Context, project string) (*ProjectMeta, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	const q = `SELECT project, kind, COALESCE(display_name, '') FROM cloud_project_meta WHERE project = $1`
	var m ProjectMeta
	err := cs.db.QueryRowContext(ctx, q, project).Scan(&m.Project, &m.Kind, &m.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get project meta: %w", err)
	}
	return &m, nil
}

// KnownProjects returns the distinct project names the server knows about — the
// union of cloud_chunks, cloud_memberships, cloud_team_projects and
// cloud_project_meta — each joined with its classification (empty Kind when not yet
// classified). It backs the OBL-15 searchable project selector; it is NOT on the
// auth hot path.
func (cs *CloudStore) KnownProjects(ctx context.Context) ([]KnownProject, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		WITH known AS (
			SELECT DISTINCT project_name AS project FROM cloud_chunks
			UNION
			SELECT DISTINCT project FROM cloud_memberships
			UNION
			SELECT DISTINCT project FROM cloud_team_projects
			UNION
			SELECT DISTINCT project FROM cloud_project_meta
		)
		SELECT k.project, COALESCE(m.kind, ''), COALESCE(m.display_name, '')
		FROM known k
		LEFT JOIN cloud_project_meta m ON m.project = k.project
		WHERE btrim(k.project) <> ''
		ORDER BY k.project ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list known projects: %w", err)
	}
	defer rows.Close()
	var out []KnownProject
	for rows.Next() {
		var kp KnownProject
		if err := rows.Scan(&kp.Project, &kp.Kind, &kp.DisplayName); err != nil {
			return nil, fmt.Errorf("cloudstore: scan known project: %w", err)
		}
		out = append(out, kp)
	}
	return out, rows.Err()
}

// ─── Effective-perms resolver (auth hot path) ────────────────────────────────

// EffectivePerms resolves the effective permission bitfield for (accountID,
// project) using the layered model (OBL-14):
//
//  1. If a cloud_memberships OVERRIDE row exists, its perms win outright — it
//     REPLACES the team-derived perms (can elevate OR restrict, including to 0).
//  2. Otherwise the perms are the bit_or (UNION) of the profiles' perms across every
//     team the account belongs to that contains the project.
//  3. Otherwise 0 (deny-by-default).
//
// It is a single indexed query: a scalar override lookup COALESCE'd over the team
// union. The override subquery returns NULL only when NO override row exists, so an
// override of 0 (explicit deny) is preserved rather than falling through to teams.
func (cs *CloudStore) EffectivePerms(ctx context.Context, accountID, project string) (int, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	project = strings.TrimSpace(project)
	if accountID == "" || project == "" {
		return 0, nil
	}
	const q = `
		SELECT COALESCE(
			(SELECT perms FROM cloud_memberships WHERE account_id = $1 AND project = $2),
			(SELECT COALESCE(bit_or(p.perms), 0)
			 FROM cloud_team_members tm
			 JOIN cloud_team_projects tp ON tp.team_id = tm.team_id
			 JOIN cloud_profiles p ON p.id = tm.profile_id
			 WHERE tm.account_id = $1 AND tp.project = $2)
		)`
	var perms int
	if err := cs.db.QueryRowContext(ctx, q, accountID, project).Scan(&perms); err != nil {
		return 0, fmt.Errorf("cloudstore: effective perms: %w", err)
	}
	return perms, nil
}
