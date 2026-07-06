package cloudserver

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/chunkcodec"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/cloud/constants"
	engramproject "github.com/velion/omnia/internal/project"
	"github.com/velion/omnia/internal/store"
	engramsync "github.com/velion/omnia/internal/sync"
)

type Option func(*CloudServer)

type ChunkStore interface {
	ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error)
	WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error
	ReadChunk(ctx context.Context, project, chunkID string) ([]byte, error)
	KnownSessionIDs(ctx context.Context, project string) (map[string]struct{}, error)
}

type Authenticator interface {
	Authorize(r *http.Request) error
}

type ProjectAuthorizer interface {
	AuthorizeProject(project string) error
}

// deviceScopeStore is the subset of cloudstore needed for device-scope checks.
type deviceScopeStore interface {
	GetDevice(id string) (*cloudstore.Device, error)
}

// RefreshService is an optional extension of AccountService.
// Servers detect it via type assertion.
type RefreshService interface {
	Refresh(currentToken string) (newToken string, err error)
}

type dashboardSessionCodec interface {
	MintDashboardSession(bearerToken string) (string, error)
	ParseDashboardSession(sessionToken string) (string, error)
}

type CloudServer struct {
	store              ChunkStore
	auth               Authenticator
	projectAuth        ProjectAuthorizer
	accountProjectAuth AccountProjectAuthorizer
	account            AccountService
	deviceStore        deviceScopeStore
	dashboardAdmin     string
	openSignup         bool
	port               int
	host               string
	maxPushBodyBytes   int64
	mux                *http.ServeMux
	listenAndServe     func(addr string, handler http.Handler) error
}

const defaultHost = "127.0.0.1"
const defaultMaxPushBodyBytes int64 = 8 * 1024 * 1024
const maxDashboardLoginBodyBytes int64 = 16 * 1024
const dashboardSessionCookieName = "engram_dashboard_token"

var ErrDashboardSessionCodecRequired = errors.New("dashboard session codec is required for dashboard auth")

func WithHost(host string) Option {
	return func(s *CloudServer) {
		s.host = strings.TrimSpace(host)
	}
}

func WithProjectAuthorizer(authorizer ProjectAuthorizer) Option {
	return func(s *CloudServer) {
		s.projectAuth = authorizer
	}
}

func WithDashboardAdminToken(adminToken string) Option {
	return func(s *CloudServer) {
		s.dashboardAdmin = strings.TrimSpace(adminToken)
	}
}

// WithOpenSignup controls whether the public POST /auth/signup endpoint accepts
// new registrations. It defaults to false (closed): once the server is bootstrapped
// with a first admin (see `omnia cloud bootstrap-admin`), anonymous self-signup is a
// security hole on a LAN-reachable homelab. Set OMNIA_CLOUD_OPEN_SIGNUP=1 to reopen
// it deliberately (e.g. for dev seeding). Insecure dev mode never registers the
// signup route at all (the account service is absent), so this only affects
// authenticated deployments.
func WithOpenSignup(enabled bool) Option {
	return func(s *CloudServer) {
		s.openSignup = enabled
	}
}

func WithMaxPushBodyBytes(limit int64) Option {
	return func(s *CloudServer) {
		if limit > 0 {
			s.maxPushBodyBytes = limit
		}
	}
}

func New(store ChunkStore, authSvc Authenticator, port int, opts ...Option) *CloudServer {
	s := &CloudServer{
		store:            store,
		auth:             authSvc,
		port:             port,
		host:             defaultHost,
		maxPushBodyBytes: defaultMaxPushBodyBytes,
		listenAndServe:   http.ListenAndServe,
	}
	if projectAuthorizer, ok := authSvc.(ProjectAuthorizer); ok {
		s.projectAuth = projectAuthorizer
	}
	if accountSvc, ok := authSvc.(AccountService); ok {
		s.account = accountSvc
	}
	// Wire up RBAC authorizer when store supports membership lookups.
	if ms, ok := store.(membershipStore); ok {
		pa := s.projectAuth
		var legacy projectLegacyAuthorizer
		if pa != nil {
			legacy = pa
		} else {
			// Fail-closed fallback: no legacy authorizer means every project
			// authorization attempt is rejected (deny-by-default).
			legacy = denyAllProjects{}
		}
		s.accountProjectAuth = &rbacAuthorizer{authSvc: legacy, store: ms}
	}
	// Detect device scope store for Phase 4 enforcement.
	if ds, ok := store.(deviceScopeStore); ok {
		s.deviceStore = ds
	}
	for _, opt := range opts {
		opt(s)
	}
	s.routes()
	return s
}

func (s *CloudServer) Start() error {
	host := strings.TrimSpace(s.host)
	if host == "" {
		host = defaultHost
	}
	addr := fmt.Sprintf("%s:%d", host, s.port)
	log.Printf("[engram-cloud] listening on %s", addr)
	return s.listenAndServe(addr, s.Handler())
}

func (s *CloudServer) Handler() http.Handler {
	if s.mux == nil {
		s.routes()
	}
	return s.mux
}

func (s *CloudServer) pushBodyLimit() int64 {
	if s.maxPushBodyBytes > 0 {
		return s.maxPushBodyBytes
	}
	return defaultMaxPushBodyBytes
}

func (s *CloudServer) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /sync/pull", s.withAuth(s.handlePullManifest))
	s.mux.HandleFunc("GET /sync/pull/{chunkID}", s.withAuth(s.handlePullChunk))
	s.mux.HandleFunc("POST /sync/push", s.withAuth(s.handlePushChunk))
	s.mux.HandleFunc("POST /sync/mutations/push", s.withAuth(s.handleMutationPush))
	s.mux.HandleFunc("GET /sync/mutations/pull", s.withAuth(s.handleMutationPull))
	// Public account endpoints — intentionally NOT wrapped in withAuth.
	// Registered only when the auth dependency exposes account capabilities.
	if s.account != nil {
		s.mux.HandleFunc("POST /auth/signup", s.handleSignup)
		s.mux.HandleFunc("POST /auth/login", s.handleLogin)
		if rs, ok := s.account.(RefreshService); ok {
			s.mux.HandleFunc("POST /auth/refresh", func(w http.ResponseWriter, r *http.Request) {
				s.handleRefresh(w, r, rs)
			})
		}
		// Member-management endpoints. Account-only (per-project roles): the
		// legacy shared token has no role, so the handlers reject claims==nil
		// with 403. Wrapped in withAuth to stash AccountClaims into the context.
		s.mux.HandleFunc("GET /projects/{project}/members", s.withAuth(s.handleListMembers))
		s.mux.HandleFunc("POST /projects/{project}/members", s.withAuth(s.handleAddMember))
		s.mux.HandleFunc("DELETE /projects/{project}/members/{account_id}", s.withAuth(s.handleRemoveMember))
		// Device-management endpoints. Account-only (requires withAuth + claims != nil).
		// Registered only when device management is available.
		if _, ok := s.store.(deviceManager); ok {
			s.mux.HandleFunc("GET /devices", s.withAuth(s.handleListDevices))
			s.mux.HandleFunc("POST /devices/{id}/scope", s.withAuth(s.handleSetDeviceScope))
			s.mux.HandleFunc("DELETE /devices/{id}", s.withAuth(s.handleDeleteDevice))
		}
		// Managed-token administration (OBL-01). Operator-only: each handler gates
		// on the admin credential itself (requireAdminBearer), so these are NOT
		// wrapped in withAuth. Registered only when the account service can issue
		// managed tokens AND the store supports token/user lifecycle.
		if _, ok := s.account.(managedTokenIssuer); ok {
			if _, ok := s.store.(managedTokenAdminStore); ok {
				s.mux.HandleFunc("POST /admin/tokens", s.handleIssueManagedToken)
				s.mux.HandleFunc("POST /admin/tokens/{id}/revoke", s.handleRevokeManagedToken)
				s.mux.HandleFunc("POST /admin/users/{id}/disable", s.handleDisableUser)
				s.mux.HandleFunc("POST /admin/users/{id}/enable", s.handleEnableUser)
			}
		}
	}

	// Mount the unified dashboard at the root catch-all, behind the cloud's
	// login/session/RBAC. Specific API patterns above take precedence; every other
	// path falls through to the dashboard gate.
	s.mountDashboard(s.mux)
}

func (s *CloudServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			if accountAuth, ok := s.auth.(interface {
				AuthorizeAccount(*http.Request) (*auth.AccountClaims, error)
			}); ok {
				claims, err := accountAuth.AuthorizeAccount(r)
				if err != nil {
					http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
					return
				}
				if claims != nil {
					r = r.WithContext(auth.ContextWithAccount(r.Context(), claims))
				}
			} else {
				if err := s.auth.Authorize(r); err != nil {
					http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
					return
				}
			}
		}
		next(w, r)
	}
}

func (s *CloudServer) withAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			if accountAuth, ok := s.auth.(interface {
				AuthorizeAccount(*http.Request) (*auth.AccountClaims, error)
			}); ok {
				claims, err := accountAuth.AuthorizeAccount(r)
				if err != nil {
					http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
					return
				}
				if claims != nil {
					r = r.WithContext(auth.ContextWithAccount(r.Context(), claims))
				}
			} else {
				if err := s.auth.Authorize(r); err != nil {
					http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *CloudServer) authorizeDashboardRequest(r *http.Request) error {
	if s.auth == nil {
		return nil
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return err
	}
	// Account session — identity embedded and HMAC-signed at mint time. A valid
	// signature plus a non-empty AccountID authorizes the dashboard request.
	if info, ok := s.dashboardSessionInfo(cookie.Value); ok && strings.TrimSpace(info.AccountID) != "" {
		return nil
	}
	bearerToken, err := s.dashboardBearerToken(cookie.Value)
	if err != nil {
		return err
	}
	if strings.TrimSpace(bearerToken) == "" {
		return fmt.Errorf("dashboard session token is empty")
	}
	if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && hmac.Equal([]byte(bearerToken), []byte(adminToken)) {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	return s.auth.Authorize(req)
}

func (s *CloudServer) dashboardSessionToken(bearerToken string) (string, error) {
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.MintDashboardSession(bearerToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

func (s *CloudServer) dashboardBearerToken(sessionToken string) (string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return "", fmt.Errorf("dashboard session token is empty")
	}
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.ParseDashboardSession(sessionToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

// hmacEqual is a constant-time string comparison for token equality checks.
func hmacEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

func dashboardCookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwardedProto := r.Header.Get("X-Forwarded-Proto")
	for _, proto := range strings.Split(forwardedProto, ",") {
		if strings.EqualFold(strings.TrimSpace(proto), "https") {
			return true
		}
	}
	return false
}

func (s *CloudServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "service": "engram-cloud"})
}

func (s *CloudServer) isDashboardAdmin(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	adminToken := strings.TrimSpace(s.dashboardAdmin)
	if adminToken == "" {
		return false
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return false
	}
	token, err := s.dashboardBearerToken(cookie.Value)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(token), []byte(adminToken))
}

// dashboardSessionClaims resolves the identity behind a dashboard session cookie.
// It returns (nil, true) for the server operator (admin token), (claims, false)
// for an account, or (nil, false) when the session cannot be resolved. The account
// path is fail-closed: an unrecognised token yields no identity and is never
// treated as operator.
// dashboardSessionInfo decodes a dashboard session cookie value into its embedded
// identity. Account-backed sessions carry a non-empty AccountID/Username.
func (s *CloudServer) dashboardSessionInfo(cookieValue string) (auth.DashboardSessionInfo, bool) {
	codec, ok := s.auth.(interface {
		ParseDashboardSessionInfo(string) (auth.DashboardSessionInfo, error)
	})
	if !ok {
		return auth.DashboardSessionInfo{}, false
	}
	info, err := codec.ParseDashboardSessionInfo(cookieValue)
	if err != nil {
		return auth.DashboardSessionInfo{}, false
	}
	return info, true
}

func (s *CloudServer) dashboardSessionClaims(r *http.Request) (claims *auth.AccountClaims, operator bool) {
	if s.auth == nil {
		return nil, false
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return nil, false
	}
	// Account session — identity embedded and HMAC-signed at mint time.
	if info, ok := s.dashboardSessionInfo(cookie.Value); ok && strings.TrimSpace(info.AccountID) != "" {
		return &auth.AccountClaims{AccountID: info.AccountID, Username: info.Username}, false
	}
	// Operator visibility (sees ALL accounts' projects) is granted ONLY by the
	// designated admin credential (OMNIA_CLOUD_ADMIN). Routing through the
	// timing-safe isDashboardAdmin puts that guarded comparison on the live path
	// (OBL-03). The sync bearer — which s.auth.Authorize also accepts — must NOT
	// grant god-mode dashboard visibility in a multi-tenant deployment.
	if s.isDashboardAdmin(r) {
		return nil, true
	}
	// Legacy single-tenant compatibility: when no account/membership store is
	// configured, there are no per-account scopes to protect, so the sync bearer
	// still resolves to the server operator (pre-multi-tenant behaviour). This
	// fallback is intentionally skipped once RBAC (accountProjectAuth) is active.
	if s.accountProjectAuth == nil {
		bearer, err := s.dashboardBearerToken(cookie.Value)
		if err != nil || strings.TrimSpace(bearer) == "" {
			return nil, false
		}
		probe, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
		probe.Header.Set("Authorization", "Bearer "+bearer)
		if s.auth.Authorize(probe) == nil {
			return nil, true
		}
	}
	return nil, false
}

// dashboardVisibleProjects implements dashboard.MountConfig.VisibleProjects. The
// operator sees every project; an account sees exactly the projects where it holds
// at least read permission. Fail-closed: any resolution failure yields an empty
// scope, never the full set.
func (s *CloudServer) dashboardVisibleProjects(r *http.Request) ([]string, bool) {
	claims, operator := s.dashboardSessionClaims(r)
	if operator {
		return nil, true
	}
	if claims == nil {
		return []string{}, false
	}
	reader, ok := s.store.(interface {
		ListMembershipsForAccount(string) ([]cloudstore.Membership, error)
	})
	if !ok {
		return []string{}, false
	}
	memberships, err := reader.ListMembershipsForAccount(claims.AccountID)
	if err != nil {
		return []string{}, false
	}
	projects := make([]string, 0, len(memberships))
	for _, m := range memberships {
		if auth.Permission(m.Perms).Has(auth.PermRead) {
			projects = append(projects, m.Project)
		}
	}
	return projects, false
}

// dashboardLoginWithCredentials exchanges an account username/password for an
// account bearer token used as the dashboard session.
func (s *CloudServer) dashboardLoginWithCredentials(username, password string) (string, error) {
	if s.account == nil {
		return "", fmt.Errorf("account login not available")
	}
	token, _, err := s.account.Login(username, password)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *CloudServer) handlePullManifest(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectOp(w, r, project, auth.PermRead) {
		return
	}
	manifest, err := s.store.ReadManifest(r.Context(), project)
	if err != nil {
		http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, manifest)
}

func (s *CloudServer) handlePullChunk(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectOp(w, r, project, auth.PermRead) {
		return
	}
	chunkID := strings.TrimSpace(r.PathValue("chunkID"))
	if chunkID == "" {
		http.Error(w, "chunkID is required", http.StatusBadRequest)
		return
	}
	chunk, err := s.store.ReadChunk(r.Context(), project, chunkID)
	if err != nil {
		if errors.Is(err, cloudstore.ErrChunkNotFound) {
			http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(chunk)
}

func (s *CloudServer) handlePushChunk(w http.ResponseWriter, r *http.Request) {
	maxPushBodyBytes := s.pushBodyLimit()
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
	var req struct {
		ChunkID         string          `json:"chunk_id"`
		CreatedBy       string          `json:"created_by"`
		ClientCreatedAt string          `json:"client_created_at"`
		Project         string          `json:"project"`
		Data            json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeActionableError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadTooLarge, fmt.Sprintf("push payload too large (max %d bytes)", maxPushBodyBytes))
			return
		}
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	if len(req.Data) == 0 {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "data is required")
		return
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	// Claim-on-first-push: the first authenticated account to push a brand-new
	// project becomes its owner. Must run BEFORE authorizeProjectOp so the
	// freshly-minted owner passes the PermInsert check below.
	if err := s.claimOrphanProject(r, project); err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("claim project: %v", err))
		return
	}
	if !s.authorizeProjectOp(w, r, project, auth.PermInsert) {
		return
	}

	// Push-path pause guard: check project sync control before accepting the chunk.
	// Uses a structural interface assertion so the ChunkStore interface is NOT extended.
	// Satisfies REQ-109 / Design Decision 5.
	if storeForControls, ok := s.store.(interface {
		IsProjectSyncEnabled(project string) (bool, error)
	}); ok {
		enabled, err := storeForControls.IsProjectSyncEnabled(project)
		if err != nil {
			writeActionableError(w, http.StatusInternalServerError,
				constants.UpgradeErrorClassBlocked,
				constants.UpgradeErrorCodeInternal,
				fmt.Sprintf("check project control: %v", err))
			return
		}
		if !enabled {
			// REQ-405: emit audit entry for chunk-push pause-rejection before writing 409.
			// Structural type assertion — ChunkStore is NOT extended.
			contributor := strings.TrimSpace(req.CreatedBy)
			if contributor == "" {
				contributor = "unknown"
			}
			if auditor, ok := s.store.(interface {
				InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
			}); ok {
				if aerr := auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{
					Contributor: contributor,
					Project:     project,
					Action:      cloudstore.AuditActionChunkPush,
					Outcome:     cloudstore.AuditOutcomeRejectedProjectPaused,
					ReasonCode:  "sync-paused",
				}); aerr != nil {
					log.Printf("cloudserver: audit insert failed (chunk push): %v", aerr)
				}
			} else {
				log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped", s.store)
			}
			// JW4: include project envelope fields in 409 response, consistent
			// with the mutation push 409 envelope (REQ-414 parity for chunk path).
			jsonResponse(w, http.StatusConflict, map[string]any{
				"error_class":    strings.TrimSpace(constants.UpgradeErrorClassPolicy),
				"error_code":     "sync-paused",
				"error":          fmt.Sprintf("sync is paused for project %q", project),
				"project":        project,
				"project_source": engramproject.SourceRequestBody,
				"project_path":   "",
			})
			return
		}
	}

	normalizedData, err := coerceChunkProject(req.Data, project)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	chunk, err := validateImportableChunkPayload(normalizedData)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	knownSessionIDs, err := s.store.KnownSessionIDs(r.Context(), project)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("validate push payload: %v", err))
		return
	}
	if err := validateChunkSessionReferences(chunk, knownSessionIDs); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}

	computedChunkID := chunkIDFromPayload(normalizedData)
	providedChunkID := strings.TrimSpace(req.ChunkID)
	if providedChunkID != "" && providedChunkID != computedChunkID {
		log.Printf("cloudserver: chunk_id mismatch for project %q: client=%q server=%q; accepting server-canonicalized payload", project, providedChunkID, computedChunkID)
	}
	clientCreatedAt := strings.TrimSpace(req.ClientCreatedAt)
	if clientCreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, clientCreatedAt); err != nil {
			writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "client_created_at must be RFC3339")
			return
		}
	}

	if err := s.store.WriteChunk(r.Context(), project, computedChunkID, req.CreatedBy, clientCreatedAt, normalizedData); err != nil {
		if errors.Is(err, cloudstore.ErrChunkConflict) {
			writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodeChunkConflict, fmt.Sprintf("write chunk: %v", err))
			return
		}
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("write chunk: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "chunk_id": computedChunkID})
}

func chunkIDFromPayload(payload []byte) string {
	return chunkcodec.ChunkID(payload)
}

func projectFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	return project, true
}

// authorizeProjectOp enforces per-operation RBAC for a single project. When
// accountProjectAuth is active it checks membership for the required permission;
// otherwise it falls back to the legacy project allowlist (which ignores the
// required permission). Returns true when the request may proceed; on denial it
// has already written the HTTP error response.
func (s *CloudServer) authorizeProjectOp(w http.ResponseWriter, r *http.Request, project string, required auth.Permission) bool {
	if s.accountProjectAuth != nil {
		claims, _ := auth.AccountFromContext(r.Context())
		err := s.accountProjectAuth.AuthorizeAccountProject(claims, project, required)
		if err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project access denied")
			} else {
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return false
		}
		// Membership check passed. Now apply the ADDITIVE device-scope gate.
		// The gate activates only when:
		//   1. The request carries account claims (not legacy shared token)
		//   2. The claims carry a non-empty DeviceID
		//   3. The device exists and has a non-empty scope_projects list
		// When active, the requested project MUST be in scope_projects, else deny.
		// This gate is AND-only: it cannot grant access the membership check denied.
		if claims != nil && strings.TrimSpace(claims.DeviceID) != "" && s.deviceStore != nil {
			if err := s.checkDeviceScope(r.Context(), claims.DeviceID, project); err != nil {
				writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project not in device scope")
				return false
			}
		}
		return true
	}
	// Legacy: no RBAC, use allowlist regardless of required permission.
	if s.projectAuth == nil {
		return true
	}
	if err := s.projectAuth.AuthorizeProject(project); err != nil {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project is not allowed")
		return false
	}
	return true
}

// checkDeviceScope loads the device by ID and checks whether the requested
// project is within its allowed scope. Returns nil if the project is allowed
// (either the device has an empty scope — meaning unrestricted — or the project
// is explicitly listed). Returns an error if the project is blocked.
func (s *CloudServer) checkDeviceScope(_ context.Context, deviceID, project string) error {
	dev, err := s.deviceStore.GetDevice(deviceID)
	if err != nil {
		// Fail-closed: if we can't load the device, deny.
		return fmt.Errorf("device lookup failed: %w", err)
	}
	if dev == nil {
		// Device no longer exists: deny.
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if len(dev.ScopeProjects) == 0 {
		// Empty scope = unrestricted; impose no additional restriction.
		return nil
	}
	for _, p := range dev.ScopeProjects {
		if p == project {
			return nil
		}
	}
	return fmt.Errorf("project %q not in device scope", project)
}

// intersectDeviceScope narrows the membership-allowed project set to a
// device-bound token's scope, so a scoped device reads back only its own
// projects via mutation pull. A non-empty device scope intersects; an empty
// scope leaves the set unchanged (unrestricted); a missing/errored device
// fail-closes to an empty set so a device token never reads beyond its device.
func (s *CloudServer) intersectDeviceScope(deviceID string, allowed []string) []string {
	dev, err := s.deviceStore.GetDevice(deviceID)
	if err != nil || dev == nil {
		return []string{}
	}
	if len(dev.ScopeProjects) == 0 {
		return allowed
	}
	scope := make(map[string]struct{}, len(dev.ScopeProjects))
	for _, p := range dev.ScopeProjects {
		scope[p] = struct{}{}
	}
	out := make([]string, 0, len(allowed))
	for _, p := range allowed {
		if _, ok := scope[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

func writeActionableError(w http.ResponseWriter, status int, class, code, message string) {
	jsonResponse(w, status, map[string]any{
		"error_class": strings.TrimSpace(class),
		"error_code":  strings.TrimSpace(code),
		"error":       strings.TrimSpace(message),
	})
}

func coerceChunkProject(payload []byte, project string) ([]byte, error) {
	return chunkcodec.CanonicalizeForProject(payload, project)
}

func decodeSyncMutationPayload(payload string, dest any) error {
	return chunkcodec.DecodeSyncMutationPayload(payload, dest)
}

func validateImportableChunkPayload(payload []byte) (engramsync.ChunkData, error) {
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return engramsync.ChunkData{}, fmt.Errorf("chunk schema: %w", err)
	}
	if err := validateDirectChunkArrayEntries(chunk); err != nil {
		return engramsync.ChunkData{}, err
	}
	return chunk, nil

}

func validateDirectChunkArrayEntries(chunk engramsync.ChunkData) error {
	for i, session := range chunk.Sessions {
		if strings.TrimSpace(session.ID) == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		if strings.TrimSpace(session.Directory) == "" {
			return fmt.Errorf("sessions[%d].directory is required", i)
		}
	}

	for i, observation := range chunk.Observations {
		if strings.TrimSpace(observation.SyncID) == "" {
			return fmt.Errorf("observations[%d].sync_id is required", i)
		}
		if strings.TrimSpace(observation.SessionID) == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if strings.TrimSpace(observation.Type) == "" {
			return fmt.Errorf("observations[%d].type is required", i)
		}
		if strings.TrimSpace(observation.Title) == "" {
			return fmt.Errorf("observations[%d].title is required", i)
		}
		if strings.TrimSpace(observation.Content) == "" {
			return fmt.Errorf("observations[%d].content is required", i)
		}
		if strings.TrimSpace(observation.Scope) == "" {
			return fmt.Errorf("observations[%d].scope is required", i)
		}
	}

	for i, prompt := range chunk.Prompts {
		if strings.TrimSpace(prompt.SyncID) == "" {
			return fmt.Errorf("prompts[%d].sync_id is required", i)
		}
		if strings.TrimSpace(prompt.SessionID) == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if strings.TrimSpace(prompt.Content) == "" {
			return fmt.Errorf("prompts[%d].content is required", i)
		}
	}

	return nil
}

func validateChunkSessionReferences(chunk engramsync.ChunkData, knownSessionIDs map[string]struct{}) error {
	chunkSessionIDs := make(map[string]struct{}, len(chunk.Sessions))
	for i, session := range chunk.Sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}
	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntitySession || mutation.Op != store.SyncOpUpsert {
			continue
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.ID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(mutation.EntityKey)
		}
		if sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.id is required for session upsert", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}

	hasSession := func(sessionID string) bool {
		if _, ok := chunkSessionIDs[sessionID]; ok {
			return true
		}
		_, ok := knownSessionIDs[sessionID]
		return ok
	}

	for i, observation := range chunk.Observations {
		sessionID := strings.TrimSpace(observation.SessionID)
		if sessionID == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("observations[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, prompt := range chunk.Prompts {
		sessionID := strings.TrimSpace(prompt.SessionID)
		if sessionID == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("prompts[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntityObservation && mutation.Entity != store.SyncEntityPrompt {
			continue
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.SessionID)
		if mutation.Op == store.SyncOpUpsert && sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.session_id is required for upsert", i)
		}
		if mutation.Op == store.SyncOpUpsert && !hasSession(sessionID) {
			return fmt.Errorf("mutations[%d] references missing session_id %q", i, sessionID)
		}
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
