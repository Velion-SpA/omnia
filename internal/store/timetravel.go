package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const revisionOpUpdate, revisionOpSoftDelete = "update", "soft_delete"

// asOfClockSkewTolerance absorbs minor clock skew between the machine that
// produced an --as-of timestamp and this server's own wall clock. Without
// it, a server whose clock runs even slightly behind true time would treat
// a genuinely-past --as-of request as "the future" (because the request's
// timestamp ends up numerically ahead of this server's lagging time.Now())
// and silently resolve it to live/current data instead of the requested
// recorded-time state. This is defensive slack, not a feature: it is
// intentionally small and not configurable.
const asOfClockSkewTolerance = 5 * time.Second

// asOfIsFuture reports whether at is far enough beyond the current instant
// (accounting for asOfClockSkewTolerance) to be treated as a future --as-of
// request, which resolves to live/current state. Every "is this timestamp
// in the future" comparison across the time-travel read paths must go
// through this single function so the tolerance is applied consistently.
func asOfIsFuture(at time.Time) bool {
	return at.After(time.Now().UTC().Add(asOfClockSkewTolerance))
}

var ErrBisectEventTombstoned = errors.New("bisect event tombstoned")
var ErrBisectStateStale = errors.New("bisect state stale")

type BisectEvent struct {
	ID        int64  `json:"id"`
	SyncID    string `json:"sync_id"`
	CreatedAt string `json:"created_at"`
}

type bisectPoint struct {
	at time.Time
	id int64
}

func compareBisectPoints(left, right bisectPoint) int {
	if left.at.Before(right.at) {
		return -1
	}
	if left.at.After(right.at) {
		return 1
	}
	if left.id < right.id {
		return -1
	}
	if left.id > right.id {
		return 1
	}
	return 0
}

func bisectBoundAvailable(point bisectPoint, observationID bool, boundary bisectPoint) bool {
	if !observationID {
		return !point.at.Before(boundary.at)
	}
	if point.id <= boundary.id {
		return compareBisectPoints(point, boundary) == 0
	}
	if !point.at.Before(boundary.at) {
		return true
	}
	return point.at.Equal(boundary.at.Truncate(time.Second))
}

func bisectEventPoint(event BisectEvent) (bisectPoint, error) {
	if event.ID <= 0 || strings.TrimSpace(event.SyncID) == "" {
		return bisectPoint{}, errors.New("invalid bisect event identity")
	}
	at, err := parseObservationTime(event.CreatedAt)
	if err != nil {
		return bisectPoint{}, err
	}
	return bisectPoint{at: at, id: event.ID}, nil
}

func ValidateBisectEvents(events []BisectEvent) error {
	var previous bisectPoint
	for i, event := range events {
		point, err := bisectEventPoint(event)
		if err != nil {
			return fmt.Errorf("invalid bisect event %d: %w", i, err)
		}
		if i > 0 && compareBisectPoints(previous, point) >= 0 {
			return errors.New("bisect candidates are not strictly ordered")
		}
		previous = point
	}
	return nil
}

func (s *Store) BisectEvents(goodRef, badRef string) ([]BisectEvent, error) {
	if !s.cfg.TimeTravelEnabled {
		return nil, errors.New("time travel is disabled")
	}
	resolve := func(ref string) (bisectPoint, bool, error) {
		if strings.EqualFold(strings.TrimSpace(ref), "now") {
			return bisectPoint{at: time.Now().UTC(), id: int64(1<<63 - 1)}, false, nil
		}
		if id, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64); err == nil && id > 0 {
			var raw string
			if err := s.db.QueryRow(`SELECT created_at FROM observations WHERE id=?`, id).Scan(&raw); err != nil {
				return bisectPoint{}, false, fmt.Errorf("resolve observation bound %d: %w", id, err)
			}
			at, err := parseObservationTime(raw)
			return bisectPoint{at: at, id: id}, true, err
		}
		at, err := parseObservationTime(ref)
		return bisectPoint{at: at, id: int64(1<<63 - 1)}, false, err
	}
	good, goodIsObservation, err := resolve(goodRef)
	if err != nil {
		return nil, fmt.Errorf("invalid good bound: %w", err)
	}
	bad, badIsObservation, err := resolve(badRef)
	if err != nil {
		return nil, fmt.Errorf("invalid bad bound: %w", err)
	}
	boundaryAt, boundaryID, err := s.timeTravelBoundary()
	if err != nil {
		return nil, err
	}
	boundary := bisectPoint{at: boundaryAt, id: boundaryID}
	if !bisectBoundAvailable(good, goodIsObservation, boundary) ||
		!bisectBoundAvailable(bad, badIsObservation, boundary) {
		return nil, historyUnavailableError(boundaryAt)
	}
	if compareBisectPoints(good, bad) >= 0 {
		return nil, errors.New("good bound must precede bad bound")
	}
	rows, err := s.db.Query(
		`SELECT id, ifnull(sync_id,''), created_at FROM observations WHERE id > ?`,
		boundaryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []BisectEvent
	for rows.Next() {
		var event BisectEvent
		if err := rows.Scan(&event.ID, &event.SyncID, &event.CreatedAt); err != nil {
			return nil, err
		}
		point, parseErr := bisectEventPoint(event)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid recorded event instant for observation %d: %w", event.ID, parseErr)
		}
		if !bisectBoundAvailable(point, true, boundary) {
			continue
		}
		if compareBisectPoints(point, good) > 0 && compareBisectPoints(point, bad) <= 0 {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		left, _ := bisectEventPoint(events[i])
		right, _ := bisectEventPoint(events[j])
		return compareBisectPoints(left, right) < 0
	})
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, ValidateBisectEvents(events)
}

func (s *Store) BisectEventState(event BisectEvent) (*Observation, error) {
	var tombstoned int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id=? AND hard=1`, event.SyncID).Scan(&tombstoned); err != nil {
		return nil, err
	}
	if tombstoned > 0 {
		return nil, ErrBisectEventTombstoned
	}
	var createdAt string
	if err := s.db.QueryRow(`SELECT created_at FROM observations WHERE id=? AND sync_id=?`, event.ID, event.SyncID).Scan(&createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrBisectStateStale
		}
		return nil, err
	}
	if createdAt != event.CreatedAt {
		return nil, ErrBisectStateStale
	}
	obs, err := s.StateAsOf(event.ID, createdAt)
	if err == sql.ErrNoRows {
		err = ErrBisectStateStale
	}
	return obs, err
}

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
	if asOfIsFuture(at) {
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
	if asOfIsFuture(at) {
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
	if asOfIsFuture(at) {
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
