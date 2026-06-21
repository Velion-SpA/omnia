package clouddash

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/engramdb"
)

// fakeCloudStore returns a fixed set of replicated rows across two projects so the
// adapter's per-account scoping can be exercised without a real Postgres store.
type fakeCloudStore struct {
	projects []cloudstore.DashboardProjectRow
	obs      []cloudstore.DashboardObservationRow
}

func (f fakeCloudStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	return f.projects, nil
}

func (f fakeCloudStore) ListRecentObservations(string, string, int) ([]cloudstore.DashboardObservationRow, error) {
	return f.obs, nil
}

func newTestSource() *Source {
	return New(fakeCloudStore{
		projects: []cloudstore.DashboardProjectRow{
			{Project: "alpha", Observations: 1},
			{Project: "beta", Observations: 1},
		},
		obs: []cloudstore.DashboardObservationRow{
			{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Alpha memory", Content: "alpha body"},
			{Project: "beta", SessionID: "s-b", SyncID: "b1", Type: "decision", Title: "Beta memory", Content: "beta body"},
		},
	})
}

// TestAccountScopeSeesOnlyItsProjects proves the core RBAC guarantee: an account
// scoped to "alpha" never observes "beta" rows through any read path, and a
// cross-account detail lookup resolves to not-found (the dashboard renders a 404).
func TestAccountScopeSeesOnlyItsProjects(t *testing.T) {
	src := newTestSource()
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}))

	structural, ok := src.Structural()
	if !ok {
		t.Fatal("cloud source must provide structural reads")
	}

	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "alpha" {
		t.Fatalf("account scoped to alpha must see only alpha, got %+v", projects)
	}

	rows, err := structural.List(ctx, engramdb.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Project != "alpha" {
		t.Fatalf("List must return only alpha rows, got %+v", rows)
	}

	records := src.Records()

	// The account's own memory resolves.
	if _, err := records.GetObservation(ctx, syncIDToInt("a1")); err != nil {
		t.Fatalf("account must see its own observation: %v", err)
	}
	// A cross-account memory is invisible — not found, never another account's data.
	if _, err := records.GetObservation(ctx, syncIDToInt("b1")); err == nil {
		t.Fatal("cross-account observation lookup must fail (404), not leak beta data")
	}

	found, err := records.Search(ctx, "memory", "", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, o := range found {
		if o.Project != "alpha" {
			t.Fatalf("search must not leak %q rows to an alpha-scoped account", o.Project)
		}
	}
}

// TestOperatorScopeSeesEverything proves the server operator (admin token) path
// retains full visibility across every replicated project.
func TestOperatorScopeSeesEverything(t *testing.T) {
	src := newTestSource()
	ctx := WithScope(context.Background(), NewScope(true, nil))

	structural, _ := src.Structural()
	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("operator must see all projects, got %+v", projects)
	}

	if _, err := src.Records().GetObservation(ctx, syncIDToInt("b1")); err != nil {
		t.Fatalf("operator must see every observation: %v", err)
	}
}

// TestMissingScopeFailsClosed proves a request with no injected scope sees nothing,
// so a wiring mistake can only ever under-expose, never leak across accounts.
func TestMissingScopeFailsClosed(t *testing.T) {
	src := newTestSource()
	ctx := context.Background() // no scope injected

	structural, _ := src.Structural()
	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("missing scope must expose nothing, got %+v", projects)
	}
	if _, err := src.Records().GetObservation(ctx, syncIDToInt("a1")); err == nil {
		t.Fatal("missing scope must not resolve any observation")
	}
}

// TestReadOnlyAndNoSemantic documents the cloud's degraded capabilities: no
// mutations (read-only replica) and no embeddings (graph/semantic search off).
func TestReadOnlyAndNoSemantic(t *testing.T) {
	src := newTestSource()
	if _, ok := src.Mutations(); ok {
		t.Fatal("cloud dashboard must be read-only")
	}
	if _, ok := src.Semantic(); ok {
		t.Fatal("cloud dashboard must not advertise a semantic index")
	}
}
