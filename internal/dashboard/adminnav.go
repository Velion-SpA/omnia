package dashboard

import (
	"context"

	"github.com/velion/omnia/internal/ui"
)

// adminNavKey marks a request context whose session is the cloud operator, so the
// shared command-center shell renders the operator-only "Admin" nav entry. Only
// the cloud mount sets it (for operator sessions); the LOCAL dashboard never does
// — there are no accounts locally, so it never grows an Admin section.
type adminNavKeyType struct{}

var adminNavKey adminNavKeyType

// WithAdminNav returns a context that renders the operator-only Admin nav entry.
// The cloud dashboard gate calls this for operator sessions; nothing calls it on
// the local dashboard, keeping the Admin section cloud-only.
func WithAdminNav(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminNavKey, true)
}

// adminNavEnabled reports whether the request context was marked as an operator
// session by the cloud gate.
func adminNavEnabled(ctx context.Context) bool {
	v, _ := ctx.Value(adminNavKey).(bool)
	return v
}

// adminNavItem is the operator-only nav entry pointing at the cloud Admin section.
var adminNavItem = ui.NavItem{Href: "/admin", Label: "Admin", ID: "admin"}

// userIdentity carries the signed-in user's display name and logout action for
// the shared shell's nav-meta block (see internal/ui/layout.templ). It is kept
// as an unexported struct behind a context key — the same pattern as adminNav —
// so internal/dashboard never needs to know about cloud accounts/sessions.
type userIdentity struct {
	Username  string
	LogoutURL string
}

// userIdentityKey marks a request context carrying the signed-in user's identity
// for the shared shell. Only the cloud mount sets it (for authenticated dashboard
// sessions); the LOCAL dashboard never does — there are no accounts locally, so
// the nav's user/logout block never renders there.
type userIdentityKeyType struct{}

var userIdentityKey userIdentityKeyType

// WithUserIdentity returns a context that renders the signed-in user's name and
// a logout button in the shared shell's nav. The cloud dashboard gate calls this
// for authenticated dashboard sessions; nothing calls it on the local dashboard,
// keeping the user/logout block cloud-only.
func WithUserIdentity(ctx context.Context, username, logoutURL string) context.Context {
	return context.WithValue(ctx, userIdentityKey, userIdentity{Username: username, LogoutURL: logoutURL})
}

// userIdentityFromContext reports the signed-in user's identity set by the cloud
// gate, if any.
func userIdentityFromContext(ctx context.Context) (userIdentity, bool) {
	v, ok := ctx.Value(userIdentityKey).(userIdentity)
	return v, ok
}

// BaseNavItems returns the standard dashboard nav entries (Overview…Activity)
// shared by the local and cloud shells. The cloud Admin pages reuse this so their
// nav stays in lockstep with the rest of the dashboard.
func BaseNavItems() []ui.NavItem {
	return []ui.NavItem{
		{Href: "/", Label: "Overview", ID: "overview"},
		{Href: "/browse", Label: "Browse", ID: "browse"},
		{Href: "/graph", Label: "Graph", ID: "graph"},
		{Href: "/sync", Label: "Sync", ID: "sync"},
		{Href: "/activity", Label: "Activity", ID: "activity"},
	}
}

// AdminNavItem returns the operator-only Admin nav entry so the cloud Admin pages
// can append it to BaseNavItems() and render the exact same shell.
func AdminNavItem() ui.NavItem { return adminNavItem }

// layoutPropsForContext builds the shared shell props for a page, appending the
// operator-only Admin entry when the request context is an operator session and
// populating the signed-in user's name + logout action when the request context
// carries one (cloud dashboard sessions). This applies to every page the shared
// dashboard renders — Overview, Browse, Graph, Sync, Activity, Detail and Admin
// — so the nav's user/logout block is consistent across the whole shell instead
// of only appearing on the Admin pages.
func layoutPropsForContext(ctx context.Context, active, title string) ui.LayoutProps {
	props := localLayoutProps(active, title)
	if adminNavEnabled(ctx) {
		props.Nav = append(props.Nav, adminNavItem)
	}
	if identity, ok := userIdentityFromContext(ctx); ok {
		props.User = identity.Username
		props.LogoutURL = identity.LogoutURL
	}
	return props
}
