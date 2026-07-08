package dashboard

import (
	"context"

	"github.com/Velion-SpA/omnia/internal/ui"
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
// operator-only Admin entry when the request context is an operator session.
func layoutPropsForContext(ctx context.Context, title string) ui.LayoutProps {
	props := localLayoutProps(title)
	if adminNavEnabled(ctx) {
		props.Nav = append(props.Nav, adminNavItem)
	}
	return props
}
