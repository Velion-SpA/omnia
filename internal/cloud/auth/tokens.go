package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Managed per-user tokens (OBL-01). A managed token is a high-entropy random
// credential. Only its HMAC-SHA256(pepper, raw) hash is persisted; the raw value
// is returned to the operator exactly once at issuance. Every sync request that
// presents a managed token is validated against the store, so the credential can
// be revoked and a disabled owner is rejected at runtime.

// ManagedTokenPrefix tags raw managed tokens. It also keeps them structurally
// distinct from account tokens (which always contain a "." separator).
const ManagedTokenPrefix = "omct_"

// managedTokenBytes is the entropy of a freshly minted managed token.
const managedTokenBytes = 32

var (
	// ErrManagedTokensNotEnabled is returned by issuance when no pepper is set.
	ErrManagedTokensNotEnabled = errors.New("auth: managed tokens are not enabled")
	// ErrInvalidManagedToken means the presented token hash matched no record.
	ErrInvalidManagedToken = errors.New("auth: invalid managed token")
	// ErrManagedTokenRevoked means the token exists but has been revoked.
	ErrManagedTokenRevoked = errors.New("auth: managed token revoked")
	// ErrManagedTokenUserDisabled means the token's owner has been disabled.
	ErrManagedTokenUserDisabled = errors.New("auth: managed token user disabled")
)

// managedTokenStore is the store seam for managed-token persistence.
// *cloudstore.CloudStore satisfies it; tests inject an in-memory fake.
type managedTokenStore interface {
	IssueManagedToken(ctx context.Context, userID, tokenHash, label string, audit cloudstore.AuditEntry) (*cloudstore.ManagedToken, error)
	ResolveManagedToken(ctx context.Context, tokenHash string) (*cloudstore.ManagedTokenResolution, error)
	TouchManagedToken(ctx context.Context, tokenID string) error
}

// SetTokenPepper configures the pepper used to hash managed tokens. A non-empty
// pepper is what "enables" managed tokens: without it, issuance fails and the
// runtime validation path is skipped entirely.
func (s *Service) SetTokenPepper(pepper string) {
	s.tokenPepper = []byte(strings.TrimSpace(pepper))
}

// ManagedTokensEnabled reports whether managed tokens are active: a pepper is
// configured AND a backing store is wired.
func (s *Service) ManagedTokensEnabled() bool {
	return len(s.tokenPepper) > 0 && s.tokenStore != nil
}

// hashManagedToken returns the hex HMAC-SHA256(pepper, raw). Only this value is
// ever persisted; the raw token is not derivable from it.
func (s *Service) hashManagedToken(raw string) string {
	mac := hmac.New(sha256.New, s.tokenPepper)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// IssueManagedToken mints a managed token for userID, persisting only its hash
// (atomically with an audit row) and returning the RAW token EXACTLY ONCE plus
// its id. The raw token must be surfaced to the operator immediately and never
// logged or stored.
func (s *Service) IssueManagedToken(ctx context.Context, userID, label string) (rawToken, tokenID string, err error) {
	if !s.ManagedTokensEnabled() {
		return "", "", ErrManagedTokensNotEnabled
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", "", fmt.Errorf("auth: user id is required")
	}
	buf := make([]byte, managedTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate managed token: %w", err)
	}
	raw := ManagedTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash := s.hashManagedToken(raw)
	audit := cloudstore.AuditEntry{
		Contributor: "operator",
		Metadata:    map[string]any{"label": strings.TrimSpace(label)},
	}
	mt, err := s.tokenStore.IssueManagedToken(ctx, userID, hash, label, audit)
	if err != nil {
		return "", "", err
	}
	return raw, mt.ID, nil
}

// validateManagedToken resolves a presented raw token to its account identity,
// enforcing revocation and owner-disabled on every call. Returns synthesized
// account claims (AccountID == the owning user id, matching membership records).
func (s *Service) validateManagedToken(ctx context.Context, raw string) (*AccountClaims, error) {
	if !s.ManagedTokensEnabled() {
		return nil, ErrInvalidManagedToken
	}
	res, err := s.tokenStore.ResolveManagedToken(ctx, s.hashManagedToken(raw))
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, ErrInvalidManagedToken
	}
	if res.Revoked {
		return nil, ErrManagedTokenRevoked
	}
	if res.UserDisabled {
		return nil, ErrManagedTokenUserDisabled
	}
	// Best-effort usage stamp — never fail a request on a stats write.
	_ = s.tokenStore.TouchManagedToken(ctx, res.TokenID)
	return &AccountClaims{
		Typ:       accountTokenType,
		AccountID: res.UserID,
		Username:  res.Username,
	}, nil
}
