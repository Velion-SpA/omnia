package auth

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Account-based authentication (multi-tenant evolution, Phase 1).
//
// Account tokens reuse the exact HMAC-SHA256 sign pattern used by dashboard
// session tokens (base64url(payload) + "." + base64url(signature)). To prevent
// the two token families from being confused — they are signed with the same
// jwtSecret — account claims carry a typ="account" domain tag that is verified
// on parse. A dashboard token therefore fails ParseAccountToken (wrong typ) and
// an account token fails ParseDashboardSession (empty token_hash).
const (
	accountTokenType  = "account"
	accountTokenTTL   = 24 * time.Hour
	minPasswordLength = 8
)

var (
	// ErrUsernameRequired is returned by Signup when the username is empty.
	ErrUsernameRequired = errors.New("auth: username is required")
	// ErrPasswordTooShort is returned by Signup when the password is shorter
	// than minPasswordLength.
	ErrPasswordTooShort = errors.New("auth: password must be at least 8 characters")
	// ErrAccountExists is returned by Signup when the username or email is taken.
	ErrAccountExists = errors.New("auth: account already exists")
	// ErrInvalidCredentials is the generic, non-leaking error returned by Login
	// for any authentication failure (unknown user or wrong password).
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrInvalidAccountToken is returned by ParseAccountToken for any malformed,
	// tampered, wrong-domain, or expired account token.
	ErrInvalidAccountToken = errors.New("auth: invalid account token")
)

// dummyBcryptHash is a valid DefaultCost bcrypt hash used only to equalize
// Login's response time when the username does not exist, so an attacker cannot
// enumerate registered usernames via a timing side channel. The plaintext is
// irrelevant; only the per-hash bcrypt work (fixed by DefaultCost) matters.
var dummyBcryptHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)

// userStore is the seam the account flows depend on. *cloudstore.CloudStore
// satisfies it; tests inject an in-memory fake so unit tests never require a
// live Postgres.
type userStore interface {
	CreateUser(username, email, passwordHash string) (*cloudstore.User, error)
	GetUserByUsername(username string) (*cloudstore.User, error)
	GetUserByEmail(email string) (*cloudstore.User, error)
}

// deviceRegistrar is the seam for registering/retrieving devices.
// *cloudstore.CloudStore satisfies it; tests inject a fake.
type deviceRegistrar interface {
	GetOrCreateDevice(accountID, name string) (*cloudstore.Device, error)
}

// AccountClaims is the verified payload of an account token.
type AccountClaims struct {
	Typ       string `json:"typ"`
	AccountID string `json:"account_id"`
	Username  string `json:"username"`
	Iat       int64  `json:"iat"`
	Exp       int64  `json:"exp"`
	DeviceID  string `json:"device_id,omitempty"`
}

// MintAccountToken builds, signs, and encodes an account token for the given
// account. It mirrors MintDashboardSession exactly, adding the typ domain tag.
func (s *Service) MintAccountToken(accountID, username string) (string, error) {
	return s.MintAccountTokenForDevice(accountID, username, "")
}

// MintAccountTokenForDevice builds a device-bound account token. When deviceID
// is empty it behaves identically to MintAccountToken (no device_id in claims).
func (s *Service) MintAccountTokenForDevice(accountID, username, deviceID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", fmt.Errorf("auth: account id is required")
	}
	issuedAt := s.now().UTC()
	claims := AccountClaims{
		Typ:       accountTokenType,
		AccountID: accountID,
		Username:  strings.TrimSpace(username),
		DeviceID:  strings.TrimSpace(deviceID),
		Iat:       issuedAt.Unix(),
		Exp:       issuedAt.Add(accountTokenTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	signature := s.sign(payloadPart)
	return payloadPart + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// ParseAccountToken verifies the signature (constant-time), the domain tag, and
// the expiry, returning the claims. It mirrors ParseDashboardSession.
func (s *Service) ParseAccountToken(token string) (*AccountClaims, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, ErrInvalidAccountToken
	}
	expectedSig := s.sign(parts[0])
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidAccountToken
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return nil, ErrInvalidAccountToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidAccountToken
	}
	var claims AccountClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidAccountToken
	}
	if claims.Typ != accountTokenType {
		return nil, ErrInvalidAccountToken
	}
	if strings.TrimSpace(claims.AccountID) == "" {
		return nil, ErrInvalidAccountToken
	}
	if claims.Exp <= s.now().UTC().Unix() {
		return nil, ErrInvalidAccountToken
	}
	return &claims, nil
}

// Signup validates input, bcrypt-hashes the password, and persists the user.
// It rejects empty usernames, short passwords, and taken username/email.
func (s *Service) Signup(username, email, password string) (*cloudstore.User, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if username == "" {
		return nil, ErrUsernameRequired
	}
	if len(password) < minPasswordLength {
		return nil, ErrPasswordTooShort
	}
	if s.accountStore == nil {
		return nil, fmt.Errorf("auth: account store is not configured")
	}
	existing, err := s.accountStore.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrAccountExists
	}
	if email != "" {
		existingEmail, err := s.accountStore.GetUserByEmail(email)
		if err != nil {
			return nil, err
		}
		if existingEmail != nil {
			return nil, ErrAccountExists
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}
	user, err := s.accountStore.CreateUser(username, email, string(hash))
	if err != nil {
		// A late unique-violation (race between the pre-check above and the INSERT)
		// surfaces as a clean "account already exists" rather than an opaque 500.
		if errors.Is(err, cloudstore.ErrUserExists) {
			return nil, ErrAccountExists
		}
		return nil, fmt.Errorf("auth: create user: %w", err)
	}
	return user, nil
}

// Login verifies credentials and mints an account token. It returns a generic
// ErrInvalidCredentials on any mismatch, never leaking which part failed.
func (s *Service) Login(username, password string) (token string, user *cloudstore.User, err error) {
	username = strings.TrimSpace(username)
	if s.accountStore == nil {
		return "", nil, fmt.Errorf("auth: account store is not configured")
	}
	found, err := s.accountStore.GetUserByUsername(username)
	if err != nil {
		return "", nil, err
	}
	if found == nil {
		// Equalize timing with the user-exists path below so response time can't
		// reveal whether the username is registered (user-enumeration side channel).
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		return "", nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(found.PasswordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}
	token, err = s.MintAccountToken(found.ID, found.Username)
	if err != nil {
		return "", nil, err
	}
	return token, found, nil
}

// LoginForDevice verifies credentials and mints a device-bound account token.
// It calls GetOrCreateDevice to register the device if new, then uses the
// returned device ID in the token claims. Falls back to a plain token when
// deviceStore is not configured.
func (s *Service) LoginForDevice(username, password, deviceName string) (token string, user *cloudstore.User, err error) {
	username = strings.TrimSpace(username)
	deviceName = strings.TrimSpace(deviceName)
	if s.accountStore == nil {
		return "", nil, fmt.Errorf("auth: account store is not configured")
	}
	found, err := s.accountStore.GetUserByUsername(username)
	if err != nil {
		return "", nil, err
	}
	if found == nil {
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		return "", nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(found.PasswordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}
	deviceID := ""
	if deviceName != "" && s.deviceStore != nil {
		dev, err := s.deviceStore.GetOrCreateDevice(found.ID, deviceName)
		if err != nil {
			return "", nil, fmt.Errorf("auth: register device: %w", err)
		}
		deviceID = dev.ID
	}
	token, err = s.MintAccountTokenForDevice(found.ID, found.Username, deviceID)
	if err != nil {
		return "", nil, err
	}
	return token, found, nil
}

// Refresh parses the current account token and mints a fresh one with a new
// expiry. It rejects expired or invalid tokens before issuing a replacement.
func (s *Service) Refresh(currentToken string) (string, error) {
	claims, err := s.ParseAccountToken(currentToken)
	if err != nil {
		return "", err
	}
	return s.MintAccountTokenForDevice(claims.AccountID, claims.Username, claims.DeviceID)
}

type accountContextKey struct{}

// ContextWithAccount returns a copy of ctx carrying the resolved account claims,
// so downstream handlers can read the authenticated account identity.
func ContextWithAccount(ctx context.Context, claims *AccountClaims) context.Context {
	return context.WithValue(ctx, accountContextKey{}, claims)
}

// AccountFromContext returns the account claims stashed by ContextWithAccount.
// ok is false when the request was authorized via the legacy shared token (no
// account identity) or when no account was attached.
func AccountFromContext(ctx context.Context) (*AccountClaims, bool) {
	claims, ok := ctx.Value(accountContextKey{}).(*AccountClaims)
	if !ok || claims == nil {
		return nil, false
	}
	return claims, true
}
