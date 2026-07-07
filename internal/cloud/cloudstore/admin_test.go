package cloudstore

import (
	"context"
	"testing"
)

// Perms bits (mirrors internal/cloud/auth/permission.go; cloudstore must not
// import auth — auth imports cloudstore).
const (
	permRead   = 1
	permInsert = 2
	permAll    = 15
)

func findAdminUser(users []AdminUser, id string) (AdminUser, bool) {
	for _, u := range users {
		if u.ID == id {
			return u, true
		}
	}
	return AdminUser{}, false
}

// TestAdminUserAndMembershipQueriesIntegration exercises the OBL-13 operator-facing
// reads/writes against a live Postgres: user listing with token aggregates, the
// managed-token list, and membership upsert/list/delete with the perms bitfield.
func TestAdminUserAndMembershipQueriesIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	alice, err := cs.CreateUser("alice", "alice@example.com", "h")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := cs.CreateUser("bob", "bob@example.com", "h")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	// ListUsers: both accounts, ordered by username, active, no tokens.
	users, err := cs.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].Username != "alice" || users[1].Username != "bob" {
		t.Fatalf("users must be ordered by username, got %q,%q", users[0].Username, users[1].Username)
	}
	if users[0].Disabled() || users[0].TokenCount != 0 || users[0].LastTokenUse != nil {
		t.Fatalf("fresh account must be active with no tokens, got %+v", users[0])
	}

	// Issue + touch a token → TokenCount 1, LastTokenUse set.
	mt, err := cs.IssueManagedToken(ctx, alice.ID, "hash-a", "laptop", AuditEntry{Contributor: "operator"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := cs.TouchManagedToken(ctx, mt.ID); err != nil {
		t.Fatalf("touch token: %v", err)
	}
	users, _ = cs.ListUsers(ctx)
	au, ok := findAdminUser(users, alice.ID)
	if !ok {
		t.Fatal("alice missing from ListUsers")
	}
	if au.TokenCount != 1 {
		t.Fatalf("expected 1 live token, got %d", au.TokenCount)
	}
	if au.LastTokenUse == nil {
		t.Fatal("expected LastTokenUse to be set after Touch")
	}

	// Token list: label present, live.
	tokens, err := cs.ListManagedTokensForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Label != "laptop" || tokens[0].Revoked() {
		t.Fatalf("unexpected token list: %+v", tokens)
	}

	// Revoke → token shows revoked, and the live TokenCount drops to 0.
	if err := cs.RevokeManagedToken(ctx, mt.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	tokens, _ = cs.ListManagedTokensForUser(ctx, alice.ID)
	if len(tokens) != 1 || !tokens[0].Revoked() {
		t.Fatalf("expected token revoked, got %+v", tokens)
	}
	users, _ = cs.ListUsers(ctx)
	au, _ = findAdminUser(users, alice.ID)
	if au.TokenCount != 0 {
		t.Fatalf("revoked token must not count as live, got %d", au.TokenCount)
	}

	// Membership upsert round-trip: read-only → read+write.
	if err := cs.UpsertMembership(ctx, alice.ID, "lab", permRead, "member"); err != nil {
		t.Fatalf("upsert lab: %v", err)
	}
	mems, err := cs.ListMembershipsForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("list memberships: %v", err)
	}
	if len(mems) != 1 || mems[0].Project != "lab" || mems[0].Perms != permRead || mems[0].Role != "member" {
		t.Fatalf("unexpected initial membership: %+v", mems)
	}
	if err := cs.UpsertMembership(ctx, alice.ID, "lab", permRead|permInsert, "moderator"); err != nil {
		t.Fatalf("update lab: %v", err)
	}
	mems, _ = cs.ListMembershipsForUser(ctx, alice.ID)
	if mems[0].Perms != permRead|permInsert || mems[0].Role != "moderator" {
		t.Fatalf("membership update did not round-trip: %+v", mems[0])
	}

	// A second project; ListMembershipsForUser is ordered by project.
	if err := cs.UpsertMembership(ctx, alice.ID, "prod", permAll, "owner"); err != nil {
		t.Fatalf("upsert prod: %v", err)
	}
	mems, _ = cs.ListMembershipsForUser(ctx, alice.ID)
	if len(mems) != 2 || mems[0].Project != "lab" || mems[1].Project != "prod" {
		t.Fatalf("expected [lab,prod] ordered, got %+v", mems)
	}

	// Isolation: bob has no memberships.
	if bobMems, _ := cs.ListMembershipsForUser(ctx, bob.ID); len(bobMems) != 0 {
		t.Fatalf("bob must have no memberships, got %+v", bobMems)
	}

	// Delete one membership (idempotent).
	if err := cs.DeleteMembership(ctx, alice.ID, "lab"); err != nil {
		t.Fatalf("delete lab: %v", err)
	}
	if err := cs.DeleteMembership(ctx, alice.ID, "lab"); err != nil {
		t.Fatalf("delete lab (idempotent): %v", err)
	}
	mems, _ = cs.ListMembershipsForUser(ctx, alice.ID)
	if len(mems) != 1 || mems[0].Project != "prod" {
		t.Fatalf("expected only prod after delete, got %+v", mems)
	}

	// Disabled state is reflected in ListUsers.
	if err := cs.SetUserDisabled(ctx, alice.ID, true); err != nil {
		t.Fatalf("disable alice: %v", err)
	}
	users, _ = cs.ListUsers(ctx)
	au, _ = findAdminUser(users, alice.ID)
	if !au.Disabled() {
		t.Fatal("expected alice to be disabled in ListUsers")
	}
}
