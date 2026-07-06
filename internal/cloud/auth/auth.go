package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/store"
)

// deviceToucher is the store seam for stamping a device's last_seen_at on each
// authenticated device-bound request (OBL-08). *cloudstore.CloudStore satisfies
// it; tests inject a fake. It is best-effort — errors never fail a request.
type deviceToucher interface {
	TouchDevice(ctx context.Context, id string) error
}

var ErrSecretTooShort = errors.New("jwt secret must be at least 32 bytes")
var ErrBearerTokenNotConfigured = errors.New("cloud bearer token is not configured")
var ErrInvalidDashboardSessionToken = errors.New("invalid dashboard session token")
var ErrProjectNotAllowed = errors.New("project is not allowed for this token")

type Service struct {
	store         *cloudstore.CloudStore
	accountStore  userStore
	deviceStore   deviceRegistrar
	deviceToucher deviceToucher
	tokenStore    managedTokenStore
	tokenPepper   []byte
	expectedToken string
	dashboardAuth map[string]struct{}
	allowed       map[string]struct{}
	allowedAll    bool
	jwtSecret     []byte
	now           func() time.Time
}

type ProjectScopeAuthorizer struct {
	allowed    map[string]struct{}
	allowedAll bool
}

func NewService(store *cloudstore.CloudStore, jwtSecret string) (*Service, error) {
	if len(jwtSecret) < 32 {
		return nil, ErrSecretTooShort
	}
	svc := &Service{store: store, accountStore: store, jwtSecret: []byte(jwtSecret), now: time.Now}
	// Wire the device registrar when the backing store supports it.
	if dr, ok := any(store).(deviceRegistrar); ok {
		svc.deviceStore = dr
	}
	// Wire the device last_seen_at toucher when the backing store supports it.
	if dt, ok := any(store).(deviceToucher); ok {
		svc.deviceToucher = dt
	}
	// Wire the managed-token store when the backing store supports it. Managed
	// tokens stay inert until a pepper is configured via SetTokenPepper.
	if ts, ok := any(store).(managedTokenStore); ok {
		svc.tokenStore = ts
	}
	return svc, nil
}

func NewProjectScopeAuthorizer(projects []string) *ProjectScopeAuthorizer {
	a := &ProjectScopeAuthorizer{allowed: make(map[string]struct{})}
	a.SetAllowedProjects(projects)
	return a
}

type dashboardSessionClaims struct {
	TokenHash string `json:"token_hash"`
	AccountID string `json:"account_id,omitempty"`
	Username  string `json:"username,omitempty"`
	Exp       int64  `json:"exp"`
	Iat       int64  `json:"iat"`
}

// DashboardSessionInfo is the decoded identity behind a dashboard session cookie.
// AccountID/Username are populated only for account-backed sessions.
type DashboardSessionInfo struct {
	TokenHash string
	AccountID string
	Username  string
}

// MintDashboardSession returns a signed dashboard session token.
// The token is opaque to clients and validated by ParseDashboardSession.
func (s *Service) MintDashboardSession(bearerToken string) (string, error) {
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	issuedAt := s.now().UTC()
	claims := dashboardSessionClaims{
		TokenHash: s.dashboardTokenHash(bearerToken),
		Iat:       issuedAt.Unix(),
		Exp:       issuedAt.Add(8 * time.Hour).Unix(),
	}
	// If the bearer is an account token, embed the account identity so the dashboard
	// can resolve per-account scope without a server-side token registry. The session
	// is HMAC-signed, so the embedded identity cannot be tampered with.
	if ac, err := s.ParseAccountToken(bearerToken); err == nil && ac != nil {
		claims.AccountID = ac.AccountID
		claims.Username = ac.Username
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	signature := s.sign(payloadPart)
	return payloadPart + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// ParseDashboardSession verifies and decodes a signed dashboard session token.
func (s *Service) ParseDashboardSession(sessionToken string) (string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	parts := strings.Split(sessionToken, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	expectedSig := s.sign(parts[0])
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return "", ErrInvalidDashboardSessionToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	var claims dashboardSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	if strings.TrimSpace(claims.TokenHash) == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	if claims.Exp <= s.now().UTC().Unix() {
		return "", ErrInvalidDashboardSessionToken
	}
	expectedToken := strings.TrimSpace(s.expectedToken)
	if expectedToken == "" {
		return "", ErrBearerTokenNotConfigured
	}
	if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(expectedToken))) {
		return expectedToken, nil
	}
	for token := range s.dashboardAuth {
		token = strings.TrimSpace(token)
		if token == "" || token == expectedToken {
			continue
		}
		if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(token))) {
			return token, nil
		}
	}
	return "", ErrInvalidDashboardSessionToken
}

// ParseDashboardSessionInfo verifies a dashboard session token's signature and
// expiry and returns its decoded identity. Unlike ParseDashboardSession it does
// NOT require the underlying bearer to be a pre-registered token — it is used to
// recover an account identity that was embedded at mint time.
func (s *Service) ParseDashboardSessionInfo(sessionToken string) (DashboardSessionInfo, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	parts := strings.Split(sessionToken, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return DashboardSessionInfo{}, ErrInvalidDashboardSessionToken
	}
	expectedSig := s.sign(parts[0])
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(expectedSig, providedSig) {
		return DashboardSessionInfo{}, ErrInvalidDashboardSessionToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return DashboardSessionInfo{}, ErrInvalidDashboardSessionToken
	}
	var claims dashboardSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return DashboardSessionInfo{}, ErrInvalidDashboardSessionToken
	}
	if strings.TrimSpace(claims.TokenHash) == "" || claims.Exp <= s.now().UTC().Unix() {
		return DashboardSessionInfo{}, ErrInvalidDashboardSessionToken
	}
	return DashboardSessionInfo{TokenHash: claims.TokenHash, AccountID: claims.AccountID, Username: claims.Username}, nil
}

func (s *Service) sign(payloadPart string) []byte {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil)
}

func (s *Service) dashboardTokenHash(token string) string {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte("dashboard:"))
	_, _ = mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) SetBearerToken(token string) {
	s.expectedToken = strings.TrimSpace(token)
}

func (s *Service) SetDashboardSessionTokens(tokens []string) {
	s.dashboardAuth = make(map[string]struct{})
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		s.dashboardAuth[token] = struct{}{}
	}
}

func (s *Service) SetAllowedProjects(projects []string) {
	s.allowed = make(map[string]struct{})
	s.allowedAll = false
	for _, project := range projects {
		if strings.TrimSpace(project) == "*" {
			s.allowedAll = true
			return
		}
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		s.allowed[normalized] = struct{}{}
	}
}

func (s *Service) AuthorizeProject(project string) error {
	if s.allowedAll {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			return fmt.Errorf("project is required")
		}
		return nil
	}
	return authorizeProjectAgainstAllowlist(project, s.allowed)
}

// EnrolledProjects returns the sorted list of projects that this Service is
// authorized to serve. Used by cloudserver's mutation pull to filter mutations
// to the caller's enrolled projects (REQ-202).
//
// When the wildcard "*" is configured, nil is returned to signal "no project
// filter" — callers must treat nil as "allow all" (matching the ListMutationsSince
// nil-means-all contract).
//
// The interface is cloudserver.EnrolledProjectsProvider; this method makes
// *Service satisfy it without importing cloudserver (structural assertion).
func (s *Service) EnrolledProjects() []string {
	if s.allowedAll {
		return nil
	}
	return sortedAllowlist(s.allowed)
}

func (a *ProjectScopeAuthorizer) SetAllowedProjects(projects []string) {
	a.allowed = make(map[string]struct{})
	a.allowedAll = false
	for _, project := range projects {
		if strings.TrimSpace(project) == "*" {
			a.allowedAll = true
			return
		}
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		a.allowed[normalized] = struct{}{}
	}
}

func (a *ProjectScopeAuthorizer) AuthorizeProject(project string) error {
	if a.allowedAll {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			return fmt.Errorf("project is required")
		}
		return nil
	}
	return authorizeProjectAgainstAllowlist(project, a.allowed)
}

// EnrolledProjects returns the sorted list of projects this authorizer allows.
// Matches the cloudserver.EnrolledProjectsProvider contract so mutation pull
// can filter server-side by the caller's enrolled projects (REQ-202) rather
// than fail-closing to an empty result set.
//
// When the wildcard "*" is configured, nil is returned to signal "no project
// filter" (matching the ListMutationsSince nil-means-all contract).
func (a *ProjectScopeAuthorizer) EnrolledProjects() []string {
	if a.allowedAll {
		return nil
	}
	return sortedAllowlist(a.allowed)
}

// sortedAllowlist returns a sorted slice of the map keys.
// Isolated to one spot so both Service and ProjectScopeAuthorizer behave
// identically and tests can pin ordering.
func sortedAllowlist(allowed map[string]struct{}) []string {
	if len(allowed) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(allowed))
	for project := range allowed {
		out = append(out, project)
	}
	sort.Strings(out)
	return out
}

func authorizeProjectAgainstAllowlist(project string, allowed map[string]struct{}) error {
	if len(allowed) == 0 {
		return fmt.Errorf("cloud project allowlist is not configured")
	}
	normalized, _ := store.NormalizeProject(project)
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return fmt.Errorf("project is required")
	}
	if _, ok := allowed[normalized]; ok {
		return nil
	}
	return fmt.Errorf("%w", ErrProjectNotAllowed)
}

// Authorize is the boolean gate used by withAuth: it accepts a request when the
// bearer is either the legacy shared token or a valid account token. The
// signature is intentionally left as (error only) for backward compatibility;
// callers that need the resolved account identity use AuthorizeAccount, which
// performs the same validation and additionally returns the parsed claims.
func (s *Service) Authorize(r *http.Request) error {
	_, err := s.AuthorizeAccount(r)
	return err
}

// AuthorizeAccount validates the request bearer and, when it is an account
// token, returns the parsed claims. For the legacy shared token it returns
// (nil, nil): authorized, but with no account identity attached. Validation
// order mirrors the spec — legacy token first (constant-time compare), then
// account token (constant-time HMAC verification inside ParseAccountToken).
func (s *Service) AuthorizeAccount(r *http.Request) (*AccountClaims, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return nil, fmt.Errorf("missing authorization header")
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return nil, fmt.Errorf("authorization must use Bearer token")
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return nil, fmt.Errorf("authorization must use Bearer token")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return nil, fmt.Errorf("bearer token is required")
	}
	if expected := strings.TrimSpace(s.expectedToken); expected != "" &&
		hmac.Equal([]byte(token), []byte(expected)) {
		return nil, nil
	}
	if claims, err := s.ParseAccountToken(token); err == nil {
		// Best-effort device activity stamp (OBL-08): a device-bound token records
		// last_seen_at so the operator can see when a notebook last synced. Never
		// fail the request on a stats write, and skip the toucher when the backing
		// store is absent (e.g. token-only unit tests).
		if deviceID := strings.TrimSpace(claims.DeviceID); deviceID != "" && s.deviceToucher != nil {
			_ = s.deviceToucher.TouchDevice(r.Context(), deviceID)
		}
		return claims, nil
	}
	// Managed per-user token: attempted only when the feature is enabled (a
	// pepper is configured). Runtime DB enforcement lives here — a revoked token
	// or a disabled owner is rejected on THIS request. A resolvable-but-rejected
	// token surfaces its specific reason; an unknown token falls through to the
	// generic invalid-bearer error.
	if s.ManagedTokensEnabled() {
		claims, err := s.validateManagedToken(r.Context(), token)
		if err == nil {
			return claims, nil
		}
		if !errors.Is(err, ErrInvalidManagedToken) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("invalid bearer token")
}
