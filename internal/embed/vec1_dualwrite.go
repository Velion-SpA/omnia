package embed

import (
	"context"
	"database/sql"
)

// This file implements v0.4's `sqlite-vec-index` capability, PR2B slice
// (design capability 7 "Write, backfill, and recovery", spec REQ-461,
// REQ-465, REQ-466): the per-mutation derived-row dual-write that keeps
// vec_embeddings incrementally in sync with every Upsert/DeleteBySyncID/
// Prune call on the source `embeddings` table (store.go).

// upsertRow mirrors a single row's vector into the derived Vec1 table within
// tx, keyed by rowid (never an observation ID or application-generated key,
// design capability 7). A dimension mismatch against the established active
// dimension is a normal, EXPECTED skip (spec REQ-466) — not a health
// failure: it returns (skipped=true, err=nil) so the caller commits the
// source-table write as usual with no derived row. The first successfully
// dimensioned row this Store instance ever writes establishes the in-memory
// active dimension (persisted readiness itself is only granted by a
// verified VecBackfill/VecReindex pass, never by an individual write).
func (v *vecIndex) upsertRow(ctx context.Context, tx *sql.Tx, rowid int64, vector []float32, project string) (skipped bool, err error) {
	if v == nil {
		return true, nil
	}
	v.mu.Lock()
	dim := v.activeDim
	if dim == 0 && len(vector) > 0 {
		dim = len(vector)
	}
	v.mu.Unlock()
	if dim == 0 || len(vector) != dim {
		return true, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vec_embeddings WHERE rowid = ?`, rowid); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vec_embeddings(rowid, vector, project) VALUES (?, ?, ?)`,
		rowid, encodeNativeVector(vector), project); err != nil {
		return false, err
	}
	v.mu.Lock()
	if v.activeDim == 0 {
		v.activeDim = dim
	}
	v.mu.Unlock()
	return false, nil
}
