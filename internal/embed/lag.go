package embed

import (
	"context"
	"database/sql"
	"fmt"
)

// LagSnapshot is the embeddings store's own watermark: how many vectors it
// holds, the highest observation id any of them covers, and when it was last
// written.
//
// It exists because #226's failure mode was invisible at every layer a caller
// would check: embeddings.db froze on Jul 31 while observations kept arriving,
// and semantic search went on answering — blind to everything created since,
// with no signal that it had. Comparing MaxObsID against the observation
// store's own watermark (store.ObservationWatermark) turns that into a
// decisive, cheap check.
//
// MaxObsID is preferred over NewestEmbeddedAt for the comparison: ids are
// assigned by the same monotonic sequence the observation store uses, so they
// need no clock agreement between the two databases.
type LagSnapshot struct {
	// Count is the number of stored vectors.
	Count int
	// MaxObsID is the highest observation id covered by a stored vector, or 0
	// when the store is empty.
	MaxObsID int
	// NewestEmbeddedAt is the most recent embedded_at timestamp, or "" when the
	// store is empty. Human-facing only ("frozen since ..."), never the
	// comparison key.
	NewestEmbeddedAt string
}

// Lag reports the store's watermark. Both aggregates are index-backed
// (idx_emb_obs on obs_id, primary key scan for the rest), so this is cheap
// enough to call on a request path, not just from a diagnostic.
func (s *Store) Lag(ctx context.Context) (LagSnapshot, error) {
	var (
		snap     LagSnapshot
		maxObsID sql.NullInt64
		newest   sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(obs_id), MAX(embedded_at) FROM embeddings`,
	).Scan(&snap.Count, &maxObsID, &newest)
	if err != nil {
		return LagSnapshot{}, fmt.Errorf("embed: Lag: %w", err)
	}
	if maxObsID.Valid {
		snap.MaxObsID = int(maxObsID.Int64)
	}
	if newest.Valid {
		snap.NewestEmbeddedAt = newest.String
	}
	return snap, nil
}
