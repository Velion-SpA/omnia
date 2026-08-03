package diagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ─── #226 [RED]: doctor must flag embeddings that fell behind the store ───
//
// embeddings.db froze on Jul 31 while observations kept arriving. Search kept
// answering, so from every side it looked healthy — the answers were simply
// blind to everything created since. "Observations newer than the newest
// embedding" is the cheap, decisive check that would have caught it on day one.

func lagScope(t *testing.T, snap EmbeddingSnapshot, err error) Scope {
	t.Helper()
	return Scope{
		ReadEmbeddingSnapshot: func(context.Context) (EmbeddingSnapshot, error) {
			return snap, err
		},
	}
}

func TestEmbeddingLagCheck_FlagsObservationsNewerThanNewestEmbedding(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(), lagScope(t, EmbeddingSnapshot{
		Enabled:           true,
		ObservationMaxID:  1880,
		ObservationCount:  1880,
		EmbeddingMaxObsID: 1855,
		EmbeddingCount:    1855,
		NewestEmbeddedAt:  "2026-07-31 09:22:14",
		EmbeddingsDBPath:  "/tmp/embeddings.db",
	}, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Result != StatusWarning {
		t.Fatalf("lag must warn, got result=%q", res.Result)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d", len(res.Findings))
	}
	f := res.Findings[0]
	if f.ReasonCode != "embeddings_behind_observations" {
		t.Fatalf("reason_code: got %q", f.ReasonCode)
	}
	// The evidence must carry the magnitude and the freeze point — the two
	// facts that turn "something is off" into an actionable report.
	var ev map[string]any
	if err := json.Unmarshal(f.Evidence, &ev); err != nil {
		t.Fatalf("evidence is not JSON: %v", err)
	}
	if ev["behind_by"] != float64(25) {
		t.Fatalf("evidence must state how many observations are unembedded: got %v", ev["behind_by"])
	}
	if ev["newest_embedded_at"] != "2026-07-31 09:22:14" {
		t.Fatalf("evidence must state when embedding stopped: got %v", ev["newest_embedded_at"])
	}
	if !strings.Contains(f.SafeNextStep, "omnia embed") {
		t.Fatalf("next step must name the command that fixes it: got %q", f.SafeNextStep)
	}
}

func TestEmbeddingLagCheck_OKWhenCaughtUp(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(), lagScope(t, EmbeddingSnapshot{
		Enabled:           true,
		ObservationMaxID:  1880,
		ObservationCount:  1880,
		EmbeddingMaxObsID: 1880,
		EmbeddingCount:    1880,
		NewestEmbeddedAt:  "2026-08-03 02:15:00",
	}, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Result != StatusOK {
		t.Fatalf("a caught-up store must be OK, got %q", res.Result)
	}
}

// Embeddings turned off is a deliberate configuration, not a defect.
func TestEmbeddingLagCheck_OKWhenEmbeddingsDisabled(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(), lagScope(t, EmbeddingSnapshot{Enabled: false}, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Result != StatusOK {
		t.Fatalf("disabled embeddings must be OK, got %q", res.Result)
	}
}

// An embeddings store that cannot be opened is exactly the #228 shape: it must
// surface, not quietly pass.
func TestEmbeddingLagCheck_WarnsWhenSnapshotUnavailable(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(),
		lagScope(t, EmbeddingSnapshot{}, errors.New("file is not a database (26)")))
	if err != nil {
		t.Fatalf("Run must not fail the whole report: %v", err)
	}
	if res.Result != StatusWarning {
		t.Fatalf("an unreadable embeddings store must warn, got %q", res.Result)
	}
	if len(res.Findings) != 1 || res.Findings[0].ReasonCode != "embeddings_store_unreadable" {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
}

// A caller that never wired the seam abstains explicitly instead of claiming
// health: "we did not look" must be distinguishable from "we looked and it is
// fine". It is not a warning — a caller with no embeddings reader could never
// clear one.
func TestEmbeddingLagCheck_AbstainsExplicitlyWhenSeamNotWired(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Result != StatusOK {
		t.Fatalf("an unwired seam must not raise an unfixable warning, got %q", res.Result)
	}
	var ev map[string]any
	if err := json.Unmarshal(res.Evidence, &ev); err != nil {
		t.Fatalf("evidence is not JSON: %v", err)
	}
	if ev["checked"] != false {
		t.Fatalf("an unwired seam must record checked=false, not silent health: got %v", ev["checked"])
	}
}

// The wired-and-caught-up case must be distinguishable from the abstention
// above: it asserts real health, so checked must not be false.
func TestEmbeddingLagCheck_CaughtUpIsNotAnAbstention(t *testing.T) {
	res, err := EmbeddingLagCheck{}.Run(context.Background(), lagScope(t, EmbeddingSnapshot{
		Enabled: true, ObservationMaxID: 10, ObservationCount: 10,
		EmbeddingMaxObsID: 10, EmbeddingCount: 10,
	}, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(res.Evidence, &ev); err != nil {
		t.Fatalf("evidence is not JSON: %v", err)
	}
	if _, abstained := ev["checked"]; abstained {
		t.Fatalf("a real check must not look like an abstention: %v", ev)
	}
}

func TestEmbeddingLagCheck_IsRegistered(t *testing.T) {
	if _, err := DefaultRegistry().Lookup(CheckEmbeddingLag); err != nil {
		t.Fatalf("embedding_lag must be in the default registry: %v", err)
	}
}
