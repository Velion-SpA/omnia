package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DiagnosticSessionEvidence is the read-only session projection used by
// operational diagnostics. It intentionally avoids observation/prompt payloads.
type DiagnosticSessionEvidence struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Directory string `json:"directory"`
	Name      string `json:"name"`
}

// SyncMutationPayloadValidation describes deterministic required-field issues
// in a pending sync mutation payload.
type SyncMutationPayloadValidation struct {
	Entity        string   `json:"entity"`
	Op            string   `json:"op"`
	EntityKey     string   `json:"entity_key,omitempty"`
	MissingFields []string `json:"missing_fields,omitempty"`
	ReasonCode    string   `json:"reason_code,omitempty"`
	Message       string   `json:"message,omitempty"`
}

// SQLiteLockSnapshot captures conservative SQLite lock/contention indicators.
// wal_checkpoint(PASSIVE) is an observational probe for this diagnostic surface;
// callers must not interpret it as a repair action.
type SQLiteLockSnapshot struct {
	JournalMode        string `json:"journal_mode"`
	BusyTimeoutMS      int    `json:"busy_timeout_ms"`
	CheckpointBusy     int    `json:"checkpoint_busy"`
	CheckpointLog      int    `json:"checkpoint_log"`
	CheckpointedFrames int    `json:"checkpointed_frames"`
}

type SessionProjectReclassification struct {
	SessionID   string
	FromProject string
	ToProject   string
}

type SessionProjectReclassificationCounts struct {
	Sessions     int64
	Observations int64
	Prompts      int64
}

type SessionProjectReclassificationResult struct {
	Counts     SessionProjectReclassificationCounts
	BackupPath string
}

// ListDiagnosticSessions returns session evidence scoped by project when
// provided. The query is read-only and ordered for deterministic diagnostics.
func (s *Store) ListDiagnosticSessions(project string) ([]DiagnosticSessionEvidence, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	query := `SELECT id, project, ifnull(directory, ''), id FROM sessions`
	args := []any{}
	if project != "" {
		query += ` WHERE project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY started_at DESC, id ASC`

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]DiagnosticSessionEvidence, 0)
	for rows.Next() {
		var ev DiagnosticSessionEvidence
		if err := rows.Scan(&ev.ID, &ev.Project, &ev.Directory, &ev.Name); err != nil {
			return nil, err
		}
		sessions = append(sessions, ev)
	}
	return sessions, rows.Err()
}

// ListPendingProjectMutations returns pending cloud mutations for one project,
// or all projects when project is empty, without enrollment filtering. Doctor
// needs to diagnose blocked metadata even when a project is not enrolled.
//
// Scans every target_key, not just DefaultSyncTargetKey ("cloud"). Multi-cloud
// fan-out (enqueueCloudFanoutMutationsTx / OBL-06) writes one additional row
// per registered non-default cloud alias, keyed by
// aliasTargetKeyForProject(alias, project) — e.g. "work:umbral" alongside the
// default "cloud" row. A target_key-scoped query here previously saw only the
// default cloud's queue and reported every non-default cloud's stuck
// mutations as a clean bill of health, which is precisely the "doctor
// validates the mutation queue... a green doctor proves nothing" gap #255
// already documented for the OTHER validators on the push path. Same failure
// shape, found in doctor's own read.
func (s *Store) ListPendingProjectMutations(project string) ([]SyncMutation, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	return s.listPendingProjectMutationsTxLike(s.db, project)
}

type rowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) listPendingProjectMutationsTxLike(q rowQuerier, project string) ([]SyncMutation, error) {
	query := `
		SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at
		FROM sync_mutations
		WHERE acked_at IS NULL`
	args := []any{}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY seq ASC`
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mutations := make([]SyncMutation, 0)
	for rows.Next() {
		var m SyncMutation
		if err := rows.Scan(&m.Seq, &m.TargetKey, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.Source, &m.Project, &m.OccurredAt, &m.AckedAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, m)
	}
	return mutations, rows.Err()
}

// ValidateSyncMutationPayload performs pure required-field validation for sync
// payloads. It is intentionally conservative: malformed/empty/unsupported
// payloads are reported as manual blocks, while complete payloads return an
// empty validation.
func ValidateSyncMutationPayload(entity, op, payload, entityKey string) SyncMutationPayloadValidation {
	entity = strings.TrimSpace(entity)
	op = strings.TrimSpace(op)
	entityKey = strings.TrimSpace(entityKey)
	result := SyncMutationPayloadValidation{Entity: entity, Op: op, EntityKey: entityKey}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = "sync mutation payload is empty"
		return result
	}

	var body map[string]any
	if err := decodeSyncPayload([]byte(trimmed), &body); err != nil {
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = fmt.Sprintf("decode sync mutation payload: %v", err)
		return result
	}
	field := func(name string) string {
		v, ok := body[name]
		if !ok || v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		encoded, _ := json.Marshal(v)
		return strings.TrimSpace(string(encoded))
	}
	missing := make([]string, 0)
	require := func(name string) {
		if field(name) == "" {
			missing = append(missing, name)
		}
	}

	switch entity {
	case SyncEntitySession:
		if field("id") == "" && entityKey == "" {
			missing = append(missing, "id")
		}
		// Directory is deliberately NOT required (#254). A session created by
		// saving a memory by hand has no working directory — there was no repo
		// involved, and an absent directory there is that session's correct
		// state, not missing data. `chunkcodec.normalizeMutationPayload` and
		// `cloudserver.validateDirectChunkArrayEntries` were relaxed for this
		// same reason in #254, but this validator — a THIRD, independent copy
		// of the same rule, reached only through `omnia doctor` — was missed.
		// That gap is exactly what #255 warned about: three validators with no
		// shared rules mean a green doctor proves nothing about whether the
		// push will be accepted. id stays required: an upsert without one
		// targets nothing.
	case SyncEntityObservation:
		if field("sync_id") == "" && entityKey == "" {
			missing = append(missing, "sync_id")
		}
		if op == SyncOpUpsert {
			require("session_id")
			require("type")
			require("title")
			require("content")
			require("scope")
		}
	case SyncEntityPrompt:
		if field("sync_id") == "" && entityKey == "" {
			missing = append(missing, "sync_id")
		}
		if op == SyncOpUpsert {
			require("session_id")
			require("content")
		}
	case SyncEntityRelation:
		if op == SyncOpUpsert {
			require("sync_id")
			require("source_id")
			require("target_id")
			require("relation")
			require("judgment_status")
			require("marked_by_actor")
			require("marked_by_kind")
			require("project")
		}
	case SyncEntityEmbedding:
		if field("sync_id") == "" && entityKey == "" {
			missing = append(missing, "sync_id")
		}
		if op == SyncOpUpsert {
			require("vector")
		}
	default:
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = fmt.Sprintf("unsupported sync mutation %q/%q", entity, op)
		return result
	}

	if len(missing) > 0 {
		result.MissingFields = missing
		result.ReasonCode = "sync_mutation_payload_missing_required_fields"
		result.Message = fmt.Sprintf("%s payload missing required fields: %s", entity, strings.Join(missing, ", "))
	}
	return result
}

// ReadSQLiteLockSnapshot returns SQLite lock-related PRAGMA values without
// starting an application write transaction.
func (s *Store) ReadSQLiteLockSnapshot(ctx context.Context) (SQLiteLockSnapshot, error) {
	var snapshot SQLiteLockSnapshot
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&snapshot.JournalMode); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&snapshot.BusyTimeoutMS); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&snapshot.CheckpointBusy, &snapshot.CheckpointLog, &snapshot.CheckpointedFrames); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *Store) EstimateSessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationCounts, error) {
	var counts SessionProjectReclassificationCounts
	for _, action := range normalizeSessionProjectReclassificationActions(actions) {
		var n int64
		if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate sessions: %w", err)
		}
		counts.Sessions += n
		if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE session_id = ? AND project = ? AND deleted_at IS NULL`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate observations: %w", err)
		}
		counts.Observations += n
		if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts WHERE session_id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate prompts: %w", err)
		}
		counts.Prompts += n
	}
	return counts, nil
}

func (s *Store) ApplySessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationResult, error) {
	normalized := normalizeSessionProjectReclassificationActions(actions)
	backupPath, err := s.BackupSQLite()
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	var result SessionProjectReclassificationResult
	result.BackupPath = backupPath
	err = s.withTx(func(tx *sql.Tx) error {
		for _, action := range normalized {
			res, err := s.execHook(tx, `UPDATE sessions SET project = ? WHERE id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify session %q: %w", action.SessionID, err)
			}
			n, _ := res.RowsAffected()
			result.Counts.Sessions += n

			res, err = s.execHook(tx, `UPDATE observations SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify observations for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Observations += n

			res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify prompts for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Prompts += n
		}
		return nil
	})
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	return result, nil
}

// BackupDir returns the directory BackupSQLite writes VACUUM INTO snapshots
// to — a full copy of the store's content, so its permissions matter exactly
// as much as the primary data dir and database file (see StoreExposureCheck).
func (s *Store) BackupDir() string {
	return filepath.Join(s.cfg.DataDir, "backups")
}

func (s *Store) BackupSQLite() (string, error) {
	backupDir := s.BackupDir()
	// Owner-only on a FRESH create, same rationale and same caveat as New()'s
	// MkdirAll(cfg.DataDir, 0o700): a directory this call actually creates is
	// locked down, but an already-existing (looser) backups dir from before
	// this fix is left as-is here and flagged instead by
	// internal/diagnostic's StoreExposureCheck — this call must never
	// silently re-permission a directory the operator may have intentionally
	// shared. Previously 0755 (operational item #3,
	// docs/conversational-retrieval-plan.md): a VACUUM INTO snapshot is a
	// full plaintext copy of the store, so group/world access here is the
	// same exposure the primary data dir was already hardened against.
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create sqlite backup dir: %w", err)
	}
	path := filepath.Join(backupDir, "engram-repair-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".db")
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("backup sqlite database: %w", err)
	}
	// Lock the backup file itself down to owner-only too, mirroring New()'s
	// unconditional Chmod on the primary database file: VACUUM INTO creates
	// the file following the process umask, which on a typical 022 umask
	// yields group/world-readable (0644) regardless of the directory mode.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("chmod sqlite backup file: %w", err)
	}
	return path, nil
}

// DeleteSyncMutationsResult reports what DeletePendingSyncMutations actually
// did, mirroring SessionProjectReclassificationResult's shape (counts +
// backup path) so both repairable checks return the same kind of evidence.
type DeleteSyncMutationsResult struct {
	Deleted    int64
	BackupPath string
}

// DeletePendingSyncMutations deletes specific pending (unacked) sync_mutations
// rows by seq — the repair action for CheckSyncMutationRequiredFields
// (docs/conversational-retrieval-plan.md operational item #1 follow-up:
// making a bad payload wedging the queue repairable through `omnia doctor
// repair` instead of by hand against an encrypted SQLite file with no
// sqlite3 path).
//
// Delete only: this never touches payload content, so it can never invent
// data the store doesn't have — the caller already decided (via
// ValidateSyncMutationPayload) that the payload cannot be trusted.
//
// Idempotent by construction: `WHERE seq IN (...) AND acked_at IS NULL` means
// a seq that no longer exists (already deleted by a prior --apply, or
// acknowledged by a sync run in the meantime) simply contributes nothing to
// RowsAffected — re-running --apply against an already-clean set of seqs is a
// safe no-op, not an error.
//
// A full VACUUM INTO backup is taken first, same as
// ApplySessionProjectReclassification, so a deletion can be undone by
// restoring the backup file even though the delete itself is irreversible.
func (s *Store) DeletePendingSyncMutations(seqs []int64) (DeleteSyncMutationsResult, error) {
	if len(seqs) == 0 {
		return DeleteSyncMutationsResult{}, nil
	}
	backupPath, err := s.BackupSQLite()
	if err != nil {
		return DeleteSyncMutationsResult{}, err
	}
	placeholders := make([]string, len(seqs))
	args := make([]any, len(seqs))
	for i, seq := range seqs {
		placeholders[i] = "?"
		args[i] = seq
	}
	res, err := s.execHook(s.db,
		`DELETE FROM sync_mutations WHERE acked_at IS NULL AND seq IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return DeleteSyncMutationsResult{}, fmt.Errorf("delete pending sync mutations: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return DeleteSyncMutationsResult{}, fmt.Errorf("delete pending sync mutations: rows affected: %w", err)
	}
	return DeleteSyncMutationsResult{Deleted: deleted, BackupPath: backupPath}, nil
}

func normalizeSessionProjectReclassificationActions(actions []SessionProjectReclassification) []SessionProjectReclassification {
	seen := make(map[string]struct{})
	out := make([]SessionProjectReclassification, 0, len(actions))
	for _, action := range actions {
		action.SessionID = strings.TrimSpace(action.SessionID)
		action.FromProject, _ = NormalizeProject(action.FromProject)
		action.ToProject, _ = NormalizeProject(action.ToProject)
		if action.SessionID == "" || action.FromProject == "" || action.ToProject == "" || action.FromProject == action.ToProject {
			continue
		}
		key := action.SessionID + "\x00" + action.FromProject + "\x00" + action.ToProject
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, action)
	}
	return out
}
