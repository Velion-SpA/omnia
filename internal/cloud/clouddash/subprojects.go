package clouddash

import (
	"context"
	"sort"

	"github.com/velion/omnia/internal/dashboard"
)

// CloudProjectLinks is the OPTIONAL sub-project parent-link capability a
// CloudStore backing may provide (satisfied by *cloudstore.CloudStore's
// ListProjectParents, Slice 5a). Detected via type assertion by the caller
// (CloudServer.subProjectResolver, internal/cloud/cloudserver/dashboard_mount.go),
// mirroring the CloudEmbeddingsSearcher pattern this package already uses in
// source.go — a store that doesn't implement it simply means the cloud
// dashboard mount never calls NewSubProjectResolver at all, and the shared
// dashboard.Server keeps its subProjects field nil (exactly like the local,
// no-links dashboard).
type CloudProjectLinks interface {
	ListProjectParents(ctx context.Context) (map[string]string, error)
}

// projectLinksResolver adapts cloudstore.ListProjectParents to
// dashboard.SubProjectResolver (Command Center v2, Slice 5b).
type projectLinksResolver struct {
	store CloudProjectLinks
}

var _ dashboard.SubProjectResolver = (*projectLinksResolver)(nil)

// NewSubProjectResolver builds a dashboard.SubProjectResolver backed by
// store's ListProjectParents. The caller is expected to have already
// confirmed store implements CloudProjectLinks (see
// CloudServer.subProjectResolver) — this constructor always returns a usable,
// non-nil resolver.
func NewSubProjectResolver(store CloudProjectLinks) dashboard.SubProjectResolver {
	return &projectLinksResolver{store: store}
}

// parents fetches the FULL child->parent map. Called once per ChildrenOf/
// ParentOf invocation rather than shared across a request — the "cache/
// one-call per request is fine" Slice 5b guidance is about NOT needing a
// broader per-request cache (the map is a handful of linked rows at most),
// not about deduplicating the two calls buildProjectDetailData already makes
// per project-detail render.
func (r *projectLinksResolver) parents(ctx context.Context) map[string]string {
	if r == nil || r.store == nil {
		return nil
	}
	m, err := r.store.ListProjectParents(ctx)
	if err != nil {
		return nil
	}
	return m
}

// ChildrenOf returns every project directly linked under project as its
// parent, sorted for a stable render order. Only ONE level is ever returned
// — the 2-level hierarchy model (cloudstore.SetProjectParent) guarantees a
// child is never itself a parent.
func (r *projectLinksResolver) ChildrenOf(ctx context.Context, project string) []string {
	parents := r.parents(ctx)
	if len(parents) == 0 {
		return nil
	}
	var children []string
	for child, parent := range parents {
		if parent == project {
			children = append(children, child)
		}
	}
	sort.Strings(children)
	return children
}

// ParentOf returns project's own parent, or "" when unlinked (or when the
// store lookup failed — a degrade, never a panic).
func (r *projectLinksResolver) ParentOf(ctx context.Context, project string) string {
	return r.parents(ctx)[project]
}
