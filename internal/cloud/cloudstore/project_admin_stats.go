package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Command Center v2, Slice 4b: read-only aggregates for the Admin Projects
// page — per-project card stats (memory count, distinct sources, last
// activity) and the reverse "who has access" view. Kept in their own file
// rather than cloudstore.go (already flagged as a large file — see the
// 2026-07-15 architecture audit) or teams.go (core team/profile CRUD), since
// both queries here are pure aggregates over EXISTING tables — no schema
// changes, no new capability beyond what EffectivePerms/KnownProjects already
// established.

// ProjectChunkStats holds the three per-project numbers the Admin Projects
// card shows at a glance, aggregated directly from cloud_chunks (the raw
// synced-chunk table) rather than the heavier dashboardReadModel cache: that
// cache materializes every individual observation/session/prompt, which is
// far more than three aggregate numbers need.
type ProjectChunkStats struct {
	Project string
	// MemoryCount is SUM(observations_count) — the total synced memories.
	MemoryCount int
	// SourceCount is COUNT(DISTINCT created_by) — the number of distinct
	// syncing clients/accounts that have pushed a chunk to this project. This
	// is deliberately NOT the ingestion-metadata Source (github/discord/jira/
	// whatsapp — see internal/meta.Meta.Source, parsed from an observation's
	// own content and only present on "ingested" rows): that concept requires
	// parsing every observation's payload and has no meaning for plain
	// CLI-synced ("curated") memories, which are the majority of projects.
	// created_by is the cloud store's own, always-populated notion of "who
	// contributed this data" and is exactly what a single GROUP BY can answer.
	SourceCount int
	// LastActivity is MAX(created_at) — the zero value when the project has
	// no chunks (never the case for a project returned by this query, since
	// GROUP BY only yields projects with at least one row).
	LastActivity time.Time
}

// ListProjectChunkStats aggregates cloud_chunks per project_name in ONE
// query, so the Admin Projects page can render stats for every known project
// without an N+1 query per card. A project with zero chunks (e.g. classified
// via UpsertProjectMeta but never synced) is simply absent from the returned
// map — callers should treat a missing key as the zero ProjectChunkStats.
func (cs *CloudStore) ListProjectChunkStats(ctx context.Context) (map[string]ProjectChunkStats, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT project_name,
		       COALESCE(SUM(observations_count), 0),
		       COUNT(DISTINCT created_by),
		       MAX(created_at)
		FROM cloud_chunks
		GROUP BY project_name`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list project chunk stats: %w", err)
	}
	defer rows.Close()

	out := make(map[string]ProjectChunkStats)
	for rows.Next() {
		var (
			s            ProjectChunkStats
			lastActivity sql.NullTime
		)
		if err := rows.Scan(&s.Project, &s.MemoryCount, &s.SourceCount, &lastActivity); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project chunk stats: %w", err)
		}
		if lastActivity.Valid {
			s.LastActivity = lastActivity.Time
		}
		out[s.Project] = s
	}
	return out, rows.Err()
}

// ProjectAccessRow is one account's effective access to a project — the
// reverse of ListReadableProjectsForAccount (keyed by project instead of
// account). Perms is the raw bitfield; the caller (the Admin Projects
// reverse-access fragment) resolves it to a human label the same way the
// unified Access page does (accessEffectiveLabel: Full/Partial/Read — a
// zero-perms row never appears here, see below) and resolves AccountID to a
// username via ListUsers, exactly like handleAdminTeamDetailPage already does
// for team members — so this store method stays scoped to permissions, not
// identity.
type ProjectAccessRow struct {
	AccountID string
	Perms     int
	// Source is "override" or "team" — which layer produced Perms, mirroring
	// unifiedAccessRow.Source in admin_access_unified.go. Unlike that page,
	// this does not resolve WHICH team(s) contributed (that would need a
	// second query per account) — a generic "team" origin is enough for the
	// reverse per-project view.
	Source string
}

// ListAccountAccessForProject returns every account with a non-zero
// effective permission on project: the union of accounts reachable via team
// membership (cloud_team_members/cloud_team_projects) and accounts with an
// explicit cloud_memberships override, each resolved through the same
// override-replaces-team-union precedence as EffectivePerms. It is the
// reverse query of ListReadableProjectsForAccount and intentionally returns
// every non-zero row (not just Read) so the caller can render Full/Partial/
// Read correctly — filtering to "can read" (the Admin Projects card's "N con
// acceso" count) is a view-layer concern (auth.Permission(perms).Has(PermRead)),
// kept out of cloudstore so this package need not import internal/cloud/auth.
func (cs *CloudStore) ListAccountAccessForProject(ctx context.Context, project string) ([]ProjectAccessRow, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return []ProjectAccessRow{}, nil
	}
	const q = `
		SELECT cand.account_id,
		       COALESCE(
		           (SELECT perms FROM cloud_memberships WHERE account_id = cand.account_id AND project = $1),
		           (SELECT COALESCE(bit_or(p.perms), 0)
		            FROM cloud_team_members tm
		            JOIN cloud_team_projects tp ON tp.team_id = tm.team_id
		            JOIN cloud_profiles p ON p.id = tm.profile_id
		            WHERE tm.account_id = cand.account_id AND tp.project = $1)
		       ) AS perms,
		       CASE WHEN EXISTS (
		           SELECT 1 FROM cloud_memberships WHERE account_id = cand.account_id AND project = $1
		       ) THEN 'override' ELSE 'team' END AS source
		FROM (
		    SELECT DISTINCT tm.account_id
		    FROM cloud_team_members tm
		    JOIN cloud_team_projects tp ON tp.team_id = tm.team_id
		    WHERE tp.project = $1
		    UNION
		    SELECT account_id FROM cloud_memberships WHERE project = $1
		) cand
		ORDER BY cand.account_id ASC`
	rows, err := cs.db.QueryContext(ctx, q, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list account access for project: %w", err)
	}
	defer rows.Close()

	out := make([]ProjectAccessRow, 0)
	for rows.Next() {
		var row ProjectAccessRow
		if err := rows.Scan(&row.AccountID, &row.Perms, &row.Source); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project access row: %w", err)
		}
		if row.Perms == 0 {
			continue
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
