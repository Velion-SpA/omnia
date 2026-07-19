package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/ui"
	"github.com/velion/omnia/internal/ui/i18n"
)

// Operator Admin section pages for Teams, Profiles and Projects (OBL-15). These
// render the operator-facing UI on top of the OBL-14 data plane (teams_admin.go):
// no new backend logic lives here — the handlers read through the existing store
// seam and shape view models for the templ pages. Every handler re-checks the
// operator (requireOperator) exactly like the OBL-13 pages; the UI is never
// trusted and each mutation still hits an operator-gated OBL-14 endpoint.
//
// URL sharing with the OBL-14 JSON endpoints: GET /admin/teams, /admin/teams/{id},
// /admin/profiles and /admin/projects already serve JSON. The browser Admin pages
// live at the SAME clean URLs, disambiguated by content negotiation — see
// wantsHTMLPage and the *Route dispatchers below. A browser navigation (Accept:
// text/html) gets the HTML page; an API/HTMX/JSON caller keeps the JSON response,
// so the OBL-14 endpoints are untouched.

// wantsHTMLPage reports whether the request is a top-level browser navigation that
// should receive the HTML Admin page rather than the JSON API payload. It is true
// only for a non-HTMX request whose Accept header explicitly asks for text/html —
// exactly what a browser sends when the operator clicks a nav tab. fetch()/HTMX and
// the API (no Accept, or Accept: application/json) fall through to JSON.
func wantsHTMLPage(r *http.Request) bool {
	if isHTMXRequest(r) {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// ─── content-negotiating dispatchers ─────────────────────────────────────────

func (s *CloudServer) handleAdminProfilesRoute(w http.ResponseWriter, r *http.Request) {
	if wantsHTMLPage(r) {
		s.handleAdminProfilesPage(w, r)
		return
	}
	s.handleAdminListProfiles(w, r)
}

func (s *CloudServer) handleAdminTeamsRoute(w http.ResponseWriter, r *http.Request) {
	if wantsHTMLPage(r) {
		s.handleAdminTeamsPage(w, r)
		return
	}
	s.handleAdminListTeams(w, r)
}

func (s *CloudServer) handleAdminTeamRoute(w http.ResponseWriter, r *http.Request) {
	if wantsHTMLPage(r) {
		s.handleAdminTeamDetailPage(w, r)
		return
	}
	s.handleAdminGetTeam(w, r)
}

func (s *CloudServer) handleAdminProjectsRoute(w http.ResponseWriter, r *http.Request) {
	if wantsHTMLPage(r) {
		s.handleAdminProjectsPage(w, r)
		return
	}
	s.handleAdminListProjects(w, r)
}

// ─── view models ─────────────────────────────────────────────────────────────

type adminProfileRow struct {
	ID        string
	Name      string
	Read      bool
	Insert    bool
	Update    bool
	Delete    bool
	Summary   string
	UpdateURL string // PUT /admin/profiles/{id}
	DeleteURL string // DELETE /admin/profiles/{id}
}

type adminProfilesView struct {
	Props    ui.LayoutProps
	Profiles []adminProfileRow
}

type adminTeamListRow struct {
	ID           string
	Name         string
	Kind         string
	ProjectCount int
	MemberCount  int
	DetailURL    string
}

type adminTeamsView struct {
	Props    ui.LayoutProps
	Personal []adminTeamListRow
	Work     []adminTeamListRow
}

type adminTeamProjectRow struct {
	Project     string
	DisplayName string
	Kind        string
	RemoveURL   string // DELETE /admin/teams/{id}/projects/{project}
}

type adminTeamMemberRow struct {
	AccountID string
	Username  string
	ProfileID string
	Profile   string
	Summary   string
	UpdateURL string // PUT /admin/teams/{id}/members/{account_id}
	RemoveURL string
}

type adminTeamDetailView struct {
	Props              ui.LayoutProps
	ID                 string
	Name               string
	Kind               string
	Projects           []adminTeamProjectRow
	Members            []adminTeamMemberRow
	Profiles           []cloudstore.Profile
	Users              []adminUserOption
	UpdateURL          string // PUT /admin/teams/{id}
	DeleteURL          string
	AddProjectTemplate string // /admin/teams/{id}/projects/{project}
	AddMemberTemplate  string // /admin/teams/{id}/members/{account_id}
}

type adminProjectRow struct {
	Project      string
	DisplayName  string
	Kind         string
	MetaURL      string // PUT /admin/projects/{project}/meta
	SyncEnabled  bool   // OBL-04: false when the project's sync is paused
	PausedReason string // set only when SyncEnabled is false
	PauseURL     string // POST /admin/projects/{project}/pause
	ResumeURL    string // POST /admin/projects/{project}/resume

	// Command Center v2, Slice 4b: card stats + reverse-access. Populated only
	// when the store supports projectAdminStatsStore — zero values render as
	// "—" in the card (see admin_teams_ui.templ), never a crash.
	Slug         string // DOM-safe id fragment (accessRowSlug), for the kebab menu + access-expand element ids
	MemoryCount  int    // total synced memories (SUM observations_count)
	SourceCount  int    // distinct contributing sync clients (COUNT DISTINCT created_by)
	AccessCount  int    // accounts with effective Read access (the "N con acceso" stat)
	LastActivity string // humanized/relative (ui.RelativeTimeLang); "" when unknown
	AccessURL    string // GET /admin/projects/{project}/access (lazy reverse-access fragment)

	// Command Center v2, Slice 5a: cloud sub-project linking. Populated only
	// when the store supports projectLinksAdminStore — a store without it
	// simply never sets ParentProject/SubProjectCount, so the card renders
	// exactly like before (no badge, no chip, no link/unlink menu items).
	// Children stay real, separate projects; this is metadata only, never a
	// chunk merge — see cloudstore.SetProjectParent.
	ParentProject    string   // "" when unlinked; the parent's project name otherwise
	SubProjectCount  int      // how many OTHER projects are linked under this one as parent
	LinkParentURL    string   // POST /admin/projects/{project}/parent (form field "parent")
	ClearParentURL   string   // POST /admin/projects/{project}/parent/clear
	ParentCandidates []string // valid parent choices for the "Link to parent project…" select, excluding self

	// Command Center v2, Slice 5b: parent roll-up. RollupMemoryCount/
	// RollupSourceCount/RollupLastActivity are what the card actually
	// DISPLAYS — for a project with no linked children they equal the own
	// MemoryCount/SourceCount/LastActivity above unchanged; for a parent they
	// are own + the sum/max of its DIRECT children's own stats (see
	// projectRollupStats). Display-only aggregation — never mutates the
	// underlying data, and MemoryCount/SourceCount above stay the project's
	// OWN figures so the page header total (adminProjectsCountLabel) is never
	// double-counted by a parent's rolled-up display value.
	RollupMemoryCount  int
	RollupSourceCount  int
	RollupLastActivity string
}

// adminProjectGroupEntry is one top-level entry in the Admin Projects grid
// (Command Center v2, Slice 5b): either a standalone project (Children is
// empty) or a parent together with its direct linked children, rendered
// nested beneath it — see buildProjectGroups.
type adminProjectGroupEntry struct {
	Row      adminProjectRow
	Children []adminProjectRow
}

// adminProjectSuggestionRow is one entry in the Admin Projects page's
// suggestion banner (Command Center v2, Slice 5a): "Suggested: link {Child}
// under {Parent}", with a one-click confirm (posts ConfirmURL with the
// parent prefilled) and a client-side-only dismiss (the suggestion is
// recomputed from current data on every page load — there is no persisted
// "dismissed" state to track).
type adminProjectSuggestionRow struct {
	Child      string
	Parent     string
	ConfirmURL string // POST /admin/projects/{child}/parent (form field "parent", prefilled)
}

// adminProjectsView is a single flat, alphabetically-ordered project grid
// (Command Center v2, Slice 4b) — replacing the earlier Personal/Work/
// Unclassified section split. Each card already carries its own kind badge
// (see kindBadge), so the grouping added a redundant second dimension without
// showing anything a per-card badge doesn't; the mockup (view ⑤) confirms a
// single grid reads just as clearly at this project count.
type adminProjectsView struct {
	Props       ui.LayoutProps
	Projects    []adminProjectRow
	Suggestions []adminProjectSuggestionRow // Slice 5a: unlinked-but-look-related projects
	// Groups is what the template actually renders (Command Center v2, Slice
	// 5b): Projects grouped so a parent's direct children render nested
	// beneath it instead of scattered at their own alphabetical position.
	// Projects itself is kept flat (used by adminProjectsCountLabel's header
	// total, which must sum each project's OWN count exactly once).
	Groups []adminProjectGroupEntry
}

// ─── Profiles page ───────────────────────────────────────────────────────────

// handleAdminProfilesPage renders the operator Profiles page: every preset with its
// R/I/U/D bits, plus create/edit/delete controls.
func (s *CloudServer) handleAdminProfilesPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return
	}
	profiles, err := ts.ListProfiles(r.Context())
	if err != nil {
		http.Error(w, "could not list profiles", http.StatusInternalServerError)
		return
	}
	lang := i18n.LangFrom(r.Context())
	rows := make([]adminProfileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, toAdminProfileRow(lang, p))
	}
	view := adminProfilesView{Props: s.adminLayoutProps(r.Context(), "Admin · Profiles", "admin"), Profiles: rows}
	if err := adminProfilesPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func toAdminProfileRow(lang i18n.Lang, p cloudstore.Profile) adminProfileRow {
	perm := auth.Permission(p.Perms)
	id := url.PathEscape(p.ID)
	return adminProfileRow{
		ID:        p.ID,
		Name:      p.Name,
		Read:      perm.Has(auth.PermRead),
		Insert:    perm.Has(auth.PermInsert),
		Update:    perm.Has(auth.PermUpdate),
		Delete:    perm.Has(auth.PermDelete),
		Summary:   permSummary(lang, p.Perms),
		UpdateURL: "/admin/profiles/" + id,
		DeleteURL: "/admin/profiles/" + id,
	}
}

// ─── Teams page ──────────────────────────────────────────────────────────────

// handleAdminTeamsPage renders the operator Teams page: teams grouped Personal /
// Work, each linking to its detail, plus a create form.
func (s *CloudServer) handleAdminTeamsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return
	}
	teams, err := ts.ListTeams(r.Context())
	if err != nil {
		http.Error(w, "could not list teams", http.StatusInternalServerError)
		return
	}
	view := adminTeamsView{Props: s.adminLayoutProps(r.Context(), "Admin · Teams", "admin")}
	for _, t := range teams {
		row := adminTeamListRow{ID: t.ID, Name: t.Name, Kind: t.Kind, DetailURL: "/admin/teams/" + url.PathEscape(t.ID)}
		if projects, perr := ts.ListProjectsForTeam(r.Context(), t.ID); perr == nil {
			row.ProjectCount = len(projects)
		}
		if members, merr := ts.ListMembersOfTeam(r.Context(), t.ID); merr == nil {
			row.MemberCount = len(members)
		}
		if t.Kind == "personal" {
			view.Personal = append(view.Personal, row)
		} else {
			view.Work = append(view.Work, row)
		}
	}
	if err := adminTeamsPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminTeamDetailPage renders a single team: its projects (add via searchable
// selector / remove) and its members (add a user + profile / change profile /
// remove).
func (s *CloudServer) handleAdminTeamDetailPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	team, err := ts.GetTeam(r.Context(), id)
	if err != nil {
		http.Error(w, "could not load team", http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}

	kindByProject := s.projectKindMap(r.Context(), ts)
	escID := url.PathEscape(team.ID)

	projects, _ := ts.ListProjectsForTeam(r.Context(), id)
	projectRows := make([]adminTeamProjectRow, 0, len(projects))
	for _, p := range projects {
		projectRows = append(projectRows, adminTeamProjectRow{
			Project:     p,
			DisplayName: kindByProject[p].DisplayName,
			Kind:        kindByProject[p].Kind,
			RemoveURL:   "/admin/teams/" + escID + "/projects/" + url.PathEscape(p),
		})
	}

	// Username lookup for member rows + the add-member account select.
	usernames := map[string]string{}
	var userOpts []adminUserOption
	if as, aok := s.adminStore(); aok {
		if users, uerr := as.ListUsers(r.Context()); uerr == nil {
			for _, u := range users {
				usernames[u.ID] = u.Username
				userOpts = append(userOpts, adminUserOption{ID: u.ID, Username: u.Username})
			}
		}
	}

	members, _ := ts.ListMembersOfTeam(r.Context(), id)
	memberLang := i18n.LangFrom(r.Context())
	memberRows := make([]adminTeamMemberRow, 0, len(members))
	for _, m := range members {
		name := usernames[m.AccountID]
		if name == "" {
			name = m.AccountID
		}
		memberRows = append(memberRows, adminTeamMemberRow{
			AccountID: m.AccountID,
			Username:  name,
			ProfileID: m.ProfileID,
			Profile:   m.ProfileName,
			Summary:   permSummary(memberLang, m.Perms),
			UpdateURL: "/admin/teams/" + escID + "/members/" + url.PathEscape(m.AccountID),
			RemoveURL: "/admin/teams/" + escID + "/members/" + url.PathEscape(m.AccountID),
		})
	}

	profiles, _ := ts.ListProfiles(r.Context())

	view := adminTeamDetailView{
		Props:              s.adminLayoutProps(r.Context(), "Admin · "+team.Name, "admin"),
		ID:                 team.ID,
		Name:               team.Name,
		Kind:               team.Kind,
		Projects:           projectRows,
		Members:            memberRows,
		Profiles:           profiles,
		Users:              userOpts,
		UpdateURL:          "/admin/teams/" + escID,
		DeleteURL:          "/admin/teams/" + escID,
		AddProjectTemplate: "/admin/teams/" + escID + "/projects/{project}",
		AddMemberTemplate:  "/admin/teams/" + escID + "/members/{account_id}",
	}
	if err := adminTeamDetailPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// ─── Projects page ───────────────────────────────────────────────────────────

// handleAdminProjectsPage renders the known projects as cards (Command Center
// v2, Slice 4b): identity + classification + sync status, folded config
// actions behind a "•••" menu, and — when the store supports
// projectAdminStatsStore — content stats (memories, distinct sources, last
// activity) and the reverse "who has access" count. Stats degrade gracefully
// to zero values when the capability is absent, exactly like SyncEnabled
// already does for projectSyncControlAdminStore below.
func (s *CloudServer) handleAdminProjectsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	lang := i18n.LangFrom(r.Context())
	ts, ok := s.teamsStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "teams admin unavailable"})
		return
	}
	known, err := ts.KnownProjects(r.Context())
	if err != nil {
		http.Error(w, "could not list projects", http.StatusInternalServerError)
		return
	}
	pcs, hasSyncControls := s.projectSyncControlStore()
	pas, hasStats := s.projectAdminStatsStore()
	pls, hasLinks := s.projectLinksStore()

	var chunkStats map[string]cloudstore.ProjectChunkStats
	if hasStats {
		if m, serr := pas.ListProjectChunkStats(r.Context()); serr == nil {
			chunkStats = m
		}
	}

	// N+1 fix (2026-07-19 performance audit): one batched query for ALL known
	// projects' sync-control rows, instead of one GetProjectSyncControl call
	// per project inside the loop below. Mirrors the chunkStats pattern above.
	var syncControls map[string]cloudstore.ProjectSyncControl
	if hasSyncControls {
		if m, serr := pcs.ListProjectSyncControlsMap(r.Context()); serr == nil {
			syncControls = m
		}
	}

	// Slice 5a: parent/child links + the reverse sub-count + the name-prefix
	// suggestion engine. All three degrade to zero values when the store
	// doesn't support linking — see projectLinksStore's doc comment.
	var parents map[string]string       // child -> parent
	subCounts := map[string]int{}       // parent -> count of linked children
	childrenOf := map[string][]string{} // parent -> sorted []child (Slice 5b: roll-up + grouping)
	var suggestions map[string]string
	if hasLinks {
		if m, lerr := pls.ListProjectParents(r.Context()); lerr == nil {
			parents = m
			for child, parent := range parents {
				subCounts[parent]++
				childrenOf[parent] = append(childrenOf[parent], child)
			}
			for parent := range childrenOf {
				sort.Strings(childrenOf[parent])
			}
			names := make([]string, 0, len(known))
			for _, k := range known {
				names = append(names, k.Project)
			}
			suggestions = cloudstore.SuggestProjectParents(names, parents)
		}
	}
	childSet := make(map[string]struct{}, len(parents))
	for child := range parents {
		childSet[child] = struct{}{}
	}

	view := adminProjectsView{Props: s.adminLayoutProps(r.Context(), "Admin · Projects", "admin")}
	for _, k := range known {
		escProject := url.PathEscape(k.Project)
		row := adminProjectRow{
			Project:        k.Project,
			DisplayName:    k.DisplayName,
			Kind:           k.Kind,
			MetaURL:        "/admin/projects/" + escProject + "/meta",
			SyncEnabled:    true, // OBL-04 default: absent control row = enabled
			PauseURL:       "/admin/projects/" + escProject + "/pause",
			ResumeURL:      "/admin/projects/" + escProject + "/resume",
			Slug:           accessRowSlug(k.Project),
			AccessURL:      "/admin/projects/" + escProject + "/access",
			LinkParentURL:  "/admin/projects/" + escProject + "/parent",
			ClearParentURL: "/admin/projects/" + escProject + "/parent/clear",
		}
		if ctrl, ok := syncControls[k.Project]; ok {
			row.SyncEnabled = ctrl.SyncEnabled
			if ctrl.PausedReason != nil {
				row.PausedReason = *ctrl.PausedReason
			}
		}
		cs, csok := chunkStats[k.Project]
		if csok {
			row.MemoryCount = cs.MemoryCount
			row.SourceCount = cs.SourceCount
			if !cs.LastActivity.IsZero() {
				row.LastActivity = ui.RelativeTimeLang(cs.LastActivity, lang)
			}
		}
		if hasStats {
			if accessRows, aerr := pas.ListAccountAccessForProject(r.Context(), k.Project); aerr == nil {
				row.AccessCount = projectAccessCountLabel(accessRows)
			}
		}
		if hasLinks {
			row.ParentProject = parents[k.Project]
			row.SubProjectCount = subCounts[k.Project]
			row.ParentCandidates = validParentCandidates(known, childSet, k.Project)
		}

		// Slice 5b: roll-up. Composes the SAME chunkStats map already fetched
		// above with the SAME childrenOf map built from ListProjectParents —
		// no new store query. A project with no children gets Rollup*==own
		// unchanged (projectRollupStats(cs, nil) is a no-op).
		var childStats []cloudstore.ProjectChunkStats
		for _, child := range childrenOf[k.Project] {
			if s, ok := chunkStats[child]; ok {
				childStats = append(childStats, s)
			}
		}
		rollup := projectRollupStats(cs, childStats)
		row.RollupMemoryCount = rollup.MemoryCount
		row.RollupSourceCount = rollup.SourceCount
		if !rollup.LastActivity.IsZero() {
			row.RollupLastActivity = ui.RelativeTimeLang(rollup.LastActivity, lang)
		}

		view.Projects = append(view.Projects, row)
	}
	if hasLinks {
		view.Suggestions = adminProjectSuggestionRows(suggestions)
	}
	view.Groups = buildProjectGroups(view.Projects, childrenOf)
	if err := adminProjectsPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// projectRollupStats computes the ROLLED-UP (own + direct children) stats a
// parent's Admin Projects card displays (Command Center v2, Slice 5b): only
// ONE level of children is ever summed — the underlying 2-level hierarchy
// model (cloudstore.SetProjectParent) guarantees a child is never itself a
// parent, so there is nothing to recurse into. A project with no children
// (children is nil/empty) gets back its own stats completely unchanged.
// Memory/source counts are a plain sum (no dedup — a synced chunk always
// belongs to exactly one project); LastActivity is the max across own and
// every child.
func projectRollupStats(own cloudstore.ProjectChunkStats, children []cloudstore.ProjectChunkStats) cloudstore.ProjectChunkStats {
	out := own
	for _, c := range children {
		out.MemoryCount += c.MemoryCount
		out.SourceCount += c.SourceCount
		if c.LastActivity.After(out.LastActivity) {
			out.LastActivity = c.LastActivity
		}
	}
	return out
}

// buildProjectGroups arranges rows (already alphabetically ordered by
// handleAdminProjectsPage's KnownProjects call) into the top-level entries
// the Projects grid actually renders (Command Center v2, Slice 5b): a row
// whose ParentProject is itself a KNOWN row is nested under that parent's
// entry instead of appearing as its own top-level entry — visually grouping
// children directly beneath their parent rather than scattered at their own
// alphabetical position in the flat grid. A row whose recorded parent is NOT
// among rows (e.g. the parent has no cloud_project_meta of its own — an edge
// case the 2-level model doesn't otherwise prevent) still renders standalone
// rather than being silently dropped.
func buildProjectGroups(rows []adminProjectRow, childrenOf map[string][]string) []adminProjectGroupEntry {
	byProject := make(map[string]adminProjectRow, len(rows))
	for _, row := range rows {
		byProject[row.Project] = row
	}
	groups := make([]adminProjectGroupEntry, 0, len(rows))
	for _, row := range rows {
		if row.ParentProject != "" {
			if _, parentKnown := byProject[row.ParentProject]; parentKnown {
				continue // rendered nested under its parent's entry below
			}
		}
		entry := adminProjectGroupEntry{Row: row}
		for _, child := range childrenOf[row.Project] {
			if childRow, ok := byProject[child]; ok {
				entry.Children = append(entry.Children, childRow)
			}
		}
		groups = append(groups, entry)
	}
	return groups
}

// validParentCandidates lists the known projects that are valid parent
// choices for the "Link to parent project…" kebab action: any known project
// that is not itself already a child (mirrors SetProjectParent's
// parent-is-child rejection), excluding the project itself (self-ref).
// Sorted for a stable, predictable <select> order.
func validParentCandidates(known []cloudstore.KnownProject, childSet map[string]struct{}, self string) []string {
	out := make([]string, 0, len(known))
	for _, k := range known {
		if k.Project == self {
			continue
		}
		if _, isChild := childSet[k.Project]; isChild {
			continue
		}
		out = append(out, k.Project)
	}
	sort.Strings(out)
	return out
}

// adminProjectSuggestionRows converts the pure SuggestProjectParents result
// into a stably-ordered view slice (map iteration order is not stable), each
// carrying the confirm URL the banner's one-click button posts to.
func adminProjectSuggestionRows(suggestions map[string]string) []adminProjectSuggestionRow {
	out := make([]adminProjectSuggestionRow, 0, len(suggestions))
	for child, parent := range suggestions {
		out = append(out, adminProjectSuggestionRow{
			Child:      child,
			Parent:     parent,
			ConfirmURL: "/admin/projects/" + url.PathEscape(child) + "/parent",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Child < out[j].Child })
	return out
}

// ─── shared helpers ──────────────────────────────────────────────────────────

// projectKindMap indexes the known-projects classification by project name so page
// handlers can decorate a project with its kind + display name without a per-row
// query. Absent projects yield the zero KnownProject (empty kind).
func (s *CloudServer) projectKindMap(ctx context.Context, ts teamsAdminStore) map[string]cloudstore.KnownProject {
	out := map[string]cloudstore.KnownProject{}
	known, err := ts.KnownProjects(ctx)
	if err != nil {
		return out
	}
	for _, k := range known {
		out[k.Project] = k
	}
	return out
}

// teamDerivedForAccount computes the read-only "from teams" view for the Access
// page: for the selected account, the union (bit_or) of profile perms per project
// across every team the account belongs to, plus which teams grant them. It reads
// through ListTeamDerivedGrantsForAccount — ONE account-keyed query joining
// cloud_team_members/cloud_team_projects/cloud_profiles (N+1 fix, 2026-07-19
// performance audit; previously ListTeams + a per-team ListMembersOfTeam
// membership test + ListProjectsForTeam, one query per team IN THE ORG rather
// than per team the account belongs to). It does NOT re-implement the
// EffectivePerms resolver; it is a display projection of the same team-union
// layer. Projects are grouped personal / work / other by their classification.
// overridden marks projects that also carry a cloud_memberships override (shown
// above), so the UI can note that the override wins.
func (s *CloudServer) teamDerivedForAccount(ctx context.Context, ts teamsAdminStore, accountID string, overridden map[string]bool) (personal, work, other []adminTeamPermRow) {
	grants, err := ts.ListTeamDerivedGrantsForAccount(ctx, accountID)
	if err != nil {
		return nil, nil, nil
	}
	kindByProject := s.projectKindMap(ctx, ts)
	lang := i18n.LangFrom(ctx)

	agg := map[string]*adminTeamPermRow{}
	var order []string
	for _, g := range grants {
		row, exists := agg[g.Project]
		if !exists {
			row = &adminTeamPermRow{Project: g.Project, Kind: kindByProject[g.Project].Kind, Overridden: overridden[g.Project]}
			agg[g.Project] = row
			order = append(order, g.Project)
		}
		row.Perms |= g.Perms
		row.Sources = append(row.Sources, adminTeamPermSource{
			Team:    g.Team,
			Profile: g.Profile,
			Summary: permSummary(lang, g.Perms),
		})
	}
	sort.Strings(order)
	for _, proj := range order {
		row := agg[proj]
		p := auth.Permission(row.Perms)
		row.Read = p.Has(auth.PermRead)
		row.Insert = p.Has(auth.PermInsert)
		row.Update = p.Has(auth.PermUpdate)
		row.Delete = p.Has(auth.PermDelete)
		row.Summary = permSummary(lang, row.Perms)
		switch row.Kind {
		case "personal":
			personal = append(personal, *row)
		case "work":
			work = append(work, *row)
		default:
			other = append(other, *row)
		}
	}
	return personal, work, other
}

// ─── Projects page label helpers (Command Center v2, Slice 4b) ──────────────

// projectCardTitle renders the Admin Projects card headline: the display
// name when the operator set one, otherwise the raw project name. The card's
// <small> subtitle always shows the raw project name regardless, so nothing
// is lost either way.
func projectCardTitle(row adminProjectRow) string {
	if strings.TrimSpace(row.DisplayName) != "" {
		return row.DisplayName
	}
	return row.Project
}

// rollupLastActivityLabel renders the card's "Last" stat from the ROLLED-UP
// value (Command Center v2, Slice 5b) — own last activity for a project with
// no children, or the max across own+children for a parent. Falls back to an
// em dash when there is no activity at all (own or rolled-up), exactly like
// the pre-Slice-5b card did for the (now display-superseded) own LastActivity.
func rollupLastActivityLabel(row adminProjectRow) string {
	if row.RollupLastActivity == "" {
		return "—"
	}
	return row.RollupLastActivity
}

// subProjectCountLabel renders the Admin Projects card's sub-project count
// chip (Command Center v2, Slice 5a), e.g. "2 sub-projects" / "1
// sub-project". i18n Slice 3: takes lang — called from admin_teams_ui.templ
// (adminProjectCard), so ctx is implicit there.
func subProjectCountLabel(lang i18n.Lang, n int) string {
	word := i18n.T(lang, "admin.projects.subProjectPlural")
	if n == 1 {
		word = i18n.T(lang, "admin.projects.subProjectSingular")
	}
	return fmt.Sprintf("%d %s", n, word)
}

// adminProjectsCountLabel renders the Projects card header count, e.g.
// "9 projects · 132 memories" — mirrors the mockup's "9 · 132 memorias".
// i18n Slice 3: takes lang — called from admin_teams_ui.templ
// (adminProjectsPage), so ctx is implicit there.
func adminProjectsCountLabel(lang i18n.Lang, rows []adminProjectRow) string {
	total := 0
	for _, r := range rows {
		total += r.MemoryCount
	}
	word := i18n.T(lang, "admin.projects.countPlural")
	if len(rows) == 1 {
		word = i18n.T(lang, "admin.projects.countSingular")
	}
	return fmt.Sprintf("%d %s · %d %s", len(rows), word, total, i18n.T(lang, "admin.projects.memories"))
}
