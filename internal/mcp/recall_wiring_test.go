package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

// fakeEmbedSearcher is a hermetic, in-memory embed.Searcher fake used to
// exercise handleSearch's fused path without a live Ollama.
type fakeEmbedSearcher struct {
	hits      []embed.Hit
	embedErr  error
	searchErr error
}

func (f fakeEmbedSearcher) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

func (f fakeEmbedSearcher) Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.hits, nil
}

func searchResultIDs(t *testing.T, body map[string]any) []int64 {
	t.Helper()
	raw, ok := body["results"].([]any)
	if !ok {
		t.Fatalf("expected results array, got %#v", body["results"])
	}
	ids := make([]int64, 0, len(raw))
	for _, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("expected result entry to be a map, got %#v", entry)
		}
		idFloat, ok := m["id"].(float64)
		if !ok {
			t.Fatalf("expected result id to be numeric, got %#v", m["id"])
		}
		ids = append(ids, int64(idFloat))
	}
	return ids
}

// TestHandleSearch_RecallDisabled_MatchesLegacyFTS5Path is the flag-OFF
// regression pin (task 3.3): with cfg.Recall == nil (recall.enabled=false,
// the default), handleSearch's result order/content must be identical to
// calling store.Search directly — proving the disabled branch is truly
// today's exact FTS5-only path, byte-for-byte, and rollback (flipping
// recall.enabled back to false) is always safe.
func TestHandleSearch_RecallDisabled_MatchesLegacyFTS5Path(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-pin", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, title := range []string{"Fix panic in parser", "Fix panic in loader", "Unrelated note"} {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-pin",
			Type:      "bugfix",
			Title:     title,
			Content:   title,
			Project:   "engram",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("add observation %q: %v", title, err)
		}
	}

	wantResults, err := s.Search("panic", store.SearchOptions{Project: "engram", Scope: "project", Limit: 5})
	if err != nil {
		t.Fatalf("legacy store.Search: %v", err)
	}
	if len(wantResults) == 0 {
		t.Fatalf("expected legacy store.Search to find matches")
	}

	// cfg.Recall is nil (zero value) — the disabled branch.
	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "panic",
		"project": "engram",
		"scope":   "project",
		"limit":   5.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	gotIDs := searchResultIDs(t, body)

	if len(gotIDs) != len(wantResults) {
		t.Fatalf("recall-disabled result count = %d; want %d (legacy store.Search)", len(gotIDs), len(wantResults))
	}
	for i, want := range wantResults {
		if gotIDs[i] != want.ID {
			t.Fatalf("recall-disabled result[%d] id = %d; want %d (legacy store.Search order): got=%v want=%v",
				i, gotIDs[i], want.ID, gotIDs, idsOfResults(wantResults))
		}
	}
}

func idsOfResults(results []store.SearchResult) []int64 {
	ids := make([]int64, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestHandleSearch_RecallEnabled_SurfacesSemanticOnlyParaphrase is the
// flag-ON fusion test (task 3.4): with cfg.Recall wired to a fake
// embed.Searcher, a memory that ISN'T a lexical (FTS5) match for the query
// must still surface in the response because the fake semantic side ranks
// it — proving the fused path actually changes recall behavior, not just
// passing lexical results through untouched.
func TestHandleSearch_RecallEnabled_SurfacesSemanticOnlyParaphrase(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-fuse", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	lexicalID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-fuse",
		Type:      "bugfix",
		Title:     "Fix login timeout",
		Content:   "Fix login timeout under load",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add lexical observation: %v", err)
	}

	// Paraphrase: no lexical overlap with the query "login timeout" at all,
	// so store.Search alone would never surface it.
	paraphraseID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-fuse",
		Type:      "bugfix",
		Title:     "Session drops under heavy traffic",
		Content:   "Users get disconnected when the server is busy",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add paraphrase observation: %v", err)
	}

	lexical, err := NewStoreLexicalSearcher(s).Search(context.Background(), "login timeout", recall.LexicalSearchOptions{
		Project: "engram", Scope: "project", Limit: 10,
	})
	if err != nil {
		t.Fatalf("sanity lexical search: %v", err)
	}
	for _, h := range lexical {
		if h.ID == paraphraseID {
			t.Fatalf("test setup invalid: paraphrase observation unexpectedly matched lexically")
		}
	}

	semantic := fakeEmbedSearcher{hits: []embed.Hit{
		{ObsID: int(paraphraseID), Score: 0.9},
	}}
	svc := recall.NewService(NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	search := handleSearch(s, MCPConfig{Recall: svc}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "login timeout",
		"project": "engram",
		"scope":   "project",
		"limit":   10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	gotIDs := searchResultIDs(t, body)

	foundLexical, foundParaphrase := false, false
	for _, id := range gotIDs {
		if id == lexicalID {
			foundLexical = true
		}
		if id == paraphraseID {
			foundParaphrase = true
		}
	}
	if !foundLexical {
		t.Fatalf("expected lexical match (id %d) in fused results, got %v", lexicalID, gotIDs)
	}
	if !foundParaphrase {
		t.Fatalf("expected semantic-only paraphrase (id %d) surfaced by fusion, got %v", paraphraseID, gotIDs)
	}
}

// TestHandleSearch_RecallEnabled_DegradesToLexicalWhenSemanticDown is the
// degrade-safety test (item 5): recall.enabled=true but the semantic side
// (Ollama, in production) is down — handleSearch must still return the
// lexical results, not an error. recall.Service already degrades this
// internally (PR2); this test proves the wired path surfaces that instead
// of swallowing or erroring on it.
func TestHandleSearch_RecallEnabled_DegradesToLexicalWhenSemanticDown(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-degrade", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	lexicalID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-degrade",
		Type:      "bugfix",
		Title:     "Fix panic in parser",
		Content:   "Fix panic in parser branch when args are missing",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Simulates Ollama down: EmbedQuery fails.
	semantic := fakeEmbedSearcher{embedErr: errOllamaDownForTest}
	svc := recall.NewService(NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	search := handleSearch(s, MCPConfig{Recall: svc}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "panic",
		"project": "engram",
		"scope":   "project",
		"limit":   5.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected degrade-to-lexical, not an error: %s", callResultText(t, res))
	}
	if !strings.Contains(callResultText(t, res), "Found 1 memories") {
		t.Fatalf("expected the lexical match still surfaced, got: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	gotIDs := searchResultIDs(t, body)
	if len(gotIDs) != 1 || gotIDs[0] != lexicalID {
		t.Fatalf("expected exactly the lexical match (id %d), got %v", lexicalID, gotIDs)
	}
}

type staticTestError string

func (e staticTestError) Error() string { return string(e) }

const errOllamaDownForTest = staticTestError("ollama down: connection refused")

// TestHandleSearch_RecallEnabled_SemanticLegScopedByProject is the
// cross-project isolation regression test for the semantic-leak bug found by
// adversarial review before PR3 merged to PR4: embed.Store.Search has no
// project WHERE clause (it ranks across every project in the shared,
// machine-wide embeddings DB), so a fake embed.Searcher that scores a
// project-B memory highly must NOT leak it into a project-A mem_search
// response — while a genuine project-A semantic-only paraphrase (no lexical
// overlap with the query at all) must still surface, proving the fix filters
// by project rather than disabling the semantic-only-hit feature entirely.
func TestHandleSearch_RecallEnabled_SemanticLegScopedByProject(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-proj-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session (proj-a): %v", err)
	}
	if err := s.CreateSession("s-proj-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session (proj-b): %v", err)
	}

	lexicalID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-proj-a",
		Type:      "bugfix",
		Title:     "Fix login timeout",
		Content:   "Fix login timeout under load",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add lexical observation (proj-a): %v", err)
	}

	// Legitimate same-project paraphrase: no lexical overlap with the query
	// "login timeout" at all, so only the semantic leg can surface it — this
	// must keep working after the fix.
	paraphraseID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-proj-a",
		Type:      "bugfix",
		Title:     "Session drops under heavy traffic",
		Content:   "Users get disconnected when the server is busy",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add paraphrase observation (proj-a): %v", err)
	}

	// Cross-project semantic-only memory: belongs to proj-b, no lexical
	// overlap with the query, but the fake semantic side scores it highest —
	// this must NEVER surface in a proj-a search.
	leakID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-proj-b",
		Type:      "secret",
		Title:     "Project B database credentials",
		Content:   "Rotated the prod database credentials for project B",
		Project:   "proj-b",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add cross-project observation (proj-b): %v", err)
	}

	// Sanity: neither semantic-only observation is a lexical match for the
	// query under proj-a's own scoped lexical search.
	lexical, err := NewStoreLexicalSearcher(s).Search(context.Background(), "login timeout", recall.LexicalSearchOptions{
		Project: "proj-a", Scope: "project", Limit: 10,
	})
	if err != nil {
		t.Fatalf("sanity lexical search: %v", err)
	}
	for _, h := range lexical {
		if h.ID == paraphraseID {
			t.Fatalf("test setup invalid: paraphrase observation unexpectedly matched lexically")
		}
	}

	// The fake semantic searcher simulates the machine-wide, unscoped
	// embeddings DB: it returns both the legitimate proj-a paraphrase AND the
	// proj-b leak, ranked with the leak scoring even higher — proving a naive
	// fix that merely "takes the top hits" would still leak.
	semantic := fakeEmbedSearcher{hits: []embed.Hit{
		{ObsID: int(leakID), Score: 0.95},
		{ObsID: int(paraphraseID), Score: 0.9},
	}}
	svc := recall.NewService(NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	search := handleSearch(s, MCPConfig{Recall: svc}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "login timeout",
		"project": "proj-a",
		"scope":   "project",
		"limit":   10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	gotIDs := searchResultIDs(t, body)

	foundLexical, foundParaphrase, foundLeak := false, false, false
	for _, id := range gotIDs {
		switch id {
		case lexicalID:
			foundLexical = true
		case paraphraseID:
			foundParaphrase = true
		case leakID:
			foundLeak = true
		}
	}
	if foundLeak {
		t.Fatalf("cross-project leak: proj-b memory (id %d) surfaced in proj-a search, got %v", leakID, gotIDs)
	}
	if !foundLexical {
		t.Fatalf("expected lexical match (id %d) in fused results, got %v", lexicalID, gotIDs)
	}
	if !foundParaphrase {
		t.Fatalf("expected same-project semantic-only paraphrase (id %d) to still surface, got %v", paraphraseID, gotIDs)
	}
}
