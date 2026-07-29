package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const revisionOpUpdate, revisionOpSoftDelete = "update", "soft_delete"

// ReadInstant returns wall time for live reads or the normalized as_of instant.
func ReadInstant(asOf string) time.Time {
	if asOf == "" {
		return time.Now()
	}
	at, _ := parseObservationTime(asOf)
	return at
}

// NormalizeAsOf validates a requested recorded-time timestamp. A timestamp
// later than the current instant is normalized to the live-read sentinel so
// every caller can take the exact same path as an omitted as_of argument.
func NormalizeAsOf(timestamp string) (string, error) {
	timestamp = strings.TrimSpace(timestamp)
	if timestamp == "" {
		return "", nil
	}
	at, err := parseObservationTime(timestamp)
	if err != nil {
		return "", fmt.Errorf("invalid as_of timestamp: %w", err)
	}
	if at.After(time.Now().UTC()) {
		return "", nil
	}
	return timestamp, nil
}

func nextRevisionTimestamp(previous string) string {
	now := time.Now().UTC()
	if parsed, err := parseObservationTime(previous); err == nil && !now.After(parsed) {
		now = parsed.Add(time.Nanosecond)
	}
	return now.Format(time.RFC3339Nano)
}

func observationMutationTimestamp(previous, proposed string) string {
	proposed = strings.TrimSpace(proposed)
	if proposed == "" {
		return nextRevisionTimestamp(previous)
	}
	proposedAt, proposedErr := parseObservationTime(proposed)
	previousAt, previousErr := parseObservationTime(previous)
	if proposedErr == nil && (previousErr != nil || proposedAt.After(previousAt)) {
		return proposed
	}
	return nextRevisionTimestamp(previous)
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
	var previousBoundary string
	if err := tx.QueryRow(`
		SELECT ifnull(MAX(valid_to), '') FROM observation_revisions
		WHERE obs_sync_id = ?`, obs.SyncID,
	).Scan(&previousBoundary); err != nil {
		return fmt.Errorf("load observation revision boundary: %w", err)
	}
	if previousAt, previousErr := parseObservationTime(previousBoundary); previousErr == nil {
		if validFromAt, validFromErr := parseObservationTime(validFrom); validFromErr != nil || previousAt.After(validFromAt) {
			validFrom = previousBoundary
		}
	}
	if _, err := s.execHook(tx, `
		INSERT INTO observation_revisions (obs_sync_id, op, valid_from, valid_to, snapshot, pinned)
		VALUES (?, ?, ?, ?, ?, ?)`,
		obs.SyncID, op, validFrom, mutationAt, string(snapshot), obs.Pinned,
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

func (s *Store) getObservationIncludingDeleted(id int64) (*Observation, error) {
	var o Observation
	return &o, scanObservationRow(s.db.QueryRow(`SELECT `+observationSelectColumns+` FROM observations WHERE id = ?`, id), &o)
}
func (s *Store) StateAsOf(id int64, timestamp string) (*Observation, error) {
	if !s.cfg.TimeTravelEnabled || strings.TrimSpace(timestamp) == "" {
		return s.GetObservation(id)
	}
	at, err := parseObservationTime(timestamp)
	if err != nil {
		return nil, fmt.Errorf("invalid as_of timestamp: %w", err)
	}
	if at.After(time.Now().UTC()) {
		return s.GetObservation(id)
	}
	live, err := s.getObservationIncludingDeleted(id)
	if err != nil {
		return nil, err
	}
	var tombstoned int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ? AND hard = 1`, live.SyncID).Scan(&tombstoned); err != nil {
		return nil, err
	}
	if tombstoned > 0 {
		return nil, sql.ErrNoRows
	}
	historyStart, initialMaxObservationID, err := s.timeTravelBoundary()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT valid_from, valid_to, snapshot, pinned FROM observation_revisions
		WHERE obs_sync_id = ? ORDER BY valid_from, id`, live.SyncID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var earliest time.Time
	hasRevisions := false
	for rows.Next() {
		hasRevisions = true
		var fromRaw, toRaw, snapshot string
		var pinned bool
		if err := rows.Scan(&fromRaw, &toRaw, &snapshot, &pinned); err != nil {
			return nil, err
		}
		from, fromErr := parseObservationTime(fromRaw)
		to, toErr := parseObservationTime(toRaw)
		if fromErr != nil || toErr != nil {
			return nil, fmt.Errorf("invalid observation revision interval")
		}
		if earliest.IsZero() || from.Before(earliest) {
			earliest = from
		}
		if !at.Before(from) && at.Before(to) {
			if live.ID <= initialMaxObservationID && at.Before(historyStart) {
				return nil, historyUnavailableError(historyStart)
			}
			var historical Observation
			if err := json.Unmarshal([]byte(snapshot), &historical); err != nil {
				return nil, fmt.Errorf("decode observation revision: %w", err)
			}
			historical.Pinned = pinned
			normalizeTrustTag(&historical)
			return &historical, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	created, err := parseObservationTime(live.CreatedAt)
	if err != nil || at.Before(created) {
		return nil, sql.ErrNoRows
	}
	updated, err := parseObservationTime(live.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if live.DeletedAt != nil {
		deleted, parseErr := parseObservationTime(*live.DeletedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		if !at.Before(deleted) {
			return nil, sql.ErrNoRows
		}
	}
	if !at.Before(updated) {
		return live, nil
	}
	if !hasRevisions {
		return nil, historyUnavailableError(updated)
	}
	if earliest.IsZero() {
		earliest = updated
	}
	return nil, historyUnavailableError(earliest)
}

func (s *Store) timeTravelBoundary() (time.Time, int64, error) {
	var raw string
	var initialMaxObservationID int64
	if err := s.db.QueryRow(`
		SELECT started_at, initial_max_observation_id
		FROM time_travel_metadata WHERE id = 1`,
	).Scan(&raw, &initialMaxObservationID); err != nil {
		return time.Time{}, 0, err
	}
	startedAt, err := parseObservationTime(raw)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid time-travel recording boundary: %w", err)
	}
	return startedAt, initialMaxObservationID, nil
}

func historyUnavailableError(start time.Time) error {
	return fmt.Errorf("history unavailable before %s (history starts when time_travel is enabled and retained)", start.Format(time.RFC3339Nano))
}

func (s *Store) SearchResultsAsOf(results []SearchResult, timestamp string) ([]SearchResult, error) {
	if !s.cfg.TimeTravelEnabled || strings.TrimSpace(timestamp) == "" {
		return results, nil
	}
	resolved := make([]SearchResult, 0, len(results))
	for _, hit := range results {
		obs, err := s.StateAsOf(hit.ID, timestamp)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		hit.Observation = *obs
		resolved = append(resolved, hit)
	}
	return resolved, nil
}
func (s *Store) SearchAsOf(query string, opts SearchOptions, timestamp string) ([]SearchResult, error) {
	if !s.cfg.TimeTravelEnabled || strings.TrimSpace(timestamp) == "" {
		return s.Search(query, opts)
	}
	at, err := parseObservationTime(timestamp)
	if err != nil {
		return nil, fmt.Errorf("invalid as_of timestamp: %w", err)
	}
	if at.After(time.Now().UTC()) {
		return s.Search(query, opts)
	}
	historyStart, _, err := s.timeTravelBoundary()
	if err != nil {
		return nil, err
	}
	project, _ := NormalizeProject(opts.Project)
	var candidateLimit int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&candidateLimit); err != nil {
		return nil, err
	}
	candidateOpts := SearchOptions{Limit: candidateLimit, Diag: opts.Diag, IncludeDeleted: true}
	candidateOpts.postFilter = func(candidates []SearchResult) ([]SearchResult, error) {
		resolved, err := s.SearchResultsAsOf(candidates, timestamp)
		if err != nil {
			return nil, err
		}
		filtered := resolved[:0]
		for _, hit := range resolved {
			hitProject, _ := NormalizeProject(derefString(hit.Project))
			if (opts.Type == "" || hit.Type == opts.Type) &&
				(project == "" || hitProject == project) &&
				(opts.Scope == "" || hit.Scope == normalizeScope(opts.Scope)) {
				filtered = append(filtered, hit)
			}
		}
		return filtered, nil
	}
	filtered, err := s.Search(query, candidateOpts)
	if err != nil {
		return nil, err
	}
	if len(filtered) == 0 && at.Before(historyStart) {
		return nil, historyUnavailableError(historyStart)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > s.cfg.MaxSearchResults {
		limit = s.cfg.MaxSearchResults
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}
