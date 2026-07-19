package cloudstore

import (
	"context"
	"testing"
)

// ─── Hermetic tests: pure cosine ranking + top-k + vector codec (no DB) ──────
//
// These exercise the exact math cloudstore uses to score cloud_embeddings
// rows, factored out of SearchEmbeddings into rankEmbeddingHits so it is
// unit-testable without a live Postgres connection.

func TestRankEmbeddingHitsOrdersByCosineDescending(t *testing.T) {
	query := []float32{1, 0}
	candidates := []embeddingCandidate{
		{SyncID: "low", Vector: []float32{0, 1}},    // orthogonal -> score 0
		{SyncID: "high", Vector: []float32{1, 0}},    // identical -> score 1
		{SyncID: "mid", Vector: []float32{0.7, 0.7}}, // score 0.7
	}

	hits := rankEmbeddingHits(query, candidates, 0)

	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(hits))
	}
	wantOrder := []string{"high", "mid", "low"}
	for i, syncID := range wantOrder {
		if hits[i].SyncID != syncID {
			t.Errorf("position %d: got %q, want %q (full order: %+v)", i, hits[i].SyncID, syncID, hits)
		}
	}
	if hits[0].Score != 1 {
		t.Errorf("expected top score 1, got %v", hits[0].Score)
	}
}

func TestRankEmbeddingHitsTopKCap(t *testing.T) {
	query := []float32{1, 0}
	candidates := []embeddingCandidate{
		{SyncID: "a", Vector: []float32{1, 0}},
		{SyncID: "b", Vector: []float32{0.9, 0.1}},
		{SyncID: "c", Vector: []float32{0.5, 0.5}},
		{SyncID: "d", Vector: []float32{0, 1}},
	}

	hits := rankEmbeddingHits(query, candidates, 2)

	if len(hits) != 2 {
		t.Fatalf("expected top-2 cap, got %d hits: %+v", len(hits), hits)
	}
	if hits[0].SyncID != "a" || hits[1].SyncID != "b" {
		t.Errorf("expected [a b], got %+v", hits)
	}
}

func TestRankEmbeddingHitsKZeroReturnsAll(t *testing.T) {
	query := []float32{1, 0}
	candidates := []embeddingCandidate{
		{SyncID: "a", Vector: []float32{1, 0}},
		{SyncID: "b", Vector: []float32{0, 1}},
	}

	hits := rankEmbeddingHits(query, candidates, 0)

	if len(hits) != 2 {
		t.Fatalf("k<=0 must return all ranked hits, got %d", len(hits))
	}
}

func TestRankEmbeddingHitsSkipsDimMismatch(t *testing.T) {
	query := []float32{1, 0, 0}
	candidates := []embeddingCandidate{
		{SyncID: "same-dim", Vector: []float32{1, 0, 0}},
		{SyncID: "half-migrated", Vector: []float32{1, 0}}, // stale dim after a model change
	}

	hits := rankEmbeddingHits(query, candidates, 0)

	if len(hits) != 1 {
		t.Fatalf("expected dim-mismatched candidate skipped, got %d hits: %+v", len(hits), hits)
	}
	if hits[0].SyncID != "same-dim" {
		t.Errorf("expected only same-dim candidate to survive, got %+v", hits)
	}
}

func TestEncodeDecodeEmbeddingVectorRoundTrip(t *testing.T) {
	v := []float32{0.1, -0.2, 0.3, 1, -1}
	blob, err := encodeEmbeddingVector(v)
	if err != nil {
		t.Fatalf("encodeEmbeddingVector: %v", err)
	}
	got, err := decodeEmbeddingVector(blob)
	if err != nil {
		t.Fatalf("decodeEmbeddingVector: %v", err)
	}
	if len(got) != len(v) {
		t.Fatalf("round trip length mismatch: got %d, want %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("index %d: got %v, want %v", i, got[i], v[i])
		}
	}
}

func TestEncodeEmbeddingVectorRejectsEmpty(t *testing.T) {
	if _, err := encodeEmbeddingVector(nil); err == nil {
		t.Fatal("expected error encoding an empty vector")
	}
}

func TestDecodeEmbeddingVectorRejectsBadLength(t *testing.T) {
	if _, err := decodeEmbeddingVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error decoding a blob whose length is not a multiple of 4")
	}
}

// ─── Postgres-gated tests: skip when CLOUDSTORE_TEST_DSN is absent ──────────
//
// Reuses openTestCloudStore (defined in project_controls_test.go) — the same
// live-Postgres-or-skip convention every other cloudstore integration test
// follows.

func TestUpsertAndSearchEmbeddingsRoundTrip(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	const account = "acc-embed-roundtrip"
	const project = "proj-embed-roundtrip"

	rows := []EmbeddingRow{
		{AccountID: account, Project: project, SyncID: "obs-close", Type: "note", Vector: []float32{1, 0}, Model: "test-model", Dim: 2, ContentHash: "h1"},
		{AccountID: account, Project: project, SyncID: "obs-far", Type: "note", Vector: []float32{0, 1}, Model: "test-model", Dim: 2, ContentHash: "h2"},
	}
	for _, r := range rows {
		if err := cs.UpsertEmbedding(ctx, r); err != nil {
			t.Fatalf("UpsertEmbedding(%s): %v", r.SyncID, err)
		}
	}

	hits, err := cs.SearchEmbeddings(ctx, account, project, []float32{1, 0}, 5)
	if err != nil {
		t.Fatalf("SearchEmbeddings: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected at least 2 hits, got %d", len(hits))
	}
	if hits[0].SyncID != "obs-close" {
		t.Errorf("expected obs-close ranked first, got %+v", hits[0])
	}

	// Upsert again with a different vector for the same key — must replace,
	// not duplicate (ON CONFLICT DO UPDATE on account_id, project, sync_id).
	rows[0].Vector = []float32{0, 1}
	if err := cs.UpsertEmbedding(ctx, rows[0]); err != nil {
		t.Fatalf("UpsertEmbedding replace: %v", err)
	}
	hitsAfter, err := cs.SearchEmbeddings(ctx, account, project, []float32{1, 0}, 5)
	if err != nil {
		t.Fatalf("SearchEmbeddings after replace: %v", err)
	}
	if len(hitsAfter) != len(hits) {
		t.Fatalf("upsert on existing key must replace, not duplicate: before=%d after=%d", len(hits), len(hitsAfter))
	}
}

// TestSearchEmbeddingsScopeIsolation is the hard requirement: SearchEmbeddings
// must NEVER return a row from a different account or a different project,
// even when that row's vector scores highest against the query. Mirrors the
// intent of the PR3 local cross-project recall fix (recallScopeFilter), but
// for the cloud multi-tenant boundary, where a leak crosses accounts too.
func TestSearchEmbeddingsScopeIsolation(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	const ownAccount = "acc-scope-own"
	const ownProject = "proj-scope-own"
	const otherAccount = "acc-scope-other"
	const otherProject = "proj-scope-other"

	query := []float32{1, 0}

	// The OTHER tenant's row is a PERFECT match for the query — if scoping
	// were broken, this is the row that would leak.
	if err := cs.UpsertEmbedding(ctx, EmbeddingRow{
		AccountID: otherAccount, Project: otherProject, SyncID: "other-perfect-match",
		Vector: []float32{1, 0}, Model: "test-model", Dim: 2, ContentHash: "ho",
	}); err != nil {
		t.Fatalf("seed other-tenant row: %v", err)
	}
	// A second variant: same account, DIFFERENT project — also must not leak.
	if err := cs.UpsertEmbedding(ctx, EmbeddingRow{
		AccountID: ownAccount, Project: otherProject, SyncID: "same-account-other-project",
		Vector: []float32{1, 0}, Model: "test-model", Dim: 2, ContentHash: "hp",
	}); err != nil {
		t.Fatalf("seed same-account-other-project row: %v", err)
	}
	// The caller's own row: a weaker match, but IN scope.
	if err := cs.UpsertEmbedding(ctx, EmbeddingRow{
		AccountID: ownAccount, Project: ownProject, SyncID: "own-weak-match",
		Vector: []float32{0.5, 0.5}, Model: "test-model", Dim: 2, ContentHash: "hw",
	}); err != nil {
		t.Fatalf("seed own-tenant row: %v", err)
	}

	hits, err := cs.SearchEmbeddings(ctx, ownAccount, ownProject, query, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings: %v", err)
	}

	for _, h := range hits {
		if h.SyncID == "other-perfect-match" {
			t.Fatalf("CROSS-ACCOUNT LEAK: SearchEmbeddings scoped to (%s,%s) returned a row from (%s,%s): %+v", ownAccount, ownProject, otherAccount, otherProject, hits)
		}
		if h.SyncID == "same-account-other-project" {
			t.Fatalf("CROSS-PROJECT LEAK: SearchEmbeddings scoped to project %q returned a row from project %q: %+v", ownProject, otherProject, hits)
		}
	}
	if len(hits) != 1 || hits[0].SyncID != "own-weak-match" {
		t.Fatalf("expected exactly the in-scope row [own-weak-match], got %+v", hits)
	}
}

func TestUpsertEmbeddingRequiresScopeAndSyncID(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	base := EmbeddingRow{AccountID: "acc", Project: "proj", SyncID: "id", Vector: []float32{1}, Model: "m", Dim: 1}

	missingAccount := base
	missingAccount.AccountID = ""
	if err := cs.UpsertEmbedding(ctx, missingAccount); err == nil {
		t.Error("expected error when account_id is empty")
	}

	missingProject := base
	missingProject.Project = ""
	if err := cs.UpsertEmbedding(ctx, missingProject); err == nil {
		t.Error("expected error when project is empty")
	}

	missingSyncID := base
	missingSyncID.SyncID = ""
	if err := cs.UpsertEmbedding(ctx, missingSyncID); err == nil {
		t.Error("expected error when sync_id is empty")
	}
}

func TestSearchEmbeddingsRequiresScope(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	if _, err := cs.SearchEmbeddings(ctx, "", "proj", []float32{1}, 5); err == nil {
		t.Error("expected error when account_id is empty")
	}
	if _, err := cs.SearchEmbeddings(ctx, "acc", "", []float32{1}, 5); err == nil {
		t.Error("expected error when project is empty")
	}
}
