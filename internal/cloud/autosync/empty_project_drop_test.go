package autosync

import (
	"context"
	"errors"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// TestPushDropsEmptyProjectMutationsAndUnblocksTheRest reproduces the live
// stall: one queued mutation with an empty project made every push cycle fail
// with `status 400: mutation entries must specify a project`, and because the
// group loop returns on the first failing group, 62 other projects stopped
// replicating entirely (last_sync_at stayed null).
//
// The empty-project entry must never reach the transport, and the healthy
// project behind it must ship in the same cycle.
func TestPushDropsEmptyProjectMutationsAndUnblocksTheRest(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "poison", Op: "upsert", Project: "", Payload: `{"id":"1"}`},
		{Seq: 2, Entity: "obs", EntityKey: "k2", Op: "upsert", Project: "proj-a", Payload: `{"id":"2"}`},
		{Seq: 3, Entity: "obs", EntityKey: "blank", Op: "upsert", Project: "   ", Payload: `{"id":"3"}`},
	}
	tr := newFakeTransport()
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{2}}

	mgr := New(ls, tr, DefaultConfig())
	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	tr.mu.Lock()
	pushed := append([][]MutationEntry(nil), tr.pushed...)
	tr.mu.Unlock()

	sawProject := map[string]bool{}
	for _, batch := range pushed {
		for _, e := range batch {
			sawProject[e.Project] = true
			if e.Project == "" || e.Project == "   " {
				t.Errorf("an empty-project entry reached the transport (entity_key=%q); the cloud always rejects these", e.EntityKey)
			}
		}
	}
	if !sawProject["proj-a"] {
		t.Error("the healthy project never shipped — the poison row still blocks the queue")
	}

	// Dropping must clear them from the queue, not just skip them: a skipped
	// row keeps occupying a slot in the fixed-size window forever.
	ls.mu.Lock()
	acked := append([]int64(nil), ls.ackedSeqs...)
	ls.mu.Unlock()
	for _, want := range []int64{1, 3} {
		found := false
		for _, got := range acked {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("seq %d was not acked out of the queue; it will be listed again forever", want)
		}
	}
}

// TestPushWhenEveryMutationIsUnsendableSucceeds covers the window that is
// entirely poison. The cycle must report success so the failure counter clears
// — the queue genuinely advanced — instead of counting toward the breaker.
func TestPushWhenEveryMutationIsUnsendableSucceeds(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "p1", Op: "upsert", Project: "", Payload: `{"id":"1"}`},
		{Seq: 2, Entity: "obs", EntityKey: "p2", Op: "upsert", Project: "", Payload: `{"id":"2"}`},
	}
	tr := newFakeTransport()
	tr.pushErr = errors.New("transport must not be called")

	mgr := New(ls, tr, DefaultConfig())
	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("push failed on an all-unsendable window: %v", err)
	}
	if len(tr.pushed) != 0 {
		t.Errorf("transport was called with %d batch(es); nothing was sendable", len(tr.pushed))
	}
}
