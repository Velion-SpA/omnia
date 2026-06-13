package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime configuration for the dashboard server.
type Config struct {
	Port      int
	EngramURL string
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

	// Determine projects: use known projects from state plus "omnia" default.
	projectNames := knownProjects(syncStatus)

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
		Query:   q.Get("q"),
	}

	// Derive list of available projects.
	syncStatus := loadSyncStatus()
	projectNames := knownProjects(syncStatus)

	// Load observations.
	searchQuery := params.Query
	if searchQuery == "" {
		searchQuery = ingestedTerm
	}

	raw, err := s.client.Search(ctx, searchQuery, params.Project, 300)
	if err != nil {
		s.logger.Warn("browse search failed", "err", err)
		raw = nil
	}

	// If user searched with custom query, also get curated via the same project query.
	if params.Query != "" {
		curated, err := s.client.Search(ctx, params.Query, params.Project, 100)
		if err == nil {
			seen := map[int]struct{}{}
			for _, o := range raw {
				seen[o.ID] = struct{}{}
			}
			for _, o := range curated {
				if _, ok := seen[o.ID]; !ok {
					raw = append(raw, o)
				}
			}
		}
	}

	views := make([]ObsView, 0, len(raw))
	for _, o := range raw {
		v := enrichObs(o)
		// Apply client-side filters.
		if params.Source != "" && v.Meta.Source != params.Source {
			continue
		}
		if params.Kind != "" && v.Meta.Kind != params.Kind {
			continue
		}
		views = append(views, v)
	}

	if err := browsePage(params, views, projectNames).Render(ctx, w); err != nil {
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

	if err := detailPage(v, backURL).Render(ctx, w); err != nil {
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

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	content := r.FormValue("content")
	obsType := r.FormValue("type")

	if err := s.client.PatchObservation(ctx, id, title, content, obsType); err != nil {
		s.logger.Error("patch observation", "id", id, "err", err)
		errMsg := fmt.Sprintf("Failed to save: %v", err)
		if rErr := errorBanner(errMsg).Render(ctx, w); rErr != nil {
			s.logger.Error("render error banner", "err", rErr)
		}
		return
	}

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

	if err := s.client.DeleteObservation(ctx, id, hard); err != nil {
		s.logger.Error("delete observation", "id", id, "err", err)
		errMsg := fmt.Sprintf("Failed to delete: %v", err)
		if rErr := errorBanner(errMsg).Render(ctx, w); rErr != nil {
			s.logger.Error("render error banner", "err", rErr)
		}
		return
	}

	if err := deleteSuccess(hard).Render(ctx, w); err != nil {
		s.logger.Error("render delete success", "err", err)
	}
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
