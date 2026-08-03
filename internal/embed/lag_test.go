package embed

import (
	"context"
	"testing"
)

// ─── #226 [RED]: the embeddings store must be able to report how far it has
// fallen behind the observation store ───
//
// embeddings.db stopped being written on Jul 31 while observations kept
// arriving, and nothing anywhere could see it: search kept answering, the
// answers were just blind to everything created since. A store that cannot
// report its own watermark cannot be checked by doctor or by mem_search.

func TestStore_Lag_EmptyStore(t *testing.T) {
	s, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	lag, err := s.Lag(context.Background())
	if err != nil {
		t.Fatalf("Lag: %v", err)
	}
	if lag.Count != 0 || lag.MaxObsID != 0 {
		t.Fatalf("empty store must report a zero watermark, got %+v", lag)
	}
	if lag.NewestEmbeddedAt != "" {
		t.Fatalf("empty store must report no newest-embedded timestamp, got %q", lag.NewestEmbeddedAt)
	}
}

func TestStore_Lag_ReportsHighestObservationIDAndCount(t *testing.T) {
	s, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	// Insert out of order: the watermark is the MAX obs_id, not the last write.
	for _, r := range []Row{
		unitRow("a", 7, []float32{1, 0, 0}),
		unitRow("b", 42, []float32{0, 1, 0}),
		unitRow("c", 13, []float32{0, 0, 1}),
	} {
		if err := s.Upsert(ctx, r); err != nil {
			t.Fatalf("Upsert %s: %v", r.SyncID, err)
		}
	}

	lag, err := s.Lag(ctx)
	if err != nil {
		t.Fatalf("Lag: %v", err)
	}
	if lag.Count != 3 {
		t.Fatalf("Count: want 3, got %d", lag.Count)
	}
	if lag.MaxObsID != 42 {
		t.Fatalf("MaxObsID must be the highest embedded obs_id regardless of write order: want 42, got %d", lag.MaxObsID)
	}
	if lag.NewestEmbeddedAt == "" {
		t.Fatal("a non-empty store must report when it was last written")
	}
}
