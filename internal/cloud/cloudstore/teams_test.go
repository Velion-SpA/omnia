package cloudstore

import (
	"context"
	"errors"
	"testing"
)

// Perms bits used across these tests (mirrors internal/cloud/auth/permission.go;
// cloudstore must not import auth). permRead/permInsert/permAll are already defined
// in admin_test.go within this package.
const (
	permUpdate = 4
	permEditor = 7  // Read+Insert+Update
	permMod    = 15 // full CRUD
)

// TestProfilesCRUDIntegration exercises the profile preset lifecycle against a live
// Postgres, including the idempotent seed of the default presets.
func TestProfilesCRUDIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	// Seeded defaults must exist idempotently (migrate ran on New).
	profiles, err := cs.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	seeded := map[string]int{}
	for _, p := range profiles {
		seeded[p.Name] = p.Perms
	}
	if seeded["Moderator"] != permMod || seeded["Editor"] != permEditor || seeded["Member"] != permRead {
		t.Fatalf("default profiles not seeded correctly: %+v", seeded)
	}

	// Create a custom profile.
	p, err := cs.CreateProfile(ctx, "Viewer", permRead)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if p.ID == "" || p.Name != "Viewer" || p.Perms != permRead {
		t.Fatalf("unexpected created profile: %+v", p)
	}

	// Duplicate name → ErrProfileNameTaken.
	if _, err := cs.CreateProfile(ctx, "Viewer", permInsert); !errors.Is(err, ErrProfileNameTaken) {
		t.Fatalf("expected ErrProfileNameTaken, got %v", err)
	}

	// Get round-trips.
	got, err := cs.GetProfile(ctx, p.ID)
	if err != nil || got == nil || got.Perms != permRead {
		t.Fatalf("get profile: %v %+v", err, got)
	}

	// Update perms + name.
	if err := cs.UpdateProfile(ctx, p.ID, "Viewer+", permEditor); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	got, _ = cs.GetProfile(ctx, p.ID)
	if got.Name != "Viewer+" || got.Perms != permEditor {
		t.Fatalf("update did not persist: %+v", got)
	}

	// Update a missing id → ErrProfileNotFound.
	if err := cs.UpdateProfile(ctx, "999999", "x", permRead); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}

	// Delete.
	if err := cs.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if got, _ := cs.GetProfile(ctx, p.ID); got != nil {
		t.Fatalf("profile should be gone, got %+v", got)
	}
}

// TestTeamsCRUDIntegration covers the team lifecycle plus project/member attachment
// and cascade-on-delete.
func TestTeamsCRUDIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	mig, err := cs.CreateTeam(ctx, "Migración", "work")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if _, err := cs.CreateTeam(ctx, "Migración", "work"); !errors.Is(err, ErrTeamNameTaken) {
		t.Fatalf("expected ErrTeamNameTaken, got %v", err)
	}
	if _, err := cs.CreateTeam(ctx, "Intervus", "personal"); err != nil {
		t.Fatalf("create team 2: %v", err)
	}

	teams, err := cs.ListTeams(ctx)
	if err != nil || len(teams) != 2 {
		t.Fatalf("list teams: %v n=%d", err, len(teams))
	}
	// Ordered by name: Intervus, Migración.
	if teams[0].Name != "Intervus" || teams[1].Name != "Migración" {
		t.Fatalf("teams not ordered by name: %+v", teams)
	}

	// Update: reclassify Migración personal.
	if err := cs.UpdateTeam(ctx, mig.ID, "Migración", "personal"); err != nil {
		t.Fatalf("update team: %v", err)
	}
	got, _ := cs.GetTeam(ctx, mig.ID)
	if got.Kind != "personal" {
		t.Fatalf("kind not updated: %+v", got)
	}

	// Attach projects (idempotent).
	if err := cs.AddTeamProject(ctx, mig.ID, "mig-proj"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := cs.AddTeamProject(ctx, mig.ID, "mig-proj"); err != nil {
		t.Fatalf("add project idempotent: %v", err)
	}
	projs, _ := cs.ListProjectsForTeam(ctx, mig.ID)
	if len(projs) != 1 || projs[0] != "mig-proj" {
		t.Fatalf("unexpected projects: %+v", projs)
	}

	// Member with the seeded Moderator profile.
	modID := profileIDByName(t, cs, "Moderator")
	if err := cs.AddTeamMember(ctx, mig.ID, "worker", modID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	members, _ := cs.ListMembersOfTeam(ctx, mig.ID)
	if len(members) != 1 || members[0].AccountID != "worker" || members[0].Perms != permMod || members[0].ProfileName != "Moderator" {
		t.Fatalf("unexpected members: %+v", members)
	}

	forAcct, _ := cs.ListTeamsForAccount(ctx, "worker")
	if len(forAcct) != 1 || forAcct[0].ID != mig.ID {
		t.Fatalf("ListTeamsForAccount wrong: %+v", forAcct)
	}

	// Re-add changes the profile (upsert on team+account).
	memID := profileIDByName(t, cs, "Member")
	if err := cs.AddTeamMember(ctx, mig.ID, "worker", memID); err != nil {
		t.Fatalf("re-add member: %v", err)
	}
	members, _ = cs.ListMembersOfTeam(ctx, mig.ID)
	if len(members) != 1 || members[0].Perms != permRead {
		t.Fatalf("profile change did not take: %+v", members)
	}

	// Delete cascades projects + members.
	if err := cs.DeleteTeam(ctx, mig.ID); err != nil {
		t.Fatalf("delete team: %v", err)
	}
	if p, _ := cs.ListProjectsForTeam(ctx, mig.ID); len(p) != 0 {
		t.Fatalf("projects not cascaded: %+v", p)
	}
	if m, _ := cs.ListMembersOfTeam(ctx, mig.ID); len(m) != 0 {
		t.Fatalf("members not cascaded: %+v", m)
	}
}

// TestEffectivePermsLayeredIntegration is the core resolver test: multi-team UNION
// (bit_or), per-project OVERRIDE precedence (elevate AND restrict, including deny),
// and deny-by-default. This is the acceptance evidence for OBL-14.
func TestEffectivePermsLayeredIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	// Two profiles whose OR is distinct from either operand so bit_or is provable.
	readOnly := profileIDByName(t, cs, "Member") // perms = 1 (Read)
	inserter, err := cs.CreateProfile(ctx, "Inserter", permInsert)
	if err != nil {
		t.Fatalf("create Inserter: %v", err)
	}

	teamA, _ := cs.CreateTeam(ctx, "TeamA", "work") // read-only members
	teamB, _ := cs.CreateTeam(ctx, "TeamB", "work") // insert-only members

	// TeamA covers projA + shared; TeamB covers projB + shared.
	for _, p := range []string{"projA", "shared"} {
		if err := cs.AddTeamProject(ctx, teamA.ID, p); err != nil {
			t.Fatalf("teamA add %s: %v", p, err)
		}
	}
	for _, p := range []string{"projB", "shared"} {
		if err := cs.AddTeamProject(ctx, teamB.ID, p); err != nil {
			t.Fatalf("teamB add %s: %v", p, err)
		}
	}

	// worker is in both teams; stranger is in neither.
	if err := cs.AddTeamMember(ctx, teamA.ID, "worker", readOnly); err != nil {
		t.Fatalf("member A: %v", err)
	}
	if err := cs.AddTeamMember(ctx, teamB.ID, "worker", inserter.ID); err != nil {
		t.Fatalf("member B: %v", err)
	}

	assertPerms := func(account, project string, want int) {
		t.Helper()
		got, err := cs.EffectivePerms(ctx, account, project)
		if err != nil {
			t.Fatalf("EffectivePerms(%s,%s): %v", account, project, err)
		}
		if got != want {
			t.Fatalf("EffectivePerms(%s,%s) = %d, want %d", account, project, got, want)
		}
	}

	// Team-derived perms: single-team projects use that team's profile.
	assertPerms("worker", "projA", permRead)   // 1
	assertPerms("worker", "projB", permInsert) // 2
	// UNION where teams overlap: 1 | 2 == 3 (distinct from either operand → real bit_or).
	assertPerms("worker", "shared", permRead|permInsert) // 3

	// Deny-by-default: no team, no override.
	assertPerms("worker", "unrelated", 0)
	assertPerms("stranger", "shared", 0)

	// OVERRIDE precedence — REPLACE (not merge). Override shared with Update(4): the
	// union was 3; the override replaces it wholesale, elevating Update and dropping
	// Read+Insert.
	if err := cs.GrantMembership("worker", "shared", permUpdate, "member"); err != nil {
		t.Fatalf("grant override: %v", err)
	}
	assertPerms("worker", "shared", permUpdate) // 4, not 3|4

	// OVERRIDE can ELEVATE: projB team perms are 2; override to full CRUD.
	if err := cs.GrantMembership("worker", "projB", permMod, "owner"); err != nil {
		t.Fatalf("grant elevate: %v", err)
	}
	assertPerms("worker", "projB", permMod) // 15

	// OVERRIDE can RESTRICT to deny: projA team perms are 1; override to 0 must be
	// respected (an existing override row of 0 does NOT fall through to teams).
	if err := cs.GrantMembership("worker", "projA", 0, "member"); err != nil {
		t.Fatalf("grant restrict: %v", err)
	}
	assertPerms("worker", "projA", 0)

	// Removing the override falls back to the team-derived perms again.
	if err := cs.RevokeMembership("worker", "projA"); err != nil {
		t.Fatalf("revoke override: %v", err)
	}
	assertPerms("worker", "projA", permRead)

	// claimOrphanProject parity: an owner override (as claim-on-first-push writes)
	// resolves to full perms with no team involved.
	if err := cs.GrantMembership("owner", "owned", permMod, "owner"); err != nil {
		t.Fatalf("grant owner: %v", err)
	}
	assertPerms("owner", "owned", permMod)
}

// TestProjectMetaAndKnownProjectsIntegration covers classification round-trip and
// the union-sourced known-projects list.
func TestProjectMetaAndKnownProjectsIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	// Classification round-trip + update.
	if err := cs.UpsertProjectMeta(ctx, "workly", "work", "Workly"); err != nil {
		t.Fatalf("upsert meta: %v", err)
	}
	m, err := cs.GetProjectMeta(ctx, "workly")
	if err != nil || m == nil || m.Kind != "work" || m.DisplayName != "Workly" {
		t.Fatalf("meta round-trip: %v %+v", err, m)
	}
	if err := cs.UpsertProjectMeta(ctx, "workly", "personal", ""); err != nil {
		t.Fatalf("re-upsert meta: %v", err)
	}
	m, _ = cs.GetProjectMeta(ctx, "workly")
	if m.Kind != "personal" || m.DisplayName != "" {
		t.Fatalf("meta not updated: %+v", m)
	}
	if none, _ := cs.GetProjectMeta(ctx, "does-not-exist"); none != nil {
		t.Fatalf("expected nil meta, got %+v", none)
	}

	// Seed each of the four union sources with a distinct project.
	if _, err := cs.db.ExecContext(ctx,
		`INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload) VALUES ('kp-chunk','c1','t','{}'::jsonb)`); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	if err := cs.GrantMembership("acct", "kp-member", permRead, "member"); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	team, _ := cs.CreateTeam(ctx, "KPTeam", "work")
	if err := cs.AddTeamProject(ctx, team.ID, "kp-team"); err != nil {
		t.Fatalf("seed team project: %v", err)
	}
	// "workly" already seeded via project_meta above.

	known, err := cs.KnownProjects(ctx)
	if err != nil {
		t.Fatalf("known projects: %v", err)
	}
	set := map[string]KnownProject{}
	for _, kp := range known {
		set[kp.Project] = kp
	}
	for _, want := range []string{"kp-chunk", "kp-member", "kp-team", "workly"} {
		if _, ok := set[want]; !ok {
			t.Fatalf("known projects missing %q: %+v", want, known)
		}
	}
	// Classified project carries its kind; unclassified ones carry empty kind.
	if set["workly"].Kind != "personal" {
		t.Fatalf("workly should be classified personal: %+v", set["workly"])
	}
	if set["kp-chunk"].Kind != "" {
		t.Fatalf("kp-chunk should be unclassified (empty kind): %+v", set["kp-chunk"])
	}
}

// profileIDByName resolves a seeded/created profile id by name for tests.
func profileIDByName(t *testing.T, cs *CloudStore, name string) string {
	t.Helper()
	profiles, err := cs.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	for _, p := range profiles {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("profile %q not found", name)
	return ""
}
