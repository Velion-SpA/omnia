package store

import (
	"database/sql"
	"fmt"
	"strings"
)

func (s *Store) Import(data *ExportData) (*ImportResult, error) {
	if data == nil {
		return nil, fmt.Errorf("import: data is required")
	}
	switch data.SchemaVersion {
	case 0:
		return s.importLegacy(data)
	case portableSchemaVersion:
		if err := validatePortableImport(data); err != nil {
			return nil, err
		}
		return s.importPortableCore(data)
	default:
		return nil, fmt.Errorf("unsupported schema version %d (supported: 0, %d)", data.SchemaVersion, portableSchemaVersion)
	}
}

func validatePortableImport(data *ExportData) error {
	want := ExportCounts{len(data.Sessions), len(data.Observations), len(data.Prompts), len(data.Relations), len(data.Anchors), len(data.Procedures)}
	if data.Counts != want {
		return fmt.Errorf("portable export count mismatch")
	}
	checksum, err := portableChecksum(data)
	if err != nil {
		return fmt.Errorf("portable export checksum: %w", err)
	}
	if data.Checksum != checksum {
		return fmt.Errorf("portable export checksum mismatch")
	}
	return validatePortableCoreKeys(data)
}

func validatePortableCoreKeys(data *ExportData) error {
	seen := map[string]bool{}
	for _, session := range data.Sessions {
		key := session.ID
		if strings.TrimSpace(key) == "" || seen["session:"+key] {
			return fmt.Errorf("portable export invalid or duplicate session id %q", session.ID)
		}
		seen["session:"+key] = true
	}
	for _, observation := range data.Observations {
		key := strings.Trim(observation.SyncID, " ")
		if key == "" || seen["observation:"+key] {
			return fmt.Errorf("portable export invalid or duplicate observation sync_id %q", observation.SyncID)
		}
		seen["observation:"+key] = true
	}
	for _, prompt := range data.Prompts {
		key := strings.Trim(prompt.SyncID, " ")
		if key == "" || seen["prompt:"+key] {
			return fmt.Errorf("portable export invalid or duplicate prompt sync_id %q", prompt.SyncID)
		}
		seen["prompt:"+key] = true
	}
	return nil
}

func (s *Store) importPortableCore(data *ExportData) (*ImportResult, error) {
	tx, err := s.beginTxHook()
	if err != nil {
		return nil, fmt.Errorf("import: begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := rejectPortableTombstones(tx, data); err != nil {
		return nil, err
	}

	result := &ImportResult{}
	for _, session := range data.Sessions {
		if _, err := s.execHook(tx, `INSERT INTO sessions (id,project,directory,started_at,ended_at,summary)
			VALUES (?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET project=excluded.project,directory=excluded.directory,
			started_at=excluded.started_at,ended_at=excluded.ended_at,summary=excluded.summary`,
			session.ID, session.Project, session.Directory, session.StartedAt, session.EndedAt, session.Summary); err != nil {
			return nil, fmt.Errorf("import session %s: %w", session.ID, err)
		}
		result.SessionsImported++
	}
	for _, observation := range data.Observations {
		if err := s.upsertPortableObservation(tx, observation); err != nil {
			return nil, fmt.Errorf("import observation %s: %w", observation.SyncID, err)
		}
		result.ObservationsImported++
	}
	for _, prompt := range data.Prompts {
		if err := s.upsertPortablePrompt(tx, prompt); err != nil {
			return nil, fmt.Errorf("import prompt %s: %w", prompt.SyncID, err)
		}
		result.PromptsImported++
	}
	if err := s.commitHook(tx); err != nil {
		return nil, fmt.Errorf("import: commit: %w", err)
	}
	return result, nil
}

func rejectPortableTombstones(tx *sql.Tx, data *ExportData) error {
	for _, observation := range data.Observations {
		if err := rejectPortableTombstone(tx, "deletion_tombstones", "observation", observation.SyncID); err != nil {
			return err
		}
	}
	for _, prompt := range data.Prompts {
		if err := rejectPortableTombstone(tx, "prompt_tombstones", "prompt", prompt.SyncID); err != nil {
			return err
		}
	}
	return nil
}

func rejectPortableTombstone(tx *sql.Tx, table, entity, syncID string) error {
	var found int
	err := tx.QueryRow(`SELECT 1 FROM `+table+` WHERE trim(sync_id)=?`, strings.Trim(syncID, " ")).Scan(&found)
	if err == nil {
		return fmt.Errorf("portable import rejected: %s %q is tombstoned", entity, syncID)
	}
	if err == sql.ErrNoRows {
		return nil
	}
	return fmt.Errorf("portable import tombstone preflight: %w", err)
}

func (s *Store) upsertPortableObservation(tx *sql.Tx, observation Observation) error {
	key, id := strings.Trim(observation.SyncID, " "), int64(0)
	err := tx.QueryRow(`SELECT id FROM observations WHERE trim(sync_id)=? ORDER BY id LIMIT 1`, key).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	args := []any{
		observation.SessionID, observation.Type, observation.Title, observation.Content, observation.ToolName,
		observation.Project, normalizeScope(observation.Scope), observation.TopicKey, hashNormalized(observation.Content),
		maxInt(observation.RevisionCount, 1), maxInt(observation.DuplicateCount, 1), observation.LastSeenAt,
		observation.ReviewAfter, observation.Pinned, observation.CreatedAt, observation.UpdatedAt, observation.DeletedAt,
		observation.ErrorSignature, observation.Outcome, observation.Source, observation.TrustTag,
	}
	if err == sql.ErrNoRows {
		_, err = s.execHook(tx, `INSERT INTO observations
			(session_id,type,title,content,tool_name,project,scope,topic_key,normalized_hash,revision_count,duplicate_count,
			 last_seen_at,review_after,pinned,created_at,updated_at,deleted_at,error_signature,outcome,source,trust_tag,sync_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, key)...)
		return err
	}
	if _, err = s.execHook(tx, `UPDATE observations SET
		session_id=?,type=?,title=?,content=?,tool_name=?,project=?,scope=?,topic_key=?,normalized_hash=?,revision_count=?,
		duplicate_count=?,last_seen_at=?,review_after=?,pinned=?,created_at=?,updated_at=?,deleted_at=?,error_signature=?,
		outcome=?,source=?,trust_tag=?,sync_id=? WHERE id=?`, append(args, key, id)...); err != nil {
		return err
	}
	_, err = s.execHook(tx, `DELETE FROM observations WHERE trim(sync_id)=? AND id<>?`, key, id)
	return err
}

func (s *Store) upsertPortablePrompt(tx *sql.Tx, prompt Prompt) error {
	key, id := strings.Trim(prompt.SyncID, " "), int64(0)
	err := tx.QueryRow(`SELECT id FROM user_prompts WHERE trim(sync_id)=? ORDER BY id LIMIT 1`, key).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		_, err = s.execHook(tx, `INSERT INTO user_prompts (sync_id,session_id,content,project,created_at) VALUES (?,?,?,?,?)`,
			key, prompt.SessionID, prompt.Content, prompt.Project, prompt.CreatedAt)
		return err
	}
	if _, err = s.execHook(tx, `UPDATE user_prompts SET session_id=?,content=?,project=?,created_at=?,sync_id=? WHERE id=?`,
		prompt.SessionID, prompt.Content, prompt.Project, prompt.CreatedAt, key, id); err != nil {
		return err
	}
	_, err = s.execHook(tx, `DELETE FROM user_prompts WHERE trim(sync_id)=? AND id<>?`, key, id)
	return err
}
