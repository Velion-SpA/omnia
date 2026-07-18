package cloudstore

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ─── DemoteUserAdminGuarded ───────────────────────────────────────────────────

// TestDemoteUserAdminGuardedRefusesLastAdmin verifies the sequential (no
// concurrency) refusal: demoting the ONLY remaining admin is rejected with
// ErrLastAdmin and the flag is left untouched.
func TestDemoteUserAdminGuardedRefusesLastAdmin(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("solo-admin", "solo@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if err := cs.DemoteUserAdminGuarded(ctx, user.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	if isAdmin, err := cs.IsUserAdmin(ctx, user.ID); err != nil || !isAdmin {
		t.Fatalf("expected admin flag untouched after refused demote, got %v err=%v", isAdmin, err)
	}
}

// TestDemoteUserAdminGuardedSucceedsWithAnotherAdminPresent verifies demote
// succeeds when at least one OTHER admin remains.
func TestDemoteUserAdminGuardedSucceedsWithAnotherAdminPresent(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	a, err := cs.CreateUser("admin-a", "a@example.com", "hash")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := cs.CreateUser("admin-b", "b@example.com", "hash")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, a.ID, true); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, b.ID, true); err != nil {
		t.Fatalf("promote b: %v", err)
	}

	if err := cs.DemoteUserAdminGuarded(ctx, a.ID); err != nil {
		t.Fatalf("expected demote to succeed with another admin present, got %v", err)
	}
	if isAdmin, _ := cs.IsUserAdmin(ctx, a.ID); isAdmin {
		t.Fatalf("expected a demoted")
	}
	if n, _ := cs.CountAdmins(ctx); n != 1 {
		t.Fatalf("expected exactly 1 admin remaining, got %d", n)
	}
}

// TestDemoteUserAdminGuardedUnknownID verifies an unknown id fails with
// ErrManagedTokenUserNotFound.
func TestDemoteUserAdminGuardedUnknownID(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if err := cs.DemoteUserAdminGuarded(ctx, "999999"); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("expected ErrManagedTokenUserNotFound, got %v", err)
	}
}

// TestDemoteUserAdminGuardedConcurrentRaceOnlyOneSucceeds is the TOCTOU
// regression guard: with EXACTLY 2 admins, two goroutines concurrently
// demote a DIFFERENT admin each. Under the OLD (non-atomic) check-then-act
// pattern, both could observe count==2 before either mutation commits and
// both would succeed, leaving 0 admins. With the SELECT ... FOR UPDATE lock
// inside the same transaction as the mutation, exactly one must succeed and
// the other must be refused with ErrLastAdmin — the admin count must never
// reach 0.
func TestDemoteUserAdminGuardedConcurrentRaceOnlyOneSucceeds(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	a, err := cs.CreateUser("race-admin-a", "race-a@example.com", "hash")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := cs.CreateUser("race-admin-b", "race-b@example.com", "hash")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, a.ID, true); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, b.ID, true); err != nil {
		t.Fatalf("promote b: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs[0] = cs.DemoteUserAdminGuarded(ctx, a.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		errs[1] = cs.DemoteUserAdminGuarded(ctx, b.ID)
	}()
	close(start)
	wg.Wait()

	successCount, refusedCount := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			successCount++
		case errors.Is(e, ErrLastAdmin):
			refusedCount++
		default:
			t.Fatalf("unexpected error from concurrent demote: %v", e)
		}
	}
	if successCount != 1 || refusedCount != 1 {
		t.Fatalf("expected exactly 1 success + 1 refusal, got success=%d refused=%d errs=%v", successCount, refusedCount, errs)
	}
	if n, err := cs.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("expected exactly 1 admin remaining after the race (never 0), got n=%d err=%v", n, err)
	}
}

// ─── DeactivateUserGuarded ────────────────────────────────────────────────────

// TestDeactivateUserGuardedRefusesLastAdmin mirrors the demote sequential
// refusal for the deactivate path.
func TestDeactivateUserGuardedRefusesLastAdmin(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("solo-admin-2", "solo2@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if err := cs.DeactivateUserGuarded(ctx, user.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	users, _ := cs.ListUsers(ctx)
	if u, ok := findAdminUser(users, user.ID); !ok || u.Disabled() {
		t.Fatalf("expected the last admin to remain enabled after a refused deactivate, got %+v ok=%v", u, ok)
	}
}

// TestDeactivateUserGuardedSucceedsWithAnotherAdminPresent verifies deactivate
// succeeds when another admin remains.
func TestDeactivateUserGuardedSucceedsWithAnotherAdminPresent(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	a, err := cs.CreateUser("deact-admin-a", "deact-a@example.com", "hash")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := cs.CreateUser("deact-admin-b", "deact-b@example.com", "hash")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, a.ID, true); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, b.ID, true); err != nil {
		t.Fatalf("promote b: %v", err)
	}

	if err := cs.DeactivateUserGuarded(ctx, a.ID); err != nil {
		t.Fatalf("expected deactivate to succeed with another admin present, got %v", err)
	}
	users, _ := cs.ListUsers(ctx)
	if u, ok := findAdminUser(users, a.ID); !ok || !u.Disabled() {
		t.Fatalf("expected a disabled, got %+v ok=%v", u, ok)
	}
}

// TestDeactivateUserGuardedUnknownID verifies an unknown id fails with
// ErrManagedTokenUserNotFound.
func TestDeactivateUserGuardedUnknownID(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	if err := cs.DeactivateUserGuarded(ctx, "999999"); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("expected ErrManagedTokenUserNotFound, got %v", err)
	}
}

// ─── Cross-operation race: demote vs hard-delete ─────────────────────────────

// TestConcurrentDemoteAndHardDeleteOnlyOneSucceeds is the cross-operation
// TOCTOU regression guard the review specifically called out ("two
// concurrent admin deletes/deactivates"): with exactly 2 admins, one
// goroutine demotes admin A while another concurrently HARD-DELETES admin B.
// Both mutate DIFFERENT rows but must still serialize against the SAME
// admin-count invariant — exactly one must succeed and the admin count must
// never reach 0.
func TestConcurrentDemoteAndHardDeleteOnlyOneSucceeds(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	a, err := cs.CreateUser("cross-admin-a", "cross-a@example.com", "hash")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := cs.CreateUser("cross-admin-b", "cross-b@example.com", "hash")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, a.ID, true); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, b.ID, true); err != nil {
		t.Fatalf("promote b: %v", err)
	}

	var wg sync.WaitGroup
	var demoteErr, deleteErr error
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		demoteErr = cs.DemoteUserAdminGuarded(ctx, a.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		deleteErr = cs.AdminHardDeleteUser(ctx, b.ID)
	}()
	close(start)
	wg.Wait()

	demoteRefused := errors.Is(demoteErr, ErrLastAdmin)
	deleteRefused := errors.Is(deleteErr, ErrLastAdmin)
	if demoteErr != nil && !demoteRefused {
		t.Fatalf("unexpected demote error: %v", demoteErr)
	}
	if deleteErr != nil && !deleteRefused {
		t.Fatalf("unexpected delete error: %v", deleteErr)
	}
	// Exactly one of the two operations must have been refused.
	if demoteRefused == deleteRefused {
		t.Fatalf("expected exactly one of {demote, hard-delete} to be refused, got demoteErr=%v deleteErr=%v", demoteErr, deleteErr)
	}
	if n, err := cs.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("expected exactly 1 admin remaining after the cross-operation race (never 0), got n=%d err=%v", n, err)
	}
}

// ─── Sequential (no-race) guard for hard-delete, direct at the store layer ──

// TestAdminHardDeleteUserRefusesLastAdminSequential verifies (without any
// concurrency) that AdminHardDeleteUser itself refuses to delete the only
// remaining admin, directly against a live Postgres — the handler-level fake
// test already covers this from the HTTP layer; this pins the same guarantee
// at the store layer where the guard actually lives.
func TestAdminHardDeleteUserRefusesLastAdminSequential(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("solo-admin-3", "solo3@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.SetUserAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if err := cs.AdminHardDeleteUser(ctx, user.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	if got, _ := cs.GetUserByUsername("solo-admin-3"); got == nil {
		t.Fatalf("expected the last admin to survive a refused hard delete")
	}
}
