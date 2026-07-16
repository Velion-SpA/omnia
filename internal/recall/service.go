package recall

import (
	"context"
	"fmt"

	"github.com/velion/omnia/internal/embed"
)

// LexicalSearchOptions filters a lexical query, mirroring internal/store's
// SearchOptions. recall defines its own copy (rather than importing
// internal/store) so this package stays a dependency-free leaf; the wiring
// layer (PR3+) adapts store.SearchOptions to/from this shape.
type LexicalSearchOptions struct {
	Type    string
	Project string
	Scope   string
	Limit   int
}

// LexicalSearcher is the lexical (FTS5/BM25) search port recall.Service
// composes against. recall defines this interface itself instead of
// importing internal/store, keeping this package a dependency-free leaf; the
// wiring layer adapts store.Store.Search's results into []LexicalHit — any
// future lexical source (e.g. a cloud lexical index) can satisfy this same
// port via its own thin adapter, mirroring embed.Searcher (design D6).
type LexicalSearcher interface {
	Search(ctx context.Context, query string, opts LexicalSearchOptions) ([]LexicalHit, error)
}

// defaultSemanticK is the number of semantic candidates requested from
// Searcher.Search when the caller didn't specify a limit.
const defaultSemanticK = 20

// Service orchestrates lexical + semantic recall through Fuse and the
// adaptive floor into one fused, ranked result. It is intentionally
// degrade-safe: a nil Semantic, a failing EmbedQuery, or a failing Search
// never turn into a hard error — semantic recall is a pure enhancement over
// the lexical baseline, never a hard dependency. This mirrors today's
// FTS5-only behavior when recall.enabled=false or Ollama is unreachable.
//
// Service has no consumer yet in PR2 — mem_search wiring (internal/mcp) is
// PR3 scope; its only caller here is its own tests.
type Service struct {
	Lexical  LexicalSearcher
	Semantic embed.Searcher // nil disables semantic recall (degrade to lexical-only)
	Params   FuseParams
}

// NewService builds a Service. semantic may be nil to disable semantic
// recall entirely (e.g. embeddings disabled in config).
func NewService(lexical LexicalSearcher, semantic embed.Searcher, params FuseParams) *Service {
	return &Service{Lexical: lexical, Semantic: semantic, Params: params}
}

// Search runs the lexical query, best-effort fuses in semantic hits, and
// returns the fused, ranked result. Lexical is mandatory — it is the
// always-on baseline store.Search already provides; Semantic degrades
// silently on any failure. A lexical query with zero matches (and no
// semantic hits) returns an empty, non-error result — never a sentinel
// error for "no results".
func (s *Service) Search(ctx context.Context, query string, opts LexicalSearchOptions) ([]Result, error) {
	if s.Lexical == nil {
		return nil, fmt.Errorf("recall: Service.Search: no LexicalSearcher configured")
	}

	lexical, err := s.Lexical.Search(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("recall: lexical search: %w", err)
	}

	semantic := s.semanticHits(ctx, query, opts.Limit)
	return Fuse(lexical, semantic, s.Params), nil
}

// semanticHits best-effort embeds the query and searches for semantic
// candidates. Any failure along the way (nil Semantic, EmbedQuery error,
// Search error) yields an empty slice so Search degrades to lexical-only
// instead of failing the whole recall.
func (s *Service) semanticHits(ctx context.Context, query string, limit int) []SemanticHit {
	if s.Semantic == nil {
		return nil
	}
	vec, err := s.Semantic.EmbedQuery(ctx, query)
	if err != nil {
		return nil
	}
	k := limit
	if k <= 0 {
		k = defaultSemanticK
	}
	hits, err := s.Semantic.Search(ctx, vec, k)
	if err != nil {
		return nil
	}
	out := make([]SemanticHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, SemanticHit{ID: int64(h.ObsID), Score: h.Score})
	}
	return out
}
