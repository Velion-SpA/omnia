package store

import (
	"strconv"
	"strings"
	"testing"
)

func TestBisectEventsUsesExactCompositeBounds(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	const instant = "2026-01-02T03:04:05.123456789Z"
	_, _ = s.db.Exec(`UPDATE time_travel_metadata SET started_at='2026-01-01T00:00:00Z', initial_max_observation_id=0 WHERE id=1`)
	ids := make([]int64, 3)
	for i := range ids {
		ids[i] = addTimeTravelObservation(t, s, "collision", "")
		_, _ = s.db.Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`, instant, instant, ids[i])
	}
	ref := func(id int64) string { return strconv.FormatInt(id, 10) }
	tests := []struct {
		name      string
		good, bad string
		want      []int64
		wantErr   string
	}{
		{"adjacent IDs at one instant", ref(ids[0]), ref(ids[1]), []int64{ids[1]}, ""},
		{"bad ID excludes later collision", "2026-01-01T00:00:01Z", ref(ids[1]), []int64{ids[0], ids[1]}, ""},
		{"bad timestamp includes whole collision", "2026-01-01T00:00:01Z", instant, ids, ""},
		{"good timestamp excludes whole collision", instant, "now", nil, ""},
		{"reversed IDs at one instant", ref(ids[2]), ref(ids[0]), nil, "good bound must precede bad bound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := s.BisectEvents(tt.good, tt.bad)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || len(events) != len(tt.want) {
				t.Fatalf("events=%v err=%v, want IDs %v", events, err, tt.want)
			}
			for i := range events {
				if events[i].ID != tt.want[i] {
					t.Fatalf("event %d ID=%d, want %d", i, events[i].ID, tt.want[i])
				}
			}
		})
	}
}

func TestBisectEventsHonorsCompositeAvailabilityBoundary(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "boundary collision", "")
	obs, _ := s.GetObservation(id)
	_, _ = s.db.Exec(`UPDATE time_travel_metadata SET started_at=?, initial_max_observation_id=? WHERE id=1`, obs.CreatedAt, id)
	events, err := s.BisectEvents(strconv.FormatInt(id, 10), "now")
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%v err=%v, want exact boundary accepted", events, err)
	}
	_, err = s.BisectEvents("2000-01-01T00:00:00Z", "now")
	if err == nil || !strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("pre-boundary error=%v", err)
	}
}

func TestValidateBisectEventsRejectsInvalidSequences(t *testing.T) {
	valid := []BisectEvent{
		{ID: 1, SyncID: "a", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: 2, SyncID: "b", CreatedAt: "2026-01-01T00:00:00Z"},
	}
	if err := ValidateBisectEvents(valid); err != nil {
		t.Fatal(err)
	}
	for name, events := range map[string][]BisectEvent{
		"reversed":  {valid[1], valid[0]},
		"duplicate": {valid[0], valid[0]},
		"identity":  {{ID: 0, SyncID: "", CreatedAt: "invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBisectEvents(events); err == nil {
				t.Fatal("invalid event sequence accepted")
			}
		})
	}
}
