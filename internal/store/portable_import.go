package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidPortableExport = errors.New("invalid portable export")

type portableRelation struct {
	SyncID                     string   `json:"sync_id"`
	SourceID                   string   `json:"source_id"`
	TargetID                   string   `json:"target_id"`
	Relation                   string   `json:"relation"`
	Reason                     *string  `json:"reason"`
	Evidence                   *string  `json:"evidence"`
	Confidence                 *float64 `json:"confidence"`
	JudgmentStatus             string   `json:"judgment_status"`
	MarkedByActor              *string  `json:"marked_by_actor"`
	MarkedByKind               *string  `json:"marked_by_kind"`
	MarkedByModel              *string  `json:"marked_by_model"`
	SessionID                  *string  `json:"session_id"`
	SupersededAt               *string  `json:"superseded_at"`
	SupersededByRelationSyncID *string  `json:"superseded_by_relation_sync_id"`
	CreatedAt                  string   `json:"created_at"`
	UpdatedAt                  string   `json:"updated_at"`
}

type portableAnchor struct {
	SyncID       string  `json:"sync_id"`
	ObsSyncID    string  `json:"obs_sync_id"`
	RepoRoot     *string `json:"repo_root"`
	FilePath     string  `json:"file_path"`
	Symbol       *string `json:"symbol"`
	LineStart    int64   `json:"line_start"`
	LineEnd      int64   `json:"line_end"`
	BlameSHA     *string `json:"blame_sha"`
	BlameAt      *string `json:"blame_at"`
	ContentHash  string  `json:"content_hash"`
	AnchorStatus string  `json:"anchor_status"`
	CreatedAt    string  `json:"created_at"`
	CheckedAt    *string `json:"checked_at"`
	StaledAt     *string `json:"staled_at"`
	NewBlameSHA  *string `json:"new_blame_sha"`
}

type portableProcedure struct {
	SyncID            string          `json:"sync_id"`
	Project           *string         `json:"project"`
	Scope             string          `json:"scope"`
	Polarity          string          `json:"polarity"`
	Trigger           string          `json:"trigger"`
	Steps             []ProcedureStep `json:"steps"`
	StepsSummary      string          `json:"steps_summary"`
	ExpectedOutcome   *string         `json:"expected_outcome"`
	PostconditionKind string          `json:"postcondition_kind"`
	PostconditionExpr *string         `json:"postcondition_expr"`
	Confidence        float64         `json:"confidence"`
	State             string          `json:"state"`
	ReuseConfirmed    int             `json:"reuse_confirmed"`
	ContradictedCount int             `json:"contradicted_count"`
	SourceObsSyncIDs  []string        `json:"source_obs_sync_ids"`
	InducedByActor    *string         `json:"induced_by_actor"`
	InducedByKind     *string         `json:"induced_by_kind"`
	InducedByModel    *string         `json:"induced_by_model"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	LastReusedAt      *string         `json:"last_reused_at"`
	ReviewAfter       *string         `json:"review_after"`
	RetiredAt         *string         `json:"retired_at"`
}

func (s *Store) Import(data *ExportData) (*ImportResult, error) {
	if data == nil {
		return nil, fmt.Errorf("import: data is required")
	}
	switch data.SchemaVersion {
	case 0:
		return s.importLegacy(data)
	case portableSchemaVersion:
		if err := validatePortableImport(data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPortableExport, err)
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
	if err := validatePortableCoreKeys(data); err != nil {
		return err
	}
	if _, _, err = decodePortableGraph(data); err != nil {
		return err
	}
	_, err = decodePortableProcedures(data.Procedures)
	return err
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

func decodePortableGraph(data *ExportData) ([]portableRelation, []portableAnchor, error) {
	relations := make([]portableRelation, len(data.Relations))
	anchors := make([]portableAnchor, len(data.Anchors))
	seen := map[string]bool{}
	for i, row := range data.Relations {
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &relations[i]); err != nil {
			return nil, nil, fmt.Errorf("portable import decode relation %d: %w", i, err)
		}
		r := &relations[i]
		r.SyncID, r.SourceID, r.TargetID = strings.Trim(r.SyncID, " "), strings.Trim(r.SourceID, " "), strings.Trim(r.TargetID, " ")
		if r.SyncID == "" || r.SourceID == "" || r.TargetID == "" || seen["relation:"+r.SyncID] {
			return nil, nil, fmt.Errorf("portable export invalid or duplicate relation sync_id/endpoints %q", r.SyncID)
		}
		if r.Relation != RelationPending && !isValidRelationVerb(r.Relation) {
			return nil, nil, fmt.Errorf("portable export invalid relation type %q", r.Relation)
		}
		switch r.JudgmentStatus {
		case JudgmentStatusPending, JudgmentStatusJudged, JudgmentStatusOrphaned, JudgmentStatusIgnored:
		default:
			return nil, nil, fmt.Errorf("portable export invalid judgment status %q", r.JudgmentStatus)
		}
		if r.SupersededByRelationSyncID != nil {
			value := strings.Trim(*r.SupersededByRelationSyncID, " ")
			r.SupersededByRelationSyncID = &value
		}
		seen["relation:"+r.SyncID] = true
	}
	for i, row := range data.Anchors {
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &anchors[i]); err != nil {
			return nil, nil, fmt.Errorf("portable import decode anchor %d: %w", i, err)
		}
		a := &anchors[i]
		a.SyncID, a.ObsSyncID = strings.Trim(a.SyncID, " "), strings.Trim(a.ObsSyncID, " ")
		if a.SyncID == "" || a.ObsSyncID == "" || seen["anchor:"+a.SyncID] {
			return nil, nil, fmt.Errorf("portable export invalid or duplicate anchor sync_id/obs_sync_id %q", a.SyncID)
		}
		switch a.AnchorStatus {
		case AnchorStatusActive, AnchorStatusStale, AnchorStatusTraveled:
		default:
			return nil, nil, fmt.Errorf("portable export invalid anchor status %q", a.AnchorStatus)
		}
		seen["anchor:"+a.SyncID] = true
	}
	return relations, anchors, nil
}

func decodePortableProcedures(rows []PortableRow) ([]portableProcedure, error) {
	procedures := make([]portableProcedure, len(rows))
	seen := map[string]bool{}
	for i, row := range rows {
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &procedures[i]); err != nil {
			return nil, fmt.Errorf("portable import decode procedure %d: %w", i, err)
		}
		p := &procedures[i]
		p.SyncID = strings.Trim(p.SyncID, " ")
		if p.SyncID == "" || seen[p.SyncID] {
			return nil, fmt.Errorf("portable export invalid or duplicate procedure sync_id %q", p.SyncID)
		}
		if !isValidProcedurePolarity(p.Polarity) {
			return nil, fmt.Errorf("portable export invalid procedure polarity %q", p.Polarity)
		}
		if !isValidPostconditionKind(p.PostconditionKind) {
			return nil, fmt.Errorf("portable export invalid procedure postcondition kind %q", p.PostconditionKind)
		}
		switch p.State {
		case ProcedureStateCandidate, ProcedureStateTrusted, ProcedureStateRetired:
		default:
			return nil, fmt.Errorf("portable export invalid procedure state %q", p.State)
		}
		if strings.TrimSpace(p.Trigger) == "" || p.Confidence < 0 || p.Confidence > 1 ||
			p.ReuseConfirmed < 0 || p.ContradictedCount < 0 {
			return nil, fmt.Errorf("portable export invalid procedure fields %q", p.SyncID)
		}
		seen[p.SyncID] = true
	}
	return procedures, nil
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
	relations, anchors, err := decodePortableGraph(data)
	if err != nil {
		return nil, err
	}
	procedures, err := decodePortableProcedures(data.Procedures)
	if err != nil {
		return nil, err
	}
	if err := validatePortableGraphReferences(tx, data, relations, anchors); err != nil {
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
	for _, relation := range relations {
		if err := s.upsertPortableRelation(tx, relation); err != nil {
			return nil, fmt.Errorf("import relation %s: %w", relation.SyncID, err)
		}
		result.RelationsImported++
	}
	for _, relation := range relations {
		if err := s.linkPortableRelation(tx, relation); err != nil {
			return nil, fmt.Errorf("import relation %s superseded reference: %w", relation.SyncID, err)
		}
	}
	for _, anchor := range anchors {
		if err := s.upsertPortableAnchor(tx, anchor); err != nil {
			return nil, fmt.Errorf("import anchor %s: %w", anchor.SyncID, err)
		}
		result.AnchorsImported++
	}
	for _, procedure := range procedures {
		if err := s.upsertPortableProcedure(tx, procedure); err != nil {
			return nil, fmt.Errorf("import procedure %s: %w", procedure.SyncID, err)
		}
		result.ProceduresImported++
	}
	if err := s.commitHook(tx); err != nil {
		return nil, fmt.Errorf("import: commit: %w", err)
	}
	return result, nil
}

func validatePortableGraphReferences(tx *sql.Tx, data *ExportData, relations []portableRelation, anchors []portableAnchor) error {
	observations, relationIDs := map[string]bool{}, map[string]bool{}
	for _, observation := range data.Observations {
		observations[strings.Trim(observation.SyncID, " ")] = true
	}
	for _, relation := range relations {
		relationIDs[relation.SyncID] = true
	}
	resolve := func(table, key, label string, imported map[string]bool) (string, error) {
		if imported[key] {
			return key, nil
		}
		var stored string
		err := tx.QueryRow(`SELECT sync_id FROM `+table+`
			WHERE trim(sync_id)=? ORDER BY (sync_id=?) DESC,id LIMIT 1`, key, key).Scan(&stored)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: portable import dangling %s %q", ErrInvalidPortableExport, label, key)
		}
		if err != nil {
			return "", fmt.Errorf("portable import validate %s: %w", label, err)
		}
		return stored, nil
	}
	for i := range relations {
		relation := &relations[i]
		var err error
		if relation.SourceID, err = resolve("observations", relation.SourceID, "relation source_id", observations); err != nil {
			return err
		}
		if relation.TargetID, err = resolve("observations", relation.TargetID, "relation target_id", observations); err != nil {
			return err
		}
		if relation.SupersededByRelationSyncID != nil && *relation.SupersededByRelationSyncID != "" {
			if _, err := resolve("memory_relations", *relation.SupersededByRelationSyncID, "superseded relation", relationIDs); err != nil {
				return err
			}
		}
	}
	for i := range anchors {
		var err error
		if anchors[i].ObsSyncID, err = resolve("observations", anchors[i].ObsSyncID, "anchor obs_sync_id", observations); err != nil {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("%w: portable import rejected: %s %q is tombstoned", ErrInvalidPortableExport, entity, syncID)
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

func (s *Store) canonicalGraphRowID(tx *sql.Tx, table, key string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM `+table+` WHERE sync_id=? LIMIT 1`, key).Scan(&id)
	if err == sql.ErrNoRows {
		err = tx.QueryRow(`SELECT id FROM `+table+` WHERE trim(sync_id)=? ORDER BY id LIMIT 1`, key).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := s.execHook(tx, `DELETE FROM `+table+` WHERE trim(sync_id)=? AND id<>?`, key, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) upsertPortableRelation(tx *sql.Tx, relation portableRelation) error {
	id, err := s.canonicalGraphRowID(tx, "memory_relations", relation.SyncID)
	if err != nil {
		return err
	}
	args := []any{relation.SyncID, relation.SourceID, relation.TargetID, relation.Relation, relation.Reason,
		relation.Evidence, relation.Confidence, relation.JudgmentStatus, relation.MarkedByActor, relation.MarkedByKind,
		relation.MarkedByModel, relation.SessionID, relation.SupersededAt, relation.CreatedAt, relation.UpdatedAt}
	if id == 0 {
		_, err = s.execHook(tx, `INSERT INTO memory_relations
			(sync_id,source_id,target_id,relation,reason,evidence,confidence,judgment_status,marked_by_actor,
			 marked_by_kind,marked_by_model,session_id,superseded_at,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		return err
	}
	_, err = s.execHook(tx, `UPDATE memory_relations SET sync_id=?,source_id=?,target_id=?,relation=?,reason=?,
		evidence=?,confidence=?,judgment_status=?,marked_by_actor=?,marked_by_kind=?,marked_by_model=?,session_id=?,
		superseded_at=?,superseded_by_relation_id=NULL,created_at=?,updated_at=? WHERE id=?`, append(args, id)...)
	return err
}

func (s *Store) linkPortableRelation(tx *sql.Tx, relation portableRelation) error {
	if relation.SupersededByRelationSyncID == nil || *relation.SupersededByRelationSyncID == "" {
		return nil
	}
	var targetID int64
	if err := tx.QueryRow(`SELECT id FROM memory_relations WHERE trim(sync_id)=? ORDER BY sync_id=? DESC,id LIMIT 1`,
		*relation.SupersededByRelationSyncID, *relation.SupersededByRelationSyncID).Scan(&targetID); err != nil {
		return err
	}
	_, err := s.execHook(tx, `UPDATE memory_relations SET superseded_by_relation_id=? WHERE sync_id=?`, targetID, relation.SyncID)
	return err
}

func (s *Store) upsertPortableAnchor(tx *sql.Tx, anchor portableAnchor) error {
	id, err := s.canonicalGraphRowID(tx, "memory_anchors", anchor.SyncID)
	if err != nil {
		return err
	}
	args := []any{anchor.SyncID, anchor.ObsSyncID, anchor.RepoRoot, anchor.FilePath, anchor.Symbol, anchor.LineStart,
		anchor.LineEnd, anchor.BlameSHA, anchor.BlameAt, anchor.ContentHash, anchor.AnchorStatus, anchor.CreatedAt,
		anchor.CheckedAt, anchor.StaledAt, anchor.NewBlameSHA}
	if id == 0 {
		_, err = s.execHook(tx, `INSERT INTO memory_anchors
			(sync_id,obs_sync_id,repo_root,file_path,symbol,line_start,line_end,blame_sha,blame_at,content_hash,
			 anchor_status,created_at,checked_at,staled_at,new_blame_sha) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		return err
	}
	_, err = s.execHook(tx, `UPDATE memory_anchors SET sync_id=?,obs_sync_id=?,repo_root=?,file_path=?,symbol=?,
		line_start=?,line_end=?,blame_sha=?,blame_at=?,content_hash=?,anchor_status=?,created_at=?,checked_at=?,
		staled_at=?,new_blame_sha=? WHERE id=?`, append(args, id)...)
	return err
}

func (s *Store) upsertPortableProcedure(tx *sql.Tx, p portableProcedure) error {
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return err
	}
	sources, err := json.Marshal(p.SourceObsSyncIDs)
	if err != nil {
		return err
	}
	args := []any{p.SyncID, p.Project, p.Scope, p.Polarity, p.Trigger, string(steps), p.StepsSummary,
		p.ExpectedOutcome, p.PostconditionKind, p.PostconditionExpr, p.Confidence, p.State, p.ReuseConfirmed,
		p.ContradictedCount, string(sources), p.InducedByActor, p.InducedByKind, p.InducedByModel,
		p.CreatedAt, p.UpdatedAt, p.LastReusedAt, p.ReviewAfter, p.RetiredAt}
	id, err := s.canonicalGraphRowID(tx, "procedures", p.SyncID)
	if err != nil {
		return err
	}
	if id != 0 {
		if _, err := s.execHook(tx, `UPDATE procedures SET sync_id=? WHERE id=?`, p.SyncID, id); err != nil {
			return err
		}
	}
	_, err = s.execHook(tx, `INSERT INTO procedures
		(sync_id,project,scope,polarity,"trigger",steps,steps_summary,expected_outcome,postcondition_kind,
		 postcondition_expr,confidence,state,reuse_confirmed,contradicted_count,source_obs_sync_ids,
		 induced_by_actor,induced_by_kind,induced_by_model,created_at,updated_at,last_reused_at,review_after,retired_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(sync_id) DO UPDATE SET project=excluded.project,scope=excluded.scope,polarity=excluded.polarity,
		"trigger"=excluded."trigger",steps=excluded.steps,steps_summary=excluded.steps_summary,
		expected_outcome=excluded.expected_outcome,postcondition_kind=excluded.postcondition_kind,
		postcondition_expr=excluded.postcondition_expr,confidence=excluded.confidence,state=excluded.state,
		reuse_confirmed=excluded.reuse_confirmed,contradicted_count=excluded.contradicted_count,
		source_obs_sync_ids=excluded.source_obs_sync_ids,induced_by_actor=excluded.induced_by_actor,
		induced_by_kind=excluded.induced_by_kind,induced_by_model=excluded.induced_by_model,
		created_at=excluded.created_at,updated_at=excluded.updated_at,last_reused_at=excluded.last_reused_at,
		review_after=excluded.review_after,retired_at=excluded.retired_at`, args...)
	return err
}
