// Package tokenrefresh provides client-side helpers for proactive account
// token rotation. It does NOT verify the HMAC signature (the client does not
// hold the server secret); it only decodes the claims so the expiry can be
// inspected and the /auth/refresh endpoint called before the token expires.
package tokenrefresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// managedTokenPrefix is the prefix that distinguishes managed tokens (omct_…)
// from account tokens. It must stay in sync with auth.ManagedTokenPrefix.
const managedTokenPrefix = "omct_"

// refreshThresholdRatio is the fraction of the token's total lifetime that
// must remain for the token to be considered fresh. When remaining < ratio *
// lifetime the refresher fires. 0.25 means "refresh when < 25% remains".
const refreshThresholdRatio = 0.25

// refreshFloor is the unconditional minimum remaining TTL below which the
// refresher fires even if the token is very long-lived and 25% > floor.
const refreshFloor = 6 * time.Hour

// accountTokenType is the typ claim value for account tokens.
const accountTokenType = "account"

// rawClaims holds the subset of account-token claims the client needs.
// Server-set fields typ/iat/exp are the only ones we care about here.
type rawClaims struct {
	Typ string `json:"typ"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// TokenInfo holds the decoded timing claims from an account token.
type TokenInfo struct {
	// IssuedAt is when the token was minted (unix seconds).
	IssuedAt int64
	// ExpiresAt is when the token expires (unix seconds).
	ExpiresAt int64
}

// ParseAccountTokenClaims decodes the claims portion of an Omnia account
// token WITHOUT verifying the HMAC signature — the client does not hold the
// server secret. It returns (nil, nil) for token types that must not be
// refreshed (managed / legacy bearer), and an error for malformed tokens.
//
// Refresh eligibility rules:
//   - Managed tokens (omct_ prefix): skip — no expiry, rotation not applicable.
//   - Tokens that do not start with a base64url segment followed by ".": skip.
//   - Decoded claims where typ != "account": skip (dashboard session etc.).
//   - Anything else: return the decoded iat/exp.
func ParseAccountTokenClaims(token string) (*TokenInfo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("tokenrefresh: empty token")
	}

	// Managed tokens have a known prefix; they never expire and must not be refreshed.
	if strings.HasPrefix(token, managedTokenPrefix) {
		return nil, nil
	}

	// Omnia tokens are base64url(claims).base64url(sig) — exactly two dot-separated parts.
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" {
		// Could be a legacy bearer (static string) or something unknown — skip.
		return nil, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		// Not a recognizable token format — skip gracefully.
		return nil, nil
	}

	var claims rawClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, nil
	}

	// Only refresh account-type tokens.
	if claims.Typ != accountTokenType {
		return nil, nil
	}

	return &TokenInfo{
		IssuedAt:  claims.Iat,
		ExpiresAt: claims.Exp,
	}, nil
}

// ShouldRefresh returns true when the token needs proactive renewal.
//
// Logic (all durations relative to now):
//  1. Already expired → false (server will reject the refresh call; reactive
//     re-login is out of scope for the proactive refresher).
//  2. remaining < max(refreshThresholdRatio × lifetime, refreshFloor) → true.
//  3. Otherwise → false.
func ShouldRefresh(info *TokenInfo, now time.Time) bool {
	if info == nil {
		return false
	}
	nowUnix := now.Unix()
	remaining := time.Duration(info.ExpiresAt-nowUnix) * time.Second
	if remaining <= 0 {
		// Already expired — do not attempt; server rejects expired tokens.
		return false
	}

	lifetime := time.Duration(info.ExpiresAt-info.IssuedAt) * time.Second
	threshold := time.Duration(float64(lifetime) * refreshThresholdRatio)
	if threshold < refreshFloor {
		threshold = refreshFloor
	}
	return remaining < threshold
}

// RefreshAccountToken calls POST /auth/refresh on the server at serverURL,
// presenting currentToken as the Bearer credential. On success it returns the
// new token string. On failure it returns a non-nil error.
//
// The caller is responsible for persisting the new token and updating any
// in-memory transport state.
func RefreshAccountToken(ctx context.Context, httpClient *http.Client, serverURL, currentToken string) (string, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	refreshURL := strings.TrimRight(serverURL, "/") + "/auth/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, nil)
	if err != nil {
		return "", fmt.Errorf("tokenrefresh: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+currentToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tokenrefresh: POST /auth/refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tokenrefresh: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("tokenrefresh: missing token in refresh response")
	}
	return strings.TrimSpace(result.Token), nil
}
