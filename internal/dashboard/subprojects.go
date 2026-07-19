package dashboard

import "context"

// SubProjectResolver is the OPTIONAL capability that lets the project-detail
// page surface sub-project relationships (Command Center v2, Slice 5b). The
// parent-link data itself lives in the CLOUD store (cloudstore.SetProjectParent
// / ListProjectParents, Slice 5a) — internal/dashboard stays layer-independent
// by depending only on this small interface rather than importing
// internal/cloud/cloudstore. Held as a nilable field on Server (see
// WithSubProjectResolver); the local dashboard never sets it, so
// buildProjectDetailData renders neither a children section nor a parent
// breadcrumb — exactly as it did before this slice.
type SubProjectResolver interface {
	// ChildrenOf returns the direct children linked under project as their
	// parent (nil/empty when project has none, or is itself unknown). Only
	// ONE level is ever expected — the underlying 2-level hierarchy model
	// (cloudstore.SetProjectParent) guarantees a child is never itself a
	// parent, so callers never need to recurse further.
	ChildrenOf(ctx context.Context, project string) []string
	// ParentOf returns project's own parent, or "" when project is unlinked
	// (or itself unknown).
	ParentOf(ctx context.Context, project string) string
}

// Option configures optional Server capabilities on top of a DataSource.
// Functional options keep every existing zero-option caller (NewServer, every
// pre-Slice-5b test, and the cloud mount's prior wiring) unaffected.
type Option func(*Server)

// WithSubProjectResolver wires the OPTIONAL sub-project resolver into the
// dashboard (Command Center v2, Slice 5b): when set, the project-detail page
// gains a "Sub-projects" section (for a parent) or a parent breadcrumb (for a
// child). Only the cloud dashboard sets this (internal/cloud/clouddash,
// backed by cloudstore.ListProjectParents); the local dashboard leaves it
// nil, and buildProjectDetailData renders neither, exactly as before.
func WithSubProjectResolver(r SubProjectResolver) Option {
	return func(s *Server) { s.subProjects = r }
}
