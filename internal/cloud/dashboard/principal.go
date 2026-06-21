package dashboard

import (
	"net/http"
	"strings"
)

// Principal represents the authenticated dashboard user. It is a read-only
// view over MountConfig closures — no context reads for identity are performed
// inside handlers. Satisfies Design Decision 6.
//
// RBAC (multi-tenant): a Principal also carries the set of projects it is allowed
// to see. `scopeAll` is true for the server operator (admin token), who sees every
// project; otherwise `visible` holds exactly the projects the logged-in account is
// a member of. Resolution is fail-closed: if identity cannot be resolved the
// account sees an empty scope, never the full set.
type Principal struct {
	displayName string
	isAdmin     bool
	scopeAll    bool
	visible     map[string]struct{}
}

// DisplayName returns the display name for this principal.
// An empty or whitespace-only name falls back to "OPERATOR".
func (p Principal) DisplayName() string {
	if strings.TrimSpace(p.displayName) == "" {
		return "OPERATOR"
	}
	return p.displayName
}

// IsAdmin returns whether this principal has admin privileges.
func (p Principal) IsAdmin() bool { return p.isAdmin }

// CanView reports whether this principal may see the given project. The server
// operator (scopeAll) may view any project; an account may view only the projects
// it is a member of. An empty project name is never viewable.
func (p Principal) CanView(project string) bool {
	if p.scopeAll {
		return true
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return false
	}
	_, ok := p.visible[project]
	return ok
}

// ScopeAll reports whether this principal sees every project (server operator).
func (p Principal) ScopeAll() bool { return p.scopeAll }

// VisibleCount returns the number of projects this principal can see. For a
// scope-all operator it returns -1 (meaning "all"), since the concrete count is
// not enumerated here.
func (p Principal) VisibleCount() int {
	if p.scopeAll {
		return -1
	}
	return len(p.visible)
}

// principalFromRequest derives a Principal from the current request using the
// MountConfig closures. Handlers call p := h.principalFromRequest(r) and never
// read r.Context() for identity.
func (h *handlers) principalFromRequest(r *http.Request) Principal {
	name := ""
	if h.cfg.GetDisplayName != nil {
		name = strings.TrimSpace(h.cfg.GetDisplayName(r))
	}
	admin := false
	if h.cfg.IsAdmin != nil {
		admin = h.cfg.IsAdmin(r)
	}
	// Resolve the project scope. When no VisibleProjects hook is wired (the
	// standalone, no-auth dashboard used in tests), default to scope-all so the
	// legacy single-tenant behaviour is preserved. When the hook IS wired (the
	// cloud server), it is authoritative and fail-closed.
	scopeAll := true
	visible := map[string]struct{}{}
	if h.cfg.VisibleProjects != nil {
		projects, all := h.cfg.VisibleProjects(r)
		scopeAll = all
		for _, pr := range projects {
			pr = strings.TrimSpace(pr)
			if pr != "" {
				visible[pr] = struct{}{}
			}
		}
	}
	return Principal{displayName: name, isAdmin: admin, scopeAll: scopeAll, visible: visible}
}
