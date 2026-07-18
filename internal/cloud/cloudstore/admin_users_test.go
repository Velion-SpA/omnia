package cloudstore

import (
	"context"
	"errors"
	"testing"
)

// TestAdminCreateUserHappyPath verifies AdminCreateUser persists a new account
// with the given username/email/password hash and returns its id, retrievable
// afterwards via the existing GetUserByUsername lookup.
func TestAdminCreateUserHappyPath(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	id, err := cs.AdminCreateUser(ctx, "newguy", "newguy@example.com", "bcrypt-hash-1")
	if err != nil {
		t.Fatalf("admin create user: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}

	got, err := cs.GetUserByUsername("newguy")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if got == nil || got.ID != id || got.Email != "newguy@example.com" || got.PasswordHash != "bcrypt-hash-1" {
		t.Fatalf("unexpected persisted user: %+v", got)
	}
}

// TestAdminCreateUserDuplicateUsername verifies a conflicting username fails
// cleanly with ErrUserExists and never mutates the existing row (OBL-02
// lesson: no silent overwrite).
func TestAdminCreateUserDuplicateUsername(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if _, err := cs.AdminCreateUser(ctx, "alice", "alice@example.com", "hash-a"); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := cs.AdminCreateUser(ctx, "alice", "different@example.com", "hash-b"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists on duplicate username, got %v", err)
	}

	// The original row must be untouched — email still alice@example.com.
	got, err := cs.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if got == nil || got.Email != "alice@example.com" || got.PasswordHash != "hash-a" {
		t.Fatalf("existing row must not be overwritten by the failed conflicting create, got %+v", got)
	}
}

// TestAdminCreateUserDuplicateEmail verifies a conflicting email (different
// username) also fails cleanly with ErrUserExists.
func TestAdminCreateUserDuplicateEmail(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if _, err := cs.AdminCreateUser(ctx, "alice", "shared@example.com", "hash-a"); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := cs.AdminCreateUser(ctx, "bob", "shared@example.com", "hash-b"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists on duplicate email, got %v", err)
	}

	// bob must not have been created at all.
	got, err := cs.GetUserByUsername("bob")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if got != nil {
		t.Fatalf("expected bob to not exist after a failed conflicting create, got %+v", got)
	}
}

// TestAdminUpdateUserHappyPath verifies AdminUpdateUser edits username/email
// and the change round-trips.
func TestAdminUpdateUserHappyPath(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	id, err := cs.AdminCreateUser(ctx, "oldname", "old@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.AdminUpdateUser(ctx, id, "newname", "new@example.com"); err != nil {
		t.Fatalf("admin update user: %v", err)
	}

	got, err := cs.GetUserByUsername("newname")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if got == nil || got.ID != id || got.Email != "new@example.com" {
		t.Fatalf("unexpected user after update: %+v", got)
	}
	if stale, _ := cs.GetUserByUsername("oldname"); stale != nil {
		t.Fatalf("old username must no longer resolve, got %+v", stale)
	}
}

// TestAdminUpdateUserConflict verifies renaming a user to a username already
// held by ANOTHER account fails with ErrUserExists and leaves both rows
// untouched.
func TestAdminUpdateUserConflict(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	aliceID, err := cs.AdminCreateUser(ctx, "alice", "alice@example.com", "hash-a")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bobID, err := cs.AdminCreateUser(ctx, "bob", "bob@example.com", "hash-b")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	if err := cs.AdminUpdateUser(ctx, bobID, "alice", "bob@example.com"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists renaming bob to alice's username, got %v", err)
	}

	// Both rows must be untouched.
	aliceRow, _ := cs.GetUserByUsername("alice")
	if aliceRow == nil || aliceRow.ID != aliceID {
		t.Fatalf("alice's row must be untouched, got %+v", aliceRow)
	}
	bobRow, _ := cs.GetUserByUsername("bob")
	if bobRow == nil || bobRow.ID != bobID {
		t.Fatalf("bob's row must be untouched after a failed rename, got %+v", bobRow)
	}
}

// TestAdminUpdateUserUnknownID verifies editing a non-existent id fails with
// ErrManagedTokenUserNotFound (the same not-found convention SetUserDisabled /
// SetUserAdmin already use), so the HTTP layer can map it to a clean 404.
func TestAdminUpdateUserUnknownID(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if err := cs.AdminUpdateUser(ctx, "999999", "ghost", "ghost@example.com"); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("expected ErrManagedTokenUserNotFound, got %v", err)
	}
}

// TestAdminSetUserPasswordHappyPath verifies AdminSetUserPassword replaces the
// stored password hash (the store only ever sees the hash — hashing happens
// in the handler, mirroring auth.Signup).
func TestAdminSetUserPasswordHappyPath(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	id, err := cs.AdminCreateUser(ctx, "reset-me", "reset@example.com", "old-hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.AdminSetUserPassword(ctx, id, "new-hash"); err != nil {
		t.Fatalf("admin set user password: %v", err)
	}

	got, err := cs.GetUserByUsername("reset-me")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if got == nil || got.PasswordHash != "new-hash" {
		t.Fatalf("expected password hash to be replaced, got %+v", got)
	}
}

// TestAdminSetUserPasswordUnknownID verifies resetting an unknown id fails
// with ErrManagedTokenUserNotFound.
func TestAdminSetUserPasswordUnknownID(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if err := cs.AdminSetUserPassword(ctx, "999999", "hash"); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("expected ErrManagedTokenUserNotFound, got %v", err)
	}
}

// TestAdminHardDeleteUserCascadesMembershipsAndTokens is the OBL-02-style
// integrity guard for hard delete: it must remove the account AND its
// cloud_memberships AND its cloud_tokens (which carries an FK REFERENCES
// cloud_users(id) with no ON DELETE CASCADE — deleting the user first would
// otherwise fail with a raw FK violation for any account that ever had a
// managed token issued) AND its cloud_devices AND its cloud_team_members
// (both plain-TEXT account_id, no FK — but an orphaned cloud_team_members row
// would silently grant its perms to any FUTURE account that reuses the same
// numeric id, since EffectivePerms/ListReadableProjectsForAccount key on
// tm.account_id with no join back to cloud_users) in ONE transaction, leaving
// no orphans anywhere.
func TestAdminHardDeleteUserCascadesMembershipsAndTokens(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	id, err := cs.AdminCreateUser(ctx, "doomed", "doomed@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.UpsertMembership(ctx, id, "lab", permAll, "owner"); err != nil {
		t.Fatalf("grant membership: %v", err)
	}
	mt, err := cs.IssueManagedToken(ctx, id, "hash-doomed", "laptop", AuditEntry{Contributor: "operator"})
	if err != nil {
		t.Fatalf("issue managed token: %v", err)
	}
	if _, err := cs.GetOrCreateDevice(id, "doomed-laptop"); err != nil {
		t.Fatalf("register device: %v", err)
	}
	team, err := cs.CreateTeam(ctx, "doomed-team", "work")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := cs.AddTeamMember(ctx, team.ID, id, ""); err != nil {
		t.Fatalf("add team member: %v", err)
	}

	if err := cs.AdminHardDeleteUser(ctx, id); err != nil {
		t.Fatalf("admin hard delete user: %v", err)
	}

	if got, _ := cs.GetUserByUsername("doomed"); got != nil {
		t.Fatalf("expected user gone after hard delete, got %+v", got)
	}
	mems, err := cs.ListMembershipsForUser(ctx, id)
	if err != nil {
		t.Fatalf("list memberships after delete: %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("expected no orphaned memberships after hard delete, got %+v", mems)
	}
	devices, err := cs.ListDevicesForAccount(id)
	if err != nil {
		t.Fatalf("list devices after delete: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected no orphaned devices after hard delete, got %+v", devices)
	}
	teamMembers, err := cs.ListMembersOfTeam(ctx, team.ID)
	if err != nil {
		t.Fatalf("list team members after delete: %v", err)
	}
	for _, tm := range teamMembers {
		if tm.AccountID == id {
			t.Fatalf("expected no orphaned team_members row for the deleted account, got %+v", teamMembers)
		}
	}
	res, err := cs.ResolveManagedToken(ctx, "hash-doomed")
	if err != nil {
		t.Fatalf("resolve managed token after delete: %v", err)
	}
	if res != nil {
		t.Fatalf("expected the deleted user's managed token to be gone too, got %+v (id=%s)", res, mt.ID)
	}
}

// TestAdminHardDeleteUserUnknownID verifies deleting a non-existent id fails
// with ErrManagedTokenUserNotFound rather than silently succeeding.
func TestAdminHardDeleteUserUnknownID(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if err := cs.AdminHardDeleteUser(ctx, "999999"); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("expected ErrManagedTokenUserNotFound, got %v", err)
	}
}
