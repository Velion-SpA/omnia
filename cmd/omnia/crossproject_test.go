package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/multiproject"
	"github.com/velion/omnia/internal/store"
)

// TestDedupeNormalizedProjects covers the fan-out-time fix for the live
// store's real project-name fragmentation ("workly"/"Workly",
// "velion"/"Velion"/"01.- velion", "habitia"/"Habitia" — see
// resolveFanoutProjects' own doc): store.Search/recall.Service both
// normalize+lowercase-compare the project filter before querying, so two
// raw names that differ only by case (or by the hyphen/underscore
// collapsing store.NormalizeProject also applies) query the EXACT SAME
// underlying rows. Fanning out over both wastes a leg AND duplicates rows
// in the merged response (multiproject.Merge has no cross-group ID dedup).
func TestDedupeNormalizedProjects(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "no duplicates passes through, normalized",
			input: []string{"omnia", "workly", "vel-voice-assistant"},
			want:  []string{"omnia", "workly", "vel-voice-assistant"},
		},
		{
			name:  "case-variant duplicate collapses to one, first-seen wins",
			input: []string{"workly", "Workly"},
			want:  []string{"workly"},
		},
		{
			name:  "case-variant duplicate collapses regardless of which case comes first",
			input: []string{"Workly", "workly"},
			want:  []string{"workly"},
		},
		{
			// store.NormalizeProject only lowercases + collapses repeated
			// -/_ (internal/projectname.Normalize) — it does NOT know "01.-
			// velion" and "velion" are the same project (that mapping lives
			// in config.yaml's project_aliases, a layer above this
			// function). So this case-only dedup collapses "velion"/"Velion"
			// into one entry and "01.- velion"/"01.- Velion" into a SEPARATE
			// one — 4 raw names, 2 normalized survivors — not all the way
			// down to a single project. Documents the actual (partial)
			// scope of this fix rather than overclaiming it also resolves
			// alias-level fragmentation.
			name:  "case-only fragmentation collapses per spelling, not across alias variants",
			input: []string{"velion", "Velion", "01.- velion", "01.- Velion"},
			want:  []string{"velion", "01.- velion"},
		},
		{
			name:  "mixed: some duplicates, some unique",
			input: []string{"workly", "omnia", "Workly", "habitia", "Habitia", "trackly"},
			want:  []string{"workly", "omnia", "habitia", "trackly"},
		},
		{
			name:  "empty/whitespace-only names are dropped, not counted as a project",
			input: []string{"", "  ", "omnia"},
			want:  []string{"omnia"},
		},
		{
			name:  "nil input returns nil-equivalent empty slice",
			input: nil,
			want:  []string{},
		},
		{
			name:  "already-normalized input is idempotent",
			input: []string{"omnia"},
			want:  []string{"omnia"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeNormalizedProjects(tt.input)
			if len(got) == 0 && len(tt.want) == 0 {
				return // both empty (nil vs []string{}) — not a meaningful difference here
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dedupeNormalizedProjects(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestCrossProjectWorkerCapFor covers the [4,8] clamp of
// GOMAXPROCS*2 that sizes the fan-out's fixed worker pool (see
// crossProjectWorkerCap's own doc for why: fan-out legs are I/O-bound
// against a single shared SQLite file plus, when semantic recall is
// configured, Ollama — unbounded concurrency was measured to make both
// worse, not better, at the live store's N~49 project scale).
func TestCrossProjectWorkerCapFor(t *testing.T) {
	tests := []struct {
		name       string
		gomaxprocs int
		want       int
	}{
		{"1 CPU clamps up to the floor of 4", 1, 4},
		{"2 CPUs (2*2=4) sits exactly at the floor", 2, 4},
		{"3 CPUs (3*2=6) is within the unclamped range", 3, 6},
		{"4 CPUs (4*2=8) sits exactly at the ceiling", 4, 8},
		{"5 CPUs (5*2=10) clamps down to the ceiling of 8", 5, 8},
		{"10 CPUs (this machine) clamps down to the ceiling of 8", 10, 8},
		{"64 CPUs clamps down to the ceiling of 8", 64, 8},
		{"0 (defensive: pathological GOMAXPROCS) clamps up to the floor", 0, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := crossProjectWorkerCapFor(tt.gomaxprocs); got != tt.want {
				t.Errorf("crossProjectWorkerCapFor(%d) = %d, want %d", tt.gomaxprocs, got, tt.want)
			}
		})
	}
}

// TestEffectiveCrossProjectWorkers covers runCrossProjectFanout's OTHER
// worker-pool-sizing rule: never start more worker goroutines than there
// are projects to search, regardless of crossProjectWorkerCap — extra
// workers would just block forever reading from an already-drained,
// closed jobs channel.
func TestEffectiveCrossProjectWorkers(t *testing.T) {
	tests := []struct {
		name        string
		workerCap   int
		numProjects int
		want        int
	}{
		{"cap below project count uses the cap", 8, 49, 8},
		{"cap above project count clamps down to project count", 8, 3, 3},
		{"cap equals project count", 8, 8, 8},
		{"single project uses exactly one worker", 8, 1, 1},
		{"zero projects starts zero workers", 8, 0, 0},
		{"negative project count (defensive) starts zero workers", 8, -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveCrossProjectWorkers(tt.workerCap, tt.numProjects); got != tt.want {
				t.Errorf("effectiveCrossProjectWorkers(%d, %d) = %d, want %d", tt.workerCap, tt.numProjects, got, tt.want)
			}
		})
	}
}

// TestSearchResultToItem covers the store.SearchResult -> multiproject.Item
// conversion crossProjectSearch feeds into multiproject.Group/Merge —
// specifically Preempt's boolean-OR (r.Rank == cliExactSentinelRank ||
// r.SignatureMatch), the exact kind of condition that silently reads
// backwards (AND instead of OR, or a flipped operand) without anyone
// noticing until a topic_key exact-match or signature-match row stops
// pre-empting the cross-project merge the way it pre-empts every
// single-project ranking stage (mcp.RankResults, MinMaxNormalizeRelevance).
func TestSearchResultToItem(t *testing.T) {
	tests := []struct {
		name         string
		result       store.SearchResult
		relevance    float64
		wantPreempt  bool
		wantID       int64
		wantUpdated  string
		wantRelevant float64
	}{
		{
			name:         "ordinary row: neither sentinel nor signature-match, not preempted",
			result:       store.SearchResult{Observation: store.Observation{ID: 42, UpdatedAt: "2026-08-01T00:00:00Z"}, Rank: -3.5, SignatureMatch: false},
			relevance:    0.75,
			wantPreempt:  false,
			wantID:       42,
			wantUpdated:  "2026-08-01T00:00:00Z",
			wantRelevant: 0.75,
		},
		{
			name:        "topic_key exact-match sentinel (Rank == cliExactSentinelRank) preempts",
			result:      store.SearchResult{Observation: store.Observation{ID: 7, UpdatedAt: "2026-08-02T00:00:00Z"}, Rank: cliExactSentinelRank, SignatureMatch: false},
			relevance:   0,
			wantPreempt: true,
			wantID:      7,
			wantUpdated: "2026-08-02T00:00:00Z",
		},
		{
			name:        "signature-match row preempts even with an ordinary Rank",
			result:      store.SearchResult{Observation: store.Observation{ID: 9, UpdatedAt: "2026-08-03T00:00:00Z"}, Rank: -12, SignatureMatch: true},
			relevance:   0,
			wantPreempt: true,
			wantID:      9,
			wantUpdated: "2026-08-03T00:00:00Z",
		},
		{
			name:        "both sentinel AND signature-match still preempts (OR, not XOR)",
			result:      store.SearchResult{Observation: store.Observation{ID: 11, UpdatedAt: "2026-08-04T00:00:00Z"}, Rank: cliExactSentinelRank, SignatureMatch: true},
			relevance:   0,
			wantPreempt: true,
			wantID:      11,
			wantUpdated: "2026-08-04T00:00:00Z",
		},
		{
			name:         "Rank merely NEAR the sentinel value does not preempt (exact match only)",
			result:       store.SearchResult{Observation: store.Observation{ID: 13, UpdatedAt: "2026-08-05T00:00:00Z"}, Rank: -999, SignatureMatch: false},
			relevance:    0.4,
			wantPreempt:  false,
			wantID:       13,
			wantUpdated:  "2026-08-05T00:00:00Z",
			wantRelevant: 0.4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchResultToItem(tt.result, tt.relevance)
			want := multiproject.Item{
				ID:        tt.wantID,
				Relevance: tt.wantRelevant,
				UpdatedAt: tt.wantUpdated,
				Preempt:   tt.wantPreempt,
			}
			if got != want {
				t.Errorf("searchResultToItem(%+v, %v) = %+v, want %+v", tt.result, tt.relevance, got, want)
			}
		})
	}
}

// TestRunCrossProjectLeg_RecoversFromPanic exercises the fix for the gap a
// fresh-context reviewer found: Go's net/http only recovers a panic in the
// goroutine handling the request itself, never in goroutines that handler
// spawns — so an unrecovered panic in any one fan-out leg (e.g.
// embed.ScopedSearcher.SearchScoped choking on a malformed/nil response
// from Ollama) would take down the entire omnia server process, not just
// degrade that one project's contribution. storeSearch (cmd/omnia/main.go)
// is an injectable package var specifically so a test can force this path
// deterministically instead of needing a live store/Ollama to misbehave.
func TestRunCrossProjectLeg_RecoversFromPanic(t *testing.T) {
	origStoreSearch := storeSearch
	defer func() { storeSearch = origStoreSearch }()

	storeSearch = func(s *store.Store, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
		panic("simulated ScopedSearcher panic (e.g. malformed Ollama response)")
	}

	// recallSvc nil routes recallOrFTSSearchWithRelevance straight into the
	// stubbed storeSearch above. Run in a background goroutine and wait on
	// a channel close: if runCrossProjectLeg's recover ever regresses, the
	// panic propagates and fails this whole test binary (a hard signal, not
	// a silent pass) rather than being swallowed by the test framework.
	done := make(chan struct{})
	var leg crossProjectLeg
	go func() {
		defer close(done)
		leg = runCrossProjectLeg(context.Background(), nil, nil, "blocked", store.SearchOptions{Limit: 10}, "workly")
	}()
	<-done

	if leg.err == nil {
		t.Fatal("expected a non-nil error after a recovered panic, got nil")
	}
	if !strings.Contains(leg.err.Error(), "workly") || !strings.Contains(leg.err.Error(), "panicked") {
		t.Errorf("leg.err = %q, want it to name the project and say it panicked", leg.err.Error())
	}
	if leg.project != "workly" {
		t.Errorf("leg.project = %q, want %q (set before the deferred recover runs, so it survives a panic)", leg.project, "workly")
	}
	if leg.results != nil || leg.relevance != nil || leg.fusionRan {
		t.Errorf("leg = %+v, want results/relevance/fusionRan all cleared to their zero value after a recovered panic", leg)
	}
}

// TestRunCrossProjectFanout_OnePanickingLegDoesNotCrashOthers is the
// fan-out-level version of the same fix: one project's leg panicking must
// degrade ONLY that project's contribution to the merge (leg.err set, empty
// results — handled identically to an ordinary non-panic error by
// crossProjectSearch's own leg.err != nil skip), while every other
// concurrently-running leg completes normally.
func TestRunCrossProjectFanout_OnePanickingLegDoesNotCrashOthers(t *testing.T) {
	origStoreSearch := storeSearch
	defer func() { storeSearch = origStoreSearch }()

	storeSearch = func(s *store.Store, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
		if opts.Project == "workly" {
			panic("simulated failure for the workly leg")
		}
		return []store.SearchResult{{Observation: store.Observation{ID: 1, UpdatedAt: "2026-08-01T00:00:00Z"}}}, nil
	}

	projects := []string{"omnia", "workly", "trackly"}
	legs := runCrossProjectFanout(context.Background(), nil, nil, "blocked", store.SearchOptions{Limit: 10}, projects)

	if len(legs) != 3 {
		t.Fatalf("got %d legs, want 3", len(legs))
	}
	byProject := make(map[string]crossProjectLeg, len(legs))
	for _, l := range legs {
		byProject[l.project] = l
	}

	if byProject["workly"].err == nil {
		t.Error("workly leg: want a recovered-panic error, got nil (an unrecovered panic here would have crashed this test process instead of reaching this assertion)")
	}
	for _, p := range []string{"omnia", "trackly"} {
		if byProject[p].err != nil {
			t.Errorf("%s leg: want no error — a healthy leg must be unaffected by workly's panic, got %v", p, byProject[p].err)
		}
		if len(byProject[p].results) != 1 {
			t.Errorf("%s leg: want 1 result, got %d", p, len(byProject[p].results))
		}
	}
}
