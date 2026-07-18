package cloudstore

import (
	"context"
	"testing"
	"time"
)

// TestListProjectChunkStatsIntegration covers the Admin Projects card stats
// (Command Center v2, Slice 4b): memory count (SUM observations_count),
// distinct-source count (COUNT DISTINCT created_by), and last-activity
// (MAX created_at), aggregated directly from cloud_chunks. Chunks are seeded
// via a raw INSERT (mirrors TestProjectMetaAndKnownProjectsIntegration's
// "kp-chunk" seed) rather than WriteChunk, since WriteChunk requires a
// payload whose hash matches chunk_id — irrelevant noise for this aggregate.
func TestListProjectChunkStatsIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	older := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	newer := time.Now().Add(-1 * time.Hour).Truncate(time.Second)

	seedChunk := func(project, chunkID, createdBy string, observations int, createdAt time.Time) {
		t.Helper()
		if _, err := cs.db.ExecContext(ctx,
			`INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, observations_count, created_at)
			 VALUES ($1, $2, $3, '{}'::jsonb, $4, $5)`,
			project, chunkID, createdBy, observations, createdAt); err != nil {
			t.Fatalf("seed chunk %s/%s: %v", project, chunkID, err)
		}
	}

	// stats-alpha: 3 chunks, 2 distinct contributors (alice contributes twice),
	// most recent activity is "newer".
	seedChunk("stats-alpha", "c1", "alice", 5, older)
	seedChunk("stats-alpha", "c2", "bob", 3, newer)
	seedChunk("stats-alpha", "c3", "alice", 2, older)
	// stats-beta: single contributor, single chunk.
	seedChunk("stats-beta", "c1", "carol", 10, older)

	stats, err := cs.ListProjectChunkStats(ctx)
	if err != nil {
		t.Fatalf("list project chunk stats: %v", err)
	}

	alpha, ok := stats["stats-alpha"]
	if !ok {
		t.Fatalf("missing stats-alpha in %+v", stats)
	}
	if alpha.MemoryCount != 10 {
		t.Fatalf("alpha memory count = %d, want 10", alpha.MemoryCount)
	}
	if alpha.SourceCount != 2 {
		t.Fatalf("alpha source count = %d, want 2 (alice, bob)", alpha.SourceCount)
	}
	if !alpha.LastActivity.Equal(newer) {
		t.Fatalf("alpha last activity = %v, want %v", alpha.LastActivity, newer)
	}

	beta, ok := stats["stats-beta"]
	if !ok {
		t.Fatalf("missing stats-beta in %+v", stats)
	}
	if beta.MemoryCount != 10 || beta.SourceCount != 1 {
		t.Fatalf("unexpected beta stats: %+v", beta)
	}
	if !beta.LastActivity.Equal(older) {
		t.Fatalf("beta last activity = %v, want %v", beta.LastActivity, older)
	}

	if _, ok := stats["does-not-exist"]; ok {
		t.Fatalf("unexpected stats entry for an unseeded project")
	}
}

// TestListAccountAccessForProjectIntegration is the reverse of
// TestEffectivePermsLayeredIntegration: given a PROJECT, list every account
// with non-zero effective access plus which layer produced it (override wins
// outright over the team-derived union — same precedence as EffectivePerms).
func TestListAccountAccessForProjectIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	readOnly := profileIDByName(t, cs, "Member") // perms = 1 (Read)

	teamA, err := cs.CreateTeam(ctx, "TeamA", "work")
	if err != nil {
		t.Fatalf("create teamA: %v", err)
	}
	if err := cs.AddTeamProject(ctx, teamA.ID, "proj-x"); err != nil {
		t.Fatalf("attach proj-x: %v", err)
	}
	if err := cs.AddTeamMember(ctx, teamA.ID, "worker", readOnly); err != nil {
		t.Fatalf("add worker: %v", err)
	}
	// A team member with NO profile assigned contributes 0 perms and must be
	// excluded from the reverse view (mirrors EffectivePerms' 0-perms handling).
	if err := cs.AddTeamMember(ctx, teamA.ID, "stranger-no-perm", ""); err != nil {
		t.Fatalf("add stranger-no-perm: %v", err)
	}

	// Override ELEVATES worker on proj-x — must replace the team-derived read,
	// not merge with it (Source becomes "override").
	if err := cs.GrantMembership("worker", "proj-x", permMod, "owner"); err != nil {
		t.Fatalf("grant elevate override: %v", err)
	}
	// An account with ONLY an override (no team route at all) still appears.
	if err := cs.GrantMembership("outsider", "proj-x", permRead, "member"); err != nil {
		t.Fatalf("grant outsider: %v", err)
	}
	// An explicit deny override (0 perms) must be excluded — an existing
	// override row of 0 does not fall through to a (nonexistent) team grant.
	if err := cs.GrantMembership("denied", "proj-x", 0, "member"); err != nil {
		t.Fatalf("grant denied: %v", err)
	}

	// An unrelated project/team must never leak into proj-x's results.
	teamB, err := cs.CreateTeam(ctx, "TeamB", "work")
	if err != nil {
		t.Fatalf("create teamB: %v", err)
	}
	if err := cs.AddTeamProject(ctx, teamB.ID, "proj-y"); err != nil {
		t.Fatalf("attach proj-y: %v", err)
	}
	if err := cs.AddTeamMember(ctx, teamB.ID, "other", readOnly); err != nil {
		t.Fatalf("add other: %v", err)
	}

	rows, err := cs.ListAccountAccessForProject(ctx, "proj-x")
	if err != nil {
		t.Fatalf("list account access for proj-x: %v", err)
	}
	byAccount := map[string]ProjectAccessRow{}
	for _, r := range rows {
		byAccount[r.AccountID] = r
	}

	if len(rows) != 2 {
		t.Fatalf("unexpected row count (want worker+outsider only): %+v", rows)
	}
	if got := byAccount["worker"]; got.Perms != permMod || got.Source != "override" {
		t.Fatalf("worker row wrong: %+v", got)
	}
	if got := byAccount["outsider"]; got.Perms != permRead || got.Source != "override" {
		t.Fatalf("outsider row wrong: %+v", got)
	}
	if _, ok := byAccount["denied"]; ok {
		t.Fatalf("denied override (0 perms) should be excluded: %+v", rows)
	}
	if _, ok := byAccount["stranger-no-perm"]; ok {
		t.Fatalf("team member with no profile (0 perms) should be excluded: %+v", rows)
	}
	if _, ok := byAccount["other"]; ok {
		t.Fatalf("unrelated project's account leaked into proj-x: %+v", rows)
	}

	// Pure team-derived case (Source == "team", no override involved).
	teamC, err := cs.CreateTeam(ctx, "TeamC", "work")
	if err != nil {
		t.Fatalf("create teamC: %v", err)
	}
	if err := cs.AddTeamProject(ctx, teamC.ID, "proj-z"); err != nil {
		t.Fatalf("attach proj-z: %v", err)
	}
	if err := cs.AddTeamMember(ctx, teamC.ID, "teamer", readOnly); err != nil {
		t.Fatalf("add teamer: %v", err)
	}
	rowsZ, err := cs.ListAccountAccessForProject(ctx, "proj-z")
	if err != nil {
		t.Fatalf("list account access for proj-z: %v", err)
	}
	if len(rowsZ) != 1 || rowsZ[0].AccountID != "teamer" || rowsZ[0].Source != "team" || rowsZ[0].Perms != permRead {
		t.Fatalf("unexpected proj-z rows: %+v", rowsZ)
	}

	// An unknown/unseeded project yields an empty (not nil-panicking) slice.
	rowsNone, err := cs.ListAccountAccessForProject(ctx, "does-not-exist")
	if err != nil || len(rowsNone) != 0 {
		t.Fatalf("unexpected rows for unseeded project: %v %+v", err, rowsNone)
	}
}
