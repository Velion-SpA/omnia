package cloudstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestClaimProjectOwnership_SerialFirstClaimWinsSecondDenied is the baseline
// serial contract: the first claim on a brand-new project wins and becomes
// owner; a second claim by a DIFFERENT account is denied (claimed=false) and
// leaves the winner's membership completely untouched.
func TestClaimProjectOwnership_SerialFirstClaimWinsSecondDenied(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	claimed, err := cs.ClaimProjectOwnership(ctx, "alice", "fresh-serial", 7, "owner")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatalf("expected first claim to win, got claimed=false")
	}
	members, err := cs.ListProjectMembers("fresh-serial")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 || members[0].AccountID != "alice" || members[0].Role != "owner" {
		t.Fatalf("expected alice as sole owner, got %+v", members)
	}

	claimed, err = cs.ClaimProjectOwnership(ctx, "eve", "fresh-serial", 7, "owner")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatalf("expected second claim (different account) to be denied, got claimed=true")
	}
	members, err = cs.ListProjectMembers("fresh-serial")
	if err != nil {
		t.Fatalf("list members after second claim: %v", err)
	}
	if len(members) != 1 || members[0].AccountID != "alice" {
		t.Fatalf("expected membership unchanged after denied claim, got %+v", members)
	}
	for _, m := range members {
		if m.AccountID == "eve" {
			t.Fatalf("eve must NOT have gained a membership on the already-claimed project")
		}
	}
}

// TestClaimProjectOwnership_ConcurrentClaimsExactlyOneOwner is a regression
// test for the CRITICAL cross-tenant race: cloudserver.claimOrphanProject used
// to read ListProjectMembers and, if empty, call GrantMembership — a
// check-then-act sequence with NO lock between the read and the write.
// cloud_memberships' unique key is (account_id, project), a COMPOSITE key, so
// nothing at the DB layer stopped two DIFFERENT accounts from both reading
// "zero members" for the SAME brand-new project name and both committing an
// owner row — two full owners on one project, a genuine cross-tenant
// isolation breach.
//
// This fires many different accounts at ClaimProjectOwnership for the SAME
// project name concurrently, all parked on a start-gate so they are released
// together to maximize interleaving, then asserts the one invariant that must
// never be violated: exactly one owner row exists afterward. Run with
// `go test -race` to also confirm no data race on the underlying connection
// pool.
func TestClaimProjectOwnership_ConcurrentClaimsExactlyOneOwner(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	const project = "race-claim-project"
	const contenders = 40

	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	claimedFlags := make([]bool, contenders)
	errs := make([]error, contenders)
	ready.Add(contenders)
	start.Add(1)
	done.Add(contenders)
	for i := 0; i < contenders; i++ {
		i := i
		go func() {
			defer done.Done()
			ready.Done()
			start.Wait()
			accountID := fmt.Sprintf("tenant-%d", i)
			claimed, err := cs.ClaimProjectOwnership(ctx, accountID, project, 7, "owner")
			claimedFlags[i] = claimed
			errs[i] = err
		}()
	}
	ready.Wait() // every goroutine is parked on start.Wait()
	start.Done() // release them all at once
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("contender %d returned error: %v", i, err)
		}
	}

	winners := 0
	for _, claimed := range claimedFlags {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly 1 contender to win the claim, got %d (flags=%v)", winners, claimedFlags)
	}

	members, err := cs.ListProjectMembers(project)
	if err != nil {
		t.Fatalf("list project members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("INVARIANT VIOLATED: expected exactly 1 member on %q, got %d: %+v", project, len(members), members)
	}
	if members[0].Role != "owner" {
		t.Fatalf("expected the sole member to be owner, got role %q", members[0].Role)
	}
	if members[0].Perms != 7 {
		t.Fatalf("expected the sole member to have perms=7 (PermAll), got %d", members[0].Perms)
	}
}

// TestClaimProjectOwnership_RequiresAccountAndProject proves the
// required-argument guard rejects blank input with a clear error rather than
// a panic or a silent no-op write.
func TestClaimProjectOwnership_RequiresAccountAndProject(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	if _, err := cs.ClaimProjectOwnership(ctx, "", "some-project", 7, "owner"); err == nil {
		t.Fatal("expected error for empty account_id")
	}
	if _, err := cs.ClaimProjectOwnership(ctx, "alice", "", 7, "owner"); err == nil {
		t.Fatal("expected error for empty project")
	}
}
