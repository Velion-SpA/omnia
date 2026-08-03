package store

import (
	"database/sql"
	"fmt"
)

// ObservationWatermark returns the highest LIVE observation id and the live
// observation count.
//
// It is the observation-store half of #226's staleness check: compared against
// embed.Store.Lag's MaxObsID, it answers "are there memories newer than the
// newest embedding?" — the cheap, decisive signal that was missing when
// embeddings.db silently froze while observations kept arriving and semantic
// search went on answering as if nothing had happened.
//
// Soft-deleted rows are excluded from BOTH values. An embedding job
// legitimately never embeds them, so counting them would report permanent
// phantom lag that no amount of embedding could clear.
func (s *Store) ObservationWatermark() (maxID int64, count int, err error) {
	var maxLive sql.NullInt64
	err = s.db.QueryRow(
		`SELECT MAX(id), COUNT(*) FROM observations WHERE deleted_at IS NULL`,
	).Scan(&maxLive, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("engram: ObservationWatermark: %w", err)
	}
	if maxLive.Valid {
		maxID = maxLive.Int64
	}
	return maxID, count, nil
}
