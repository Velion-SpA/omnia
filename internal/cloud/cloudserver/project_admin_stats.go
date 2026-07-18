package cloudserver

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Command Center v2, Slice 4b: the Admin Projects reverse-access fragment.
// The stats side (memory/source/last-activity counts on each card) is wired
// directly into handleAdminProjectsPage (admin_teams_dashboard.go) since it
// reshapes an existing view model; this file holds the NEW store-capability
// seam both that page and this fragment depend on, plus the fragment handler
// itself — mirroring project_sync_admin.go's pattern (a dedicated small file
// per capability, detected via type assertion, so the core ChunkStore
// interface is never extended).

// projectAdminStatsStore is the store capability needed to render per-project
// stats and the reverse "who has access" view on the Admin Projects page.
// *cloudstore.CloudStore satisfies it; detected via type assertion the same
// way teamsAdminStore / projectSyncControlAdminStore are, so a store that
// doesn't support it (e.g. a future non-Postgres backend) still renders the
// Projects page — just without stats (see admin_teams_dashboard.go).
type projectAdminStatsStore interface {
	ListProjectChunkStats(ctx context.Context) (map[string]cloudstore.ProjectChunkStats, error)
	ListAccountAccessForProject(ctx context.Context, project string) ([]cloudstore.ProjectAccessRow, error)
}

// Compile-time assertion: the concrete store must satisfy the seam.
var _ projectAdminStatsStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) projectAdminStatsStore() (projectAdminStatsStore, bool) {
	ps, ok := s.store.(projectAdminStatsStore)
	return ps, ok
}

// adminProjectAccessRow is the fragment's per-account view row: a resolved
// username (falling back to the raw account id, exactly like
// handleAdminTeamDetailPage does for team members), the effective-permission
// label reused verbatim from Slice 3 (accessEffectiveLabel: Full/Partial/
// Read — a zero-perms account never reaches this view, see
// ListAccountAccessForProject), and which layer granted it.
type adminProjectAccessRow struct {
	AccountID string
	Username  string
	Label     string
	Source    string // "override" | "team"
}

// projectAccessCountLabel counts how many rows can actually READ the
// project — the Admin Projects card's "N con acceso" stat. A store's reverse-
// access rows can (rarely) include a Partial grant without the Read bit (e.g.
// Insert-only); the card's headline count is specifically about who can SEE
// the project's memories, so it filters on PermRead rather than "any nonzero
// perms" (which ListAccountAccessForProject already guarantees for every row
// it returns).
func projectAccessCountLabel(rows []cloudstore.ProjectAccessRow) int {
	n := 0
	for _, r := range rows {
		if auth.Permission(r.Perms).Has(auth.PermRead) {
			n++
		}
	}
	return n
}

// handleAdminProjectAccessFragment handles GET /admin/projects/{project}/access:
// the reverse of the Slice 3 unified Access page — for ONE project, every
// account with non-zero effective access. Operator-gated FIRST (mirrors
// handleAdminAccessRowView and every other Admin fragment handler); lazily
// loaded on expand (see the "Who has access" trigger in admin_teams_ui.templ)
// so the Projects page itself never eagerly renders every account row for
// every project, only the cheap headline count.
func (s *CloudServer) handleAdminProjectAccessFragment(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	ps, ok := s.projectAdminStatsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "project stats unavailable"})
		return
	}
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	rows, err := ps.ListAccountAccessForProject(r.Context(), project)
	if err != nil {
		http.Error(w, "could not load project access", http.StatusInternalServerError)
		return
	}

	usernames := map[string]string{}
	if as, aok := s.adminStore(); aok {
		if users, uerr := as.ListUsers(r.Context()); uerr == nil {
			for _, u := range users {
				usernames[u.ID] = u.Username
			}
		}
	}

	view := make([]adminProjectAccessRow, 0, len(rows))
	for _, row := range rows {
		name := usernames[row.AccountID]
		if name == "" {
			name = row.AccountID
		}
		view = append(view, adminProjectAccessRow{
			AccountID: row.AccountID,
			Username:  name,
			Label:     accessEffectiveLabel(row.Perms),
			Source:    row.Source,
		})
	}
	sort.Slice(view, func(i, j int) bool { return view[i].Username < view[j].Username })

	if err := adminProjectAccessFragment(project, view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
