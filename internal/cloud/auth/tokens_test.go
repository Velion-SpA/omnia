package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeManagedTokenStore is an in-memory managedTokenStore for auth unit tests, so
// the runtime-enforcement path is exercised without a live Postgres.
type fakeManagedTokenStore struct {
	rows     map[string]*fakeManagedRow // keyed by token hash
	issueErr error
	touched  []string
}

type fakeManagedRow struct {
	id           string
	userID       string
	username     string
	revoked      bool
	userDisabled bool
}

func newFakeManagedTokenStore() *fakeManagedTokenStore {
	return &fakeManagedTokenStore{rows: make(map[string]*fakeManagedRow)}
}

func (f *fakeManagedTokenStore) IssueManagedToken(_ context.Context, userID, tokenHash, label string, _ cloudstore.AuditEntry) (*cloudstore.ManagedToken, error) {
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	id := fmt.Sprintf("tok-%d", len(f.rows)+1)
	f.rows[tokenHash] = &fakeManagedRow{id: id, userID: userID, username: "user-" + userID}
	return &cloudstore.ManagedToken{ID: id, UserID: userID, Label: label}, nil
}

func (f *fakeManagedTokenStore) ResolveManagedToken(_ context.Context, tokenHash string) (*cloudstore.ManagedTokenResolution, error) {
	row, ok := f.rows[tokenHash]
	if !ok {
		return nil, nil
	}
	return &cloudstore.ManagedTokenResolution{
		TokenID:      row.id,
		UserID:       row.userID,
		Username:     row.username,
		Revoked:      row.revoked,
		UserDisabled: row.userDisabled,
	}, nil
}

func (f *fakeManagedTokenStore) TouchManagedToken(_ context.Context, tokenID string) error {
	f.touched = append(f.touched, tokenID)
	return nil
}

func enableManagedTokens(t *testing.T, pepper string) (*Service, *fakeManagedTokenStore) {
	t.Helper()
	svc := newAccountService(t)
	store := newFakeManagedTokenStore()
	svc.tokenStore = store
	svc.SetTokenPepper(pepper)
	if !svc.ManagedTokensEnabled() {
		t.Fatal("managed tokens should be enabled with a pepper and store")
	}
	return svc, store
}

func TestManagedTokenHashIsDeterministicAndPepperScoped(t *testing.T) {
	a := newAccountService(t)
	a.SetTokenPepper("pepper-one")
	b := newAccountService(t)
	b.SetTokenPepper("pepper-two")

	raw := "omct_abc123"
	if a.hashManagedToken(raw) != a.hashManagedToken(raw) {
		t.Fatal("hash must be deterministic for a fixed pepper")
	}
	if a.hashManagedToken(raw) == b.hashManagedToken(raw) {
		t.Fatal("hash must differ under a different pepper")
	}
	if a.hashManagedToken(raw) == raw {
		t.Fatal("stored hash must not equal the raw token")
	}
}

func TestManagedTokensDisabledWithoutPepper(t *testing.T) {
	svc := newAccountService(t)
	svc.tokenStore = newFakeManagedTokenStore()
	if svc.ManagedTokensEnabled() {
		t.Fatal("managed tokens must be disabled until a pepper is set")
	}
	if _, _, err := svc.IssueManagedToken(context.Background(), "1", "laptop"); !errors.Is(err, ErrManagedTokensNotEnabled) {
		t.Fatalf("issuance without pepper must fail with ErrManagedTokensNotEnabled, got %v", err)
	}
	// An unknown bearer must not panic and must be rejected generically.
	req := httptest.NewRequest("GET", "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer omct_whatever")
	if _, err := svc.AuthorizeAccount(req); err == nil {
		t.Fatal("unknown bearer must be rejected when managed tokens are disabled")
	}
}

func TestIssueManagedTokenReturnsRawOnceAndStoresHash(t *testing.T) {
	svc, store := enableManagedTokens(t, "strong-pepper-value")

	raw, id, err := svc.IssueManagedToken(context.Background(), "42", "ci-runner")
	if err != nil {
		t.Fatalf("issue managed token: %v", err)
	}
	if !strings.HasPrefix(raw, ManagedTokenPrefix) {
		t.Fatalf("raw token must carry the managed prefix, got %q", raw)
	}
	if id == "" {
		t.Fatal("expected a token id")
	}
	// Only the hash is stored — the raw token must not be recoverable from it.
	hash := svc.hashManagedToken(raw)
	if _, ok := store.rows[hash]; !ok {
		t.Fatal("store must hold the token hash")
	}
	for storedHash := range store.rows {
		if storedHash == raw {
			t.Fatal("store must never hold the raw token value")
		}
	}
}

func TestManagedTokenRuntimeEnforcement(t *testing.T) {
	svc, store := enableManagedTokens(t, "strong-pepper-value")

	raw, _, err := svc.IssueManagedToken(context.Background(), "7", "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	hash := svc.hashManagedToken(raw)

	// 1. Valid token authorizes and yields the owning user's claims.
	req := httptest.NewRequest("GET", "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	claims, err := svc.AuthorizeAccount(req)
	if err != nil {
		t.Fatalf("valid managed token must authorize, got %v", err)
	}
	if claims == nil || claims.AccountID != "7" {
		t.Fatalf("expected claims for user 7, got %+v", claims)
	}
	if len(store.touched) == 0 {
		t.Fatal("expected last_used_at touch on a successful validation")
	}

	// 2. Revoking the token rejects the NEXT request (per-request DB enforcement).
	store.rows[hash].revoked = true
	req2 := httptest.NewRequest("GET", "/sync/pull", nil)
	req2.Header.Set("Authorization", "Bearer "+raw)
	if _, err := svc.AuthorizeAccount(req2); !errors.Is(err, ErrManagedTokenRevoked) {
		t.Fatalf("revoked token must be rejected with ErrManagedTokenRevoked, got %v", err)
	}

	// 3. Disabling the owner rejects a still-un-revoked token.
	store.rows[hash].revoked = false
	store.rows[hash].userDisabled = true
	req3 := httptest.NewRequest("GET", "/sync/pull", nil)
	req3.Header.Set("Authorization", "Bearer "+raw)
	if _, err := svc.AuthorizeAccount(req3); !errors.Is(err, ErrManagedTokenUserDisabled) {
		t.Fatalf("disabled owner must reject token with ErrManagedTokenUserDisabled, got %v", err)
	}

	// 4. An unknown managed token falls through to a generic invalid-bearer error.
	req4 := httptest.NewRequest("GET", "/sync/pull", nil)
	req4.Header.Set("Authorization", "Bearer omct_unknown")
	if _, err := svc.AuthorizeAccount(req4); err == nil {
		t.Fatal("unknown managed token must be rejected")
	}
}

func TestManagedTokenDoesNotBreakLegacyOrAccountTokens(t *testing.T) {
	svc, _ := enableManagedTokens(t, "strong-pepper-value")
	svc.SetBearerToken("legacy-shared")

	// Legacy shared secret still authorizes with no account identity.
	legacy := httptest.NewRequest("GET", "/sync/pull", nil)
	legacy.Header.Set("Authorization", "Bearer legacy-shared")
	if claims, err := svc.AuthorizeAccount(legacy); err != nil || claims != nil {
		t.Fatalf("legacy token must authorize with nil claims even when managed tokens are enabled, got claims=%+v err=%v", claims, err)
	}

	// A self-verifying account token still authorizes.
	acct, err := svc.MintAccountToken("acct-x", "dana")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	accountReq := httptest.NewRequest("GET", "/sync/pull", nil)
	accountReq.Header.Set("Authorization", "Bearer "+acct)
	claims, err := svc.AuthorizeAccount(accountReq)
	if err != nil || claims == nil || claims.AccountID != "acct-x" {
		t.Fatalf("account token must still authorize, got claims=%+v err=%v", claims, err)
	}
}
