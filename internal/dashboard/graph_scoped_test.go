package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/engramdb"
)

// fakeGraphStructural is a minimal StructuralReader stand-in whose Projects()
// returns a fixed raw project name list, so graphRawProjects' alias/group
// expansion (rawProjectsForCanonical / groupRawNames) can be exercised without
// a real engramdb.
type fakeGraphStructural struct {
	rawProjects []string
}

func (f fakeGraphStructural) List(context.Context, engramdb.Filter) ([]engramdb.Observation, error) {
	return nil, nil
}
func (f fakeGraphStructural) ListByIDs(context.Context, []int) ([]engramdb.Observation, error) {
	return nil, nil
}
func (f fakeGraphStructural) Projects(context.Context) ([]engramdb.ProjectCount, error) {
	out := make([]engramdb.ProjectCount, len(f.rawProjects))
	for i, n := range f.rawProjects {
		out[i] = engramdb.ProjectCount{Name: n, Count: 1}
	}
	return out, nil
}
func (f fakeGraphStructural) ProjectsCanonical(context.Context, func(string) string) ([]engramdb.ProjectCount, error) {
	return nil, nil
}
func (f fakeGraphStructural) Types(context.Context) ([]engramdb.TypeCount, error) { return nil, nil }

// graphTestDataSource is a minimal DataSource exposing only what handleGraph's
// code path touches: an optional Structural (for raw project-name resolution)
// and an optional Semantic (the embeddings graph). Records/Mutations are
// unused by this handler and return safe zero values.
type graphTestDataSource struct {
	structural StructuralReader
	sem        SemanticIndex
}

func (d graphTestDataSource) Health(context.Context) error { return nil }
func (d graphTestDataSource) Records() RecordReader        { return nil }
func (d graphTestDataSource) Structural() (StructuralReader, bool) {
	if d.structural == nil {
		return nil, false
	}
	return d.structural, true
}
func (d graphTestDataSource) Semantic() (SemanticIndex, bool) {
	if d.sem == nil {
		return nil, false
	}
	return d.sem, true
}
func (d graphTestDataSource) Mutations() (MutationWriter, bool) { return nil, false }
func (d graphTestDataSource) Close() error                      { return nil }

func newGraphTestServer(t *testing.T, cfg Config, ds DataSource) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServerWithDataSource(cfg, ds, logger)
}

// --- graphRawProjects: unit-level ---

func TestGraphRawProjects_EmptyProjectReturnsNil(t *testing.T) {
	srv := newGraphTestServer(t, Config{}, graphTestDataSource{})
	got := srv.graphRawProjects(context.Background(), "")
	if got != nil {
		t.Errorf("graphRawProjects(\"\") = %v, want nil (whole store, unscoped)", got)
	}
}

// TestGraphRawProjects_ResolvesAliasedRawNames proves a canonical project
// filter expands to ALL its raw DB variants (case/alias) via the SAME
// rawProjectsForCanonical machinery browseFromDB already uses for the
// structural DB — so H3's SQL scoping never drops a row the previous
// whole-store scan used to include just because of a raw/canonical mismatch.
func TestGraphRawProjects_ResolvesAliasedRawNames(t *testing.T) {
	cfg := Config{ProjectAliases: map[string]string{"01.- velion": "velion"}}
	ds := graphTestDataSource{structural: fakeGraphStructural{
		rawProjects: []string{"01.- Velion", "01.- velion", "velion", "other"},
	}}
	srv := newGraphTestServer(t, cfg, ds)

	got := srv.graphRawProjects(context.Background(), "velion")
	want := map[string]bool{"01.- Velion": true, "01.- velion": true, "velion": true}
	if len(got) != len(want) {
		t.Fatalf("graphRawProjects(velion) = %v, want exactly %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("graphRawProjects(velion) included unexpected raw name %q", n)
		}
	}
}

// TestGraphRawProjects_GroupParentIncludesChildren proves a group-parent
// project filter resolves to the parent's raw names PLUS every child's raw
// names — preserving buildGraphView's existing "parent + children" scope
// after the SQL WHERE...IN narrowing (no cross-child edges silently lost).
func TestGraphRawProjects_GroupParentIncludesChildren(t *testing.T) {
	cfg := Config{ProjectGroups: map[string][]string{"workly": {"workly-marketing"}}}
	ds := graphTestDataSource{structural: fakeGraphStructural{
		rawProjects: []string{"workly", "workly-marketing", "unrelated"},
	}}
	srv := newGraphTestServer(t, cfg, ds)

	got := srv.graphRawProjects(context.Background(), "workly")
	want := map[string]bool{"workly": true, "workly-marketing": true}
	if len(got) != len(want) {
		t.Fatalf("graphRawProjects(workly) = %v, want exactly %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("graphRawProjects(workly) included unexpected raw name %q", n)
		}
	}
}

// TestGraphRawProjects_NoStructuralDBFallsBackToCanonical proves that when
// raw-name resolution is unavailable (no structural DB), graphRawProjects
// degrades to the literal canonical project (+ children) instead of failing
// closed — the SQL filter still narrows the common (unaliased) case.
func TestGraphRawProjects_NoStructuralDBFallsBackToCanonical(t *testing.T) {
	cfg := Config{ProjectGroups: map[string][]string{"workly": {"workly-marketing"}}}
	srv := newGraphTestServer(t, cfg, graphTestDataSource{}) // no structural reader
	got := srv.graphRawProjects(context.Background(), "workly")
	want := map[string]bool{"workly": true, "workly-marketing": true}
	if len(got) != len(want) {
		t.Fatalf("graphRawProjects(workly, no db) = %v, want fallback %v", got, want)
	}
}

// --- handleGraph: HTTP-level ---

// TestHandleGraph_ProjectScoped_RendersNo500 exercises the full HTTP path
// with a REAL embed.Store (via LocalSearcher) seeded across two projects,
// proving ?project=projA renders successfully (no 500) and the graph payload
// contains only projA's memory — the observable outcome of H3's SQL scoping.
func TestHandleGraph_ProjectScoped_RendersNo500(t *testing.T) {
	store, err := embed.OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rowA := embed.Row{
		SyncID: "a1", ObsID: 1, Project: "projA", Type: "decision", Title: "A one",
		UpdatedAt: "2024-01-01 00:00:00", ContentHash: "ha1", Model: "test", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2024-01-01 00:00:00",
	}
	rowA2 := rowA
	rowA2.SyncID, rowA2.ObsID, rowA2.ContentHash = "a2", 2, "ha2"
	rowA2.Vector = []float32{0.999, 0.045, 0}

	rowB := rowA
	rowB.SyncID, rowB.ObsID, rowB.Project, rowB.ContentHash = "b1", 3, "projB", "hb1"
	rowB.Vector = []float32{0, 1, 0}

	ctx := context.Background()
	for _, r := range []embed.Row{rowA, rowA2, rowB} {
		if err := store.Upsert(ctx, r); err != nil {
			t.Fatalf("Upsert(%s): %v", r.SyncID, err)
		}
	}

	sem := embed.NewSearcher(store, nil)
	srv := newGraphTestServer(t, Config{}, graphTestDataSource{sem: sem})

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	testSrv := httptest.NewServer(mux)
	t.Cleanup(testSrv.Close)

	resp, err := http.Get(testSrv.URL + "/graph?project=projA")
	if err != nil {
		t.Fatalf("GET /graph?project=projA: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}
