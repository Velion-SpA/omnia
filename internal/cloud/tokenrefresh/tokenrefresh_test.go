package tokenrefresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// buildToken creates a minimal fake account token (base64url(claims).base64url(sig)).
// The signature is a dummy — ParseAccountTokenClaims never verifies it.
func buildToken(claims any) string {
	b, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(b)
	// Use a dummy signature part (any base64url string).
	return payload + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))
}

// ─── ParseAccountTokenClaims ─────────────────────────────────────────────────

func TestParseAccountTokenClaims_ValidAccountToken(t *testing.T) {
	now := time.Now().UTC()
	token := buildToken(map[string]any{
		"typ":        "account",
		"account_id": "acc-1",
		"username":   "alice",
		"iat":        now.Unix(),
		"exp":        now.Add(24 * time.Hour).Unix(),
	})

	info, err := ParseAccountTokenClaims(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil TokenInfo for valid account token")
	}
	if info.IssuedAt != now.Unix() {
		t.Errorf("IssuedAt: got %d, want %d", info.IssuedAt, now.Unix())
	}
	if info.ExpiresAt != now.Add(24*time.Hour).Unix() {
		t.Errorf("ExpiresAt: got %d, want %d", info.ExpiresAt, now.Add(24*time.Hour).Unix())
	}
}

func TestParseAccountTokenClaims_ManagedToken(t *testing.T) {
	// Managed tokens start with "omct_" — they must be skipped (nil, nil).
	info, err := ParseAccountTokenClaims("omct_abc123def456")
	if err != nil {
		t.Fatalf("unexpected error for managed token: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil TokenInfo for managed token, got %+v", info)
	}
}

func TestParseAccountTokenClaims_DashboardSessionToken(t *testing.T) {
	// Dashboard session tokens have no typ claim — should be skipped (nil, nil).
	token := buildToken(map[string]any{
		"token_hash": "abc",
		"account_id": "acc-2",
		"username":   "bob",
		"iat":        time.Now().Unix(),
		"exp":        time.Now().Add(8 * time.Hour).Unix(),
	})
	info, err := ParseAccountTokenClaims(token)
	if err != nil {
		t.Fatalf("unexpected error for dashboard session: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil TokenInfo for dashboard session, got %+v", info)
	}
}

func TestParseAccountTokenClaims_WrongTypClaim(t *testing.T) {
	token := buildToken(map[string]any{
		"typ": "other",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(1 * time.Hour).Unix(),
	})
	info, err := ParseAccountTokenClaims(token)
	if err != nil {
		t.Fatalf("unexpected error for wrong typ: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil TokenInfo for non-account typ, got %+v", info)
	}
}

func TestParseAccountTokenClaims_Malformed_NoDot(t *testing.T) {
	info, err := ParseAccountTokenClaims("notavalidtoken")
	if err != nil {
		t.Fatalf("unexpected error for malformed token: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for malformed (no dot) token, got %+v", info)
	}
}

func TestParseAccountTokenClaims_Malformed_NotBase64(t *testing.T) {
	info, err := ParseAccountTokenClaims("not-valid-base64!!!!.sig")
	if err != nil {
		t.Fatalf("unexpected error for bad base64: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for bad base64 token, got %+v", info)
	}
}

func TestParseAccountTokenClaims_EmptyToken(t *testing.T) {
	_, err := ParseAccountTokenClaims("")
	if err == nil {
		t.Error("expected error for empty token, got nil")
	}
}

// ─── ShouldRefresh ───────────────────────────────────────────────────────────

func TestShouldRefresh_FreshToken_NoRefresh(t *testing.T) {
	// 24h token issued just now — 100% TTL remaining → no refresh.
	now := time.Now().UTC()
	info := &TokenInfo{
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(24 * time.Hour).Unix(),
	}
	if ShouldRefresh(info, now) {
		t.Error("expected no refresh for a brand-new 24h token")
	}
}

func TestShouldRefresh_NearExpiry_Refresh(t *testing.T) {
	// 24h token where only 5h remain (< 25% of 24h = 6h, so should trigger).
	now := time.Now().UTC()
	iat := now.Add(-19 * time.Hour) // issued 19h ago
	exp := now.Add(5 * time.Hour)   // expires in 5h
	info := &TokenInfo{
		IssuedAt:  iat.Unix(),
		ExpiresAt: exp.Unix(),
	}
	if !ShouldRefresh(info, now) {
		t.Error("expected refresh when less than 6h remains on a 24h token")
	}
}

func TestShouldRefresh_AtThreshold_NoRefresh(t *testing.T) {
	// 24h token where exactly 6h remain (= the threshold for a 24h token).
	// ShouldRefresh uses a strict < comparison, so at exactly the threshold it
	// does NOT fire. One second less than the threshold DOES fire.
	now := time.Now().UTC()
	iat := now.Add(-18 * time.Hour)
	exp := now.Add(6 * time.Hour) // exactly 6h remaining = threshold
	info := &TokenInfo{
		IssuedAt:  iat.Unix(),
		ExpiresAt: exp.Unix(),
	}
	// remaining == threshold → strictly NOT less than → no refresh.
	if ShouldRefresh(info, now) {
		t.Error("expected no refresh when remaining equals the threshold exactly (strict < gate)")
	}
}

func TestShouldRefresh_BelowThreshold_Refresh(t *testing.T) {
	// 24h token where just under 6h remains (< threshold = 6h) → should trigger.
	now := time.Now().UTC()
	iat := now.Add(-18 * time.Hour).Add(-time.Second)
	exp := now.Add(6 * time.Hour).Add(-time.Second) // 5h59m59s remaining
	info := &TokenInfo{
		IssuedAt:  iat.Unix(),
		ExpiresAt: exp.Unix(),
	}
	if !ShouldRefresh(info, now) {
		t.Error("expected refresh when less than 6h remains (strictly below threshold)")
	}
}

func TestShouldRefresh_AlreadyExpired_NoRefresh(t *testing.T) {
	// Server rejects expired tokens; the proactive refresher should skip.
	now := time.Now().UTC()
	info := &TokenInfo{
		IssuedAt:  now.Add(-25 * time.Hour).Unix(),
		ExpiresAt: now.Add(-1 * time.Hour).Unix(), // already expired
	}
	if ShouldRefresh(info, now) {
		t.Error("expected no refresh for an already-expired token")
	}
}

func TestShouldRefresh_NilInfo_NoRefresh(t *testing.T) {
	if ShouldRefresh(nil, time.Now()) {
		t.Error("expected no refresh for nil TokenInfo")
	}
}

func TestShouldRefresh_FloorDominates(t *testing.T) {
	// A very long-lived token (e.g. 30 days). 25% of 30d = 7.5 days, which is
	// > refreshFloor (6h). Token has 10 days remaining → above 25% threshold → no refresh.
	now := time.Now().UTC()
	info := &TokenInfo{
		IssuedAt:  now.Add(-20 * 24 * time.Hour).Unix(),
		ExpiresAt: now.Add(10 * 24 * time.Hour).Unix(),
	}
	if ShouldRefresh(info, now) {
		t.Error("expected no refresh when 10 days remain on a 30-day token (above 25% threshold)")
	}
}

// ─── RefreshAccountToken ─────────────────────────────────────────────────────

func TestRefreshAccountToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/refresh" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer old-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "new-token-xyz"})
	}))
	defer srv.Close()

	newToken, err := RefreshAccountToken(context.Background(), nil, srv.URL, "old-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newToken != "new-token-xyz" {
		t.Errorf("expected 'new-token-xyz', got %q", newToken)
	}
}

func TestRefreshAccountToken_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := RefreshAccountToken(context.Background(), nil, srv.URL, "expired-token")
	if err == nil {
		t.Error("expected error for 401 response, got nil")
	}
}

func TestRefreshAccountToken_MissingTokenInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"other": "field"})
	}))
	defer srv.Close()

	_, err := RefreshAccountToken(context.Background(), nil, srv.URL, "tok")
	if err == nil {
		t.Error("expected error when token field is absent from response, got nil")
	}
}

func TestRefreshAccountToken_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow server.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := RefreshAccountToken(ctx, nil, srv.URL, "tok")
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}
