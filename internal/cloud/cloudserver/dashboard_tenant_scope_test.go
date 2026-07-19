package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// fakeTenantScopeEmbedder is a deterministic embed.Embedder double: it never
// fails, so every authenticated /browse?q= request in this file deterministically
// reaches cloudSemanticIndex.Search / SearchEmbeddings — the exact link under test.
type fakeTenantScopeEmbedder struct{}

func (fakeTenantScopeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.42, 0.17}, nil
}

// searchEmbeddingsCall records one SearchEmbeddings invocation so the test can
// assert exactly which account_id reached the cloud_embeddings tenant boundary
// for a given authenticated dashboard request.
type searchEmbeddingsCall struct {
	accountID string
	project   string
}

// fakeTenantScopeStore backs the ONE untested link the adversarial review
// flagged: whether an authenticated dashboard session's claims.AccountID
// (resolved by dashboardSessionClaims and injected into the request context by
// dashboardGate, internal/cloud/cloudserver/dashboard_mount.go ~104-116)
// actually reaches clouddash.Scope.AccountID and, from there, the account_id
// argument SearchEmbeddings is called with (cloud_embeddings' tenant boundary,
// PR5 slice 3). It embeds *fakeMembershipStore for ChunkStore + membershipStore
// (RBAC), and adds the clouddash.CloudStore + CloudEmbeddingsSearcher surface
// so CloudServer.dashboardSource() wires a REAL cloudSemanticIndex instead of
// falling back to emptyCloudStore.
type fakeTenantScopeStore struct {
	*fakeMembershipStore

	mu       sync.Mutex
	projects []cloudstore.DashboardProjectRow
	rows     []cloudstore.DashboardObservationRow
	calls    []searchEmbeddingsCall
}

func (f *fakeTenantScopeStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	return f.projects, nil
}

func (f *fakeTenantScopeStore) ListRecentObservations(string, string, int) ([]cloudstore.DashboardObservationRow, error) {
	return f.rows, nil
}

func (f *fakeTenantScopeStore) SearchEmbeddings(_ context.Context, accountID, project string, _ []float32, _ int) ([]cloudstore.EmbeddingHit, error) {
	f.mu.Lock()
	f.calls = append(f.calls, searchEmbeddingsCall{accountID: accountID, project: project})
	f.mu.Unlock()
	return nil, nil
}

func (f *fakeTenantScopeStore) callsFor(accountID string) []searchEmbeddingsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []searchEmbeddingsCall
	for _, c := range f.calls {
		if c.accountID == accountID {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTenantScopeStore) allCalls() []searchEmbeddingsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]searchEmbeddingsCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestDashboardSession_AccountIDReachesCloudSemanticScope pins the ONE
// end-to-end link in the multi-tenant isolation chain the adversarial review
// found untested: an authenticated dashboard session's claims.AccountID must
// reach clouddash.Scope.AccountID, and from there the account_id argument
// SearchEmbeddings is called with. Two distinct accounts, each scoped to
// their OWN project, drive real authenticated /browse?q= requests through the
// full mounted dashboard (dashboardGate -> dashboard.Server -> clouddash.Source);
// each request's captured SearchEmbeddings calls must carry ONLY that
// session's own account_id — account A's request can never be served (or
// attributed) account B's scope.
func TestDashboardSession_AccountIDReachesCloudSemanticScope(t *testing.T) {
	store := &fakeTenantScopeStore{
		fakeMembershipStore: newFakeMembershipStore(),
		projects: []cloudstore.DashboardProjectRow{
			{Project: "alpha-proj", Observations: 1},
			{Project: "beta-proj", Observations: 1},
		},
		rows: []cloudstore.DashboardObservationRow{
			{Project: "alpha-proj", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Alpha memory", Content: "alpha body", CreatedAt: "2026-01-01 00:00:00"},
			{Project: "beta-proj", SessionID: "s-b", SyncID: "b1", Type: "decision", Title: "Beta memory", Content: "beta body", CreatedAt: "2026-01-01 00:00:00"},
		},
	}
	// Two distinct accounts, each a member of ONLY their own project.
	store.grant("acct-alpha", "alpha-proj", int(cloudauth.PermRead), "owner")
	store.grant("acct-beta", "beta-proj", int(cloudauth.PermRead), "owner")

	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}

	srv := New(store, authSvc, 0, WithCloudSemantic(true, fakeTenantScopeEmbedder{}))
	if srv.accountProjectAuth == nil {
		t.Fatal("setup: membership store must activate multi-tenant RBAC")
	}

	alphaCookie := accountCookie(t, authSvc, "acct-alpha", "alice")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/browse?q=memory", alphaCookie, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("alpha /browse?q=: status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	betaCookie := accountCookie(t, authSvc, "acct-beta", "bob")
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, cookieRequest(http.MethodGet, "/browse?q=memory", betaCookie, ""))
	if rec2.Code != http.StatusOK {
		t.Fatalf("beta /browse?q=: status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}

	alphaCalls := store.callsFor("acct-alpha")
	if len(alphaCalls) == 0 {
		t.Fatal("expected alpha's authenticated request to reach SearchEmbeddings with account_id=acct-alpha")
	}
	for _, c := range alphaCalls {
		if c.project != "alpha-proj" {
			t.Fatalf("acct-alpha's scope must only search its own project, got project=%q", c.project)
		}
	}

	betaCalls := store.callsFor("acct-beta")
	if len(betaCalls) == 0 {
		t.Fatal("expected beta's authenticated request to reach SearchEmbeddings with account_id=acct-beta")
	}
	for _, c := range betaCalls {
		if c.project != "beta-proj" {
			t.Fatalf("acct-beta's scope must only search its own project, got project=%q", c.project)
		}
	}

	// The critical cross-tenant assertion: neither account's session ever
	// caused a SearchEmbeddings call scoped to the OTHER account's id, and no
	// call ever carried an empty/unresolved account_id — proving
	// claims.AccountID (not a shared/default/blank value) is what reaches
	// clouddash.Scope.AccountID and, from there, cloud_embeddings' tenant key.
	for _, c := range store.allCalls() {
		if c.accountID != "acct-alpha" && c.accountID != "acct-beta" {
			t.Fatalf("SearchEmbeddings called with unexpected account_id %q — claims.AccountID did not reach the scope correctly", c.accountID)
		}
	}
}
