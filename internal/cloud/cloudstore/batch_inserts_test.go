package cloudstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// ─── H4 (perf): batch per-item INSERT loops into single multi-row INSERTs ───
//
// The sync ingestion hot path (WriteChunk / InsertMutationBatch) issued one
// INSERT per session/mutation/chunk instead of a single multi-row INSERT,
// turning a 300-observation chunk push into ~300 sequential round trips
// inside one transaction. This file covers:
//
//  1. A genuine RED/GREEN regression test (insertCountingDriver, fake driver,
//     no real Postgres) that counts prepared INSERT statements — this is the
//     ONLY assertion that can meaningfully fail before the fix and pass after
//     it, since the whole point of a perf rewrite is that RESULTS stay
//     identical (see strict-tdd.md "Approval Testing" — behavior-preserving
//     refactors get approval tests that pass before AND after; the
//     query-count test is where the "spec says behavior should change" case
//     applies).
//  2. Approval tests (real Postgres via newTokenTestStore) that lock in the
//     exact correctness invariants the rewrite must preserve: all rows
//     present, RETURNING seq mapped back to input order, ON CONFLICT DO
//     NOTHING idempotency on re-push, and empty-batch no-op.

// ─── 1. Fake counting driver — proves round trips collapsed ─────────────────

// insertCountingDriver is a minimal fake database/sql driver used ONLY to
// count how many INSERT statements get prepared for a given batch. It does
// not persist anything real — data correctness is covered separately by the
// newTokenTestStore-backed integration tests below. Kept deliberately
// separate from partialFailDriver (project_controls_test.go) to avoid
// coupling two unrelated tests to the same fake driver's shared state.
type insertCountingDriver struct {
	insertCount int
}

var insertCountingDriverSingleton = &insertCountingDriver{}

func init() {
	sql.Register("cloudstore-insert-counting-driver", insertCountingDriverSingleton)
}

// resetInsertCountingDriver resets the singleton so -count=2 and repeated
// runs are safe.
func resetInsertCountingDriver() {
	insertCountingDriverSingleton.insertCount = 0
}

func (d *insertCountingDriver) Open(_ string) (driver.Conn, error) {
	return &insertCountingConn{d: d}, nil
}

type insertCountingConn struct{ d *insertCountingDriver }

func (c *insertCountingConn) Prepare(query string) (driver.Stmt, error) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "INSERT") {
		c.d.insertCount++
	}
	return &insertCountingStmt{}, nil
}
func (c *insertCountingConn) Close() error              { return nil }
func (c *insertCountingConn) Begin() (driver.Tx, error) { return insertCountingTx{}, nil }

type insertCountingTx struct{}

func (insertCountingTx) Commit() error   { return nil }
func (insertCountingTx) Rollback() error { return nil }

type insertCountingStmt struct{}

func (s *insertCountingStmt) Close() error  { return nil }
func (s *insertCountingStmt) NumInput() int { return -1 }
func (s *insertCountingStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

// Query fakes a RETURNING seq result. cloud_mutations INSERTs always send
// exactly 5 bind params per row (project, entity, entity_key, op, payload),
// so len(args)/5 recovers how many rows were in the VALUES list — whether
// the caller batches 1 row per statement (old behavior) or many (new
// behavior), the fake returns exactly that many seq rows, so production
// code's row-count safety check is always satisfied.
func (s *insertCountingStmt) Query(args []driver.Value) (driver.Rows, error) {
	rowCount := len(args) / 5
	if rowCount < 1 {
		rowCount = 1
	}
	return &insertCountingRows{remaining: rowCount}, nil
}

type insertCountingRows struct {
	remaining int
	next      int64
}

func (r *insertCountingRows) Columns() []string { return []string{"seq"} }
func (r *insertCountingRows) Close() error      { return nil }
func (r *insertCountingRows) Next(dest []driver.Value) error {
	if r.remaining <= 0 {
		return io.EOF
	}
	r.next++
	dest[0] = r.next
	r.remaining--
	return nil
}

// TestInsertMutationBatchIssuesConstantInsertStatementsRegardlessOfBatchSize
// is the RED/GREEN anchor for H4. Before the fix, a 300-entry single-project
// batch issues 300 individual `INSERT ... RETURNING seq` statements (one per
// entry) plus 1 `INSERT ... ON CONFLICT` for the materialized chunk = 301+
// prepared INSERT statements. After the fix, both loops collapse into a
// small constant number of statements regardless of batch size.
func TestInsertMutationBatchIssuesConstantInsertStatementsRegardlessOfBatchSize(t *testing.T) {
	resetInsertCountingDriver()
	d := insertCountingDriverSingleton

	db, err := sql.Open("cloudstore-insert-counting-driver", "dsn")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cs := &CloudStore{db: db}

	const n = 300
	batch := make([]MutationEntry, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, MutationEntry{
			Project:   "proj-a",
			Entity:    store.SyncEntityObservation,
			EntityKey: fmt.Sprintf("obs-%04d", i),
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(fmt.Sprintf(`{"sync_id":"obs-%04d","session_id":"sess-1","type":"note","title":"t","content":"c","scope":"project"}`, i)),
		})
	}

	seqs, err := cs.InsertMutationBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}
	if len(seqs) != n {
		t.Fatalf("expected %d seqs, got %d", n, len(seqs))
	}

	if d.insertCount > 5 {
		t.Fatalf("expected a small constant number of INSERT statements for a %d-entry single-project batch, got %d prepared INSERT statements (one-row-per-item regression)", n, d.insertCount)
	}
}

// ─── 2. Approval tests (real Postgres) — correctness invariants preserved ───

func countProjectSessions(t *testing.T, cs *CloudStore, project string) int {
	t.Helper()
	var count int
	if err := cs.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cloud_project_sessions WHERE project_name = $1`, project).Scan(&count); err != nil {
		t.Fatalf("count project sessions: %v", err)
	}
	return count
}

func countChunksForProject(t *testing.T, cs *CloudStore, project string) int {
	t.Helper()
	var count int
	if err := cs.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cloud_chunks WHERE project_name = $1`, project).Scan(&count); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	return count
}

// TestIndexChunkSessionsWithBatchInsertsAllSessionRowsAndIsIdempotent covers
// the "chunk with M sessions" requirement: all M session-index rows must be
// present after one call, and re-indexing the identical payload must not
// duplicate rows (ON CONFLICT (project_name, session_id) DO NOTHING).
func TestIndexChunkSessionsWithBatchInsertsAllSessionRowsAndIsIdempotent(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	const project = "test-batch-sessions"

	const sessionCount = 120
	sessions := make([]map[string]string, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		sessions = append(sessions, map[string]string{"id": fmt.Sprintf("sess-%03d", i)})
	}
	payload, err := json.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := cs.indexChunkSessionsWith(ctx, cs.db, project, payload); err != nil {
		t.Fatalf("indexChunkSessionsWith: %v", err)
	}
	if count := countProjectSessions(t, cs, project); count != sessionCount {
		t.Fatalf("expected %d session-index rows in one go, got %d", sessionCount, count)
	}

	// Re-index the exact same payload — must be idempotent.
	if err := cs.indexChunkSessionsWith(ctx, cs.db, project, payload); err != nil {
		t.Fatalf("re-index indexChunkSessionsWith: %v", err)
	}
	if count := countProjectSessions(t, cs, project); count != sessionCount {
		t.Fatalf("expected re-index to be idempotent (no dup rows), got %d rows (want %d)", count, sessionCount)
	}
}

// TestIndexChunkSessionsWithEmptyPayloadIsNoOp guards the "empty input =
// no-op" requirement — no malformed zero-row VALUES statement, no error.
func TestIndexChunkSessionsWithEmptyPayloadIsNoOp(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	const project = "test-batch-sessions-empty"

	payload, err := json.Marshal(map[string]any{"sessions": []map[string]string{}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := cs.indexChunkSessionsWith(ctx, cs.db, project, payload); err != nil {
		t.Fatalf("indexChunkSessionsWith with no sessions must be a no-op, got error: %v", err)
	}
	if count := countProjectSessions(t, cs, project); count != 0 {
		t.Fatalf("expected 0 session-index rows for empty payload, got %d", count)
	}
}

// TestInsertMaterializedMutationsBatchInsertsAllEntriesInOrder covers the
// insertMaterializedMutations hot path used by WriteChunk: all entries must
// land in cloud_mutations, in the same order they were provided (verified
// via ORDER BY seq, since seq is assigned in VALUES-list order).
func TestInsertMaterializedMutationsBatchInsertsAllEntriesInOrder(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	const project = "test-batch-materialized"

	const n = 300
	entries := make([]MutationEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, MutationEntry{
			Project:   project,
			Entity:    store.SyncEntityObservation,
			EntityKey: fmt.Sprintf("obs-%04d", i),
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(fmt.Sprintf(`{"sync_id":"obs-%04d"}`, i)),
		})
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertMaterializedMutations(ctx, tx, entries); err != nil {
		t.Fatalf("insertMaterializedMutations: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rows, err := cs.db.QueryContext(ctx, `SELECT entity_key FROM cloud_mutations WHERE project = $1 ORDER BY seq ASC`, project)
	if err != nil {
		t.Fatalf("query mutations: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(keys) != n {
		t.Fatalf("expected %d materialized mutations, got %d", n, len(keys))
	}
	for i, key := range keys {
		want := fmt.Sprintf("obs-%04d", i)
		if key != want {
			t.Fatalf("mutation order mismatch at index %d: got %q want %q", i, key, want)
		}
	}
}

// TestInsertMaterializedMutationsEmptyIsNoOp guards the empty-input no-op
// requirement for the plain-insert (no RETURNING, no ON CONFLICT) loop.
func TestInsertMaterializedMutationsEmptyIsNoOp(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertMaterializedMutations(ctx, tx, nil); err != nil {
		t.Fatalf("insertMaterializedMutations with no entries must be a no-op, got error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestInsertMutationBatchReturnsSeqsMappedToInputOrderAndChunkRowStaysIdempotentOnRepush
// covers: (a) InsertMutationBatch returns exactly N seqs for a large
// single-project batch, strictly increasing and correctly mapped back to the
// input order (the RETURNING seq contract a multi-row INSERT must uphold
// without relying on undocumented row ordering); (b) re-pushing the
// identical batch appends new journal rows (cloud_mutations is an
// append-only log — that's expected, not a bug) but the derived chunk row
// stays idempotent (ON CONFLICT (project_name, chunk_id) DO NOTHING).
func TestInsertMutationBatchReturnsSeqsMappedToInputOrderAndChunkRowStaysIdempotentOnRepush(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	const project = "test-batch-mutation-push"

	const n = 300
	batch := make([]MutationEntry, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, MutationEntry{
			Project:   project,
			Entity:    store.SyncEntityObservation,
			EntityKey: fmt.Sprintf("obs-%04d", i),
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(fmt.Sprintf(`{"sync_id":"obs-%04d","session_id":"sess-batch","type":"note","title":"t","content":"c","scope":"project"}`, i)),
		})
	}

	seqs, err := cs.InsertMutationBatch(ctx, batch)
	if err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}
	if len(seqs) != n {
		t.Fatalf("expected %d seqs, got %d", n, len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("expected strictly increasing seqs matching input order, got %v at index %d", seqs, i)
		}
	}
	for i, seq := range seqs {
		var entityKey string
		if err := cs.db.QueryRowContext(ctx, `SELECT entity_key FROM cloud_mutations WHERE seq = $1`, seq).Scan(&entityKey); err != nil {
			t.Fatalf("lookup seq %d: %v", seq, err)
		}
		if want := batch[i].EntityKey; entityKey != want {
			t.Fatalf("seq[%d]=%d maps to entity_key %q, want %q (input order broken)", i, seq, entityKey, want)
		}
	}

	if chunkCount := countChunksForProject(t, cs, project); chunkCount != 1 {
		t.Fatalf("expected exactly 1 materialized chunk row, got %d", chunkCount)
	}

	// Re-push the identical batch.
	seqs2, err := cs.InsertMutationBatch(ctx, batch)
	if err != nil {
		t.Fatalf("InsertMutationBatch (repush): %v", err)
	}
	if len(seqs2) != n {
		t.Fatalf("expected %d seqs on repush, got %d", n, len(seqs2))
	}
	if chunkCount := countChunksForProject(t, cs, project); chunkCount != 1 {
		t.Fatalf("expected chunk row to stay idempotent on repush (ON CONFLICT DO NOTHING), got %d", chunkCount)
	}
}

// TestInsertMutationBatchEmptyBatchIsNoOp guards the empty-input contract at
// the public API surface: no error, no rows, empty seqs slice.
func TestInsertMutationBatchEmptyBatchIsNoOp(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()
	const project = "test-batch-mutation-push-empty"

	seqs, err := cs.InsertMutationBatch(ctx, nil)
	if err != nil {
		t.Fatalf("InsertMutationBatch with nil batch must be a no-op, got error: %v", err)
	}
	if len(seqs) != 0 {
		t.Fatalf("expected 0 seqs for empty batch, got %d", len(seqs))
	}
	if chunkCount := countChunksForProject(t, cs, project); chunkCount != 0 {
		t.Fatalf("expected 0 chunk rows for empty batch, got %d", chunkCount)
	}
}
