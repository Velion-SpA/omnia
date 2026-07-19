package cloudstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// TestProjectLinksIntegration covers the Slice 5a sub-project linking
// lifecycle against a live Postgres: link, list, unlink, and every
// 2-level-hierarchy rejection (self-ref, parent-is-child, child-is-parent).
func TestProjectLinksIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	// Linking a project with no cloud_project_meta row yet must upsert one
	// (mirrors UpsertProjectMeta's own insert-if-absent behavior).
	if err := cs.SetProjectParent(ctx, "workly-marketing", "workly"); err != nil {
		t.Fatalf("set project parent: %v", err)
	}
	parents, err := cs.ListProjectParents(ctx)
	if err != nil {
		t.Fatalf("list project parents: %v", err)
	}
	if parents["workly-marketing"] != "workly" {
		t.Fatalf("expected workly-marketing -> workly, got %+v", parents)
	}

	// Linking a second child under the same parent must not disturb the
	// first link, and both must show up in ListProjectParents.
	if err := cs.SetProjectParent(ctx, "workly-videos", "workly"); err != nil {
		t.Fatalf("set second project parent: %v", err)
	}
	parents, err = cs.ListProjectParents(ctx)
	if err != nil {
		t.Fatalf("list project parents (2): %v", err)
	}
	if len(parents) != 2 || parents["workly-videos"] != "workly" || parents["workly-marketing"] != "workly" {
		t.Fatalf("expected 2 links under workly, got %+v", parents)
	}

	// Re-linking the same child to a DIFFERENT parent overwrites (the PK on
	// project naturally prevents multi-parent — no separate check needed).
	if err := cs.SetProjectParent(ctx, "workly-marketing", "velion"); err != nil {
		t.Fatalf("relink project parent: %v", err)
	}
	parents, _ = cs.ListProjectParents(ctx)
	if parents["workly-marketing"] != "velion" {
		t.Fatalf("expected relinked workly-marketing -> velion, got %+v", parents)
	}

	// Self-reference is rejected.
	if err := cs.SetProjectParent(ctx, "workly", "workly"); !errors.Is(err, ErrProjectLinkSelf) {
		t.Fatalf("expected ErrProjectLinkSelf, got %v", err)
	}

	// Parent is itself a child ("workly-videos" is linked under "workly") →
	// cannot become a parent of anything else (would create a 3-level chain).
	if err := cs.SetProjectParent(ctx, "workly-marketing-videos", "workly-videos"); !errors.Is(err, ErrProjectLinkParentIsChild) {
		t.Fatalf("expected ErrProjectLinkParentIsChild, got %v", err)
	}

	// Child already has its own linked sub-projects ("workly" is the parent
	// of workly-videos) → cannot become a child itself.
	if err := cs.SetProjectParent(ctx, "workly", "velion"); !errors.Is(err, ErrProjectLinkChildIsParent) {
		t.Fatalf("expected ErrProjectLinkChildIsParent, got %v", err)
	}

	// Unlink is idempotent: clearing a linked child removes the parent, and
	// clearing an already-unlinked (or never-linked) project is a no-op, not
	// an error.
	if err := cs.ClearProjectParent(ctx, "workly-marketing"); err != nil {
		t.Fatalf("clear project parent: %v", err)
	}
	parents, _ = cs.ListProjectParents(ctx)
	if _, stillLinked := parents["workly-marketing"]; stillLinked {
		t.Fatalf("expected workly-marketing unlinked, got %+v", parents)
	}
	if err := cs.ClearProjectParent(ctx, "workly-marketing"); err != nil {
		t.Fatalf("clear already-unlinked project should be a no-op, got: %v", err)
	}
	if err := cs.ClearProjectParent(ctx, "never-linked-project"); err != nil {
		t.Fatalf("clear never-linked project should be a no-op, got: %v", err)
	}

	// After unlinking workly-marketing, "workly" no longer has 2 children —
	// but it still has "workly-videos", so it must still be rejected as a
	// child (ErrProjectLinkChildIsParent), proving the rejection is based on
	// "has ANY linked child", not a stale first-check snapshot.
	if err := cs.SetProjectParent(ctx, "workly", "velion"); !errors.Is(err, ErrProjectLinkChildIsParent) {
		t.Fatalf("expected ErrProjectLinkChildIsParent (workly still has workly-videos), got %v", err)
	}

	// Now unlink the last child and confirm workly becomes a valid child
	// candidate again (proves the check is live, not permanently sticky).
	if err := cs.ClearProjectParent(ctx, "workly-videos"); err != nil {
		t.Fatalf("clear last child: %v", err)
	}
	if err := cs.SetProjectParent(ctx, "workly", "velion"); err != nil {
		t.Fatalf("workly should now be linkable as a child: %v", err)
	}
}

// TestSetProjectParent_ConcurrentLinksNeverViolateHierarchy is a regression
// test for a TOCTOU race in SetProjectParent: before the fix, the
// 2-level-hierarchy validation reads (projectParentOf,
// projectHasLinkedChildren) and the upsert ran as three separate,
// unserialized statements against the connection pool. Two concurrent
// calls could each read a pre-mutation state that looked valid in
// isolation and both commit, producing either a forbidden 3-level chain
// (SetProjectParent(a,b) racing SetProjectParent(b,c) -> a->b->c) or a
// cycle (SetProjectParent(a,b) racing SetProjectParent(b,a) -> a->b AND
// b->a at once).
//
// The test fires many such conflicting triples at once — all goroutines
// parked on a start-gate so they are released together to maximize
// interleaving — then asserts the one invariant that must never be
// violated regardless of which individual calls won or lost: no project
// may simultaneously be a child (a key in ListProjectParents) and a
// parent (a value in ListProjectParents). Individual calls legitimately
// returning one of the rejection sentinel errors is expected; a committed
// structure that violates the invariant is not.
func TestSetProjectParent_ConcurrentLinksNeverViolateHierarchy(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	type attempt struct{ child, parent string }
	var attempts []attempt
	const triples = 60
	for i := 0; i < triples; i++ {
		a := fmt.Sprintf("race-%d-a", i)
		b := fmt.Sprintf("race-%d-b", i)
		c := fmt.Sprintf("race-%d-c", i)
		// (a,b) racing (b,c): if both win, a->b->c is a 3-level chain.
		// (a,b) racing (b,a): if both win, a->b and b->a is a cycle.
		attempts = append(attempts,
			attempt{child: a, parent: b},
			attempt{child: b, parent: c},
			attempt{child: b, parent: a},
		)
	}

	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(len(attempts))
	start.Add(1)
	done.Add(len(attempts))
	for _, at := range attempts {
		at := at
		go func() {
			defer done.Done()
			ready.Done()
			start.Wait()
			_ = cs.SetProjectParent(ctx, at.child, at.parent)
		}()
	}
	ready.Wait() // every goroutine is parked on start.Wait()
	start.Done() // release them all at once
	done.Wait()

	parents, err := cs.ListProjectParents(ctx)
	if err != nil {
		t.Fatalf("list project parents: %v", err)
	}
	for child, parent := range parents {
		if grandparent, parentIsAlsoChild := parents[parent]; parentIsAlsoChild {
			t.Fatalf("hierarchy violation: %q -> %q -> %q (a 3-level chain, or %q/%q form a cycle); full map: %+v",
				child, parent, grandparent, child, parent, parents)
		}
	}
}

// TestSetProjectParent_RequiresChildAndParent proves the required-argument
// guard rejects blank input with a clear error rather than a panic or a
// silent no-op write.
func TestSetProjectParent_RequiresChildAndParent(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	if err := cs.SetProjectParent(ctx, "", "workly"); err == nil {
		t.Fatal("expected error for empty child")
	}
	if err := cs.SetProjectParent(ctx, "workly-marketing", ""); err == nil {
		t.Fatal("expected error for empty parent")
	}
}
