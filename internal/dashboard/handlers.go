package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/audit"
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
}

// Server is the Omnia dashboard HTTP server.
type Server struct {
	cfg    Config
	client *engramClient
	logger *slog.Logger
}

// NewServer creates a new dashboard Server.
func NewServer(cfg Config, logger *slog.Logger) *Server {
	return &Server{
		cfg:    cfg,
		client: newEngramClient(cfg.EngramURL),
		logger: logger,
	}
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

	// Determine projects: union of config.Projects, routes targets, and "omnia" default.
	projectNames := knownProjects(syncStatus, s.cfg)

	var stats []ProjectStats
	for _, proj := range projectNames {
		views, err := loadProjectObs(ctx, s.client, proj, 200)
		if err != nil {
			s.logger.Warn("failed to load project obs", "project", proj, "err", err)
			views = nil
		}
		stats = append(stats, computeProjectStats(proj, views))
	}

	if err := overviewPage(stats, engUp, syncStatus).Render(ctx, w); err != nil {
		s.logger.Error("render overview", "err", err)
	}
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	params := BrowseParams{
		Project: q.Get("project"),
		Source:  q.Get("source"),
		Kind:    q.Get("kind"),
		Type:    q.Get("type"),
		Query:   q.Get("q"),
	}

	// Derive list of available projects.
	syncStatus := loadSyncStatus()
	projectNames := knownProjects(syncStatus, s.cfg)

	// Load observations. Keep the browse list consistent with the overview counts:
	// when a project is selected without a free-text query, reuse the same two-search
	// loader the overview uses (ingested term + project name) so curated-only projects
	// (e.g. workly, trackly, homelab) are not missed. Engram has no "list all by
	// project" endpoint, so this FTS workaround is the best available until Omnia's
	// own index lands.
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
		// No query, project selected: reuse the overview loader for consistent results.
		v, err := loadProjectObs(ctx, s.client, params.Project, 300)
		if err != nil {
			s.logger.Warn("browse project load failed", "err", err)
		}
		allViews = v
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
	// so the Category dropdown always shows every available type in the current context.
	types := distinctTypes(allViews)

	views := make([]ObsView, 0, len(allViews))
	for _, v := range allViews {
		// Apply client-side filters.
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

	if err := browsePage(params, views, projectNames, types).Render(ctx, w); err != nil {
		s.logger.Error("render browse", "err", err)
	}
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
