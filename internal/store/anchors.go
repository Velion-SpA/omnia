package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// ─── Anchor status vocabulary (memory_anchors.anchor_status) ─────────────────

// Anchor status values. Mirrors memory_relations' small locked-vocabulary
// convention (RelationPending etc. in relations.go).
const (
	AnchorStatusActive   = "active"
	AnchorStatusStale    = "stale"
	AnchorStatusTraveled = "traveled"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// MemoryAnchor represents a row in memory_anchors: a cheap code anchor
// (file + symbol + git-blame line range + blame SHA + content hash) linked
// 1:N to a memory via ObsSyncID (design decision: separate table, not
// columns on observations — mirrors the memory_relations precedent).
type MemoryAnchor struct {
	ID           int64
	SyncID       string
	ObsSyncID    string
	RepoRoot     string
	FilePath     string
	Symbol       string
	LineStart    int
	LineEnd      int
	BlameSHA     string
	BlameAt      string
	ContentHash  string
	AnchorStatus string
	CreatedAt    string
	CheckedAt    *string
	StaledAt     *string
}

// UpsertAnchorParams holds the inputs for UpsertAnchor.
type UpsertAnchorParams struct {
	// ObsSyncID is the TEXT sync_id of the memory this anchor is linked to (required).
	ObsSyncID string
	// RepoRoot is the absolute path to the git repo root this anchor was captured in.
	RepoRoot string
	// FilePath is the anchored file, relative to RepoRoot (required).
	FilePath string
	// Symbol is the anchored symbol name (function/type/etc). May be empty.
	Symbol string
	// LineStart/LineEnd are the 1-based, inclusive git-blame line range.
	LineStart int
	LineEnd   int
	// BlameSHA is the commit SHA git blame attributes the range to.
	BlameSHA string
	// BlameAt is BlameSHA's commit time (RFC3339), for audit/receipt display.
	BlameAt string
	// ContentHash is the normalized-content hash of the range at capture time (required).
	ContentHash string
}

// UpdateAnchorRangeParams holds the inputs for UpdateAnchorRange (travel:
// the anchor's underlying code moved to a new line range without changing
// materially — REQ-004).
type UpdateAnchorRangeParams struct {
	// SyncID identifies the anchor row to update (required).
	SyncID string
	// LineStart/LineEnd are the anchor's new 1-based, inclusive line range.
	LineStart int
	LineEnd   int
	// BlameSHA/ContentHash are refreshed to the current re-blame result.
	BlameSHA    string
	ContentHash string
}

// ─── UpsertAnchor ─────────────────────────────────────────────────────────────

// UpsertAnchor inserts a new active memory_anchors row linked to
// p.ObsSyncID, or — if an anchor already exists for the same
// (obs_sync_id, file_path, symbol) — updates that existing row in place
// (reactivating it to 'active' and clearing staled_at, so a topic_key
// revision save that re-supplies the same code_anchors entry does not pile
// up duplicate rows). Returns the anchor's sync_id either way.
func (s *Store) UpsertAnchor(p UpsertAnchorParams) (string, error) {
	if strings.TrimSpace(p.ObsSyncID) == "" {
		return "", fmt.Errorf("UpsertAnchor: ObsSyncID is required")
	}
	if strings.TrimSpace(p.FilePath) == "" {
		return "", fmt.Errorf("UpsertAnchor: FilePath is required")
	}

	var resultSyncID string
	err := s.withTx(func(tx *sql.Tx) error {
		var existingSyncID string
		err := tx.QueryRow(`
			SELECT sync_id FROM memory_anchors
			WHERE obs_sync_id = ? AND file_path = ? AND ifnull(symbol,'') = ifnull(?, '')
			LIMIT 1
		`, p.ObsSyncID, p.FilePath, nullableString(p.Symbol)).Scan(&existingSyncID)

		switch {
		case err == sql.ErrNoRows:
			existingSyncID = newSyncID("anc")
			if _, execErr := tx.Exec(`
				INSERT INTO memory_anchors
					(sync_id, obs_sync_id, repo_root, file_path, symbol, line_start, line_end,
					 blame_sha, blame_at, content_hash, anchor_status, created_at, checked_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
			`, existingSyncID, p.ObsSyncID, nullableString(p.RepoRoot), p.FilePath, nullableString(p.Symbol),
				p.LineStart, p.LineEnd, nullableString(p.BlameSHA), nullableString(p.BlameAt), p.ContentHash,
				AnchorStatusActive,
			); execErr != nil {
				return fmt.Errorf("UpsertAnchor: insert: %w", execErr)
			}
		case err != nil:
			return fmt.Errorf("UpsertAnchor: check existing: %w", err)
		default:
			if _, execErr := tx.Exec(`
				UPDATE memory_anchors
				SET repo_root     = ?,
				    line_start    = ?,
				    line_end      = ?,
				    blame_sha     = ?,
				    blame_at      = ?,
				    content_hash  = ?,
				    anchor_status = ?,
				    staled_at     = NULL,
				    checked_at    = datetime('now')
				WHERE sync_id = ?
			`, nullableString(p.RepoRoot), p.LineStart, p.LineEnd, nullableString(p.BlameSHA), nullableString(p.BlameAt),
				p.ContentHash, AnchorStatusActive, existingSyncID,
			); execErr != nil {
				return fmt.Errorf("UpsertAnchor: update: %w", execErr)
			}
		}

		resultSyncID = existingSyncID
		return nil
	})
	if err != nil {
		return "", err
	}
	return resultSyncID, nil
}

// ─── ListActiveAnchors ────────────────────────────────────────────────────────

// ListActiveAnchors returns all memory_anchors rows with anchor_status =
// 'active', joined to their (non-deleted) observation so a forget-scan pass
// (a later slice) can be scoped to a single project. An empty project
// returns active anchors across all projects.
func (s *Store) ListActiveAnchors(project string) ([]MemoryAnchor, error) {
	project, _ = NormalizeProject(project)

	query := `
		SELECT a.id, a.sync_id, a.obs_sync_id, ifnull(a.repo_root,''), a.file_path, ifnull(a.symbol,''),
		       a.line_start, a.line_end, ifnull(a.blame_sha,''), ifnull(a.blame_at,''), a.content_hash,
		       a.anchor_status, a.created_at, a.checked_at, a.staled_at
		FROM memory_anchors a
		JOIN observations o ON o.sync_id = a.obs_sync_id
		WHERE a.anchor_status = ?
		  AND o.deleted_at IS NULL
	`
	args := []any{AnchorStatusActive}
	if project != "" {
		query += " AND LOWER(ifnull(o.project,'')) = ?"
		args = append(args, project)
	}
	query += " ORDER BY a.id ASC"

	rows, err := s.queryHook(s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListActiveAnchors: %w", err)
	}
	defer rows.Close()

	var out []MemoryAnchor
	for rows.Next() {
		var a MemoryAnchor
		if err := rows.Scan(
			&a.ID, &a.SyncID, &a.ObsSyncID, &a.RepoRoot, &a.FilePath, &a.Symbol,
			&a.LineStart, &a.LineEnd, &a.BlameSHA, &a.BlameAt, &a.ContentHash,
			&a.AnchorStatus, &a.CreatedAt, &a.CheckedAt, &a.StaledAt,
		); err != nil {
			return nil, fmt.Errorf("ListActiveAnchors: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListActiveAnchors: %w", err)
	}
	return out, nil
}

// ─── UpdateAnchorRange (travel) ───────────────────────────────────────────────

// UpdateAnchorRange updates an anchor's line range and re-blamed
// BlameSHA/ContentHash in place — the "travel" outcome (REQ-004): the
// anchored code moved (e.g. a refactor shifted it to a new line range) but
// its body did not materially change, so the anchor relocates instead of
// staling. anchor_status is intentionally left untouched (stays 'active')
// so a traveled anchor keeps being re-checked by future forget-scan passes,
// exactly like any other active anchor.
func (s *Store) UpdateAnchorRange(p UpdateAnchorRangeParams) error {
	if strings.TrimSpace(p.SyncID) == "" {
		return fmt.Errorf("UpdateAnchorRange: SyncID is required")
	}

	res, err := s.execHook(s.db, `
		UPDATE memory_anchors
		SET line_start   = ?,
		    line_end     = ?,
		    blame_sha    = ?,
		    content_hash = ?,
		    checked_at   = datetime('now')
		WHERE sync_id = ?
	`, p.LineStart, p.LineEnd, nullableString(p.BlameSHA), p.ContentHash, p.SyncID)
	if err != nil {
		return fmt.Errorf("UpdateAnchorRange: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateAnchorRange: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("UpdateAnchorRange: anchor %q not found", p.SyncID)
	}
	return nil
}

// ─── MarkAnchorStale ──────────────────────────────────────────────────────────

// MarkAnchorStale marks the anchor identified by syncID as 'stale' and sets
// its linked memory's review_after to now, surfacing it in the existing
// review flow ("code changed — still true?"). It mirrors JudgeBySemantic's
// system provenance (marked_by_actor='engram', marked_by_kind='system').
//
// The memory and the anchor row are NEVER deleted (design's supersede/
// tombstone decision, honoring the locked "never hard-delete" decision).
//
// newerObsSyncID, when non-nil and non-empty, is the sync_id of a newer
// memory that already covers the same subject; a system-provenance
// 'supersedes' relation row (newer -> staled) is written ONLY in that case.
// nil means "no newer memory exists" — no relation row is written.
//
// Idempotent: calling this again for an already-stale anchor is a no-op
// beyond refreshing checked_at/review_after, and never inserts a second
// supersedes row for the same pair.
func (s *Store) MarkAnchorStale(syncID string, newerObsSyncID *string) error {
	if strings.TrimSpace(syncID) == "" {
		return fmt.Errorf("MarkAnchorStale: syncID is required")
	}

	return s.withTx(func(tx *sql.Tx) error {
		var obsSyncID, status string
		err := tx.QueryRow(
			`SELECT obs_sync_id, anchor_status FROM memory_anchors WHERE sync_id = ?`, syncID,
		).Scan(&obsSyncID, &status)
		if err == sql.ErrNoRows {
			return fmt.Errorf("MarkAnchorStale: anchor %q not found", syncID)
		}
		if err != nil {
			return fmt.Errorf("MarkAnchorStale: lookup: %w", err)
		}

		if status != AnchorStatusStale {
			if _, execErr := tx.Exec(`
				UPDATE memory_anchors
				SET anchor_status = ?,
				    staled_at     = datetime('now'),
				    checked_at    = datetime('now')
				WHERE sync_id = ?
			`, AnchorStatusStale, syncID); execErr != nil {
				return fmt.Errorf("MarkAnchorStale: update anchor: %w", execErr)
			}
		} else if _, execErr := tx.Exec(
			`UPDATE memory_anchors SET checked_at = datetime('now') WHERE sync_id = ?`, syncID,
		); execErr != nil {
			return fmt.Errorf("MarkAnchorStale: refresh checked_at: %w", execErr)
		}

		// System-provenance surfacing: set review_after=now on the linked
		// memory so it appears in the existing review flow (REQ-005/REQ-007).
		if _, execErr := tx.Exec(
			`UPDATE observations SET review_after = datetime('now'), updated_at = datetime('now') WHERE sync_id = ? AND deleted_at IS NULL`,
			obsSyncID,
		); execErr != nil {
			return fmt.Errorf("MarkAnchorStale: set review_after: %w", execErr)
		}

		// Supersedes relation row — written ONLY when a newer memory exists,
		// and only once per (newer, staled) pair (idempotent).
		if newerObsSyncID != nil && strings.TrimSpace(*newerObsSyncID) != "" {
			var existingRel string
			relErr := tx.QueryRow(`
				SELECT sync_id FROM memory_relations
				WHERE source_id = ? AND target_id = ? AND relation = 'supersedes'
				LIMIT 1
			`, *newerObsSyncID, obsSyncID).Scan(&existingRel)

			if relErr == sql.ErrNoRows {
				relSyncID := newSyncID("rel")
				if _, execErr := tx.Exec(`
					INSERT INTO memory_relations
						(sync_id, source_id, target_id, relation, judgment_status,
						 marked_by_actor, marked_by_kind,
						 created_at, updated_at)
					VALUES (?, ?, ?, 'supersedes', 'judged', 'engram', 'system', datetime('now'), datetime('now'))
				`, relSyncID, *newerObsSyncID, obsSyncID); execErr != nil {
					return fmt.Errorf("MarkAnchorStale: insert supersedes relation: %w", execErr)
				}
			} else if relErr != nil {
				return fmt.Errorf("MarkAnchorStale: check existing supersedes: %w", relErr)
			}
		}

		return nil
	})
}
