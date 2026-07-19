package cloudserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// Command Center v2, Slice 5a: cloud sub-project linking. Operator-only
// endpoints on top of cloudstore's SetProjectParent/ClearProjectParent/
// ListProjectParents — mirrors project_admin_stats.go's pattern (a
// dedicated small file per capability, detected via type assertion, so the
// core ChunkStore interface is never extended and a store that doesn't
// support linking still renders the Projects page, just without the
// badges/banner/kebab actions — see handleAdminProjectsPage).

// projectLinksAdminStore is the store capability needed for sub-project
// linking. *cloudstore.CloudStore satisfies it; the compile-time assertion
// below keeps it honest.
type projectLinksAdminStore interface {
	SetProjectParent(ctx context.Context, child, parent string) error
	ClearProjectParent(ctx context.Context, child string) error
	ListProjectParents(ctx context.Context) (map[string]string, error)
}

// Compile-time assertion: the concrete store must satisfy the seam.
var _ projectLinksAdminStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) projectLinksStore() (projectLinksAdminStore, bool) {
	pl, ok := s.store.(projectLinksAdminStore)
	return pl, ok
}

// handleAdminSetProjectParent handles POST /admin/projects/{project}/parent:
// links {project} (the child) under the form field "parent". Operator-gated
// FIRST (mirrors handleAdminProjectAccessFragment and every other Admin
// mutation handler). Every rejection — missing input or one of the three
// 2-level-hierarchy violations — surfaces as a friendly 400, never a 500 or
// panic; see cloudstore.SetProjectParent's doc comment for the exact rules.
func (s *CloudServer) handleAdminSetProjectParent(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	pls, ok := s.projectLinksStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "project linking unavailable"})
		return
	}
	child := strings.TrimSpace(r.PathValue("project"))
	if child == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	parent := strings.TrimSpace(r.PostFormValue("parent"))
	if parent == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "parent is required"})
		return
	}
	if err := pls.SetProjectParent(r.Context(), child, parent); err != nil {
		status, msg := projectLinkErrorResponse(err)
		jsonResponse(w, status, map[string]string{"error": msg})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"project": child, "parent": parent})
}

// handleAdminClearProjectParent handles POST
// /admin/projects/{project}/parent/clear: unlinks {project} from its parent,
// if any. Idempotent — clearing an already-unlinked (or never-linked)
// project is success, not an error, matching cloudstore.ClearProjectParent.
func (s *CloudServer) handleAdminClearProjectParent(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	pls, ok := s.projectLinksStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "project linking unavailable"})
		return
	}
	child := strings.TrimSpace(r.PathValue("project"))
	if child == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}
	if err := pls.ClearProjectParent(r.Context(), child); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not unlink project"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"project": child})
}

// projectLinkErrorResponse maps a SetProjectParent validation error to a
// friendly HTTP status + message. Every expected rejection (self-ref,
// parent-is-child, child-is-parent) is a 400 the operator can act on;
// anything unrecognized falls back to a generic 500 rather than leaking an
// internal error string.
func projectLinkErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, cloudstore.ErrProjectLinkSelf):
		return http.StatusBadRequest, "A project cannot be linked to itself."
	case errors.Is(err, cloudstore.ErrProjectLinkParentIsChild):
		return http.StatusBadRequest, "That project is itself a sub-project and cannot be a parent (only two levels are supported)."
	case errors.Is(err, cloudstore.ErrProjectLinkChildIsParent):
		return http.StatusBadRequest, "That project already has its own sub-projects and cannot become one itself (only two levels are supported)."
	default:
		return http.StatusInternalServerError, "Could not link project."
	}
}
