package cloudserver

import (
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── accessEffectiveLabel ─────────────────────────────────────────────────────

func TestAccessEffectiveLabel(t *testing.T) {
	cases := []struct {
		name  string
		perms int
		want  string
	}{
		{"zero is None", 0, "None"},
		{"read-only is Read", int(cloudauth.PermRead), "Read"},
		{"all bits is Full", int(cloudauth.PermAll), "Full"},
		{"read+update is Partial", int(cloudauth.PermRead | cloudauth.PermUpdate), "Partial"},
		{"write-only (no read) is Partial", int(cloudauth.PermInsert), "Partial"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := accessEffectiveLabel(c.perms); got != c.want {
				t.Fatalf("accessEffectiveLabel(%d) = %q, want %q", c.perms, got, c.want)
			}
		})
	}
}

// ─── mergeUnifiedAccessRows ───────────────────────────────────────────────────

// TestMergeUnifiedAccessRows_OverrideWinsOverTeam verifies the core precedence
// rule: an override REPLACES the team-derived union outright, mirroring
// cloudstore.EffectivePerms — the merged row must show the override's perms,
// role and "override" source, never a blend with the team data.
func TestMergeUnifiedAccessRows_OverrideWinsOverTeam(t *testing.T) {
	teamRows := []adminTeamPermRow{
		{
			Project: "trackly",
			Perms:   int(cloudauth.PermRead | cloudauth.PermInsert | cloudauth.PermUpdate), // Editor profile, no delete
			Sources: []adminTeamPermSource{{Team: "velion", Profile: "Editor"}},
		},
	}
	overrides := []cloudstore.Membership{
		{AccountID: "1", Project: "trackly", Perms: int(cloudauth.PermAll), Role: "owner"},
	}

	rows := mergeUnifiedAccessRows("1", overrides, teamRows, nil)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Source != "override" {
		t.Fatalf("expected source=override, got %q", row.Source)
	}
	if row.Perms != int(cloudauth.PermAll) {
		t.Fatalf("expected override perms (PermAll), got %d", row.Perms)
	}
	if row.Label != "Full" {
		t.Fatalf("expected label=Full, got %q", row.Label)
	}
	if row.Role != "owner" {
		t.Fatalf("expected role=owner, got %q", row.Role)
	}
	if row.TeamSummary != "" || row.TeamProfile != "" {
		t.Fatalf("override row must not carry team detail, got summary=%q profile=%q", row.TeamSummary, row.TeamProfile)
	}
	if !row.Read || !row.Insert || !row.Update || !row.Delete {
		t.Fatalf("expected all RWUD bits set for a Full override, got %+v", row)
	}
}

// TestMergeUnifiedAccessRows_OverrideCanRestrictToZero verifies an override of
// 0 (explicit deny) still wins over a non-zero team union — the override
// layer can restrict, not just elevate.
func TestMergeUnifiedAccessRows_OverrideCanRestrictToZero(t *testing.T) {
	teamRows := []adminTeamPermRow{
		{Project: "trackly", Perms: int(cloudauth.PermAll), Sources: []adminTeamPermSource{{Team: "velion", Profile: "Moderator"}}},
	}
	overrides := []cloudstore.Membership{
		{AccountID: "1", Project: "trackly", Perms: 0, Role: "member"},
	}

	rows := mergeUnifiedAccessRows("1", overrides, teamRows, nil)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.Source != "override" {
		t.Fatalf("expected source=override even at zero perms, got %q", row.Source)
	}
	if row.Label != "None" {
		t.Fatalf("expected label=None, got %q", row.Label)
	}
	if row.Read || row.Insert || row.Update || row.Delete {
		t.Fatalf("expected zero perms, got %+v", row)
	}
}

// TestMergeUnifiedAccessRows_TeamOnly verifies a project reachable only
// through team membership (no override) shows source=team with the team's
// union perms and a single contributing team's name + profile.
func TestMergeUnifiedAccessRows_TeamOnly(t *testing.T) {
	teamRows := []adminTeamPermRow{
		{
			Project: "socrates",
			Perms:   int(cloudauth.PermRead),
			Sources: []adminTeamPermSource{{Team: "velion", Profile: "Member"}},
		},
	}

	rows := mergeUnifiedAccessRows("1", nil, teamRows, nil)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.Source != "team" {
		t.Fatalf("expected source=team, got %q", row.Source)
	}
	if row.Label != "Read" {
		t.Fatalf("expected label=Read, got %q", row.Label)
	}
	if row.TeamSummary != "velion" || row.TeamProfile != "Member" {
		t.Fatalf("expected team=velion profile=Member, got summary=%q profile=%q", row.TeamSummary, row.TeamProfile)
	}
	if row.Role != "" {
		t.Fatalf("team-only row must not carry a role, got %q", row.Role)
	}
}

// TestMergeUnifiedAccessRows_TeamUnionMultipleSources verifies that when
// SEVERAL teams contribute to the same project, the row joins every
// contributing team's name and leaves TeamProfile blank (no single profile
// applies to a union of teams).
func TestMergeUnifiedAccessRows_TeamUnionMultipleSources(t *testing.T) {
	teamRows := []adminTeamPermRow{
		{
			Project: "socrates",
			Perms:   int(cloudauth.PermAll),
			Sources: []adminTeamPermSource{
				{Team: "velion", Profile: "Member"},
				{Team: "migracion-axoft", Profile: "Moderator"},
			},
		},
	}

	rows := mergeUnifiedAccessRows("1", nil, teamRows, nil)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.TeamSummary != "velion + migracion-axoft" {
		t.Fatalf("expected joined team names, got %q", row.TeamSummary)
	}
	if row.TeamProfile != "" {
		t.Fatalf("a multi-team union must not show a single profile, got %q", row.TeamProfile)
	}
}

// TestMergeUnifiedAccessRows_NoneRow verifies a project the account touches
// through neither an override nor a team still gets a row (from the
// known-project universe) with source=none and zero perms — this is what lets
// the operator see "no access yet" for a project and grant it in place.
func TestMergeUnifiedAccessRows_NoneRow(t *testing.T) {
	rows := mergeUnifiedAccessRows("1", nil, nil, []string{"workly"})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Source != "none" {
		t.Fatalf("expected source=none, got %q", row.Source)
	}
	if row.Label != "None" {
		t.Fatalf("expected label=None, got %q", row.Label)
	}
	if row.Perms != 0 || row.Read || row.Insert || row.Update || row.Delete {
		t.Fatalf("expected zero perms/bits, got %+v", row)
	}
	if row.EditURL == "" || row.ViewURL == "" || row.RevokeConfirmURL == "" {
		t.Fatalf("expected every row to carry its fragment URLs, got %+v", row)
	}
}

// TestMergeUnifiedAccessRows_SourceAttribution is a combined scenario
// exercising all three sources at once plus stable, sorted ordering by
// project name.
func TestMergeUnifiedAccessRows_SourceAttribution(t *testing.T) {
	teamRows := []adminTeamPermRow{
		{Project: "trackly", Perms: int(cloudauth.PermRead), Sources: []adminTeamPermSource{{Team: "velion", Profile: "Member"}}},
	}
	overrides := []cloudstore.Membership{
		{AccountID: "1", Project: "intervus", Perms: int(cloudauth.PermAll), Role: "owner"},
	}
	known := []string{"workly", "intervus", "trackly"}

	rows := mergeUnifiedAccessRows("1", overrides, teamRows, known)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
	// Sorted alphabetically: intervus, trackly, workly.
	if rows[0].Project != "intervus" || rows[1].Project != "trackly" || rows[2].Project != "workly" {
		t.Fatalf("expected sorted project order, got %q, %q, %q", rows[0].Project, rows[1].Project, rows[2].Project)
	}
	if rows[0].Source != "override" {
		t.Fatalf("intervus: expected source=override, got %q", rows[0].Source)
	}
	if rows[1].Source != "team" {
		t.Fatalf("trackly: expected source=team, got %q", rows[1].Source)
	}
	if rows[2].Source != "none" {
		t.Fatalf("workly: expected source=none, got %q", rows[2].Source)
	}
}

// TestAccessRowSlugIsDOMSafe verifies project names with characters unsafe in
// an HTML id attribute (spaces, dots, slashes) collapse to a safe slug rather
// than being embedded raw.
func TestAccessRowSlugIsDOMSafe(t *testing.T) {
	cases := map[string]string{
		"lab":              "lab",
		"workly-marketing": "workly-marketing",
		"my project":       "my-project",
		"a.b/c":            "a-b-c",
		"":                 "project",
	}
	for in, want := range cases {
		if got := accessRowSlug(in); got != want {
			t.Fatalf("accessRowSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAccessRowFormRole verifies the edit-form default role: the override's
// own role when present, otherwise "member" (matching the old standalone
// add-override form's default).
func TestAccessRowFormRole(t *testing.T) {
	if got := accessRowFormRole(unifiedAccessRow{Source: "override", Role: "moderator"}); got != "moderator" {
		t.Fatalf("expected moderator, got %q", got)
	}
	if got := accessRowFormRole(unifiedAccessRow{Source: "team"}); got != cloudauth.RoleMember {
		t.Fatalf("expected default role member, got %q", got)
	}
	if got := accessRowFormRole(unifiedAccessRow{Source: "none"}); got != cloudauth.RoleMember {
		t.Fatalf("expected default role member, got %q", got)
	}
}

// TestAccountComboLabel verifies the account picker's closed-state label.
func TestAccountComboLabel(t *testing.T) {
	if got := accountComboLabel("ale", "ale@velion.lab"); got != "ale (ale@velion.lab)" {
		t.Fatalf("unexpected label: %q", got)
	}
	if got := accountComboLabel("ale", ""); got != "ale" {
		t.Fatalf("unexpected label with no email: %q", got)
	}
}
