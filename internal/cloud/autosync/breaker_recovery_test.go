package autosync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velion/omnia/internal/store"
)

// TestManagerRecoversAfterFailureCeiling locks the half-open breaker.
//
// The ceiling check used to return before running anything, which latched the
// breaker open for the life of the process: ConsecutiveFailures is cleared only
// by recordSuccess, and recordSuccess is only reachable by running a cycle the
// ceiling branch refused to run. A cloud that came back up stayed dead until an
// operator restarted the daemon by hand.
//
// Once the backoff window expires the manager must probe again, and a probe
// that succeeds must clear the failure count and report healthy.
func TestManagerRecoversAfterFailureCeiling(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "k1", Op: "upsert", Project: "proj-a", Payload: `{"id":"1"}`},
	}
	tr := newFakeTransport()
	tr.pushErr = errors.New("status 401: unauthorized: invalid bearer token")

	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 3
	cfg.DebounceDuration = time.Millisecond
	cfg.PollInterval = 5 * time.Millisecond
	cfg.BaseBackoff = time.Millisecond
	cfg.MaxBackoff = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go mgr.Run(ctx)

	// Drive it past the ceiling.
	if !waitFor(2*time.Second, func() bool {
		return mgr.Status().ConsecutiveFailures >= cfg.MaxConsecutiveFailures
	}) {
		t.Fatalf("never reached the failure ceiling; status=%+v", mgr.Status())
	}

	// The cloud comes back. Nothing restarts the process.
	callsAtRepair := atomic.LoadInt32(&tr.pushCalls)
	tr.mu.Lock()
	tr.pushErr = nil
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{1}}
	tr.mu.Unlock()

	// It must probe again on its own once the backoff window expires.
	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&tr.pushCalls) > callsAtRepair
	}) {
		t.Fatal("breaker latched: no push attempted after the cloud recovered")
	}

	// And a successful probe must actually clear the degraded state.
	if !waitFor(2*time.Second, func() bool {
		st := mgr.Status()
		return st.Phase == PhaseHealthy && st.ConsecutiveFailures == 0
	}) {
		t.Fatalf("did not recover to healthy; status=%+v", mgr.Status())
	}
}

func waitFor(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}
