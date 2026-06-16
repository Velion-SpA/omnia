package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/engramdb"
)

// Config holds the runtime configuration for the dashboard server.
type Config struct {
	Port      int
	EngramURL string
	// Actor is the provisional identity used in audit log entries.
	// Resolution order: X-Omnia-Actor request header → this field → USER env var → "unknown".
	// This is PROVISIONAL — replaced by per-user tokens in the Omnia gateway phase.
	Actor string
	// Projects is the explicit list of Engram projects to surface in the UI.
	// knownProjects() always includes "omnia" and any route targets from Routes
	// in addition to this slice. Duplicates are removed; result is sorted.
	Projects []string
	// Routes is the raw config.Routes map (key = "github/{owner}/{repo}" etc.,
	// value = Engram project name). The dashboard uses it to surface every
	// routing target as a known project automatically.
	Routes map[string]string
	// EngramDataDir is the directory containing engram.db (passed to engramdb.Open).
	// Resolution order: this field → $ENGRAM_DATA_DIR → ~/.engram.
	// If the DB cannot be opened, the dashboard logs a warning and falls back to
	// HTTP/FTS for structural queries — the dashboard runs without the DB.
	EngramDataDir string
	// ProjectHidden is the list of canonical project names to exclude from all dashboard views.
	// Values are canonicalized (lowercase+trim + alias lookup) before matching.
	// Populated from ~/.config/omnia/config.yaml's project_hidden list.
	ProjectHidden []string
	// ProjectAliases is an optional map of raw project name → canonical name for
	// non-case-fold merges. Leave empty; populated from project_aliases in config.yaml.
	ProjectAliases map[string]string
	// ProjectGroups is the project_groups map from config.yaml.
	ProjectGroups map[string][]string
}

// Server is the Omnia dashboard HTTP server.
type Server struct {
	cfg    Config
	client *engramClient
	db     *engramdb.DB // nil when unavailable; dashboard falls back to HTTP/FTS
	logger *slog.Logger
	groups *GroupIndex
}

// NewServer creates a new dashboard Server.
// It attempts to open the Engram SQLite DB for structural queries; if that
// fails it logs a warning and the dashboard continues using HTTP/FTS.
func NewServer(cfg Config, logger *slog.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		client: newEngramClient(cfg.EngramURL),
		logger: logger,
	}
	s.groups = newGroupIndex(cfg.ProjectGroups, logger)
	db, err := engramdb.Open(cfg.EngramDataDir)
	if err != nil {
		logger.Warn("engramdb unavailable; structural queries fall back to HTTP/FTS", "err", err)
	} else {
		s.db = db
	}
	return s
}

// Start binds to localhost and serves until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	fmt.Printf("Omnia dashboard: http://%s\n", addr)
	s.logger.Info("dashboard listening", "addr", addr, "engram", s.cfg.EngramURL)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// registerRoutes sets up all HTTP routes.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Pages
	mux.HandleFunc("GET /", s.handleOverview)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /detail/{id}", s.handleDetail)
	mux.HandleFunc("GET /sync", s.handleSyncStatus)
	mux.HandleFunc("GET /activity", s.handleActivity)

	// HTMX API fragments
	mux.HandleFunc("GET /api/obs/{id}/edit-form", s.handleEditForm)
	mux.HandleFunc("GET /api/obs/{id}/edit-cancel", s.handleEditCancel)
	mux.HandleFunc("PATCH /api/obs/{id}", s.handlePatch)
	mux.HandleFunc("GET /api/obs/{id}/delete-confirm", s.handleDeleteConfirm)
	mux.HandleFunc("GET /api/obs/{id}/delete-cancel", s.handleDeleteCancel)
	mux.HandleFunc("DELETE /api/obs/{id}", s.handleDelete)
}

// --- Page handlers ---

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	engUp := s.client.Health(ctx) == nil
	syncStatus := loadSyncStatus()

	// Prefer the structural DB path for exact counts and type breakdowns.
	var stats []ProjectStats
	if s.db != nil {
		stats = s.overviewStatsFromDB(ctx, syncStatus)
	}

	if stats == nil {
		// FTS fallback: DB unavailable or all DB queries failed.
		projectNames := knownProjectsCanonical(syncStatus, s.cfg)
		hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)
		projectNames = filterHidden(projectNames, hidden)
		// Exclude group children from top-level overview — they are shown inside the parent card.
		projectNames = filterGroupChildren(projectNames, s.groups)
		for _, proj := range projectNames {
			var views []ObsView
			var err error
			if s.groups.IsParent(proj) {
				// Load parent's own obs plus each child's obs, then deduplicate.
				views, err = loadProjectObs(ctx, s.client, proj, 200)
				if err == nil {
					for _, child := range s.groups.Children(proj) {
						childViews, childErr := loadProjectObs(ctx, s.client, child, 200)
						if childErr == nil {
							views = append(views, childViews...)
						}
					}
					views = dedupeViews(views)
				}
			} else {
				views, err = loadProjectObs(ctx, s.client, proj, 200)
			}
			if err != nil {
				s.logger.Warn("failed to load project obs", "project", proj, "err", err)
				views = nil
			}
			if s.groups.IsParent(proj) {
				stats = append(stats, computeGroupProjectStats(proj, views, s.groups, s.cfg.ProjectAliases))
			} else {
				stats = append(stats, computeProjectStats(proj, views))
			}
		}
	}

	if err := overviewPage(stats, engUp, syncStatus).Render(ctx, w); err != nil {
		s.logger.Error("render overview", "err", err)
	}
}

// overviewStatsFromDB builds ProjectStats for all projects using the SQLite DB.
// Returns nil if db.Projects fails, signaling the caller to fall back to FTS.
//
// It builds a canonical→rawNames map in one db.Projects call so that aliased
// canonicals (e.g. "velion") retrieve ALL raw variants via SQL IN rather than
// LOWER(TRIM(project)) = ?, which would miss structurally different raw names
// like "01.- velion".
func (s *Server) overviewStatsFromDB(ctx context.Context, syncStatus SyncStatus) []ProjectStats {
	canonicalize := canonicalizerFunc(s.cfg.ProjectAliases)
	hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)

	rawAll, err := s.db.Projects(ctx)
	if err != nil {
		s.logger.Warn("engramdb.Projects failed", "err", err)
		return nil
	}

	// Build canonical → raw names map so List queries use RawProjects (exact IN)
	// instead of CanonicalProject (LOWER(TRIM) = ?) — the latter misses aliased
	// raw names that don't reduce to the canonical via case-fold alone.
	type canonInfo struct {
		rawNames []string
	}
	canonMap := make(map[string]*canonInfo, len(rawAll))
	for _, pc := range rawAll {
		key := canonicalize(pc.Name)
		if canonMap[key] == nil {
			canonMap[key] = &canonInfo{}
		}
		canonMap[key].rawNames = append(canonMap[key].rawNames, pc.Name)
	}

	// Build a flat []string of all raw DB project names for group expansion.
	rawAllNames := make([]string, len(rawAll))
	for i, pc := range rawAll {
		rawAllNames[i] = pc.Name
	}

	dbNames := make([]string, 0, len(canonMap))
	for name := range canonMap {
		dbNames = append(dbNames, name)
	}
	merged := mergeProjectNames(dbNames, knownProjectsCanonical(syncStatus, s.cfg))
	merged = filterHidden(merged, hidden)
	// Exclude group children from top-level overview — they appear inside the parent card.
	merged = filterGroupChildren(merged, s.groups)

	stats := make([]ProjectStats, 0, len(merged))
	for _, proj := range merged {
		f := engramdb.Filter{Limit: 2000}
		if s.groups.IsParent(proj) {
			// For a group parent, fetch observations for the whole group
			// (parent raw names + all children raw names) in a single SQL IN.
			groupRaw := s.groups.groupRawNames(proj, rawAllNames, s.cfg.ProjectAliases)
			if len(groupRaw) > 0 {
				f.RawProjects = groupRaw
			} else {
				f.CanonicalProject = proj
			}
		} else if info, ok := canonMap[proj]; ok && len(info.rawNames) > 0 {
			// Use exact IN for aliased canonicals — CanonicalProject would miss
			// raw names that don't case-fold to the canonical (e.g. "01.- velion").
			f.RawProjects = info.rawNames
		} else {
			// Config-only project (not yet in DB); CanonicalProject covers any
			// future case-only variants written into the DB later.
			f.CanonicalProject = proj
		}
		dbObs, err := s.db.List(ctx, f)
		if err != nil {
			s.logger.Warn("engramdb.List failed for project", "project", proj, "err", err)
			dbObs = nil
		}
		views := make([]ObsView, len(dbObs))
		for i, o := range dbObs {
			views[i] = enrichObs(obsFromDB(o))
		}
		if s.groups.IsParent(proj) {
			stats = append(stats, computeGroupProjectStats(proj, views, s.groups, s.cfg.ProjectAliases))
		} else {
			stats = append(stats, computeProjectStats(proj, views))
		}
	}
	return stats
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	params := BrowseParams{
		Project: q.Get("project"),
		Sub:     q.Get("sub"),
		Source:  q.Get("source"),
		Kind:    q.Get("kind"),
		Type:    q.Get("type"),
		Query:   q.Get("q"),
	}

	syncStatus := loadSyncStatus()
	// Sidebar project list: union of DB projects + config projects.
	projectNames := s.effectiveProjectNames(ctx, syncStatus)

	// Build group nav when the selected project is a group parent.
	var groupNav *GroupNav
	if s.groups.IsParent(params.Project) {
		items := []GroupNavItem{
			{
				Sub:      "",
				Label:    "All",
				URL:      "/browse?project=" + params.Project,
				IsActive: params.Sub == "",
			},
			{
				Sub:      "core",
				Label:    params.Project + " (core)",
				URL:      "/browse?project=" + params.Project + "&sub=core",
				IsActive: params.Sub == "core",
			},
		}
		for _, child := range s.groups.Children(params.Project) {
			items = append(items, GroupNavItem{
				Sub:      child,
				Label:    child,
				URL:      "/browse?project=" + params.Project + "&sub=" + child,
				IsActive: params.Sub == child,
			})
		}
		groupNav = &GroupNav{
			Parent:    params.Project,
			ActiveSub: params.Sub,
			Items:     items,
		}
	}

	// Structural path: DB available and no free-text query.
	// Project and Type filters are pushed to SQL; Source and Kind are applied
	// client-side (they live in the parsed omnia-meta block, not DB columns).
	if s.db != nil && params.Query == "" {
		views, types, ok := s.browseFromDB(ctx, params)
		if ok {
			if err := browsePage(params, views, projectNames, types, groupNav).Render(ctx, w); err != nil {
				s.logger.Error("render browse", "err", err)
			}
			return
		}
		s.logger.Warn("engramdb browse failed; falling back to HTTP/FTS")
	}

	// FTS fallback: free-text query, or DB unavailable / errored.
	// For free-text queries this is correct behavior — FTS is exactly what they need.
	// For structural queries it is a best-effort workaround (see loadProjectObs).
	var allViews []ObsView
	switch {
	case params.Query != "":
		// Free-text search within the (optional) project.
		raw, err := s.client.Search(ctx, params.Query, params.Project, 300)
		if err != nil {
			s.logger.Warn("browse search failed", "err", err)
		}
		for _, o := range raw {
			allViews = append(allViews, enrichObs(o))
		}
	case params.Project != "":
		// No query, project selected.
		if s.groups.IsParent(params.Project) {
			switch params.Sub {
			case "core":
				// Parent's own observations only.
				v, err := loadProjectObs(ctx, s.client, params.Project, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Project, "err", err)
				}
				allViews = v
			case "":
				// All: parent + all children, deduplicated.
				v, err := loadProjectObs(ctx, s.client, params.Project, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Project, "err", err)
				}
				allViews = v
				for _, child := range s.groups.Children(params.Project) {
					childViews, childErr := loadProjectObs(ctx, s.client, child, 300)
					if childErr == nil {
						allViews = append(allViews, childViews...)
					}
				}
				allViews = dedupeViews(allViews)
			default:
				// A specific child canonical.
				v, err := loadProjectObs(ctx, s.client, params.Sub, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Sub, "err", err)
				}
				allViews = v
			}
		} else {
			// Non-group project: load normally.
			v, err := loadProjectObs(ctx, s.client, params.Project, 300)
			if err != nil {
				s.logger.Warn("browse project load failed", "err", err)
			}
			allViews = v
		}
	default:
		// No query, no project: ingested observations across all projects.
		raw, err := s.client.Search(ctx, ingestedTerm, "", 300)
		if err != nil {
			s.logger.Warn("browse search failed", "err", err)
		}
		for _, o := range raw {
			allViews = append(allViews, enrichObs(o))
		}
	}

	// Compute distinct types from the full loaded set (before client-side filters)
	// so the Category dropdown shows every available type in the current context.
	types := distinctTypes(allViews)

	views := make([]ObsView, 0, len(allViews))
	for _, v := range allViews {
		if params.Source != "" && v.Meta.Source != params.Source {
			continue
		}
		if params.Kind != "" && v.Meta.Kind != params.Kind {
			continue
		}
		if params.Type != "" && v.Obs.Type != params.Type {
			continue
		}
		views = append(views, v)
	}

	if err := browsePage(params, views, projectNames, types, groupNav).Render(ctx, w); err != nil {
		s.logger.Error("render browse", "err", err)
	}
}

// effectiveProjectNames returns the sorted, deduplicated union of DB-derived
// canonical project names and config-derived names, with hidden projects removed.
// Falls back to config-only when the DB is unavailable or its query fails.
func (s *Server) effectiveProjectNames(ctx context.Context, syncStatus SyncStatus) []string {
	canonicalize := canonicalizerFunc(s.cfg.ProjectAliases)
	hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)
	cfgNames := knownProjectsCanonical(syncStatus, s.cfg)
	if s.db == nil {
		return filterHidden(cfgNames, hidden)
	}
	dbProjects, err := s.db.ProjectsCanonical(ctx, canonicalize)
	if err != nil {
		s.logger.Warn("engramdb.ProjectsCanonical failed in effectiveProjectNames", "err", err)
		return filterHidden(cfgNames, hidden)
	}
	dbNames := make([]string, 0, len(dbProjects))
	for _, pc := range dbProjects {
		dbNames = append(dbNames, pc.Name)
	}
	merged := mergeProjectNames(dbNames, cfgNames)
	return filterHidden(merged, hidden)
}

// expandCanonical returns all raw DB project names that canonicalize to the
// given canonical name using the configured alias map. This is used to build
// the RawProjects filter for List queries so that aliased canonicals fetch ALL
// their raw variants (e.g. "velion" → ["01.- velion", "01.- Velion", "velion"]).
// Returns nil (not an error) when the DB has no raw names for that canonical —
// callers fall back to CanonicalProject in that case.
func (s *Server) expandCanonical(ctx context.Context, canonical string) ([]string, error) {
	raw, err := s.db.Projects(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(raw))
	for i, pc := range raw {
		names[i] = pc.Name
	}
	return rawProjectsForCanonical(canonical, names, s.cfg.ProjectAliases), nil
}

// browseFromDB loads and enriches observations for the browse page using the
// SQLite DB. Project (expanded to raw names) and Type are pushed to SQL;
// Source and Kind are applied client-side after meta.Parse.
// Returns (nil, nil, false) on any DB error so the caller can fall back.
//
// When a project is selected, expandCanonical resolves the canonical to its raw
// DB project names and passes them via RawProjects (exact IN). This ensures that
// aliased canonicals like "velion" retrieve "01.- velion" and "01.- Velion" rows
// that CanonicalProject's LOWER(TRIM) = ? would miss.
func (s *Server) browseFromDB(ctx context.Context, params BrowseParams) ([]ObsView, []string, bool) {
	f := engramdb.Filter{
		Type:  params.Type,
		Limit: 1000,
	}
	if params.Project != "" {
		if s.groups.IsParent(params.Project) {
			// For group parents, load the raw DB project name list once and
			// apply the sub-project filter to determine which raw names to query.
			rawAll, err := s.db.Projects(ctx)
			if err != nil {
				s.logger.Warn("engramdb.Projects failed in group browse", "err", err)
				return nil, nil, false
			}
			rawAllNames := make([]string, len(rawAll))
			for i, pc := range rawAll {
				rawAllNames[i] = pc.Name
			}
			switch params.Sub {
			case "core":
				// Parent's own raw names only (no children).
				rawNames := s.groups.coreRawNames(params.Project, rawAllNames, s.cfg.ProjectAliases)
				if len(rawNames) > 0 {
					f.RawProjects = rawNames
				} else {
					f.CanonicalProject = params.Project
				}
			case "":
				// All: parent + all children raw names.
				rawNames := s.groups.groupRawNames(params.Project, rawAllNames, s.cfg.ProjectAliases)
				if len(rawNames) > 0 {
					f.RawProjects = rawNames
				} else {
					f.CanonicalProject = params.Project
				}
			default:
				// A specific child canonical.
				rawNames := rawProjectsForCanonical(params.Sub, rawAllNames, s.cfg.ProjectAliases)
				if len(rawNames) > 0 {
					f.RawProjects = rawNames
				} else {
					f.CanonicalProject = params.Sub
				}
			}
		} else {
			rawNames, err := s.expandCanonical(ctx, params.Project)
			if err != nil {
				s.logger.Warn("engramdb.expandCanonical failed", "err", err)
				return nil, nil, false
			}
			if len(rawNames) > 0 {
				f.RawProjects = rawNames
			} else {
				// Project exists in config but not yet in DB; CanonicalProject
				// handles future case-only variants correctly.
				f.CanonicalProject = params.Project
			}
		}
	}

	dbObs, err := s.db.List(ctx, f)
	if err != nil {
		s.logger.Warn("engramdb.List failed", "err", err)
		return nil, nil, false
	}

	// Enrich and apply meta-only filters (Source, Kind) client-side.
	views := make([]ObsView, 0, len(dbObs))
	for _, o := range dbObs {
		v := enrichObs(obsFromDB(o))
		if params.Source != "" && v.Meta.Source != params.Source {
			continue
		}
		if params.Kind != "" && v.Meta.Kind != params.Kind {
			continue
		}
		views = append(views, v)
	}

	// Types for the Category dropdown: derive from the loaded view set when a
	// project is selected (canonical match may span multiple raw names);
	// query the DB globally otherwise.
	var types []string
	if params.Project != "" {
		types = distinctTypes(views)
	} else {
		dbTypes, _ := s.db.Types(ctx)
		for _, tc := range dbTypes {
			types = append(types, tc.Name)
		}
		sort.Strings(types)
	}

	return views, types, true
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	obs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	v := enrichObs(*obs)
	backURL := r.Header.Get("Referer")
	if backURL == "" {
		backURL = "/browse"
	}

	var lastAudit *audit.Entry
	if entries, err := audit.EntriesForObservation(id); err == nil && len(entries) > 0 {
		lastAudit = &entries[0]
	}

	if err := detailPage(v, backURL, lastAudit).Render(ctx, w); err != nil {
		s.logger.Error("render detail", "err", err)
	}
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := loadSyncStatus()
	if err := syncStatusPage(status).Render(ctx, w); err != nil {
		s.logger.Error("render sync status", "err", err)
	}
}

// --- HTMX fragment handlers ---

func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	obs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	v := enrichObs(*obs)
	if err := editForm(v).Render(ctx, w); err != nil {
		s.logger.Error("render edit form", "err", err)
	}
}

func (s *Server) handleEditCancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := editToggleButton(id).Render(ctx, w); err != nil {
		s.logger.Error("render edit cancel", "err", err)
	}
}

func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Read old observation to capture project and old title for audit.
	oldObs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	oldTitle := oldObs.Title
	project := oldObs.Project

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	content := r.FormValue("content")
	obsType := r.FormValue("type")

	actor := s.resolveActor(r)

	if err := s.client.PatchObservation(ctx, id, title, content, obsType); err != nil {
		s.logger.Error("patch observation", "id", id, "err", err)
		audit.Append(audit.Entry{
			Ts:            audit.Now(),
			Actor:         actor,
			Action:        audit.ActionEdit,
			ObservationID: id,
			Project:       project,
			Summary:       truncateSummary(fmt.Sprintf("title: %s → %s", oldTitle, title), 120),
			Result:        "error",
		})
		errMsg := fmt.Sprintf("Failed to save: %v", err)
		if rErr := errorBanner(errMsg).Render(ctx, w); rErr != nil {
			s.logger.Error("render error banner", "err", rErr)
		}
		return
	}

	audit.Append(audit.Entry{
		Ts:            audit.Now(),
		Actor:         actor,
		Action:        audit.ActionEdit,
		ObservationID: id,
		Project:       project,
		Summary:       truncateSummary(fmt.Sprintf("title: %s → %s", oldTitle, title), 120),
		Result:        "ok",
	})

	if err := editSuccess(id).Render(ctx, w); err != nil {
		s.logger.Error("render edit success", "err", err)
	}
}

func (s *Server) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	obs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	if err := deleteConfirm(id, obs.Title).Render(ctx, w); err != nil {
		s.logger.Error("render delete confirm", "err", err)
	}
}

func (s *Server) handleDeleteCancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	obs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	if err := deleteButton(id, obs.Title).Render(ctx, w); err != nil {
		s.logger.Error("render delete cancel", "err", err)
	}
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	hard := strings.ToLower(r.URL.Query().Get("hard")) == "true"

	// Read old observation to capture project and title for audit.
	oldObs, err := s.client.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	obsTitle := oldObs.Title
	project := oldObs.Project

	actor := s.resolveActor(r)
	action := audit.ActionSoftDelete
	if hard {
		action = audit.ActionHardDelete
	}

	if err := s.client.DeleteObservation(ctx, id, hard); err != nil {
		s.logger.Error("delete observation", "id", id, "err", err)
		audit.Append(audit.Entry{
			Ts:            audit.Now(),
			Actor:         actor,
			Action:        action,
			ObservationID: id,
			Project:       project,
			Summary:       truncateSummary(obsTitle, 80),
			Result:        "error",
		})
		errMsg := fmt.Sprintf("Failed to delete: %v", err)
		if rErr := errorBanner(errMsg).Render(ctx, w); rErr != nil {
			s.logger.Error("render error banner", "err", rErr)
		}
		return
	}

	audit.Append(audit.Entry{
		Ts:            audit.Now(),
		Actor:         actor,
		Action:        action,
		ObservationID: id,
		Project:       project,
		Summary:       truncateSummary(obsTitle, 80),
		Result:        "ok",
	})

	if err := deleteSuccess(hard).Render(ctx, w); err != nil {
		s.logger.Error("render delete success", "err", err)
	}
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, err := audit.Read(200)
	if err != nil {
		s.logger.Warn("read audit log", "err", err)
	}
	if err := activityPage(entries).Render(ctx, w); err != nil {
		s.logger.Error("render activity", "err", err)
	}
}

// resolveActor returns the provisional identity for an HTTP request.
// Resolution order: X-Omnia-Actor header → cfg.Actor → USER env var → "unknown".
// NOTE: This is provisional identity only — no authentication. Replaced by
// per-user tokens in the Omnia gateway phase.
func (s *Server) resolveActor(r *http.Request) string {
	if v := r.Header.Get("X-Omnia-Actor"); v != "" {
		return v
	}
	if s.cfg.Actor != "" {
		return s.cfg.Actor
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "unknown"
}

// truncateSummary truncates a summary string to max runes.
func truncateSummary(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// parseID extracts the {id} path value and parses it as an integer.
func parseID(r *http.Request) (int, error) {
	raw := r.PathValue("id")
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id: %q", raw)
	}
	return id, nil
}
