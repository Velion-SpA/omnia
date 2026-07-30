package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const portableSchemaVersion = 2

type ExportCounts struct {
	Sessions     int `json:"sessions"`
	Observations int `json:"observations"`
	Prompts      int `json:"prompts"`
	Relations    int `json:"relations"`
	Anchors      int `json:"anchors"`
	Procedures   int `json:"procedures"`
	// DuplicatesCollapsed counts observation/prompt rows discarded by
	// canonicalPortable because they shared a sync_id with a higher-ranked
	// row (the schema allows duplicate sync_id for observations/user_prompts
	// — no UNIQUE constraint). It is a diagnostic only: it cannot be
	// reconstructed from a decoded export (the duplicates are already gone),
	// so decode/import count-mismatch checks carry it through unchecked
	// rather than recomputing it.
	DuplicatesCollapsed int `json:"duplicates_collapsed,omitempty"`
}
type PortableRow map[string]any
type portableWire struct {
	SchemaVersion int           `json:"schema_version"`
	Version       string        `json:"version"`
	ExportedAt    string        `json:"exported_at"`
	Counts        ExportCounts  `json:"counts"`
	Checksum      string        `json:"checksum"`
	Sessions      []Session     `json:"sessions"`
	Observations  []PortableRow `json:"observations"`
	Prompts       []PortableRow `json:"prompts"`
	Relations     []PortableRow `json:"relations"`
	Anchors       []PortableRow `json:"anchors"`
	Procedures    []PortableRow `json:"procedures"`
}
type portablePayload struct {
	Sessions     []Session
	Observations []PortableRow
	Prompts      []PortableRow
	Relations    []PortableRow
	Anchors      []PortableRow
	Procedures   []PortableRow
}

func nonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
func portableObservations(in []Observation) []PortableRow {
	out := make([]PortableRow, len(in))
	for i, observation := range in {
		out[i] = portableRecord(observation)
		out[i]["pinned"] = observation.Pinned
	}
	return out
}
func portablePrompts(in []Prompt) []PortableRow {
	out := make([]PortableRow, len(in))
	for i, prompt := range in {
		out[i] = portableRecord(prompt)
	}
	return out
}

// canonicalPortable collapses rows sharing the same key (sync_id) down to
// the single highest-ranked row, and reports how many rows were discarded
// as duplicates so callers can surface that count as a diagnostic instead
// of silently dropping data with no signal.
func canonicalPortable[T any](in []T, key, timestamp func(T) string, record func(T) PortableRow) ([]T, int) {
	chosen, rank := map[string]T{}, map[string]string{}
	for _, value := range in {
		raw, _ := json.Marshal(record(value))
		k, candidate := key(value), normalizeComparableTimestamp(timestamp(value))+"\x00"+string(raw)
		if previous, ok := rank[k]; !ok || candidate > previous {
			chosen[k], rank[k] = value, candidate
		}
	}
	out := make([]T, 0, len(chosen))
	for _, value := range chosen {
		out = append(out, value)
	}
	return out, len(in) - len(out)
}
func portableRecord(value any) PortableRow {
	raw, _ := json.Marshal(value)
	row := PortableRow{}
	_ = json.Unmarshal(raw, &row)
	delete(row, "id")
	return row
}
func portablePayloadFor(data *ExportData) portablePayload {
	return portablePayload{
		nonNil(data.Sessions), nonNil(portableObservations(data.Observations)), nonNil(portablePrompts(data.Prompts)),
		nonNil(data.Relations), nonNil(data.Anchors), nonNil(data.Procedures),
	}
}
func portableChecksum(data *ExportData) (string, error) {
	raw, err := json.Marshal(portablePayloadFor(data))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum), nil
}
func (data *ExportData) MarshalJSON() ([]byte, error) {
	if data.SchemaVersion == 0 {
		return json.Marshal(struct {
			Version      string        `json:"version"`
			ExportedAt   string        `json:"exported_at"`
			Sessions     []Session     `json:"sessions"`
			Observations []Observation `json:"observations"`
			Prompts      []Prompt      `json:"prompts"`
		}{data.Version, data.ExportedAt, data.Sessions, data.Observations, data.Prompts})
	}
	checksum, err := portableChecksum(data)
	if err != nil {
		return nil, fmt.Errorf("portable export checksum: %w", err)
	}
	return json.Marshal(portableWire{
		data.SchemaVersion, data.Version, data.ExportedAt, data.Counts, checksum,
		nonNil(data.Sessions), nonNil(portableObservations(data.Observations)), nonNil(portablePrompts(data.Prompts)),
		nonNil(data.Relations), nonNil(data.Anchors), nonNil(data.Procedures),
	})
}
func DecodeExportData(raw []byte) (*ExportData, error) {
	var wire portableWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	if wire.SchemaVersion != 0 && wire.SchemaVersion != portableSchemaVersion {
		return nil, fmt.Errorf("unsupported schema version %d (supported: 0, %d)", wire.SchemaVersion, portableSchemaVersion)
	}
	observations := make([]Observation, len(wire.Observations))
	for i, row := range wire.Observations {
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &observations[i]); err != nil {
			return nil, fmt.Errorf("malformed observation: %w", err)
		}
		observations[i].Pinned, _ = row["pinned"].(bool)
	}
	prompts := make([]Prompt, len(wire.Prompts))
	for i, row := range wire.Prompts {
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &prompts[i]); err != nil {
			return nil, fmt.Errorf("malformed prompt: %w", err)
		}
	}
	data := &ExportData{wire.SchemaVersion, wire.Version, wire.ExportedAt, wire.Counts, wire.Checksum,
		wire.Sessions, observations, prompts, wire.Relations, wire.Anchors, wire.Procedures}
	if wire.SchemaVersion == 0 {
		return data, nil
	}
	if wire.Sessions == nil || wire.Observations == nil || wire.Prompts == nil ||
		wire.Relations == nil || wire.Anchors == nil || wire.Procedures == nil {
		return nil, fmt.Errorf("malformed portable export: every collection must be an array")
	}
	// DuplicatesCollapsed cannot be recomputed here: canonicalization
	// already ran at export time and the duplicates are gone from the
	// wire payload, so it is carried through from wire.Counts unchecked
	// rather than compared like the other (reconstructible) counts.
	wantCounts := ExportCounts{
		Sessions: len(wire.Sessions), Observations: len(observations), Prompts: len(wire.Prompts),
		Relations: len(wire.Relations), Anchors: len(wire.Anchors), Procedures: len(wire.Procedures),
		DuplicatesCollapsed: wire.Counts.DuplicatesCollapsed,
	}
	if wire.Counts != wantCounts {
		return nil, fmt.Errorf("portable export count mismatch")
	}
	wantChecksum, err := portableChecksum(data)
	if err != nil {
		return nil, err
	}
	if wire.Checksum != wantChecksum {
		return nil, fmt.Errorf("portable export checksum mismatch")
	}
	return data, nil
}
func (s *Store) ExportPortable() (*ExportData, error) {
	var data *ExportData
	err := s.withTx(func(tx *sql.Tx) error {
		// Defensive backfill: an empty sync_id shouldn't happen given
		// migrate()'s startup backfill, but migrate() only runs once, at
		// New() — a future insert-path bug or direct DB tampering could
		// still leave a row with an empty sync_id before the next store
		// restart. Left alone, such a row exports fine with a valid
		// checksum and then fails the ENTIRE import elsewhere, at
		// disaster-recovery time. Reuse migrate()'s exact generator SQL
		// (see s.migrate()) here as a lightweight pre-export pass so a
		// well-formed export is always importable, and the bad row is
		// caught early instead.
		if _, err := s.execHook(tx, `UPDATE observations SET sync_id = 'obs-' || lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`); err != nil {
			return fmt.Errorf("export: backfill empty observation sync_id: %w", err)
		}
		if _, err := s.execHook(tx, `UPDATE user_prompts SET sync_id = 'prompt-' || lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`); err != nil {
			return fmt.Errorf("export: backfill empty prompt sync_id: %w", err)
		}
		var err error
		data, err = s.exportPortable(tx)
		return err
	})
	return data, err
}

func (s *Store) exportPortable(db queryer) (*ExportData, error) {
	data, err := s.exportWithProjectScopeFrom(db, "")
	if err != nil {
		return nil, err
	}
	for i := range data.Observations {
		data.Observations[i].SyncID = strings.Trim(data.Observations[i].SyncID, " ")
	}
	for i := range data.Prompts {
		data.Prompts[i].SyncID = strings.Trim(data.Prompts[i].SyncID, " ")
	}
	var obsCollapsed, promptCollapsed int
	data.Observations, obsCollapsed = canonicalPortable(data.Observations,
		func(value Observation) string { return value.SyncID },
		func(value Observation) string { return value.UpdatedAt },
		func(value Observation) PortableRow { return portableObservations([]Observation{value})[0] })
	data.Prompts, promptCollapsed = canonicalPortable(data.Prompts,
		func(value Prompt) string { return value.SyncID },
		func(value Prompt) string { return value.CreatedAt },
		func(value Prompt) PortableRow { return portableRecord(value) })
	sort.Slice(data.Sessions, func(i, j int) bool {
		return data.Sessions[i].StartedAt < data.Sessions[j].StartedAt || data.Sessions[i].StartedAt == data.Sessions[j].StartedAt && data.Sessions[i].ID < data.Sessions[j].ID
	})
	sort.Slice(data.Observations, func(i, j int) bool { return data.Observations[i].SyncID < data.Observations[j].SyncID })
	sort.Slice(data.Prompts, func(i, j int) bool { return data.Prompts[i].SyncID < data.Prompts[j].SyncID })
	if data.Relations, err = s.exportPortableRelations(db); err != nil {
		return nil, err
	}
	if data.Anchors, err = s.exportPortableAnchors(db); err != nil {
		return nil, err
	}
	if data.Procedures, err = s.exportPortableProcedures(db); err != nil {
		return nil, err
	}
	data.SchemaVersion = portableSchemaVersion
	data.ExportedAt, err = s.portableExportedAt(db)
	if err != nil {
		return nil, err
	}
	data.Counts = ExportCounts{
		Sessions: len(data.Sessions), Observations: len(data.Observations), Prompts: len(data.Prompts),
		Relations: len(data.Relations), Anchors: len(data.Anchors), Procedures: len(data.Procedures),
		DuplicatesCollapsed: obsCollapsed + promptCollapsed,
	}
	data.Checksum, err = portableChecksum(data)
	return data, err
}

func (s *Store) exportPortableRelations(db queryer) ([]PortableRow, error) {
	return s.exportPortableRows(db, `SELECT r.sync_id,r.source_id,r.target_id,r.relation,r.reason,r.evidence,r.confidence,r.judgment_status,
		r.marked_by_actor,r.marked_by_kind,r.marked_by_model,r.session_id,r.superseded_at,p.sync_id AS superseded_by_relation_sync_id,r.created_at,r.updated_at
		FROM memory_relations r LEFT JOIN memory_relations p ON p.id=r.superseded_by_relation_id ORDER BY r.created_at,r.sync_id`)
}

func (s *Store) exportPortableAnchors(db queryer) ([]PortableRow, error) {
	return s.exportPortableRows(db, `SELECT sync_id,obs_sync_id,repo_root,file_path,symbol,line_start,line_end,blame_sha,blame_at,
		content_hash,anchor_status,created_at,checked_at,staled_at,new_blame_sha FROM memory_anchors ORDER BY created_at,sync_id`)
}

func (s *Store) exportPortableProcedures(db queryer) ([]PortableRow, error) {
	out, err := s.exportPortableRows(db, `SELECT sync_id,project,scope,polarity,"trigger",steps,steps_summary,expected_outcome,postcondition_kind,postcondition_expr,
		confidence,state,reuse_confirmed,contradicted_count,source_obs_sync_ids,induced_by_actor,induced_by_kind,induced_by_model,
		created_at,updated_at,last_reused_at,review_after,retired_at FROM procedures ORDER BY created_at,sync_id`)
	if err != nil {
		return nil, err
	}
	for _, row := range out {
		for _, field := range []string{"steps", "source_obs_sync_ids"} {
			var decoded any
			value, ok := row[field].(string)
			if !ok || json.Unmarshal([]byte(value), &decoded) != nil {
				return nil, fmt.Errorf("export procedure %v: invalid %s JSON", row["sync_id"], field)
			}
			row[field] = decoded
		}
	}
	return out, nil
}

func (s *Store) exportPortableRows(db queryer, query string) ([]PortableRow, error) {
	rows, err := s.queryHook(db, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []PortableRow{}
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		row := PortableRow{}
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				value = string(bytes)
			}
			row[columns[i]] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) portableExportedAt(db queryer) (string, error) {
	var latest string
	rows, err := s.queryItHook(db, `SELECT COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',max(datetime(ts))),'1970-01-01T00:00:00Z') FROM (
		SELECT started_at ts FROM sessions UNION ALL SELECT ended_at FROM sessions
		UNION ALL SELECT updated_at FROM observations UNION ALL SELECT created_at FROM user_prompts
		UNION ALL SELECT updated_at FROM memory_relations UNION ALL SELECT created_at FROM memory_anchors
		UNION ALL SELECT updated_at FROM procedures
	)`)
	if err != nil {
		return "", fmt.Errorf("export timestamp: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return "", fmt.Errorf("export timestamp: missing row")
	}
	if err := rows.Scan(&latest); err != nil {
		return "", err
	}
	return latest, rows.Err()
}
