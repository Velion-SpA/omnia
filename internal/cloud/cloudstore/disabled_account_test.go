package cloudstore

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestUserDisabledMethod is a pure unit test (no Postgres) for the User.Disabled
// helper: it must be nil-safe and reflect the DisabledAt NullTime.
func TestUserDisabledMethod(t *testing.T) {
	var nilUser *User
	if nilUser.Disabled() {
		t.Fatal("nil *User must report Disabled()==false (nil-safe)")
	}

	active := &User{ID: "1", Username: "alice"}
	if active.Disabled() {
		t.Fatal("user with zero DisabledAt must be active")
	}

	disabled := &User{ID: "2", Username: "bob", DisabledAt: sql.NullTime{Valid: true, Time: time.Now()}}
	if !disabled.Disabled() {
		t.Fatal("user with a valid DisabledAt must report Disabled()==true")
	}
}

// TestGetUserByUsernameSelectsDisabledAt proves GetUserByUsername (and
// GetUserByEmail) now project disabled_at so the account auth flows can enforce
// it — the C1 gap was that these queries never SELECTed the column.
func TestGetUserByUsernameSelectsDisabledAt(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	u, err := cs.CreateUser("alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Fresh account: active on both lookups.
	got, err := cs.GetUserByUsername("alice")
	if err != nil || got == nil {
		t.Fatalf("get by username: got=%v err=%v", got, err)
	}
	if got.Disabled() {
		t.Fatal("fresh account must be active via GetUserByUsername")
	}
	gotEmail, err := cs.GetUserByEmail("alice@example.com")
	if err != nil || gotEmail == nil {
		t.Fatalf("get by email: got=%v err=%v", gotEmail, err)
	}
	if gotEmail.Disabled() {
		t.Fatal("fresh account must be active via GetUserByEmail")
	}

	// Disable, then both lookups must report disabled with a populated DisabledAt.
	if err := cs.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	got, _ = cs.GetUserByUsername("alice")
	if got == nil || !got.Disabled() || !got.DisabledAt.Valid {
		t.Fatalf("disabled account must report Disabled() via GetUserByUsername, got %+v", got)
	}
	gotEmail, _ = cs.GetUserByEmail("alice@example.com")
	if gotEmail == nil || !gotEmail.Disabled() {
		t.Fatalf("disabled account must report Disabled() via GetUserByEmail, got %+v", gotEmail)
	}
}

// TestIsUserAdminExcludesDisabledAdmin proves the per-request operator-promotion
// lookup folds disabled_at IS NULL: a disabled admin is NOT admin, so a live
// session can never keep re-promoting to operator (C1 point 3).
func TestIsUserAdminExcludesDisabledAdmin(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	u, err := cs.CreateUser("labadmin", "labadmin@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, u.ID, true); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	if isAdmin, err := cs.IsUserAdmin(ctx, u.ID); err != nil || !isAdmin {
		t.Fatalf("active admin must resolve is_admin=true, got %v err=%v", isAdmin, err)
	}

	// Disable the admin: IsUserAdmin must now resolve false (never promoted).
	if err := cs.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("disable admin: %v", err)
	}
	if isAdmin, err := cs.IsUserAdmin(ctx, u.ID); err != nil || isAdmin {
		t.Fatalf("disabled admin must resolve is_admin=false, got %v err=%v", isAdmin, err)
	}

	// Re-enable: promotion is restored (disable only masks, never demotes the flag).
	if err := cs.SetUserDisabled(ctx, u.ID, false); err != nil {
		t.Fatalf("re-enable admin: %v", err)
	}
	if isAdmin, err := cs.IsUserAdmin(ctx, u.ID); err != nil || !isAdmin {
		t.Fatalf("re-enabled admin must resolve is_admin=true again, got %v err=%v", isAdmin, err)
	}
}

// TestIsAccountDisabledIntegration proves the cheap PK-indexed disabled lookup
// that backs the sync data-plane RBAC guard (C1 point 4).
func TestIsAccountDisabledIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	u, err := cs.CreateUser("alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if disabled, err := cs.IsAccountDisabled(ctx, u.ID); err != nil || disabled {
		t.Fatalf("active account must report IsAccountDisabled=false, got %v err=%v", disabled, err)
	}

	// Unknown id resolves false without error (no row).
	if disabled, err := cs.IsAccountDisabled(ctx, "999999"); err != nil || disabled {
		t.Fatalf("unknown id must resolve IsAccountDisabled=false, got %v err=%v", disabled, err)
	}

	if err := cs.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled, err := cs.IsAccountDisabled(ctx, u.ID); err != nil || !disabled {
		t.Fatalf("disabled account must report IsAccountDisabled=true, got %v err=%v", disabled, err)
	}
}

// TestIsAccountDisabledDeniesDespiteGrantedPerms is the CRITICAL sync data-plane
// proof: an account that HOLDS full membership perms but is disabled must resolve
// to "disabled" so the authorizer denies it. EffectivePerms still returns the raw
// grant (the disabled gate lives in the authorizer, not folded into the hot query),
// so this test documents the split and proves the gate input is correct.
func TestIsAccountDisabledDeniesDespiteGrantedPerms(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	u, err := cs.CreateUser("alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.UpsertMembership(ctx, u.ID, "proj", permAll, "owner"); err != nil {
		t.Fatalf("grant membership: %v", err)
	}
	// Active: full perms, not disabled.
	if perms, err := cs.EffectivePerms(ctx, u.ID, "proj"); err != nil || perms != permAll {
		t.Fatalf("active account must resolve full perms, got %d err=%v", perms, err)
	}
	if disabled, _ := cs.IsAccountDisabled(ctx, u.ID); disabled {
		t.Fatal("active account must not be disabled")
	}

	// Disable: the grant is untouched, but IsAccountDisabled flips to true so the
	// authorizer denies on the sync data plane.
	if err := cs.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled, err := cs.IsAccountDisabled(ctx, u.ID); err != nil || !disabled {
		t.Fatalf("disabled account with live membership must report IsAccountDisabled=true, got %v err=%v", disabled, err)
	}
}
