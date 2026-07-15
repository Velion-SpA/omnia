package dashboard

import (
	"context"
	"testing"
)

// TestLayoutPropsForContext_NoIdentity_UserEmpty pins the LOCAL dashboard's
// existing behavior: when the request context carries no signed-in identity
// (as is always the case for the standalone local dashboard — there are no
// accounts locally), the shared shell's User/LogoutURL stay empty, so the
// nav-meta block renders no user/logout UI (internal/ui/layout.templ checks
// props.User != "").
func TestLayoutPropsForContext_NoIdentity_UserEmpty(t *testing.T) {
	props := layoutPropsForContext(context.Background(), "Overview")
	if props.User != "" {
		t.Fatalf("expected empty User with no identity in context, got %q", props.User)
	}
	if props.LogoutURL != "" {
		t.Fatalf("expected empty LogoutURL with no identity in context, got %q", props.LogoutURL)
	}
}

// TestLayoutPropsForContext_WithUserIdentity_PopulatesUserAndLogout is the
// regression guard for the bug fix: previously only adminLayoutProps (used
// exclusively by the two Admin pages) populated User/LogoutURL. Every other
// shared-dashboard page (Overview, Browse, Graph, Sync, Activity, Detail) went
// through layoutPropsForContext, which never read any identity, so a signed-in
// cloud account never saw a logout button outside the Admin section. Now the
// context injected by the cloud gate (dashboard.WithUserIdentity) must flow
// through to props on any page title, not just Admin pages.
func TestLayoutPropsForContext_WithUserIdentity_PopulatesUserAndLogout(t *testing.T) {
	ctx := WithUserIdentity(context.Background(), "alice", "/logout")
	props := layoutPropsForContext(ctx, "Overview")
	if props.User != "alice" {
		t.Fatalf("expected User to be populated from context identity, got %q", props.User)
	}
	if props.LogoutURL != "/logout" {
		t.Fatalf("expected LogoutURL to be populated from context identity, got %q", props.LogoutURL)
	}
}

// TestLayoutPropsForContext_AdminNavAndUserIdentityComposeTogether guards the
// case an operator-promoted account sees: BOTH the Admin nav entry (from
// WithAdminNav) AND their own username/logout (from WithUserIdentity) applied
// to the SAME request context, since dashboard_mount.go's gate sets both
// independently.
func TestLayoutPropsForContext_AdminNavAndUserIdentityComposeTogether(t *testing.T) {
	ctx := WithAdminNav(context.Background())
	ctx = WithUserIdentity(ctx, "operator-admin", "/logout")
	props := layoutPropsForContext(ctx, "Overview")

	if props.User != "operator-admin" {
		t.Fatalf("expected User to be populated, got %q", props.User)
	}
	if props.LogoutURL != "/logout" {
		t.Fatalf("expected LogoutURL to be populated, got %q", props.LogoutURL)
	}
	found := false
	for _, item := range props.Nav {
		if item.ID == "admin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Admin nav entry to be present, got %+v", props.Nav)
	}
}
