// Package clouddash adapts the cloud's replicated chunk store
// (internal/cloud/cloudstore) to the unified dashboard's DataSource interface
// (internal/dashboard). It lets the cloud server mount the SAME dashboard.Server
// (and the same templ pages) as the local dashboard, differing only in where the
// data comes from. Every read is scoped per request to the projects the logged-in
// account may see, so an account never observes another account's memories.
package clouddash

import (
	"context"
	"strings"
)

// Scope is the per-request visibility envelope. All is true for the server
// operator (admin token), who sees every replicated project; otherwise projects
// holds exactly the project names the logged-in account is a member of.
type Scope struct {
	// All grants visibility into every replicated project (server operator).
	All bool

	projects map[string]struct{}
}

// NewScope builds a Scope from the operator flag and the account's visible
// project names. Blank names are dropped.
func NewScope(all bool, projects []string) Scope {
	set := make(map[string]struct{}, len(projects))
	for _, p := range projects {
		p = strings.TrimSpace(p)
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return Scope{All: all, projects: set}
}

// CanView reports whether this scope may see the given project. The operator may
// view any project; an account may view only the projects it is a member of. An
// empty project is never viewable.
func (s Scope) CanView(project string) bool {
	if s.All {
		return true
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return false
	}
	_, ok := s.projects[project]
	return ok
}

type scopeKeyType struct{}

var scopeKey scopeKeyType

// WithScope returns a context carrying the request's visibility scope. The cloud
// server injects this before delegating to the dashboard handler.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey, s)
}

// scopeFrom extracts the request scope. It is FAIL-CLOSED: a request without an
// injected scope sees nothing (never the full set), so a wiring mistake can only
// ever under-expose, never leak across accounts.
func scopeFrom(ctx context.Context) Scope {
	if s, ok := ctx.Value(scopeKey).(Scope); ok {
		return s
	}
	return Scope{}
}
