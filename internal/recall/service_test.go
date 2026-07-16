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
