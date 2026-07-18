package cloudserver

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Command Center v2, Slice 3: the unified Access page view-model. The old page
// rendered the SAME projects twice — an editable per-project OVERRIDE list
// (cloud_memberships) and, below it, a read-only "from teams" section
// (teamDerivedForAccount) — leaving the operator to reconcile the two lists in
// their head. This file MERGES both layers, plus the full known-project
// universe (so a project the account cannot touch at all still gets a row,
// answering "does X have access to workly?"), into ONE row per project. It is
// a pure view-layer projection: every input already comes from an existing
// store call the Access page (or teamDerivedForAccount) already made — no new
// cloudstore query is introduced. The precedence mirrors
// cloudstore.EffectivePerms exactly: an override REPLACES the team-derived
// union outright (it can elevate or restrict, including to zero); with no
// override, the team union applies; with neither, the row is empty.

// unifiedAccessRow is one project's merged effective access for a single
// account: which bits it can touch, a human label for that, and where the
// access comes from (override / team / none) so the operator never has to
// cross-reference two separate lists to answer "why does this account see
// project X?".
type unifiedAccessRow struct {
	AccountID string
	Project   string
	// Slug is a DOM-safe id fragment derived from Project (project names come
	// from operator/sync-supplied data, not guaranteed HTML-id-safe).
	Slug string

	Perms                        int
	Read, Insert, Update, Delete bool
	// Label is the effective permission label: Full (RWUD) | Read (R only) |
	// Partial (any other non-zero combination) | None (zero).
	Label string

	// Source is "override" | "team" | "none".
	Source string
	// Role is the override's role (only set when Source == "override").
	Role string
	// TeamSummary names the contributing team(s) (only set when Source ==
	// "team"): a single team's name, or every contributing team joined with
	// " + " when more than one team's union grants the project.
	TeamSummary string
	// TeamProfile is the contributing profile's name — only set when exactly
	// ONE team contributes (a union of several teams has no single profile to
	// show).
	TeamProfile string

	// Edit-in-place fragment URLs (Slice 3 view-only routes — see
	// handleAdminAccessRowView/-Edit/-RevokeConfirm). Every mutation they
	// eventually trigger still goes through the pre-existing, operator-gated
	// PUT/DELETE /admin/memberships routes.
	ViewURL          string
	EditURL          string
	RevokeConfirmURL string
}

// accessEffectiveLabel renders the human label for a perms bitfield.
func accessEffectiveLabel(perms int) string {
	p := auth.Permission(perms)
	switch {
	case perms == 0:
		return "None"
	case p.Has(auth.PermAll):
		return "Full"
	case perms == int(auth.PermRead):
		return "Read"
	default:
		return "Partial"
	}
}

// accessRowSlug builds a DOM-safe id fragment for a project name: anything
// outside [a-zA-Z0-9_-] becomes '-' rather than being embedded raw in an HTML
// id attribute.
func accessRowSlug(project string) string {
	b := make([]byte, 0, len(project))
	for _, r := range project {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b = append(b, byte(r))
		default:
			b = append(b, '-')
		}
	}
	if len(b) == 0 {
		return "project"
	}
	return string(b)
}

func adminAccessRowViewPath(accountID, project string) string {
	return "/admin/access/rows/" + url.PathEscape(accountID) + "/" + url.PathEscape(project)
}

func adminAccessRowEditPath(accountID, project string) string {
	return adminAccessRowViewPath(accountID, project) + "/edit"
}

func adminAccessRowRevokeConfirmPath(accountID, project string) string {
	return adminAccessRowViewPath(accountID, project) + "/revoke"
}

// teamSourceDetail summarizes the contributing team(s) for a team-derived
// row: a single source names its team + profile directly; a union of several
// teams joins their names (no single profile applies to a union).
func teamSourceDetail(sources []adminTeamPermSource) (teamSummary, profile string) {
	if len(sources) == 0 {
		return "", ""
	}
	if len(sources) == 1 {
		return sources[0].Team, sources[0].Profile
	}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Team)
	}
	return strings.Join(names, " + "), ""
}

// mergeUnifiedAccessRows folds the override layer, the team-derived union,
// and the full known-project universe into ONE row per project, keyed by
// project name. Overrides are applied LAST so they replace any team-derived
// perms already set — the same "override wins outright" precedence
// cloudstore.EffectivePerms enforces at the auth layer. known may include
// projects present in neither overrides nor teamRows; those render as a
// zero-access "None" row.
func mergeUnifiedAccessRows(accountID string, overrides []cloudstore.Membership, teamRows []adminTeamPermRow, known []string) []unifiedAccessRow {
	byProject := map[string]*unifiedAccessRow{}
	var order []string

	ensure := func(project string) *unifiedAccessRow {
		if row, ok := byProject[project]; ok {
			return row
		}
		row := &unifiedAccessRow{Project: project}
		byProject[project] = row
		order = append(order, project)
		return row
	}

	for _, p := range known {
		if strings.TrimSpace(p) == "" {
			continue
		}
		ensure(p)
	}
	for _, t := range teamRows {
		row := ensure(t.Project)
		row.Perms = t.Perms
		row.Source = "team"
		row.TeamSummary, row.TeamProfile = teamSourceDetail(t.Sources)
	}
	for _, m := range overrides {
		row := ensure(m.Project)
		row.Perms = m.Perms
		row.Source = "override"
		row.Role = m.Role
		// An override replaces the team projection entirely — it is not a
		// blend, so clear anything a team loop above may have set.
		row.TeamSummary = ""
		row.TeamProfile = ""
	}

	sort.Strings(order)
	out := make([]unifiedAccessRow, 0, len(order))
	for _, project := range order {
		row := *byProject[project]
		row.AccountID = accountID
		p := auth.Permission(row.Perms)
		row.Read = p.Has(auth.PermRead)
		row.Insert = p.Has(auth.PermInsert)
		row.Update = p.Has(auth.PermUpdate)
		row.Delete = p.Has(auth.PermDelete)
		row.Label = accessEffectiveLabel(row.Perms)
		if row.Source == "" {
			row.Source = "none"
		}
		row.Slug = accessRowSlug(project)
		row.ViewURL = adminAccessRowViewPath(accountID, project)
		row.EditURL = adminAccessRowEditPath(accountID, project)
		row.RevokeConfirmURL = adminAccessRowRevokeConfirmPath(accountID, project)
		out = append(out, row)
	}
	return out
}

// buildUnifiedAccessRows assembles mergeUnifiedAccessRows' inputs for one
// account: its per-project overrides (ListMembershipsForUser), its
// team-derived union (teamDerivedForAccount, unchanged from OBL-15), and the
// full known-project universe (KnownProjects) when the store supports team
// administration. Every call here already exists elsewhere on this page —
// this only recombines their results, so the full Access page and every
// per-row edit/view/revoke-confirm fragment (Slice 3) share ONE source of
// truth and can never drift from each other.
func (s *CloudServer) buildUnifiedAccessRows(ctx context.Context, as adminDashboardStore, accountID string) ([]unifiedAccessRow, error) {
	if accountID == "" {
		return nil, nil
	}
	overrides, err := as.ListMembershipsForUser(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("list memberships for account: %w", err)
	}

	var teamRows []adminTeamPermRow
	var known []string
	if ts, ok := s.teamsStore(); ok {
		personal, work, other := s.teamDerivedForAccount(ctx, ts, accountID, nil)
		teamRows = append(teamRows, personal...)
		teamRows = append(teamRows, work...)
		teamRows = append(teamRows, other...)
		if kp, kerr := ts.KnownProjects(ctx); kerr == nil {
			known = make([]string, 0, len(kp))
			for _, k := range kp {
				known = append(known, k.Project)
			}
		}
	}

	return mergeUnifiedAccessRows(accountID, overrides, teamRows, known), nil
}

// accessRowFormRole is the edit-form's default role select value: the
// override's own role when one exists, otherwise "member" — the same default
// the old standalone "add override" form used.
func accessRowFormRole(row unifiedAccessRow) string {
	if row.Role != "" {
		return row.Role
	}
	return auth.RoleMember
}

// accessProjectCountLabel renders the "N projects" summary in the Access
// table header.
func accessProjectCountLabel(rows []unifiedAccessRow) string {
	word := "projects"
	if len(rows) == 1 {
		word = "project"
	}
	return fmt.Sprintf("%d %s", len(rows), word)
}

// accountComboLabel renders the searchable account combobox's closed-state
// value: "username (email)", or just "username" when no email is on file.
func accountComboLabel(username, email string) string {
	if strings.TrimSpace(email) == "" {
		return username
	}
	return username + " (" + email + ")"
}
