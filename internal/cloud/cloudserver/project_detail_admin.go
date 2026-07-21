package cloudserver

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/ui"
	"github.com/velion/omnia/internal/ui/i18n"
)

// Admin projects redesign (issue #93): the NEW per-project detail page
// (GET /admin/projects/{project}), mirroring handleAdminProjectsPage's data
// assembly for exactly one project, plus three tabs the list page's compact
// cards never had room for (Memorias/Acceso/Actividad).
//
// Memorias tab decision: cloud_chunks' payload column is plain JSONB (not
// E2E/encrypted — see cloudstore.CloudStore's migrate()), and the dashboard
// read model already materializes individual observation metadata (type,
// title, created-at) from it — ListRecentObservations is the SAME store
// method internal/cloud/clouddash.Source already uses to back the unified
// dashboard's own /project/{name} page. So per-memory titles ARE available
// server-side, and the Memorias tab lists them newest-first rather than
// falling back to a source/activity-only view.

// projectMemoriesAdminStore is the store capability needed for the Memorias
// tab. Detected via type assertion, mirroring projectAdminStatsStore /
// projectLinksAdminStore — a store without it simply renders the tab with a
// "not available" note (MemoriesSupported stays false) instead of crashing.
type projectMemoriesAdminStore interface {
	ListRecentObservations(project, query string, limit int) ([]cloudstore.DashboardObservationRow, error)
}

// Compile-time assertion: the concrete store must satisfy the seam.
var _ projectMemoriesAdminStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) projectMemoriesStore() (projectMemoriesAdminStore, bool) {
	pm, ok := s.store.(projectMemoriesAdminStore)
	return pm, ok
}

// adminProjectDetailMemoriesLimit caps the Memorias tab at the newest N
// observations — a fast landing/summary view, not a second copy of Browse.
const adminProjectDetailMemoriesLimit = 50

// adminProjectDetailActivityLimit caps the Actividad tab's audit timeline.
const adminProjectDetailActivityLimit = 20

// ─── view models ─────────────────────────────────────────────────────────────

// adminProjectDetailMemoryRow is one Memorias tab row: a type chip, the
// title, and a relative age — see the doc comment above for why per-memory
// titles are available on the cloud (plain JSONB payload, not encrypted).
type adminProjectDetailMemoryRow struct {
	Type          string
	Class         string // CSS modifier (mirrors internal/ui's unexported cardTypeAccentClass)
	Title         string
	Age           string
	SourceProject string // display name of the project this memory belongs to
	IsOwn         bool   // true = this (parent) project's own; false = a child's, rolled up
}

// adminProjectSubTile is one "Sub-proyectos" violet tile on a PARENT's
// detail page: a direct linked child, its own memory count/last-activity,
// and a link to its own detail page.
type adminProjectSubTile struct {
	Project      string
	DisplayName  string
	URL          string
	MemoryCount  int
	LastActivity string
	SyncEnabled  bool
}

// adminProjectDetailActivityRow is one Actividad tab timeline entry — a
// project-scoped audit log row (pause/resume, classification changes, etc.),
// mirroring the existing Admin Audit page's raw action/outcome vocabulary
// (no separate label catalog exists for these today — see admin_audit.go).
type adminProjectDetailActivityRow struct {
	OccurredAt  string // relative, e.g. "hace 2 h"
	Action      string
	Outcome     string
	Contributor string
	ReasonCode  string
}

// adminProjectDetailView is the full view model for adminProjectDetailPage.
type adminProjectDetailView struct {
	Props ui.LayoutProps

	Project      string // raw project name
	DisplayName  string // display name when set, else Project (mirrors projectCardTitle)
	CreatedLabel string // relative "created ~X ago"; "" when unknown

	SyncEnabled  bool
	PausedReason string
	PauseURL     string
	ResumeURL    string

	// IsParent / Children: a prominent "Sub-proyectos" section between the
	// stat strip and the tabs (Admin projects redesign, locked design).
	IsParent bool
	Children []adminProjectSubTile

	// ParentProject / ParentProjectURL: set only when THIS project is itself
	// a linked child — renders a "↳ sub-proyecto de {parent}" back-link
	// instead of a Sub-proyectos section (2-level hierarchy: never both).
	ParentProject    string
	ParentProjectURL string

	// Stat strip — rolled up (own + direct children) exactly like the list
	// page's cards, via the SAME projectRollupStats helper.
	StatMemories int
	StatAccess   int
	StatSources  int
	StatLast     string

	MemoriesSupported bool
	Memories          []adminProjectDetailMemoryRow
	MemoriesTotal     int
	MemoriesOwnTotal  int // count of rolled-up rows that are THIS project's own

	Access []adminProjectAccessRow

	ActivitySupported bool
	Activity          []adminProjectDetailActivityRow
}

// handleAdminProjectDetailPage handles GET /admin/projects/{project}: the
// Admin projects redesign's own per-project page (issue #93). 404s cleanly
// when the project name doesn't resolve to a known project.
func (s *CloudServer) handleAdminProjectDetailPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return
	}
	lang := i18n.LangFrom(r.Context())
	project := strings.TrimSpace(r.PathValue("project"))

	known, err := ts.KnownProjects(r.Context())
	if err != nil {
		http.Error(w, "could not load project", http.StatusInternalServerError)
		return
	}
	knownByProject := make(map[string]cloudstore.KnownProject, len(known))
	for _, k := range known {
		knownByProject[k.Project] = k
	}
	kp, found := knownByProject[project]
	if project == "" || !found {
		http.Error(w, i18n.T(lang, "admin.projects.detailNotFound"), http.StatusNotFound)
		return
	}

	escProject := url.PathEscape(project)
	view := adminProjectDetailView{
		Props:       s.adminLayoutProps(r.Context(), "Admin · "+knownProjectDisplayOrRaw(kp), "projects"),
		Project:     project,
		DisplayName: knownProjectDisplayOrRaw(kp),
		SyncEnabled: true,
		PauseURL:    "/admin/projects/" + escProject + "/pause",
		ResumeURL:   "/admin/projects/" + escProject + "/resume",
	}

	// Sync control: fetched ONCE for own+children (N+1 fix precedent, see
	// admin_teams_dashboard.go's ListProjectSyncControlsMap comment).
	var syncControls map[string]cloudstore.ProjectSyncControl
	if pcs, ok := s.projectSyncControlStore(); ok {
		if m, serr := pcs.ListProjectSyncControlsMap(r.Context()); serr == nil {
			syncControls = m
		}
	}
	if ctrl, ok := syncControls[project]; ok {
		view.SyncEnabled = ctrl.SyncEnabled
		if ctrl.PausedReason != nil {
			view.PausedReason = *ctrl.PausedReason
		}
	}

	var chunkStats map[string]cloudstore.ProjectChunkStats
	if pas, ok := s.projectAdminStatsStore(); ok {
		if m, serr := pas.ListProjectChunkStats(r.Context()); serr == nil {
			chunkStats = m
		}
		if accessRows, aerr := pas.ListAccountAccessForProject(r.Context(), project); aerr == nil {
			view.Access = s.buildAdminProjectAccessRows(r.Context(), accessRows)
			view.StatAccess = projectAccessCountLabel(accessRows)
		}
	}

	// Parent/child links (Slice 5a/5b, same maps handleAdminProjectsPage
	// composes): this project's own parent (child back-link) and its DIRECT
	// children (Sub-proyectos section), both read from ONE ListProjectParents
	// call, no per-project query.
	var parent string
	var childrenNames []string
	if pls, ok := s.projectLinksStore(); ok {
		if parents, perr := pls.ListProjectParents(r.Context()); perr == nil {
			parent = parents[project]
			for child, p := range parents {
				if p == project {
					childrenNames = append(childrenNames, child)
				}
			}
			sort.Strings(childrenNames)
		}
	}
	if parent != "" {
		view.ParentProject = knownProjectDisplayOrRaw(knownByProject[parent])
		if view.ParentProject == "" {
			view.ParentProject = parent
		}
		view.ParentProjectURL = "/admin/projects/" + url.PathEscape(parent)
	}

	own := chunkStats[project]
	childStats := make([]cloudstore.ProjectChunkStats, 0, len(childrenNames))
	for _, child := range childrenNames {
		cs := chunkStats[child]
		childStats = append(childStats, cs)
		tile := adminProjectSubTile{
			Project:     child,
			DisplayName: knownProjectDisplayOrRaw(knownByProject[child]),
			URL:         "/admin/projects/" + url.PathEscape(child),
			MemoryCount: cs.MemoryCount,
			SyncEnabled: true,
		}
		if !cs.LastActivity.IsZero() {
			tile.LastActivity = ui.RelativeTimeLang(cs.LastActivity, lang)
		}
		if ctrl, ok := syncControls[child]; ok {
			tile.SyncEnabled = ctrl.SyncEnabled
		}
		view.Children = append(view.Children, tile)
	}
	view.IsParent = len(view.Children) > 0

	rollup := projectRollupStats(own, childStats)
	view.StatMemories = rollup.MemoryCount
	view.StatSources = rollup.SourceCount
	if !rollup.LastActivity.IsZero() {
		view.StatLast = ui.RelativeTimeLang(rollup.LastActivity, lang)
	}
	if !rollup.FirstActivity.IsZero() {
		view.CreatedLabel = ui.RelativeTimeLang(rollup.FirstActivity, lang)
	}

	if pms, ok := s.projectMemoriesStore(); ok {
		view.MemoriesSupported = true
		// Roll up the parent's own memories together with its direct children's,
		// so the list matches the rolled-up stat strip — the parent is the
		// umbrella. Each row keeps its source project (a chip in the UI) and an
		// isOwn flag, so the "solo este proyecto" toggle can filter to own-only.
		type detailObs struct {
			row     cloudstore.DashboardObservationRow
			display string
			isOwn   bool
		}
		var collected []detailObs
		gather := func(proj, display string, isOwn bool) {
			rows, merr := pms.ListRecentObservations(proj, "", adminProjectDetailMemoriesLimit)
			if merr != nil {
				return
			}
			for _, row := range rows {
				collected = append(collected, detailObs{row: row, display: display, isOwn: isOwn})
			}
		}
		gather(project, view.DisplayName, true)
		for _, child := range childrenNames {
			gather(child, knownProjectDisplayOrRaw(knownByProject[child]), false)
		}
		// CreatedAt is a fixed-width "YYYY-MM-DD HH:MM:SS" string, so a plain
		// descending lexicographic sort orders newest-first across projects.
		sort.SliceStable(collected, func(i, j int) bool {
			return collected[i].row.CreatedAt > collected[j].row.CreatedAt
		})
		if len(collected) > adminProjectDetailMemoriesLimit {
			collected = collected[:adminProjectDetailMemoriesLimit]
		}
		view.Memories = make([]adminProjectDetailMemoryRow, 0, len(collected))
		for _, c := range collected {
			if c.isOwn {
				view.MemoriesOwnTotal++
			}
			view.Memories = append(view.Memories, adminProjectDetailMemoryRow{
				Type:          c.row.Type,
				Class:         projectDetailObsTypeClass(c.row.Type),
				Title:         c.row.Title,
				Age:           projectDetailEngramAge(c.row.CreatedAt, lang),
				SourceProject: c.display,
				IsOwn:         c.isOwn,
			})
		}
		view.MemoriesTotal = len(view.Memories)
	}

	if as, ok := s.auditStore(); ok {
		view.ActivitySupported = true
		if rows, _, aerr := as.ListAuditEntriesPaginated(r.Context(), cloudstore.AuditFilter{Project: project}, adminProjectDetailActivityLimit, 0); aerr == nil {
			view.Activity = make([]adminProjectDetailActivityRow, 0, len(rows))
			for _, row := range rows {
				view.Activity = append(view.Activity, adminProjectDetailActivityRow{
					OccurredAt:  projectDetailRFC3339Age(row.OccurredAt, lang),
					Action:      row.Action,
					Outcome:     row.Outcome,
					Contributor: row.Contributor,
					ReasonCode:  row.ReasonCode,
				})
			}
		}
	}

	if err := adminProjectDetailPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// knownProjectDisplayOrRaw mirrors projectCardTitle for a cloudstore.KnownProject
// rather than an adminProjectRow: the operator-set display name when present,
// otherwise the raw project name.
func knownProjectDisplayOrRaw(kp cloudstore.KnownProject) string {
	if strings.TrimSpace(kp.DisplayName) != "" {
		return kp.DisplayName
	}
	return kp.Project
}

// projectDetailObsTypeClass maps an observation type to the SAME accent-color
// modifier class internal/ui's cards.go uses (cardTypeAccentClass) — kept as
// its own small copy since that helper is unexported in package ui and the
// two type vocabularies (dashboard.ObsView vs cloudstore.DashboardObservationRow)
// are otherwise unrelated; duplicating one switch is simpler and safer than
// exporting a cross-package dependency for it.
func projectDetailObsTypeClass(t string) string {
	switch t {
	case "architecture":
		return "card-type-architecture"
	case "decision":
		return "card-type-decision"
	case "bugfix":
		return "card-type-bugfix"
	case "config":
		return "card-type-config"
	case "discovery":
		return "card-type-discovery"
	case "pattern":
		return "card-type-pattern"
	default:
		return "card-type-default"
	}
}

// projectDetailEngramAge parses a DashboardObservationRow.CreatedAt string —
// the engram/engramdb storage format ("2006-01-02 15:04:05", UTC, no zone
// suffix; synced verbatim from the contributing client) — into a relative
// label. Mirrors internal/dashboard/projectdetail.go's parseEngramTimestamp;
// kept as its own copy rather than exporting that one across packages for a
// single call site, consistent with that helper's own doc comment.
func projectDetailEngramAge(ts string, lang i18n.Lang) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(ts), time.UTC)
	if err != nil {
		return ""
	}
	return ui.RelativeTimeLang(t, lang)
}

// adminProjectDetailLastLabel mirrors rollupLastActivityLabel's em-dash
// fallback for the detail page's own "Last" stat tile.
func adminProjectDetailLastLabel(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// projectDetailRFC3339Age parses a DashboardAuditRow.OccurredAt string
// ("RFC3339 UTC", per its own doc comment) into a relative label.
func projectDetailRFC3339Age(ts string, lang i18n.Lang) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
	if err != nil {
		return ts
	}
	return ui.RelativeTimeLang(t, lang)
}
