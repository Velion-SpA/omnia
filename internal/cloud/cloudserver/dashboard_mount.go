package cloudserver

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/clouddash"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/dashboard"
)

// The cloud dashboard is the SAME unified dashboard.Server the local dashboard
// runs (internal/dashboard), mounted at the root of the cloud server and fronted
// by the cloud's own login/session/RBAC. It differs from the local dashboard ONLY
// in its DataSource: a clouddash.Source over the replicated chunk store, scoped
// per request to the logged-in account's projects.
const (
	defaultDashboardHomePath = "/"
	dashboardLoginPath       = "/login"
	dashboardLogoutPath      = "/logout"
	dashboardStaticPrefix    = "/static/"
)

// dashboardCloudStore returns the clouddash data source for the configured store.
// When the store cannot serve dashboard rows (e.g. a minimal test ChunkStore) the
// dashboard renders an empty—but fully functional—shell.
func (s *CloudServer) dashboardSource() *clouddash.Source {
	if store, ok := s.store.(clouddash.CloudStore); ok {
		return clouddash.New(store, clouddash.WithCloudSemantic(s.cloudSemanticEnabled, s.cloudSemanticEmbedder))
	}
	return clouddash.New(emptyCloudStore{})
}

// subProjectResolver returns a dashboard.SubProjectResolver backed by the
// configured store's sub-project links (Command Center v2, Slice 5b), or nil
// when the store doesn't support ListProjectParents (Slice 5a) — mirroring
// dashboardSource's own optional-capability degrade: a store without the
// capability simply means the shared dashboard's Server.subProjects stays
// nil, and the project-detail page renders neither a Sub-projects section
// nor a parent breadcrumb, exactly as before this slice.
func (s *CloudServer) subProjectResolver() dashboard.SubProjectResolver {
	if store, ok := s.store.(clouddash.CloudProjectLinks); ok {
		return clouddash.NewSubProjectResolver(store)
	}
	return nil
}

// mountDashboard registers the unified dashboard at the root of the cloud mux,
// behind the cloud's login/session middleware. It is the cloud's replacement for
// the deleted internal/cloud/dashboard package.
//
// Routing is method-specific rather than a blanket "/" catch-all so that genuinely
// unknown API requests (e.g. POST /auth/login when no account service is wired)
// still 404 instead of being swallowed by the dashboard. "GET /" covers every
// dashboard page, static asset, login page and HTMX GET fragment; the dashboard's
// only non-GET routes (login/logout submits and the patch/delete mutations) are
// registered explicitly.
func (s *CloudServer) mountDashboard(mux *http.ServeMux) {
	dashSrv := dashboard.NewServerWithDataSource(dashboard.Config{}, s.dashboardSource(), slog.Default(), dashboard.WithSubProjectResolver(s.subProjectResolver()))
	gate := s.dashboardGate(dashSrv.Handler())
	mux.Handle("GET /", gate)
	mux.Handle("POST /login", gate)
	mux.Handle("POST /logout", gate)
	mux.Handle("PATCH /api/obs/{id}", gate)
	mux.Handle("DELETE /api/obs/{id}", gate)
}

// dashboardGate wraps the dashboard handler with the cloud's auth surface:
// the login/logout pages and static assets are public; every other path requires
// a valid dashboard session and is served with the request's visibility scope
// injected into the context so the data source can enforce per-account isolation.
func (s *CloudServer) dashboardGate(dashHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dashboard" || strings.HasPrefix(r.URL.Path, "/dashboard/"):
			// Back-compat: the cloud dashboard used to live under /dashboard. Redirect
			// to the unified root routes so old links/bookmarks (and any next=/dashboard
			// after login) land correctly instead of 404'ing.
			target := strings.TrimPrefix(r.URL.Path, "/dashboard")
			if target == "" {
				target = "/"
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		case r.URL.Path == dashboardLoginPath:
			s.handleDashboardLogin(w, r)
			return
		case r.URL.Path == dashboardLogoutPath:
			s.handleDashboardLogout(w, r)
			return
		case strings.HasPrefix(r.URL.Path, dashboardStaticPrefix):
			// Shared design-system assets must load on the (unauthenticated) login page.
			dashHandler.ServeHTTP(w, r)
			return
		}

		if err := s.authorizeDashboardRequest(r); err != nil {
			loginRedirect := dashboardLoginPathWithNext(r.URL.RequestURI())
			if isHTMXRequest(r) {
				w.Header().Set("HX-Redirect", loginRedirect)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginRedirect, http.StatusSeeOther)
			return
		}

		// Inject the per-request visibility scope (operator → all, account → its
		// memberships). The cloud data source reads this from the context and an
		// account can never observe another account's memories. claims also
		// carries the account_id cloud semantic search scopes cloud_embeddings
		// by (PR5 slice 3) — "" for the operator/legacy-no-RBAC path, exactly
		// mirroring materializeEmbeddingMutations' own account resolution.
		projects, all := s.dashboardVisibleProjects(r)
		claims, _ := s.dashboardSessionClaims(r)
		accountID := ""
		if claims != nil {
			accountID = claims.AccountID
		}
		ctx := clouddash.WithScope(r.Context(), clouddash.NewScope(all, projects, accountID))
		// Operator sessions additionally get the Admin nav entry on every dashboard
		// page (OBL-13). The flag is set ONLY for operators and ONLY on the cloud
		// surface, so the local dashboard never grows an Admin section.
		if all {
			ctx = dashboard.WithAdminNav(ctx)
		}
		// Authenticated account sessions get their username + a logout action in
		// the shared shell's nav on EVERY dashboard page, not just Admin. claims is
		// nil for the bare operator-token path (OMNIA_CLOUD_ADMIN / legacy sync
		// bearer) since there's no account username to show there; the Admin pages
		// keep their own hardcoded "operator" label for that case (adminLayoutProps).
		if claims != nil && strings.TrimSpace(claims.Username) != "" {
			ctx = dashboard.WithUserIdentity(ctx, claims.Username, dashboardLogoutPath)
		}
		dashHandler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleDashboardLogin renders (GET) and processes (POST) the cloud login page.
// Ported from the deleted internal/cloud/dashboard login handlers, adapted to the
// root-mounted dashboard.
func (s *CloudServer) handleDashboardLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		next := sanitizeDashboardNext(r.URL.Query().Get("next"))
		if s.authorizeDashboardRequest(r) == nil {
			http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
			return
		}
		renderDashboardLoginPage(w, http.StatusOK, "", next)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if maxDashboardLoginBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxDashboardLoginBodyBytes)
	}
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("login payload too large (max %d bytes)", maxDashboardLoginBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid form payload", http.StatusBadRequest)
		return
	}

	token := strings.TrimSpace(r.PostForm.Get("token"))
	username := strings.TrimSpace(r.PostForm.Get("username"))
	password := r.PostForm.Get("password")
	next := sanitizeDashboardNext(r.PostForm.Get("next"))
	if next == "" {
		next = sanitizeDashboardNext(r.URL.Query().Get("next"))
	}

	if s.authorizeDashboardRequest(r) == nil {
		http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
		return
	}

	// Insecure no-auth mode: no auth service is wired, so the dashboard is open.
	if s.auth == nil {
		http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
		return
	}

	// Account login (username + password) takes precedence when credentials are
	// supplied. On success it yields an account bearer token that becomes the
	// dashboard session; the operator-token path remains for server administrators.
	switch {
	case username != "" && password != "" && s.account != nil:
		accountToken, err := s.dashboardLoginWithCredentials(username, password)
		if err != nil {
			renderDashboardLoginPage(w, http.StatusOK, "invalid username or password", next)
			return
		}
		token = accountToken
	case token == "":
		renderDashboardLoginPage(w, http.StatusOK, "enter your account credentials or an operator token", next)
		return
	default:
		if err := s.validateDashboardLoginToken(token); err != nil {
			renderDashboardLoginPage(w, http.StatusOK, "invalid token", next)
			return
		}
	}

	if err := s.createDashboardSessionCookie(w, r, token); err != nil {
		http.Error(w, "unable to create dashboard session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
}

func (s *CloudServer) handleDashboardLogout(w http.ResponseWriter, r *http.Request) {
	s.clearDashboardSessionCookie(w, r)
	http.Redirect(w, r, dashboardLoginPath, http.StatusSeeOther)
}

// validateDashboardLoginToken accepts the operator admin token or a token the auth
// service recognises. Mirrors the old MountConfig.ValidateLoginToken closure.
func (s *CloudServer) validateDashboardLoginToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("bearer token is required")
	}
	if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && hmacEqual(token, adminToken) {
		return nil
	}
	if s.auth == nil {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, dashboardLoginPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return s.auth.Authorize(req)
}

func (s *CloudServer) createDashboardSessionCookie(w http.ResponseWriter, r *http.Request, token string) error {
	sessionToken, err := s.dashboardSessionToken(token)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     dashboardSessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   dashboardCookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((8 * time.Hour).Seconds()),
	})
	return nil
}

func (s *CloudServer) clearDashboardSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     dashboardSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   dashboardCookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func isHTMXRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true")
}

// ─── login redirect target sanitisation ──────────────────────────────────────

// sanitizeDashboardNext returns a safe in-app redirect target, or "" when the
// supplied value is unsafe (absolute, protocol-relative, or pointing at the login
// page itself). Mirrors the deleted helper, adapted to the root-mounted dashboard.
func sanitizeDashboardNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.Scheme != "" || parsed.User != nil {
		return ""
	}
	escapedPath := parsed.EscapedPath()
	if strings.TrimSpace(escapedPath) == "" {
		escapedPath = parsed.Path
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(decodedPath, "/") {
		return ""
	}
	cleaned := path.Clean(decodedPath)
	if cleaned == "." {
		cleaned = "/"
	}
	// Never bounce back to the auth endpoints.
	if cleaned == dashboardLoginPath || cleaned == dashboardLogoutPath {
		return ""
	}
	normalizedPath := (&url.URL{Path: cleaned}).EscapedPath()
	if normalizedPath == "" {
		return ""
	}
	rawQuery := parsed.Query().Encode()
	if rawQuery == "" {
		return normalizedPath
	}
	return normalizedPath + "?" + rawQuery
}

func dashboardLoginPathWithNext(next string) string {
	next = sanitizeDashboardNext(next)
	if next == "" {
		return dashboardLoginPath
	}
	v := url.Values{}
	v.Set("next", next)
	return dashboardLoginPath + "?" + v.Encode()
}

func dashboardPostLoginPath(next string) string {
	next = sanitizeDashboardNext(next)
	if next == "" {
		return defaultDashboardHomePath
	}
	return next
}

// ─── login page ──────────────────────────────────────────────────────────────

// renderDashboardLoginPage renders the cloud login page using the shared Omnia
// design-system stylesheet. It preserves the field names and copy the previous
// templ login page exposed (account credentials + operator token), so existing
// auth flows are unchanged.
func renderDashboardLoginPage(w http.ResponseWriter, status int, errorMsg, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	nextField := ""
	if n := sanitizeDashboardNext(next); n != "" {
		nextField = fmt.Sprintf(`<input type="hidden" name="next" value="%s">`, html.EscapeString(n))
	}
	errorBlock := ""
	if strings.TrimSpace(errorMsg) != "" {
		errorBlock = fmt.Sprintf(`<p class="login-error" role="alert">%s</p>`, html.EscapeString(errorMsg))
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sign In — Omnia Cloud</title>
<link rel="stylesheet" href="/static/pico.min.css">
<link rel="stylesheet" href="/static/omnia.css">
</head>
<body class="shell-body">
<div class="shell-backdrop">
<main class="login-shell">
<section class="login-sidepanel">
<div style="margin-bottom:18px;"><svg width="48" height="48" viewBox="0 0 36 36" fill="none" aria-label="Omnia" style="flex-shrink:0;"><defs><radialGradient id="omnia-core" cx="50%%" cy="50%%" r="50%%"><stop offset="0%%" stop-color="#22d3ee"></stop><stop offset="100%%" stop-color="#0e9ab5"></stop></radialGradient></defs><circle cx="18" cy="18" r="16.5" stroke="rgba(34,211,238,0.22)" stroke-width="0.75"></circle><circle cx="18" cy="18" r="11.5" stroke="rgba(34,211,238,0.40)" stroke-width="0.75" stroke-dasharray="3 2.5"></circle><circle cx="18" cy="18" r="5" fill="url(#omnia-core)"></circle><circle cx="18" cy="18" r="2.2" fill="#080a10" opacity="0.5"></circle><line x1="18" y1="1.5" x2="18" y2="6.5" stroke="#22d3ee" stroke-width="1.4" stroke-linecap="round"></line><line x1="34.5" y1="18" x2="29.5" y2="18" stroke="#22d3ee" stroke-width="1.4" stroke-linecap="round"></line><line x1="18" y1="34.5" x2="18" y2="29.5" stroke="#22d3ee" stroke-width="1.4" stroke-linecap="round"></line><line x1="1.5" y1="18" x2="6.5" y2="18" stroke="#22d3ee" stroke-width="1.4" stroke-linecap="round"></line></svg></div>
<p class="section-kicker">CLOUD ACTIVE</p>
<h1>Omnia Cloud</h1>
<p class="login-lead">Your shared memory, scoped to your account. Sign in to see the projects you belong to — and nothing else.</p>
<div class="hero-console login-console">
<p><span class="console-key">identity</span> per-account access</p>
<p><span class="console-key">scope</span> projects by membership</p>
<p><span class="console-key">model</span> local-first / cloud policy aware</p>
</div>
</section>
<section class="login-container">
<p class="section-kicker">SIGN IN</p>
<h2>Sign In</h2>
<p class="login-copy">Use your account credentials to open a signed dashboard session.</p>
%s
<form method="post" action="%s" class="login-form">
%s
<label>Username <input type="text" name="username" placeholder="username" autocomplete="username"></label>
<label>Password <input type="password" name="password" placeholder="password" autocomplete="current-password"></label>
<button type="submit" class="shell-button">Sign In</button>
</form>
<details class="login-operator">
<summary>Sign in as server operator</summary>
<form method="post" action="%s" class="login-form">
%s
<label>Operator token <input type="password" name="token" placeholder="cloud operator token" autocomplete="off"></label>
<button type="submit" class="shell-button shell-button-ghost">Sign In as Operator</button>
</form>
</details>
</section>
</main>
</div>
</body>
</html>`, errorBlock, dashboardLoginPath, nextField, dashboardLoginPath, nextField)
}

// emptyCloudStore is a no-op clouddash.CloudStore for deployments whose backing
// store cannot serve dashboard rows. The dashboard renders an empty shell.
type emptyCloudStore struct{}

func (emptyCloudStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	return nil, nil
}

func (emptyCloudStore) ListRecentObservations(string, string, int) ([]cloudstore.DashboardObservationRow, error) {
	return nil, nil
}
