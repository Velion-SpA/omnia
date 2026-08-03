package store

import (
	"fmt"
	"testing"
)

// ─── #226 [RED]: the observation store must expose its own watermark ───
//
// The decisive staleness question is "are there observations newer than the
// newest embedding?". Answering it needs the highest LIVE observation id from
// this side; the embeddings side already knows its own (embed.Store.Lag).

func TestObservationWatermark_EmptyStore(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	maxID, count, err := s.ObservationWatermark()
	if err != nil {
		t.Fatalf("ObservationWatermark: %v", err)
	}
	if maxID != 0 || count != 0 {
		t.Fatalf("empty store must report a zero watermark, got maxID=%d count=%d", maxID, count)
	}
}

func TestObservationWatermark_ReportsHighestLiveID(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.CreateSession("wm-session", "wm-project", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var last int64
	for i := 0; i < 3; i++ {
		id, err := s.AddObservation(AddObservationParams{
			SessionID: "wm-session", Type: "decision",
			Title: fmt.Sprintf("t%d", i), Content: fmt.Sprintf("c%d", i), Project: "wm-project", Scope: "project",
		})
		if err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
		last = id
	}

	maxID, count, err := s.ObservationWatermark()
	if err != nil {
		t.Fatalf("ObservationWatermark: %v", err)
	}
	if maxID != last {
		t.Fatalf("maxID: want %d, got %d", last, maxID)
	}
	if count != 3 {
		t.Fatalf("count: want 3, got %d", count)
	}
}

// A soft-deleted row must not count, and must not hold the watermark: an
// embedding job legitimately skips it, so counting it would report permanent
// phantom lag.
func TestObservationWatermark_IgnoresSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.CreateSession("wm-session", "wm-project", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	keep, err := s.AddObservation(AddObservationParams{
		SessionID: "wm-session", Type: "decision",
		Title: "keep", Content: "c", Project: "wm-project", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	gone, err := s.AddObservation(AddObservationParams{
		SessionID: "wm-session", Type: "decision",
		Title: "gone", Content: "c", Project: "wm-project", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.DeleteObservation(gone, false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	maxID, count, err := s.ObservationWatermark()
	if err != nil {
		t.Fatalf("ObservationWatermark: %v", err)
	}
	if maxID != keep {
		t.Fatalf("a soft-deleted row must not hold the watermark: want %d, got %d", keep, maxID)
	}
	if count != 1 {
		t.Fatalf("count must exclude soft-deleted rows: want 1, got %d", count)
	}
}
