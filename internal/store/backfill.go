package store

import (
	"context"
	"fmt"
)

// ─── Recall reliability (#1399) — error_signature backfill ──────────────────
//
// error_signature (signature.go, design obs #1498, slice 1) is computed at
// SAVE time — inside AddObservation, for bugfix-family types (isBugfixFamilyType),
// via NormalizeErrorSignature(extractErrorText(content)). Observations saved
// BEFORE that feature existed (or imported from a legacy export) have
// error_signature = NULL and can never surface through Search's signature
// lane. Since the real value of #1399 is finding PAST fixes for recurring
// bugs — which by definition predate the feature — a one-shot backfill is
// required to make existing data participate in the signature lane at all.
//
// BackfillErrorSignatures reuses the EXACT SAME derivation used at save time
// so a backfilled row behaves identically to one that had always had a
// signature.

// errorSignatureBackfillCandidate is the minimal projection needed to derive
// a signature for one existing observation.
type errorSignatureBackfillCandidate struct {
	id      int64
	content string
}

// collectErrorSignatureBackfillCandidates selects bugfix-family observations
// (per isBugfixFamilyType) that are not soft-deleted and still lack a stored
// error_signature (NULL or empty), optionally scoped to project. The
// bugfix-family filter is applied in Go (mirroring AddObservation's own
// isBugfixFamilyType check) rather than duplicated as a SQL IN-list.
//
// The returned rows are fully drained and rows.Close() is called BEFORE this
// function returns. This matters: the store's *sql.DB has
// db.SetMaxOpenConns(1) (see store.go), so holding an open *sql.Rows while a
// caller issues further statements (the per-row UPDATEs in
// BackfillErrorSignatures) on the same connection would deadlock — the UPDATE
// can never acquire the single connection this open Rows is still holding.
// Search's topic_key/signature lanes hit the exact same constraint and fix it
// the same way: collect first, close explicitly, THEN mutate.
func (s *Store) collectErrorSignatureBackfillCandidates(ctx context.Context, project string) ([]errorSignatureBackfillCandidate, error) {
	project, _ = NormalizeProject(project)

	sqlQ := `
		SELECT id, type, content
		FROM observations
		WHERE deleted_at IS NULL
		  AND (error_signature IS NULL OR error_signature = '')
	`
	var args []any
	if project != "" {
		sqlQ += " AND LOWER(project) = ?"
		args = append(args, project)
	}
	sqlQ += " ORDER BY id ASC"

	rows, err := s.db.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, fmt.Errorf("backfill error signatures: query candidates: %w", err)
	}

	var candidates []errorSignatureBackfillCandidate
	for rows.Next() {
		var c errorSignatureBackfillCandidate
		var typ string
		if scanErr := rows.Scan(&c.id, &typ, &c.content); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("backfill error signatures: scan candidate: %w", scanErr)
		}
		if !isBugfixFamilyType(typ) {
			continue
		}
		candidates = append(candidates, c)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("backfill error signatures: close candidate rows: %w", closeErr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backfill error signatures: iterate candidates: %w", err)
	}
	return candidates, nil
}

// BackfillErrorSignatures scans existing bugfix-family observations that
// still lack a stored error_signature and derives + persists one, using the
// same extraction as save time (extractErrorText + NormalizeErrorSignature).
//
// scanned is the number of bugfix-family rows examined (still missing a
// signature); updated is the number actually written. scanned-updated rows
// had no extractable error-shaped text (pure-prose bugfix memories) and are
// deliberately left with error_signature = NULL rather than falling back to
// indexing their full content — identical to AddObservation's save-time
// contract.
//
// Idempotent: only rows still missing a signature are selected each run, so
// invoking this again after a successful pass updates 0 rows. Filters by
// project when project is non-empty.
func (s *Store) BackfillErrorSignatures(ctx context.Context, project string) (scanned int, updated int, err error) {
	candidates, err := s.collectErrorSignatureBackfillCandidates(ctx, project)
	if err != nil {
		return 0, 0, err
	}
	scanned = len(candidates)

	for _, c := range candidates {
		sig := NormalizeErrorSignature(extractErrorText(c.content))
		if sig == "" {
			continue
		}
		if _, execErr := s.db.ExecContext(ctx, `UPDATE observations SET error_signature = ? WHERE id = ?`, sig, c.id); execErr != nil {
			return scanned, updated, fmt.Errorf("backfill error signatures: update id=%d: %w", c.id, execErr)
		}
		updated++
	}
	return scanned, updated, nil
}

// PreviewBackfillErrorSignatures computes exactly what BackfillErrorSignatures
// would do — the same candidate selection and the same signature derivation —
// WITHOUT writing anything. Backs `omnia recall-backfill --dry-run`.
func (s *Store) PreviewBackfillErrorSignatures(ctx context.Context, project string) (scanned int, wouldUpdate int, err error) {
	candidates, err := s.collectErrorSignatureBackfillCandidates(ctx, project)
	if err != nil {
		return 0, 0, err
	}
	scanned = len(candidates)
	for _, c := range candidates {
		if NormalizeErrorSignature(extractErrorText(c.content)) != "" {
			wouldUpdate++
		}
	}
	return scanned, wouldUpdate, nil
}
