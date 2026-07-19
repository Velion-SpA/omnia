package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/velion/omnia/internal/embed"
)

// fakeLexicalSearcher is a hermetic LexicalSearcher stub — no FTS5/SQLite
// needed to test Service's orchestration logic.
type fakeLexicalSearcher struct {
	hits      []LexicalHit
	err       error
	calls     int
	lastQuery string
	lastOpts  LexicalSearchOptions
}

func (f *fakeLexicalSearcher) Search(ctx context.Context, query string, opts LexicalSearchOptions) ([]LexicalHit, error) {
	f.calls++
	f.lastQuery = query
	f.lastOpts = opts
	return f.hits, f.err
}

// fakeSemanticSearcher is a hermetic embed.Searcher stub — no live Ollama
// needed to test Service's degrade-safe composition.
type fakeSemanticSearcher struct {
	vec         []float32
	embedErr    error
	hits        []embed.Hit
	searchErr   error
	embedCalls  int
	searchCalls int
}

func (f *fakeSemanticSearcher) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	f.embedCalls++
	return f.vec, f.embedErr
}

func (f *fakeSemanticSearcher) Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error) {
	f.searchCalls++
	return f.hits, f.searchErr
}

// fakeScopedSemanticSearcher additionally implements embed.ScopedSearcher,
// so tests can assert Service.semanticHits prefers SearchScoped (and forwards
// opts.Project into it) whenever the concrete Searcher supports it — the
// crowding-out bugfix (engram obs #1436).
type fakeScopedSemanticSearcher struct {
	fakeSemanticSearcher
	scopedHits        []embed.Hit
	scopedErr         error
	scopedCalls       int
	lastScopedProject string
	lastScopedK       int
}

func (f *fakeScopedSemanticSearcher) SearchScoped(ctx context.Context, vec []float32, k int, project string) ([]embed.Hit, error) {
	f.scopedCalls++
	f.lastScopedProject = project
	f.lastScopedK = k
	return f.scopedHits, f.scopedErr
}

// TestService_Search_PrefersScopedSearchWhenProjectKnown proves the
// crowding-out bugfix's wiring: when opts.Project is set AND the configured
// Semantic searcher implements embed.ScopedSearcher, Service.Search calls
// SearchScoped (forwarding the project) instead of the unscoped Search, and
// uses its results.
func TestService_Search_PrefersScopedSearchWhenProjectKnown(t *testing.T) {
	lex := &fakeLexicalSearcher{}
	sem := &fakeScopedSemanticSearcher{
		fakeSemanticSearcher: fakeSemanticSearcher{vec: []float32{1, 0, 0}},
		scopedHits:           []embed.Hit{{ObsID: 7, Score: 0.9}},
	}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{Project: "omnia", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sem.scopedCalls != 1 {
		t.Fatalf("SearchScoped calls: got %d, want 1", sem.scopedCalls)
	}
	if sem.searchCalls != 0 {
		t.Fatalf("unscoped Search calls: got %d, want 0 (SearchScoped should be preferred)", sem.searchCalls)
	}
	if sem.lastScopedProject != "omnia" {
		t.Errorf("SearchScoped project: got %q, want %q", sem.lastScopedProject, "omnia")
	}
	assertIDOrder(t, got, []int64{7})
}

// TestService_Search_NoProject_UsesUnscopedSearchEvenWithScopedSearcher
// proves an empty opts.Project degrades to the plain, unscoped Search even
// when the configured Searcher supports ScopedSearcher — mirrors
// SearchScoped("")'s own "no restriction" semantics.
func TestService_Search_NoProject_UsesUnscopedSearchEvenWithScopedSearcher(t *testing.T) {
	lex := &fakeLexicalSearcher{}
	sem := &fakeScopedSemanticSearcher{
		fakeSemanticSearcher: fakeSemanticSearcher{
			vec:  []float32{1, 0, 0},
			hits: []embed.Hit{{ObsID: 9, Score: 0.9}},
		},
	}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sem.searchCalls != 1 || sem.scopedCalls != 0 {
		t.Fatalf("expected unscoped Search=1/SearchScoped=0 calls, got Search=%d SearchScoped=%d", sem.searchCalls, sem.scopedCalls)
	}
	assertIDOrder(t, got, []int64{9})
}

// TestService_Search_ScopedSearcherWithoutProject_FallsBackToUnscopedSearch
// proves backward compatibility: a Semantic searcher that does NOT implement
// embed.ScopedSearcher (e.g. today's test fakes / cloud-side adapters) keeps
// working exactly as before, via plain Search, regardless of opts.Project.
func TestService_Search_ScopedSearcherWithoutProject_FallsBackToUnscopedSearch(t *testing.T) {
	lex := &fakeLexicalSearcher{}
	sem := &fakeSemanticSearcher{
		vec:  []float32{1, 0, 0},
		hits: []embed.Hit{{ObsID: 3, Score: 0.9}},
	}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{Project: "omnia", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sem.searchCalls != 1 {
		t.Fatalf("unscoped Search calls: got %d, want 1 (fallback when Searcher lacks ScopedSearcher)", sem.searchCalls)
	}
	assertIDOrder(t, got, []int64{3})
}

func TestService_Search_FusesLexicalAndSemantic(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: []LexicalHit{
		{ID: 1, UpdatedAt: "2024-01-01"},
		{ID: 2, UpdatedAt: "2024-01-01"},
	}}
	sem := &fakeSemanticSearcher{
		vec:  []float32{1, 0, 0},
		hits: []embed.Hit{{ObsID: 2, Score: 0.9}, {ObsID: 3, Score: 0.8}},
	}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sem.embedCalls != 1 || sem.searchCalls != 1 {
		t.Fatalf("expected exactly one EmbedQuery/Search call, got embed=%d search=%d", sem.embedCalls, sem.searchCalls)
	}
	// id 2 appears in both lists and must outrank the single-list ids 1 and 3.
	if len(got) == 0 || got[0].ID != 2 {
		t.Fatalf("Search() top result = %v, want id 2 (present in both lists) first", idsOf(got))
	}
}

func TestService_Search_NilSemantic_DegradesToLexicalOnly(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: []LexicalHit{
		{ID: 1, UpdatedAt: "2024-01-01"},
		{ID: 2, UpdatedAt: "2024-01-02"},
	}}
	svc := NewService(lex, nil, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertIDOrder(t, got, []int64{1, 2})
}

func TestService_Search_EmbedQueryError_DegradesToLexicalOnly(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: []LexicalHit{{ID: 1, UpdatedAt: "2024-01-01"}}}
	sem := &fakeSemanticSearcher{embedErr: errors.New("ollama down")}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("Search: expected no error (degrade to lexical-only), got %v", err)
	}
	assertIDOrder(t, got, []int64{1})
	if sem.searchCalls != 0 {
		t.Errorf("Search should not be called after EmbedQuery fails, got %d calls", sem.searchCalls)
	}
}

func TestService_Search_SemanticSearchError_DegradesToLexicalOnly(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: []LexicalHit{{ID: 1, UpdatedAt: "2024-01-01"}}}
	sem := &fakeSemanticSearcher{vec: []float32{1, 0, 0}, searchErr: errors.New("ollama down")}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("Search: expected no error (degrade to lexical-only), got %v", err)
	}
	assertIDOrder(t, got, []int64{1})
}

func TestService_Search_NilLexicalSearcher_ReturnsError(t *testing.T) {
	svc := NewService(nil, nil, DefaultFuseParams())
	if _, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{}); err == nil {
		t.Fatal("Search: expected error for nil LexicalSearcher, got nil")
	}
}

func TestService_Search_LexicalError_ReturnsError(t *testing.T) {
	lex := &fakeLexicalSearcher{err: errors.New("fts5 down")}
	svc := NewService(lex, nil, DefaultFuseParams())
	if _, err := svc.Search(context.Background(), "hola", LexicalSearchOptions{}); err == nil {
		t.Fatal("Search: expected error when the mandatory lexical search fails, got nil")
	}
}

// TestService_Search_ZeroMatches_ReturnsEmptyNotError proves that a query
// with no lexical AND no semantic matches degrades to an empty result set,
// never an error — "no results" is not a failure.
func TestService_Search_ZeroMatches_ReturnsEmptyNotError(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: nil}
	sem := &fakeSemanticSearcher{vec: []float32{1, 0, 0}, hits: nil}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "zzzznomatch", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("Search: expected no error for zero matches, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search: got %v, want empty result set", got)
	}
}

// TestService_Search_SemanticOnlyHitSurfacesDespiteZeroLexicalMatches proves
// the whole point of hybrid recall: a paraphrase FTS5 alone would miss (zero
// lexical matches) still surfaces through the semantic side.
func TestService_Search_SemanticOnlyHitSurfacesDespiteZeroLexicalMatches(t *testing.T) {
	lex := &fakeLexicalSearcher{hits: nil}
	sem := &fakeSemanticSearcher{
		vec:  []float32{1, 0, 0},
		hits: []embed.Hit{{ObsID: 42, Score: 0.9}},
	}
	svc := NewService(lex, sem, DefaultFuseParams())

	got, err := svc.Search(context.Background(), "paraphrase of a saved memory", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertIDOrder(t, got, []int64{42})
}

func TestService_Search_PassesQueryAndOptsToLexical(t *testing.T) {
	lex := &fakeLexicalSearcher{}
	svc := NewService(lex, nil, DefaultFuseParams())
	opts := LexicalSearchOptions{Type: "decision", Project: "omnia", Scope: "project", Limit: 5}

	if _, err := svc.Search(context.Background(), "recall query", opts); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if lex.calls != 1 {
		t.Fatalf("Lexical.Search calls: got %d, want 1", lex.calls)
	}
	if lex.lastQuery != "recall query" {
		t.Errorf("query: got %q, want %q", lex.lastQuery, "recall query")
	}
	if lex.lastOpts != opts {
		t.Errorf("opts: got %+v, want %+v", lex.lastOpts, opts)
	}
}
