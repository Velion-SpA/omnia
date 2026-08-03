package diagnostic

import (
	"context"
	"fmt"
)

// CheckEmbeddingLag is the code for the embeddings-behind-observations check.
const CheckEmbeddingLag = "embedding_lag"

// EmbeddingSnapshot pairs the two watermarks the lag check compares:
// store.ObservationWatermark on one side, embed.Store.Lag on the other.
//
// internal/diagnostic deliberately does not import internal/embed. The
// snapshot is supplied through Scope.ReadEmbeddingSnapshot, the same
// injected-reader seam Scope.ReadSQLiteLockSnapshot already uses, so this
// package keeps its single store dependency.
type EmbeddingSnapshot struct {
	// Enabled is false when the operator has embeddings turned off — a
	// deliberate configuration, never a defect.
	Enabled bool
	// ObservationMaxID / ObservationCount come from store.ObservationWatermark
	// (live rows only).
	ObservationMaxID int64
	ObservationCount int
	// EmbeddingMaxObsID / EmbeddingCount / NewestEmbeddedAt come from
	// embed.Store.Lag.
	EmbeddingMaxObsID int
	EmbeddingCount    int
	NewestEmbeddedAt  string
	// EmbeddingsDBPath is reported as evidence so the operator can inspect the
	// file the numbers came from.
	EmbeddingsDBPath string
}

// EmbeddingLagCheck reports when the embeddings store has fallen behind the
// observation store.
//
// #226: embeddings.db stopped being written minutes after at-rest encryption
// was enabled (the read-path bug fixed in #228), and NOTHING surfaced it.
// Search kept answering, so every layer a user or an automated caller would
// inspect looked healthy — the results were simply blind to every memory
// created since. Comparing the two watermarks is cheap and decisive, and would
// have caught it the same day.
type EmbeddingLagCheck struct{}

func (EmbeddingLagCheck) Code() string { return CheckEmbeddingLag }

func (c EmbeddingLagCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	// An unwired seam abstains EXPLICITLY rather than claiming health: the
	// evidence carries checked=false, so a reader can tell "we looked and it
	// is fine" from "we did not look". It is not a warning, because a caller
	// that cannot supply an embeddings reader (internal/server's doctor
	// endpoint has none) could never clear it — a permanent unfixable warning
	// is noise, and noise is what trains people to ignore reports.
	if scope.ReadEmbeddingSnapshot == nil {
		return okResult(c.Code(), map[string]any{
			"checked": false,
			"reason":  "this caller did not provide an embeddings reader; run `omnia doctor` from the CLI to check embedding lag",
		}), nil
	}

	snap, err := scope.ReadEmbeddingSnapshot(ctx)
	if err != nil {
		return resultFromFindings(c.Code(), nil, []Finding{{
			CheckID:      c.Code(),
			Severity:     SeverityWarning,
			ReasonCode:   "embeddings_store_unreadable",
			Message:      "The embeddings store could not be read.",
			Why:          "Semantic recall silently degrades to lexical matching when the embeddings store is unavailable, and callers cannot tell the difference from the results alone.",
			Evidence:     mustJSON(map[string]any{"error": err.Error()}),
			SafeNextStep: "Run `omnia embed` and read the error it reports.",
		}}), nil
	}

	if !snap.Enabled {
		return okResult(c.Code(), map[string]any{"embeddings_enabled": false}), nil
	}

	evidence := map[string]any{
		"embeddings_enabled":   true,
		"observation_max_id":   snap.ObservationMaxID,
		"observation_count":    snap.ObservationCount,
		"embedding_max_obs_id": snap.EmbeddingMaxObsID,
		"embedding_count":      snap.EmbeddingCount,
		"newest_embedded_at":   snap.NewestEmbeddedAt,
	}
	if snap.EmbeddingsDBPath != "" {
		evidence["embeddings_db_path"] = snap.EmbeddingsDBPath
	}

	// Compare ids, not timestamps: both sides draw from the same monotonic
	// observation sequence, so no clock agreement between the two databases is
	// required.
	if snap.ObservationMaxID <= int64(snap.EmbeddingMaxObsID) {
		return okResult(c.Code(), evidence), nil
	}

	behind := snap.ObservationCount - snap.EmbeddingCount
	if behind < 0 {
		behind = 0
	}
	evidence["behind_by"] = behind

	return resultFromFindings(c.Code(), nil, []Finding{{
		CheckID:    c.Code(),
		Severity:   SeverityWarning,
		ReasonCode: "embeddings_behind_observations",
		Message: fmt.Sprintf("Embeddings are behind the store: %d live observation(s) have no vector (newest embedded id %d, newest observation id %d).",
			behind, snap.EmbeddingMaxObsID, snap.ObservationMaxID),
		Why:          "Semantic search cannot reach an observation that was never embedded. It still returns results, so the degradation is invisible from the answers alone — recall silently narrows to whatever was embedded before the job stopped.",
		Evidence:     mustJSON(evidence),
		SafeNextStep: "Run `omnia embed` to reconcile, and read its error if it fails.",
	}}), nil
}
