package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/store"
)

// TestDisabledAccountDeniedOnSyncDataPlane is the CRITICAL C1 proof: an account
// that still holds FULL membership perms (its stateless token is still valid) is
// denied on BOTH the push path (through authorizeProjectOp -> AuthorizeAccountProject)
// AND the mutation-pull path (which does NOT go through AuthorizeAccountProject and
// must be gated separately) once it is disabled.
func TestDisabledAccountDeniedOnSyncDataPlane(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermAll), "owner")
	// Seed a mutation in proj-a so a non-denied pull would leak it.
	ms.mutations = append(ms.mutations, MutationEntry{
		Project:   "proj-a",
		Entity:    store.SyncEntitySession,
		EntityKey: "s-a",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"id":"s-a"}`),
	})
	// The account is DISABLED — but its stateless token still authenticates.
	ms.disableAccount("alice")

	srv := newRBACTestServer(ms, authSvc)

	// Push: even with PermAll, a disabled account must be denied (403).
	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, _ := coerceChunkProject(payload, "proj-a")
	chunkID := chunkIDFromPayload(normalized)
	pushBody, _ := json.Marshal(map[string]any{
		"chunk_id":   chunkID,
		"project":    "proj-a",
		"created_by": "alice",
		"data":       json.RawMessage(payload),
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/sync/push", "token-alice", pushBody))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled account push: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Manifest pull is also RBAC-gated -> 403.
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("disabled account manifest pull: expected 403, got %d body=%q", rec2.Code, rec2.Body.String())
	}

	// Mutation pull must return NOTHING for a disabled account (deny-by-default),
	// even though alice still has a PermAll membership on proj-a.
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, makeAccountRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0", "token-alice", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("mutation pull: expected 200, got %d", rec3.Code)
	}
	var pullResp struct {
		Mutations []StoredMutation `json:"mutations"`
	}
	if err := json.NewDecoder(rec3.Body).Decode(&pullResp); err != nil {
		t.Fatalf("decode mutation pull: %v", err)
	}
	if len(pullResp.Mutations) != 0 {
		t.Fatalf("disabled account must pull zero mutations, got %d: %+v", len(pullResp.Mutations), pullResp.Mutations)
	}
}

// TestActiveAccountUnaffectedBySyncGate is the regression guard: a NON-disabled
// account behaves byte-for-byte as before (push accepted, pull returns its data).
func TestActiveAccountUnaffectedBySyncGate(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermAll), "owner")
	ms.mutations = append(ms.mutations, MutationEntry{
		Project:   "proj-a",
		Entity:    store.SyncEntitySession,
		EntityKey: "s-a",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"id":"s-a"}`),
	})
	// NOT disabled.

	srv := newRBACTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("active account manifest pull: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0", "token-alice", nil))
	var pullResp struct {
		Mutations []StoredMutation `json:"mutations"`
	}
	_ = json.NewDecoder(rec2.Body).Decode(&pullResp)
	if len(pullResp.Mutations) != 1 {
		t.Fatalf("active account must pull its 1 mutation, got %d", len(pullResp.Mutations))
	}
}

// ctxKey is a sentinel context key used to prove the request ctx (not
// context.Background()) reaches the effective-perms resolver (H1).
type ctxKey struct{}

// ctxRecordingResolver records the ctx it was called with so a test can assert the
// request ctx was threaded through AuthorizeAccountProject -> EffectivePerms.
type ctxRecordingResolver struct {
	gotValue any
}

func (r *ctxRecordingResolver) EffectivePerms(ctx context.Context, _, _ string) (int, error) {
	r.gotValue = ctx.Value(ctxKey{})
	return int(cloudauth.PermAll), nil
}

// TestAuthorizeAccountProjectThreadsRequestContext proves H1: the ctx passed to
// AuthorizeAccountProject is threaded to the resolver's DB call instead of
// context.Background(). A sentinel value on the ctx must survive to the resolver.
func TestAuthorizeAccountProjectThreadsRequestContext(t *testing.T) {
	res := &ctxRecordingResolver{}
	ra := &rbacAuthorizer{store: newFakeMembershipStore(), resolver: res}
	claims := &cloudauth.AccountClaims{AccountID: "acct"}

	ctx := context.WithValue(context.Background(), ctxKey{}, "req-42")
	if err := ra.AuthorizeAccountProject(ctx, claims, "proj", cloudauth.PermRead); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if res.gotValue != "req-42" {
		t.Fatalf("request ctx not threaded to resolver: got value %v, want \"req-42\" (still on context.Background()?)", res.gotValue)
	}
}

// fakeDisabledAccountAuth is an Authenticator+AccountService whose Login always
// reports the account is disabled, so the login handler mapping can be exercised.
type fakeDisabledAccountAuth struct{}

func (fakeDisabledAccountAuth) Authorize(*http.Request) error { return nil }
func (fakeDisabledAccountAuth) Signup(username, email, _ string) (*cloudstore.User, error) {
	return &cloudstore.User{ID: "user-1", Username: username, Email: email}, nil
}
func (fakeDisabledAccountAuth) Login(string, string) (string, *cloudstore.User, error) {
	return "", nil, cloudauth.ErrAccountDisabled
}

// TestLoginDisabledAccountReturns401 proves the login handler maps ErrAccountDisabled
// to 401 with the SAME generic "invalid credentials" body as a bad password — a
// disabled account must not be distinguishable from a nonexistent/wrong-password one.
func TestLoginDisabledAccountReturns401(t *testing.T) {
	srv := New(&fakeStore{}, fakeDisabledAccountAuth{}, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"username":"alice","password":"supersecret"}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled login: expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error != "invalid credentials" {
		t.Fatalf("disabled login must return the generic bad-cred message (no leak), got %q", body.Error)
	}
}
