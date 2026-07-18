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
	"github.com/velion/omnia/internal/ui"
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
	// Embeddings* configure the optional local semantic-search layer. When
	// EmbeddingsEnabled is false (default), the dashboard serves keyword (FTS)
	// search only. When true, NewServer opens Omnia's own embeddings store and an
	// Ollama client; a failure to open either degrades gracefully back to FTS.
	EmbeddingsEnabled bool
	EmbeddingsBaseURL string
	EmbeddingsModel   string
	EmbeddingsDim     int
	EmbeddingsDBPath  string
}

// Server is the Omnia dashboard HTTP server. It depends only on a DataSource, so
// the same server (and the same templ pages) runs over the local Engram stack or
// the cloud's replicated store — they differ ONLY in which DataSource is wired in.
//
// db, sem and mut are the optional capabilities resolved from the DataSource at
// construction time. They are nil when the backend does not provide them, and the
// handlers keep their original nil-checks (e.g. `if s.db != nil`) so behaviour is
// identical to when these were concrete *engramdb.DB / *embed.Store / client fields.
type Server struct {
	cfg     Config
	src     DataSource
	records RecordReader     // always present (single observation + keyword search)
	db      StructuralReader // nil → structural queries fall back to HTTP/FTS
	sem     SemanticIndex    // nil → semantic search/graph unavailable
	mut     MutationWriter   // nil → dashboard is read-only
	logger  *slog.Logger
	groups  *GroupIndex
}

// NewServer creates a new dashboard Server backed by the LOCAL Engram stack.
// It attempts to open the Engram SQLite DB for structural queries and the
// optional embeddings store; failures are logged and the dashboard continues with
// reduced capabilities (HTTP/FTS, no graph). cmd/omnia uses this unchanged.
func NewServer(cfg Config, logger *slog.Logger) *Server {
	return NewServerWithDataSource(cfg, newLocalDataSource(cfg, logger), logger)
}

// NewServerWithDataSource builds a Server over an arbitrary DataSource. The cloud
// wires its replicated-store DataSource here; everything else (routing, handlers,
// templ pages) is identical to the local dashboard.
func NewServerWithDataSource(cfg Config, src DataSource, logger *slog.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		src:    src,
		logger: logger,
	}
	s.groups = newGroupIndex(cfg.ProjectGroups, logger)
	s.records = src.Records()
	if sr, ok := src.Structural(); ok {
		s.db = sr
	}
	if si, ok := src.Semantic(); ok {
		s.sem = si
	}
	if mw, ok := src.Mutations(); ok {
		s.mut = mw
	}
	return s
}

// Handler returns the dashboard's HTTP handler with all routes registered. The
// cloud server mounts this directly (fronted by its own auth/session middleware),
// so both surfaces share identical routing.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
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
		err := srv.Shutdown(shutCtx)
		// Release backend resources (e.g. checkpoint SQLite WAL) on clean shutdown.
		_ = s.src.Close()
		return err
	case err := <-errCh:
		return err
	}
}

// registerRoutes sets up all HTTP routes.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Shared Omnia design-system assets (internal/ui) — the exact same CSS the cloud
	// dashboard serves, so both surfaces stay visually identical.
	mux.Handle("GET /static/", ui.StaticHandler("/static/"))

	// Pages
	mux.HandleFunc("GET /", s.handleOverview)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /detail/{id}", s.handleDetail)
	mux.HandleFunc("GET /sync", s.handleSyncStatus)
	mux.HandleFunc("GET /activity", s.handleActivity)
	mux.HandleFunc("GET /graph", s.handleGraph)

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

	engUp := s.src.Health(ctx) == nil
	syncStatus := loadSyncStatus()

	// Load project stats (existing logic preserved).
	var stats []ProjectStats
	if s.db != nil {
		stats = s.overviewStatsFromDB(ctx, syncStatus)
	}
	if stats == nil {
		projectNames := knownProjectsCanonical(syncStatus, s.cfg)
		hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)
		projectNames = filterHidden(projectNames, hidden)
		projectNames = filterGroupChildren(projectNames, s.groups)
		for _, proj := range projectNames {
			var views []ObsView
			var err error
			if s.groups.IsParent(proj) {
				views, err = loadProjectObs(ctx, s.records, proj, 200)
				if err == nil {
					for _, child := range s.groups.Children(proj) {
						childViews, childErr := loadProjectObs(ctx, s.records, child, 200)
						if childErr == nil {
							views = append(views, childViews...)
						}
					}
					views = dedupeViews(views)
				}
			} else {
				views, err = loadProjectObs(ctx, s.records, proj, 200)
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

	// Sort stats by count descending.
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Total > stats[j].Total
	})

	// Build OverviewData.
	canonicalize := canonicalizerFunc(s.cfg.ProjectAliases)

	// Per-project cloud placement (local surface only; read-only). A group parent
	// also surfaces any clouds its children are loaded on.
	cloudMap, showClouds := s.cloudsByProject(ctx, canonicalize)
	if showClouds {
		for i := range stats {
			clouds := cloudMap[canonicalize(stats[i].Name)]
			if stats[i].IsGroup {
				for _, child := range s.groups.Children(stats[i].Name) {
					clouds = mergeCloudPlacements(clouds, cloudMap[canonicalize(child)])
				}
			}
			stats[i].Clouds = clouds
		}
	}

	data := s.buildOverviewData(ctx, stats, syncStatus, engUp, canonicalize)
	data.ShowClouds = showClouds

	if err := overviewPage(data).Render(ctx, w); err != nil {
		s.logger.Error("render overview", "err", err)
	}
}

// buildOverviewData assembles the OverviewData struct from already-loaded stats,
// plus live feed and type breakdown from the DB (when available).
func (s *Server) buildOverviewData(ctx context.Context, stats []ProjectStats, syncStatus SyncStatus, engUp bool, canonicalize func(string) string) OverviewData {
	// Total memories = sum of project totals (group parents include children).
	var totalMem int
	for _, p := range stats {
		totalMem += p.Total
	}

	// Last sync: most recent cursor by timestamp.
	var lastSync, lastSyncSource string
	for _, c := range syncStatus.Cursors {
		if lastSync == "" || c.Timestamp > lastSync {
			lastSync = c.Timestamp
			lastSyncSource = c.Source
		}
	}
	lastSyncAge := ""
	if lastSync != "" {
		// Use the Age already computed by loadSyncStatus.
		for _, c := range syncStatus.Cursors {
			if c.Timestamp == lastSync {
				lastSyncAge = c.Age
				break
			}
		}
	}

	// Type breakdown from DB (or aggregate from stats as fallback).
	var byType []TypeCount
	if s.db != nil {
		if types, err := s.db.Types(ctx); err == nil {
			byType = make([]TypeCount, len(types))
			for i, t := range types {
				byType[i] = TypeCount{Name: t.Name, Count: t.Count}
			}
		}
	}
	if byType == nil {
		// Aggregate from stats.
		agg := map[string]int{}
		for _, p := range stats {
			for t, cnt := range p.ByType {
				agg[t] += cnt
			}
		}
		byType = sortedTypeCounts(agg)
	}
	// Cap to the top 8 types — the Memory Types panel renders at most 8 bars.
	const maxTypeBars = 8
	if len(byType) > maxTypeBars {
		byType = byType[:maxTypeBars]
	}

	// Live feed: most recent observations from DB.
	var liveFeed []FeedItem
	if s.db != nil {
		obs, err := s.db.List(ctx, engramdb.Filter{Limit: 8})
		if err == nil {
			for _, o := range obs {
				title := o.Title
				if title == "" {
					// Truncate content as fallback title.
					title = o.Content
					if len([]rune(title)) > 80 {
						title = string([]rune(title)[:80]) + "…"
					}
				}
				liveFeed = append(liveFeed, FeedItem{
					ID:      o.ID,
					Title:   title,
					Type:    o.Type,
					Project: canonicalize(o.Project),
					Age:     formatAge(o.UpdatedAt),
				})
			}
		}
	}

	// Sources: aggregate from stats.
	var githubCount, discordCount, curatedCount int
	for _, p := range stats {
		githubCount += p.BySource["github"]
		discordCount += p.BySource["discord"]
		curatedCount += p.Curated
	}
	sources := []SourceStat{
		{Name: "GitHub", Sub: "PRs · commits · reviews", IconKey: "github", Count: githubCount},
		{Name: "Discord", Sub: "digests · threads · mentions", IconKey: "discord", Count: discordCount},
		{Name: "Claude Code", Sub: "sessions · fixes · decisions", IconKey: "claude", Count: curatedCount},
	}

	return OverviewData{
		Projects:       stats,
		TotalMemories:  totalMem,
		TotalProjects:  len(stats),
		LastSync:       lastSyncAge,
		LastSyncSource: lastSyncSource,
		ByType:         byType,
		LiveFeed:       liveFeed,
		Sources:        sources,
		EngUp:          engUp,
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
			s.renderBrowse(w, r, params, views, projectNames, types, groupNav)
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
		// Semantic search when the embeddings layer is available; FTS otherwise.
		if sem, ok := s.semanticSearch(ctx, params); ok {
			allViews = sem
		} else {
			raw, err := s.records.Search(ctx, params.Query, params.Project, 300)
			if err != nil {
				s.logger.Warn("browse search failed", "err", err)
			}
			for _, o := range raw {
				allViews = append(allViews, enrichObs(o))
			}
		}
	case params.Project != "":
		// No query, project selected.
		if s.groups.IsParent(params.Project) {
			switch params.Sub {
			case "core":
				// Parent's own observations only.
				v, err := loadProjectObs(ctx, s.records, params.Project, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Project, "err", err)
				}
				allViews = v
			case "":
				// All: parent + all children, deduplicated.
				v, err := loadProjectObs(ctx, s.records, params.Project, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Project, "err", err)
				}
				allViews = v
				for _, child := range s.groups.Children(params.Project) {
					childViews, childErr := loadProjectObs(ctx, s.records, child, 300)
					if childErr == nil {
						allViews = append(allViews, childViews...)
					}
				}
				allViews = dedupeViews(allViews)
			default:
				// A specific child canonical.
				v, err := loadProjectObs(ctx, s.records, params.Sub, 300)
				if err != nil {
					s.logger.Warn("browse project load failed", "project", params.Sub, "err", err)
				}
				allViews = v
			}
		} else {
			// Non-group project: load normally.
			v, err := loadProjectObs(ctx, s.records, params.Project, 300)
			if err != nil {
				s.logger.Warn("browse project load failed", "err", err)
			}
			allViews = v
		}
	default:
		// No query, no project: ingested observations across all projects.
		raw, err := s.records.Search(ctx, ingestedTerm, "", 300)
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

	s.renderBrowse(w, r, params, views, projectNames, types, groupNav)
}

// renderBrowse writes the browse response, branching on whether this is an
// htmx-driven filter change or a plain navigation. Requests carrying the
// HX-Request header (set automatically by htmx for every ajax call — see
// browseRegion's hx-get attributes) get just the #browse-region fragment;
// everything else — including a plain GET with query params typed/bookmarked
// directly, or any client with JS/htmx unavailable — gets the full page. Both
// render paths in handleBrowse (structural DB path and FTS/semantic
// fallback) funnel through here so the fragment/full-page decision is made
// in exactly one place.
func (s *Server) renderBrowse(w http.ResponseWriter, r *http.Request, params BrowseParams, views []ObsView, projectNames []string, types []string, groupNav *GroupNav) {
	ctx := r.Context()
	if r.Header.Get("HX-Request") == "true" {
		if err := browseRegion(params, views, projectNames, types, groupNav).Render(ctx, w); err != nil {
			s.logger.Error("render browse fragment", "err", err)
		}
		return
	}
	if err := browsePage(params, views, projectNames, types, groupNav).Render(ctx, w); err != nil {
		s.logger.Error("render browse", "err", err)
	}
}

// Semantic search tuning. Measured against the real store with nomic-embed-text:
// scores are compressed and every query — including pure gibberish — produces a
// long tail of weakly-related rows plateauing around 0.47–0.55. A 0.55 cosine
// floor cuts that tail (drops a 200-row result set to tens) without starving
// specific queries the way a stricter 0.60 floor would. maxResults caps the
// rendered feed so broad queries don't dump hundreds of cards.
const (
	semanticSearchK    = 150  // neighbors to pull from the store before filtering
	semanticMinScore   = 0.55 // cosine floor; below this is nomic's noise plateau
	semanticMaxResults = 50   // hard cap on rendered cards, ranked by similarity
)

// semanticSearch runs vector search for params.Query when the embeddings layer
// is available. It embeds the query, finds nearest neighbors in Omnia's own
// store, drops the weak-similarity tail below semanticMinScore, re-fetches the
// full rows from engram.db (for rich rendering), and preserves similarity
// ranking. When a project is selected it scopes results to that project
// (including a group parent's children) and caps the feed at semanticMaxResults.
// Returns (views, true) on success, or (nil, false) to fall back to FTS — on any
// nil dependency or error.
func (s *Server) semanticSearch(ctx context.Context, params BrowseParams) ([]ObsView, bool) {
	if s.sem == nil || s.db == nil {
		return nil, false
	}

	// Bound the interactive query embedding so a slow/down Ollama can't hang the
	// browse request for the client's full 60s timeout before FTS takes over.
	embCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	vec, err := s.sem.EmbedQuery(embCtx, params.Query)
	if err != nil {
		s.logger.Warn("semantic embed query failed; falling back to FTS", "err", err)
		return nil, false
	}
	hits, err := s.sem.Search(ctx, vec, semanticSearchK)
	if err != nil {
		s.logger.Warn("semantic search failed; falling back to FTS", "err", err)
		return nil, false
	}
	if len(hits) == 0 {
		// Empty store (not yet backfilled) — FTS is the better answer.
		return nil, false
	}

	// Drop the weak-similarity tail. Hits are score-descending, so truncate at
	// the first row below the floor. If nothing clears it, FTS is the better
	// answer than a page of barely-related rows.
	cut := len(hits)
	for i, h := range hits {
		if h.Score < semanticMinScore {
			cut = i
			break
		}
	}
	hits = hits[:cut]
	if len(hits) == 0 {
		return nil, false
	}

	ids := make([]int, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ObsID)
	}
	rows, err := s.db.ListByIDs(ctx, ids)
	if err != nil {
		s.logger.Warn("semantic ListByIDs failed; falling back to FTS", "err", err)
		return nil, false
	}
	byID := make(map[int]ObsView, len(rows))
	for _, o := range rows {
		byID[o.ID] = enrichObs(obsFromDB(o))
	}

	// Optional project scoping (canonical match; group parents include children).
	var keep func(project string) bool
	if params.Project != "" {
		canon := canonicalizerFunc(s.cfg.ProjectAliases)
		allowed := map[string]struct{}{canon(params.Project): {}}
		if s.groups.IsParent(params.Project) {
			for _, child := range s.groups.Children(params.Project) {
				allowed[canon(child)] = struct{}{}
			}
		}
		keep = func(p string) bool { _, ok := allowed[canon(p)]; return ok }
	}

	views := make([]ObsView, 0, semanticMaxResults)
	for _, h := range hits {
		v, ok := byID[h.ObsID]
		if !ok {
			continue
		}
		if keep != nil && !keep(v.Obs.Project) {
			continue
		}
		views = append(views, v)
		if len(views) >= semanticMaxResults {
			break
		}
	}
	if len(views) == 0 {
		// Project scope eliminated every hit — fall back to FTS instead of
		// rendering an empty page when keyword search might still match.
		return nil, false
	}
	return views, true
}

// effectiveProjectNames returns the sorted, deduplicated union of DB-derived
// canonical project names and config-derived names, with hidden projects removed.
// Falls back to config-only when the DB is unavailable or its query fails.
func (s *Server) effectiveProjectNames(ctx context.Context, syncStatus SyncStatus) []string {
	canonicalize := canonicalizerFunc(s.cfg.ProjectAliases)
	hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)
	cfgNames := knownProjectsCanonical(syncStatus, s.cfg)
	if s.db == nil {
		// Hide group children too: they are reached via the sub-project nav, not the dropdown.
		return filterGroupChildren(filterHidden(cfgNames, hidden), s.groups)
	}
	dbProjects, err := s.db.ProjectsCanonical(ctx, canonicalize)
	if err != nil {
		s.logger.Warn("engramdb.ProjectsCanonical failed in effectiveProjectNames", "err", err)
		return filterGroupChildren(filterHidden(cfgNames, hidden), s.groups)
	}
	dbNames := make([]string, 0, len(dbProjects))
	for _, pc := range dbProjects {
		dbNames = append(dbNames, pc.Name)
	}
	merged := mergeProjectNames(dbNames, cfgNames)
	// Hide group children: the Project dropdown shows only parents + standalone projects;
	// children are navigated via the sub-project nav bar.
	return filterGroupChildren(filterHidden(merged, hidden), s.groups)
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

	obs, err := s.records.GetObservation(ctx, id)
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
	targets := s.syncTargetViews(ctx)
	if err := syncStatusPage(status, targets).Render(ctx, w); err != nil {
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
	if s.readOnly(ctx, w) {
		return
	}
	obs, err := s.records.GetObservation(ctx, id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	v := enrichObs(*obs)
	if err := editForm(v).Render(ctx, w); err != nil {
		s.logger.Error("render edit form", "err", err)
	}
}

// readOnly reports whether the backend has no mutation capability and, when so,
// renders a clear banner. The cloud dashboard is read-only (it serves replicated
// data), so edits and deletes are reported as unsupported rather than silently
// failing. Returns true when the caller should stop.
func (s *Server) readOnly(ctx context.Context, w http.ResponseWriter) bool {
	if s.mut != nil {
		return false
	}
	if err := errorBanner("Editing is not available on this dashboard (read-only).").Render(ctx, w); err != nil {
		s.logger.Error("render read-only banner", "err", err)
	}
	return true
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
	if s.readOnly(ctx, w) {
		return
	}

	// Read old observation to capture project and old title for audit.
	oldObs, err := s.records.GetObservation(ctx, id)
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

	if err := s.mut.PatchObservation(ctx, id, title, content, obsType); err != nil {
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
	if s.readOnly(ctx, w) {
		return
	}
	obs, err := s.records.GetObservation(ctx, id)
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
	obs, err := s.records.GetObservation(ctx, id)
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
	if s.readOnly(ctx, w) {
		return
	}

	hard := strings.ToLower(r.URL.Query().Get("hard")) == "true"

	// Read old observation to capture project and title for audit.
	oldObs, err := s.records.GetObservation(ctx, id)
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

	if err := s.mut.DeleteObservation(ctx, id, hard); err != nil {
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

// handleGraph renders the semantic knowledge graph. Edges are REAL cosine
// similarities from Omnia's own embeddings store — never synthesized. When the
// embeddings layer is unavailable the page renders a clear disabled state with
// instructions instead of faking data.
//
// Query params: ?project= scopes to a canonical project (and a group parent's
// children); ?k= and ?min= tune the kNN neighbor cap and similarity threshold.
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	syncStatus := loadSyncStatus()
	projects := s.effectiveProjectNames(ctx, syncStatus)

	q := r.URL.Query()
	project := q.Get("project")
	k := clampInt(parseIntDefault(q.Get("k"), defaultGraphK), 1, 24)
	minScore := clampFloat(parseFloatDefault(q.Get("min"), defaultGraphMin), 0, 0.99)

	// No embeddings layer → honest unavailable state (do NOT fabricate edges).
	if s.sem == nil {
		view := GraphView{Available: false, Projects: projects, Project: project, K: k, Min: minScore}
		if err := graphPage(view).Render(ctx, w); err != nil {
			s.logger.Error("render graph", "err", err)
		}
		return
	}

	nodes, edges, err := s.sem.Graph(k, float32(minScore))
	if err != nil {
		s.logger.Error("build semantic graph", "err", err)
		view := GraphView{Available: false, Projects: projects, Project: project, K: k, Min: minScore}
		if rErr := graphPage(view).Render(ctx, w); rErr != nil {
			s.logger.Error("render graph", "err", rErr)
		}
		return
	}

	view := s.buildGraphView(nodes, edges, project, projects, k, minScore, len(nodes))
	if err := graphPage(view).Render(ctx, w); err != nil {
		s.logger.Error("render graph", "err", err)
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
