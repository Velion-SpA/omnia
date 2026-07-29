package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const revisionOpUpdate, revisionOpSoftDelete = "update", "soft_delete"

func nextRevisionTimestamp(previous string) string {
	now := time.Now().UTC()
	if parsed, err := parseObservationTime(previous); err == nil && !now.After(parsed) {
		now = parsed.Add(time.Nanosecond)
	}
	return now.Format(time.RFC3339Nano)
}

func (s *Store) captureObservationRevisionTx(tx *sql.Tx, obs *Observation, op, mutationAt string) error {
	if !s.cfg.TimeTravelEnabled || obs == nil || strings.TrimSpace(obs.SyncID) == "" {
		return nil
	}
	snapshot, err := json.Marshal(obs)
	if err != nil {
		return fmt.Errorf("marshal observation revision: %w", err)
	}
	validFrom := obs.UpdatedAt
	if validFrom == "" {
		validFrom = obs.CreatedAt
	}
	if _, err := s.execHook(tx, `
		INSERT INTO observation_revisions (obs_sync_id, op, valid_from, valid_to, snapshot)
		VALUES (?, ?, ?, ?, ?)`,
		obs.SyncID, op, validFrom, mutationAt, string(snapshot),
	); err != nil {
		return fmt.Errorf("capture observation revision: %w", err)
	}
	if s.cfg.HistoryRevisionCap <= 0 {
		return nil
	}
	if _, err := s.execHook(tx, `
		DELETE FROM observation_revisions
		WHERE id IN (
			SELECT id FROM observation_revisions
			WHERE obs_sync_id = ?
			ORDER BY id DESC LIMIT -1 OFFSET ?
		)`, obs.SyncID, s.cfg.HistoryRevisionCap); err != nil {
		return fmt.Errorf("prune observation revisions: %w", err)
	}
	return nil
}

func (s *Store) purgeObservationRevisionsTx(tx *sql.Tx, syncID string) error {
	if strings.TrimSpace(syncID) == "" {
		return nil
	}
	if _, err := s.execHook(tx, `DELETE FROM observation_revisions WHERE obs_sync_id = ?`, syncID); err != nil {
		return fmt.Errorf("purge observation revisions: %w", err)
	}
	return nil
}
