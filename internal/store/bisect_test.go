package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBisectEventsAcceptsImmediateFirstObservationAfterEnable(t *testing.T) {
	const maxAttempts = 8
	for attempt := 0; attempt < maxAttempts; attempt++ {
		s := newTimeTravelStore(t, true, 0)
		ids := []int64{
			addTimeTravelObservation(t, s, "immediate first", ""),
			addTimeTravelObservation(t, s, "immediate second", ""),
			addTimeTravelObservation(t, s, "immediate third", ""),
		}

		var startedAt, firstCreatedAt string
		if err := s.db.QueryRow(`SELECT started_at FROM time_travel_metadata WHERE id=1`).Scan(&startedAt); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT created_at FROM observations WHERE id=?`, ids[0]).Scan(&firstCreatedAt); err != nil {
			t.Fatal(err)
		}
		started, err := parseObservationTime(startedAt)
		if err != nil {
			t.Fatal(err)
		}
		firstCreated, err := parseObservationTime(firstCreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if !started.Truncate(time.Second).Equal(firstCreated.Truncate(time.Second)) {
			continue
		}

		events, err := s.BisectEvents(strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[2], 10))
		if err != nil {
			t.Fatalf("BisectEvents rejected immediate post-enable bound (started_at=%q created_at=%q): %v", startedAt, firstCreatedAt, err)
		}
		if len(events) != 2 || events[0].ID != ids[1] || events[1].ID != ids[2] {
			t.Fatalf("events=%v, want observation IDs %v", events, ids[1:])
		}
		return
	}
	t.Fatalf("could not create a same-second recording boundary fixture after %d attempts", maxAttempts)
}

func TestBisectEventsUsesInitialIDToDisambiguateBoundarySecond(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	ids := []int64{
		addTimeTravelObservation(t, s, "pre-boundary first", ""),
		addTimeTravelObservation(t, s, "pre-boundary last", ""),
		addTimeTravelObservation(t, s, "post-boundary first", ""),
		addTimeTravelObservation(t, s, "post-boundary bad", ""),
	}
	const (
		storedSecond = "2026-01-02 03:04:05"
		boundary     = "2026-01-02T03:04:05.123456789Z"
	)
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`, storedSecond, storedSecond, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(
		`UPDATE time_travel_metadata SET started_at=?, initial_max_observation_id=? WHERE id=1`,
		boundary, ids[1],
	); err != nil {
		t.Fatal(err)
	}

	if _, err := s.BisectEvents(strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[3], 10)); err == nil ||
		!strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("pre-boundary same-second observation accepted: %v", err)
	}
	if _, err := s.BisectEvents(storedSecond, "now"); err == nil ||
		!strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("pre-boundary timestamp accepted through observation-ID precision rule: %v", err)
	}
	events, err := s.BisectEvents(strconv.FormatInt(ids[2], 10), strconv.FormatInt(ids[3], 10))
	if err != nil || len(events) != 1 || events[0].ID != ids[3] {
		t.Fatalf("post-boundary same-second observation rejected: events=%v err=%v", events, err)
	}
}

func TestBisectEventsValidatesBothBoundsAgainstRecordingBoundary(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	ids := []int64{
		addTimeTravelObservation(t, s, "pre-enable first", ""),
		addTimeTravelObservation(t, s, "pre-enable last", ""),
		addTimeTravelObservation(t, s, "post-enable first", ""),
		addTimeTravelObservation(t, s, "post-enable last", ""),
		addTimeTravelObservation(t, s, "post-enable backdated fractional", ""),
	}
	const boundary = "2026-01-02T03:04:05.123456789Z"
	timestamps := []string{
		"2026-01-02T03:04:05.800000000Z",
		"2026-01-02T03:04:05.900000000Z",
		"2026-01-02 03:04:05",
		"2026-01-02T03:04:05.950000000Z",
		"2026-01-02T03:04:05.100000000Z",
	}
	for i, id := range ids {
		if _, err := s.db.Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`, timestamps[i], timestamps[i], id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(
		`UPDATE time_travel_metadata SET started_at=?, initial_max_observation_id=? WHERE id=1`,
		boundary, ids[1],
	); err != nil {
		t.Fatal(err)
	}

	ref := func(id int64) string { return strconv.FormatInt(id, 10) }
	tests := []struct {
		name      string
		good, bad string
		wantErr   string
	}{
		{"good timestamp before boundary", "2026-01-02T03:04:05Z", ref(ids[3]), "history unavailable before"},
		{"good pre-enable ID after boundary instant", ref(ids[0]), ref(ids[3]), "history unavailable before"},
		{"bad timestamp before boundary", ref(ids[2]), "2026-01-02T03:04:05Z", "history unavailable before"},
		{"bad pre-enable ID after boundary instant", ref(ids[2]), ref(ids[1]), "history unavailable before"},
		{"bad post-enable fractional ID before boundary", ref(ids[2]), ref(ids[4]), "history unavailable before"},
		{"post-enable ID bounds", ref(ids[2]), ref(ids[3]), ""},
		{"exact boundary timestamp", boundary, "now", ""},
		{"offset exact boundary timestamp", "2026-01-01T22:04:05.123456789-05:00", "now", ""},
		{"offset timestamp before boundary", ref(ids[2]), "2026-01-01T22:04:05.100000000-05:00", "history unavailable before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.BisectEvents(tt.good, tt.bad)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("BisectEvents(%q, %q): %v", tt.good, tt.bad, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("BisectEvents(%q, %q) error=%v, want %q", tt.good, tt.bad, err, tt.wantErr)
			}
		})
	}
}

func TestBisectEventsExcludesPreEnableCandidates(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	ids := []int64{
		addTimeTravelObservation(t, s, "pre-enable secret first", ""),
		addTimeTravelObservation(t, s, "pre-enable secret last", ""),
		addTimeTravelObservation(t, s, "post-enable good", ""),
		addTimeTravelObservation(t, s, "post-enable bad", ""),
		addTimeTravelObservation(t, s, "post-enable backdated secret", ""),
	}
	timestamps := []string{
		"2026-01-02T03:04:05.800000000Z",
		"2026-01-02T03:04:05.900000000Z",
		"2026-01-02 03:04:05",
		"2026-01-02T03:04:05.950000000Z",
		"2026-01-02T03:04:05.100000000Z",
	}
	for i, id := range ids {
		if _, err := s.db.Exec(`UPDATE observations SET created_at=?, updated_at=? WHERE id=?`, timestamps[i], timestamps[i], id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(
		`UPDATE time_travel_metadata SET started_at='2026-01-02T03:04:05.123456789Z', initial_max_observation_id=? WHERE id=1`,
		ids[1],
	); err != nil {
		t.Fatal(err)
	}

	events, err := s.BisectEvents(strconv.FormatInt(ids[2], 10), strconv.FormatInt(ids[3], 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != ids[3] {
		t.Fatalf("events=%v, want only post-enable observation %d", events, ids[3])
	}
}

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
