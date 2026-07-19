package cloudserver

import (
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// handleListProjects handles GET /projects.
//
// Account-facing project discovery: returns the projects the CALLING account
// itself can read (teams ∪ per-project membership overrides), so a read-only
// account can auto-discover what it has access to without needing operator
// access to GET /admin/projects. Account-only: the legacy shared token
// carries no per-account identity, so it is rejected with 403 — the same gate
// requireMemberManagement uses for the member-management endpoints.
func (s *CloudServer) handleListProjects(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.AccountFromContext(r.Context())
	if !ok || claims == nil || strings.TrimSpace(claims.AccountID) == "" {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "account authentication required"})
		return
	}

	// Resolution mirrors dashboardVisibleProjects exactly: prefer the layered
	// ListReadableProjectsForAccount (teams ∪ per-project overrides), falling
	// back to ListMembershipsForAccount filtered to PermRead for stores that
	// predate the teams model. Fail-closed: if neither seam is available, or
	// resolution errors, return an empty list — never nil, never the full set.
	var projects []string
	if teamReader, ok := s.store.(interface {
		ListReadableProjectsForAccount(string) ([]string, error)
	}); ok {
		if p, err := teamReader.ListReadableProjectsForAccount(claims.AccountID); err == nil {
			projects = p
		}
	} else if reader, ok := s.store.(interface {
		ListMembershipsForAccount(string) ([]cloudstore.Membership, error)
	}); ok {
		if memberships, err := reader.ListMembershipsForAccount(claims.AccountID); err == nil {
			for _, m := range memberships {
				if auth.Permission(m.Perms).Has(auth.PermRead) {
					projects = append(projects, m.Project)
				}
			}
		}
	}
	if projects == nil {
		projects = make([]string, 0)
	}

	jsonResponse(w, http.StatusOK, map[string]any{"projects": projects})
}
