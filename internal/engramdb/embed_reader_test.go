package engramdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/engramdb"
)

// createSyncTestDB builds a throwaway DB whose rows carry sync_id values, so the
// embedding-oriented readers (ListForEmbedding, ListByIDs) can be exercised.
func createSyncTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engram.db")

	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("createSyncTestDB: open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE observations (
		id             INTEGER PRIMARY KEY,
		sync_id        TEXT,
		session_id     TEXT,
		type           TEXT,
		title          TEXT NOT NULL DEFAULT '',
		content        TEXT,
		tool_name      TEXT,
		project        TEXT,
		scope          TEXT,
		topic_key      TEXT,
		revision_count INTEGER DEFAULT 0,
		created_at     TEXT,
		updated_at     TEXT,
		deleted_at     TEXT
	)`); err != nil {
		t.Fatalf("createSyncTestDB: schema: %v", err)
	}

	rows := []struct {
		id        int
		syncID    string
		content   string
		deletedAt any
	}{
		{1, "obs-aaa", "content one", nil},
		{2, "obs-bbb", "content two", nil},
		{3, "obs-ccc", "content deleted", "2024-02-01 00:00:00"},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO observations (id, sync_id, type, title, content, project, scope, topic_key, updated_at, deleted_at)
			 VALUES (?, ?, 'decision', ?, ?, 'p1', 'project', ?, '2024-01-02 00:00:00', ?)`,
			r.id, r.syncID, "Title "+r.syncID, r.content, "tk-"+r.syncID, r.deletedAt,
		); err != nil {
			t.Fatalf("createSyncTestDB: insert %d: %v", r.id, err)
		}
	}
	return dir
}

func TestListForEmbedding_PopulatesSyncID_ExcludesDeleted(t *testing.T) {
	dir := createSyncTestDB(t)
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	obs, err := db.ListForEmbedding(context.Background())
	if err != nil {
		t.Fatalf("ListForEmbedding: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("ListForEmbedding: got %d rows, want 2 (deleted excluded)", len(obs))
	}
	for _, o := range obs {
		if o.SyncID == "" {
			t.Errorf("row id=%d has empty SyncID; ListForEmbedding must populate it", o.ID)
		}
		if o.ID == 3 {
			t.Error("ListForEmbedding returned soft-deleted row id=3")
		}
		if o.Content == "" || o.TopicKey == "" {
			t.Errorf("row id=%d missing content/topic_key: %+v", o.ID, o)
		}
	}
}

func TestListByIDs_FullContent_ExcludesDeleted(t *testing.T) {
	dir := createSyncTestDB(t)
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	obs, err := db.ListByIDs(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	// id 3 is soft-deleted → excluded even though requested.
	if len(obs) != 2 {
		t.Fatalf("ListByIDs: got %d rows, want 2", len(obs))
	}
	for _, o := range obs {
		if o.ID == 3 {
			t.Error("ListByIDs returned soft-deleted row id=3")
		}
		if o.SyncID == "" || o.Content == "" {
			t.Errorf("row id=%d missing SyncID/Content: %+v", o.ID, o)
		}
	}
}

func TestListByIDs_EmptyInput(t *testing.T) {
	dir := createSyncTestDB(t)
	db, err := engramdb.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	obs, err := db.ListByIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListByIDs(nil): %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("ListByIDs(nil): got %d rows, want 0", len(obs))
	}
}
