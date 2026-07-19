package dashboard

import (
	"context"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/engramdb"
)

// DataSource is the dashboard's pluggable backend. The SAME Server can run over
// the local Engram stack (engramdb + embed + the Engram HTTP API) or over the
// cloud's replicated store, differing ONLY in this implementation.
//
// The mandatory surface (Records + Health) is always available. Optional
// capabilities are exposed through the (T, bool) accessors so an implementation
// can degrade gracefully: a backend without embeddings returns (nil, false) from
// Semantic and the dashboard falls back to keyword search and an "unavailable"
// graph; a read-only backend returns (nil, false) from Mutations and edit/delete
// surface a clear "not supported" message.
type DataSource interface {
	// Health reports whether the backend is reachable. It drives the "engine up"
	// status pill on the overview page.
	Health(ctx context.Context) error

	// Records is the always-present record reader (single observation + keyword
	// search). It mirrors the Engram HTTP API the dashboard has always depended on.
	Records() RecordReader

	// Structural exposes exact, SQL-style enumeration (the engramdb reader). When
	// unsupported the dashboard falls back to keyword (FTS) search via Records.
	Structural() (StructuralReader, bool)

	// Semantic exposes the embeddings layer (vector search + the similarity graph).
	// When unsupported, browse falls back to keyword search and the graph page
	// renders a clear unavailable state instead of fabricated edges.
	Semantic() (SemanticIndex, bool)

	// Mutations exposes write operations (patch + delete). When unsupported the
	// dashboard is read-only and the mutation handlers report it cleanly.
	Mutations() (MutationWriter, bool)

	// Close releases any backend resources (SQLite handles, etc.). It is invoked
	// once on server shutdown.
	Close() error
}

// RecordReader is the mandatory single-record + keyword-search surface. It
// returns the dashboard's HTTP-shaped Observation so the templ pages are
// unchanged. *engramClient satisfies this directly.
type RecordReader interface {
	GetObservation(ctx context.Context, id int) (*Observation, error)
	Search(ctx context.Context, query, project string, limit int) ([]Observation, error)
}

// StructuralReader is the exact-enumeration surface. *engramdb.DB satisfies this
// directly, so the cloud adapter returns the same engramdb types and the templ
// pages remain untouched.
type StructuralReader interface {
	List(ctx context.Context, f engramdb.Filter) ([]engramdb.Observation, error)
	ListByIDs(ctx context.Context, ids []int) ([]engramdb.Observation, error)
	Projects(ctx context.Context) ([]engramdb.ProjectCount, error)
	ProjectsCanonical(ctx context.Context, canonicalize func(string) string) ([]engramdb.ProjectCount, error)
	Types(ctx context.Context) ([]engramdb.TypeCount, error)
}

// SemanticIndex is the embeddings surface. EmbedQuery embeds an interactive
// query; Search and Graph return the same embed types the graph view already
// consumes.
type SemanticIndex interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error)
	// Graph returns the k-NN similarity graph, scoped to projects (nil/empty
	// = whole store). Implementations MUST push this scoping into the
	// underlying query (e.g. a SQL WHERE...IN) BEFORE the O(N^2) pairwise
	// scan — never compute the whole-store graph and filter afterward (audit
	// finding H3: handleGraph used to ignore its ?project filter entirely and
	// scan every project's embeddings on every /graph view).
	Graph(projects []string, k int, minScore float32) ([]embed.GraphNode, []embed.GraphEdge, error)
}

// MutationWriter is the write surface. *engramClient satisfies this directly.
type MutationWriter interface {
	PatchObservation(ctx context.Context, id int, title, content, obsType string) error
	DeleteObservation(ctx context.Context, id int, hard bool) error
}
