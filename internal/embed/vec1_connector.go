package embed

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/vec1"
)

// This file implements v0.4's `sqlite-vec-index` capability, PR2A slice
// (design ADR-4, spec
// `openspec/changes/omnia-0.4-memory-frontier/specs/sqlite-vec-index/spec.md`,
// REQ-460, REQ-461, REQ-463, REQ-464, REQ-466 through REQ-468): an opt-in,
// additive Vec1 flat/cos exact float32 KNN index over the SAME
// `embeddings.db`, registered via the bundled, pure-Go
// `github.com/ncruces/go-sqlite3@v0.35.2` connector. This slice covers only
// the connector, derived-state bookkeeping, and the backfill/reindex
// maintenance API — NO production composition and NO read routing (both
// land in later slices, vec1_dualwrite.go / vec1_search.go). The
// `embeddings` table remains the sole authoritative source of truth; the
// brute-force scan (store.go) is NEVER removed and is the permanent fallback
// whenever Vec1 is disabled, absent, unhealthy, corrupt, or hits a dimension
// mismatch.
//
// Ownership is deliberately confined to this package (design: "the connector
// is deliberately private to internal/embed; core-store driver and
// encryption composition must not acquire a dependency on Vec1").

// Option configures OpenStore. The zero value of every option reproduces
// OpenStore's pre-v0.4 behavior exactly, so existing callers that pass no
// options (`embed.OpenStore(path)`) are byte-for-byte unaffected (spec
// REQ-460's "disabled parity" scenario).
type Option func(*storeOptions)

type storeOptions struct {
	vecIndexEnabled bool
	// encryptionEnabled/encryptionKeychainService/encryptionAllowPlaintextFallback
	// are set by WithEncryption (encryption.go, v0.4 memory-at-rest-security
	// capability). Kept on the same storeOptions struct as vecIndexEnabled
	// since both flags jointly decide OpenStore's driver selection (design
	// ADR-1: ncruces is selected when EITHER is enabled; encryption.go's
	// openEncryptedEmbedDB composes both in one connector when both apply).
	encryptionEnabled                bool
	encryptionKeychainService        string
	encryptionAllowPlaintextFallback bool
}

// WithVecIndex opts a Store into the additive Vec1 flat/cos KNN index. Vec1
// is only actually engaged when enabled is true, the host is little-endian
// (spec REQ-468), and the bundled connector registers successfully (REQ-464);
// any other outcome degrades silently to the existing modernc/brute-force
// path — WithVecIndex(true) never causes OpenStore itself to fail.
func WithVecIndex(enabled bool) Option {
	return func(o *storeOptions) { o.vecIndexEnabled = enabled }
}

// hostIsLittleEndian reports whether this process's native byte order is
// little-endian. v0.4 supports Vec1 on little-endian targets only (spec
// REQ-468): Omnia's canonical `embeddings.vector` codec is always
// little-endian (encodeVector/decodeVector), but the derived Vec1 BLOB is
// explicitly re-encoded in machine-native order and must never be assumed
// interchangeable with the little-endian source on a big-endian host.
func hostIsLittleEndian() bool {
	var buf [2]byte
	binary.NativeEndian.PutUint16(buf[:], 1)
	return buf[0] == 1
}

// encodeNativeVector re-encodes v as machine-native-order packed float32
// bytes for the derived Vec1 column. This is a deliberately separate code
// path from encodeVector's little-endian source codec (spec REQ-468: the
// derived BLOB "MUST NOT be treated as a generic little-endian Vec1
// representation" and "MUST be explicitly re-encoded").
func encodeNativeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		binary.NativeEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

// openVec1DB opens dsn through the bundled, pure-Go ncruces/go-sqlite3
// driver with the Vec1 extension registered on every physical connection via
// driver.Open's per-connection init callback (design ADR-4: Vec1 is "never
// assumed to be process-global"). It reuses the exact same dsn OpenStore
// already builds for modernc, so the on-disk file, WAL mode, and busy
// timeout are identical either way — only the driver differs (spec REQ-464).
func openVec1DB(dsn string) (*sql.DB, error) {
	db, err := ncrucesdriver.Open(dsn, vec1.Register)
	if err != nil {
		return nil, fmt.Errorf("embed: open vec1 connector: %w", err)
	}
	return db, nil
}

// vecStateDDL creates Omnia's own bookkeeping table for the derived index's
// lifecycle. It lives in the SAME embeddings.db (design: "no second
// embedding database is created") and is the "completion marker" that a
// backfill/reindex pass only writes after its derived transaction, count
// verification, and dimension report succeed (spec REQ-461/467).
const vecStateDDL = `
CREATE TABLE IF NOT EXISTS vec_index_state (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    active_dim           INTEGER NOT NULL DEFAULT 0,
    ready                INTEGER NOT NULL DEFAULT 0,
    native_little_endian INTEGER NOT NULL DEFAULT 1,
    updated_at           TEXT NOT NULL DEFAULT ''
);`

// vecTableDDL creates the additive Vec1 virtual table. Column shape mirrors
// design.md's Capability 7 data model exactly: one vector column plus a
// single `project` metadata column (used for scoped-KNN pushdown filtering,
// verified empirically to filter BEFORE top-K accumulation).
const vecTableDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS vec_embeddings USING vec1(vector, project);`

// vecRebuildFlatCos pins the table's index/distance mode. Configured exactly
// once per database (idempotent thereafter — Vec1 persists this
// configuration across process restarts without needing to be reissued).
// v0.4's pinned contract is flat/cos only: no quantization, IVF, or ANN.
const vecRebuildFlatCos = `INSERT INTO vec_embeddings(cmd, vector) VALUES ('rebuild', '{index:"flat", distance:"cos"}');`

// vecIndex holds the per-Store runtime state of the optional Vec1 index. A
// nil *vecIndex on Store means the capability is off or unsupported on this
// host — every method on vecIndex is nil-receiver-safe and reports "not
// usable," so every call site can treat "vec == nil" and "vec present but
// unhealthy/not ready" identically: always fall back to brute force.
type vecIndex struct {
	mu        sync.Mutex
	healthy   bool // false permanently disables Vec1 for the rest of this Store's life (spec REQ-465)
	ready     bool // a backfill/reindex pass has completed and verified at least once
	activeDim int  // 0 until an active dimension has been established
}

// usable reports whether reads may route through Vec1 right now.
func (v *vecIndex) usable() bool {
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.healthy && v.ready && v.activeDim > 0
}

// healthyOK reports whether Vec1 maintenance/dual-write operations may still
// be attempted (population doesn't require "ready" — populating IS what
// establishes readiness).
func (v *vecIndex) healthyOK() bool {
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.healthy
}

// dim returns the established active dimension, or 0 if none yet.
func (v *vecIndex) dim() int {
	if v == nil {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.activeDim
}

// markUnhealthy permanently disables Vec1 reads/writes for the rest of this
// Store's life (design: "index corruption ... disable Vec1 for that Store
// instance and fall back silently to the existing caller contract (with
// diagnostic logging); source-table errors still propagate normally"). It is
// safe to call on a nil receiver.
func (v *vecIndex) markUnhealthy(err error) {
	if v == nil {
		return
	}
	v.mu.Lock()
	v.healthy = false
	v.mu.Unlock()
	if err != nil {
		log.Printf("[embed/vec1] disabling Vec1 for this store instance after an unexpected failure: %v", err)
	}
}

// reset clears derived-index state in memory ahead of a VecReindex pass —
// reads fall back to brute force for the remainder of the reindex window
// until a fresh pass verifies and re-marks readiness.
func (v *vecIndex) reset() {
	if v == nil {
		return
	}
	v.mu.Lock()
	v.activeDim = 0
	v.ready = false
	v.mu.Unlock()
}

// persistReady writes the completion marker (spec REQ-461/467: only written
// after the derived transaction, count verification, and dimension report
// succeed) and updates in-memory state to match. The persisted
// native_little_endian marker records the byte order this pass encoded
// derived vectors in (spec REQ-468) — setupVecIndex compares it against the
// CURRENT host on every open and disables Vec1 on a mismatch, rather than
// assuming a previously-written derived index is safe to reuse verbatim.
func (v *vecIndex) persistReady(ctx context.Context, db *sql.DB, dim int) error {
	if v == nil {
		return nil
	}
	nativeLE := 0
	if hostIsLittleEndian() {
		nativeLE = 1
	}
	_, err := db.ExecContext(ctx,
		`UPDATE vec_index_state SET active_dim = ?, ready = 1, native_little_endian = ?, updated_at = ? WHERE id = 1`,
		dim, nativeLE, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.activeDim = dim
	v.ready = true
	v.mu.Unlock()
	return nil
}

// setupVecIndex prepares the derived Vec1 scaffolding on an already-open
// ncruces-backed db: Omnia's own bookkeeping table, the Vec1 virtual table
// (idempotent creation only — no data rebuild implied by mere creation), and
// loads any previously persisted readiness/active-dimension state. It
// returns nil (Vec1 unavailable for this Store, brute force only) rather
// than an error for ANY setup failure — Vec1 is always an additive,
// gracefully degrading capability and must never fail OpenStore itself
// (spec REQ-465).
func setupVecIndex(db *sql.DB) *vecIndex {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, vecStateDDL); err != nil {
		log.Printf("[embed/vec1] state table unavailable (%v); vector_index falls back to brute force", err)
		return nil
	}
	if _, err := db.ExecContext(ctx, vecTableDDL); err != nil {
		log.Printf("[embed/vec1] virtual table unavailable (%v); vector_index falls back to brute force", err)
		return nil
	}

	v := &vecIndex{healthy: true}
	var dim, ready, nativeLE int
	err := db.QueryRowContext(ctx, `SELECT active_dim, ready, native_little_endian FROM vec_index_state WHERE id = 1`).Scan(&dim, &ready, &nativeLE)
	switch {
	case err == sql.ErrNoRows:
		// First time this database has ever seen Vec1: configure flat/cos
		// once and seed the state row so future opens skip DDL/rebuild
		// reissue (config persists across reopens without reissuing
		// 'rebuild' — verified empirically against the pinned dependency).
		if _, err := db.ExecContext(ctx, vecRebuildFlatCos); err != nil {
			log.Printf("[embed/vec1] rebuild config failed (%v); vector_index falls back to brute force", err)
			return nil
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO vec_index_state(id, active_dim, ready, native_little_endian, updated_at) VALUES (1, 0, 0, 1, ?)`,
			time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("[embed/vec1] state seed failed (%v); vector_index falls back to brute force", err)
			return nil
		}
	case err != nil:
		log.Printf("[embed/vec1] state read failed (%v); vector_index falls back to brute force", err)
		return nil
	default:
		if ready != 0 && (nativeLE != 0) != hostIsLittleEndian() {
			// spec REQ-468: a persisted byte-order marker that doesn't match
			// THIS process's native order means the derived index was built
			// under different byte-order assumptions — never trust it.
			log.Printf("[embed/vec1] persisted byte-order marker does not match this host; vector_index falls back to brute force")
			return nil
		}
		v.activeDim = dim
		v.ready = ready != 0
	}
	return v
}

// VecMaintenanceReport summarizes one VecBackfill or VecReindex pass over
// the derived Vec1 index (spec REQ-461/466/467: lifecycle work must report
// indexed/skipped counts and reasons while verifying source rows survive).
type VecMaintenanceReport struct {
	// Enabled is false when the vec index capability is off or unsupported
	// for this Store — every other field is meaningless in that case.
	Enabled bool
	// TotalRows is the authoritative `embeddings` row count, checked both
	// before and after this pass to prove no source row was lost.
	TotalRows int
	// Indexed is the number of rows newly mirrored into vec_embeddings by
	// this pass.
	Indexed int
	// Skipped is the number of source rows this pass did not index.
	Skipped int
	// SkippedReasons buckets Skipped by cause (e.g. "dimension_mismatch",
	// "undecodable").
	SkippedReasons map[string]int
	// ActiveDim is the dimension this pass indexed against (0 if no valid
	// vector was ever found).
	ActiveDim int
}

// VecBackfill indexes any `embeddings` rows not yet mirrored into the
// derived Vec1 table, establishing the active dimension from the first
// valid source vector (in rowid order) if one has not already been
// established by an incremental dual-write. It is safe to call repeatedly —
// already-indexed rowids are left untouched — and is the operation the
// normal `omnia embed` reconcile run uses to keep the derived index caught
// up and to certify readiness (spec REQ-461/466/467). A disabled/unsupported
// Store is a no-op, never an error.
func (s *Store) VecBackfill(ctx context.Context) (VecMaintenanceReport, error) {
	return s.vecPopulate(ctx, false)
}

// VecReindex discards ONLY the derived Vec1 state — never the authoritative
// `embeddings` rows — and rebuilds it from scratch, choosing the first valid
// source dimension again. This is the explicit recovery/model-change
// operation behind `omnia embed --reindex` (spec REQ-461/466/467).
func (s *Store) VecReindex(ctx context.Context) (VecMaintenanceReport, error) {
	return s.vecPopulate(ctx, true)
}

func (s *Store) vecPopulate(ctx context.Context, discard bool) (VecMaintenanceReport, error) {
	report := VecMaintenanceReport{SkippedReasons: map[string]int{}}
	if !s.vec.healthyOK() {
		return report, nil
	}

	before, err := s.Count(ctx)
	if err != nil {
		return report, fmt.Errorf("embed: vec maintenance: count before: %w", err)
	}
	report.TotalRows = before

	if discard {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM vec_embeddings`); err != nil {
			s.vec.markUnhealthy(err)
			return report, fmt.Errorf("embed: vec reindex: clear derived state: %w", err)
		}
		s.vec.reset()
	}

	existing := map[int64]struct{}{}
	if !discard {
		rows, err := s.db.QueryContext(ctx, `SELECT rowid FROM vec_embeddings`)
		if err != nil {
			s.vec.markUnhealthy(err)
			return report, fmt.Errorf("embed: vec backfill: list existing: %w", err)
		}
		for rows.Next() {
			var rowid int64
			if err := rows.Scan(&rowid); err != nil {
				rows.Close()
				s.vec.markUnhealthy(err)
				return report, fmt.Errorf("embed: vec backfill: scan existing: %w", err)
			}
			existing[rowid] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			s.vec.markUnhealthy(err)
			return report, err
		}
		rows.Close()
	}

	dim := s.vec.dim()

	// Buffer every source row BEFORE issuing any derived INSERT. Store.db
	// has MaxOpenConns(1) (mirrors Search/GraphScoped's own "load everything
	// into memory first" pattern): nesting an ExecContext call inside a
	// still-open Rows loop on the same *sql.DB would try to check out a
	// SECOND connection from a pool capped at one, deadlocking forever. This
	// was caught empirically while implementing VecReindex.
	type srcRow struct {
		rowid   int64
		blob    []byte
		project string
	}
	var srcRowsBuf []srcRow
	srcRows, err := s.db.QueryContext(ctx, `SELECT rowid, vector, COALESCE(project,'') FROM embeddings ORDER BY rowid ASC`)
	if err != nil {
		return report, fmt.Errorf("embed: vec maintenance: scan source: %w", err)
	}
	for srcRows.Next() {
		var r srcRow
		if err := srcRows.Scan(&r.rowid, &r.blob, &r.project); err != nil {
			srcRows.Close()
			return report, fmt.Errorf("embed: vec maintenance: scan row: %w", err)
		}
		srcRowsBuf = append(srcRowsBuf, r)
	}
	if err := srcRows.Err(); err != nil {
		srcRows.Close()
		return report, err
	}
	srcRows.Close()

	for _, r := range srcRowsBuf {
		if _, already := existing[r.rowid]; already {
			continue
		}
		vec, derr := decodeVector(r.blob)
		if derr != nil {
			report.Skipped++
			report.SkippedReasons["undecodable"]++
			continue
		}
		if dim == 0 {
			dim = len(vec)
		}
		if len(vec) != dim {
			report.Skipped++
			report.SkippedReasons["dimension_mismatch"]++
			continue
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO vec_embeddings(rowid, vector, project) VALUES (?, ?, ?)`,
			r.rowid, encodeNativeVector(vec), r.project); err != nil {
			s.vec.markUnhealthy(err)
			return report, fmt.Errorf("embed: vec maintenance: insert derived row: %w", err)
		}
		report.Indexed++
	}

	after, err := s.Count(ctx)
	if err != nil {
		return report, fmt.Errorf("embed: vec maintenance: count after: %w", err)
	}
	if after != before {
		return report, fmt.Errorf("embed: vec maintenance: source row count changed during maintenance (before=%d after=%d); refusing to mark ready", before, after)
	}

	report.ActiveDim = dim
	report.Enabled = true
	if dim > 0 {
		if err := s.vec.persistReady(ctx, s.db, dim); err != nil {
			return report, fmt.Errorf("embed: vec maintenance: persist readiness: %w", err)
		}
	}
	return report, nil
}
