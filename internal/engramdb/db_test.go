package engramdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/engramdb"
)

// createTestDB builds a throwaway SQLite database in a temp dir with the same
// observations schema as Engram. Returns the data directory (not the file path).
func createTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engram.db")

	// Open read-write-create to populate fixtures.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("createTestDB: open rw: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE observations (
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
	)`)
	if err != nil {
		t.Fatalf("createTestDB: create table: %v", err)
	}

	// Fixture rows. updated_at is unique per row to ensure deterministic ORDER BY.
	fixtures := []struct {
		id        int
		typ       string
		title     string
		content   string
		project   string // empty string → inserted as NULL
		scope     string
		topicKey  string
		createdAt string
		updatedAt string
		deleted   bool
	}{
		// workly: 2 live (arch + decision), 1 deleted
		{1, "architecture", "Auth design", "content-1", "workly", "project", "arch/auth", "2024-01-01 00:00:00", "2024-01-02 00:00:00", false},
		{2, "decision", "Use JWT", "content-2", "workly", "project", "dec/jwt", "2024-01-03 00:00:00", "2024-01-04 00:00:00", false},
		{5, "decision", "Soft-deleted row", "content-5", "workly", "project", "", "2024-01-09 00:00:00", "2024-01-10 00:00:00", true},
		// trackly: 2 live (bugfix + arch)
		{3, "bugfix", "Fixed N+1", "content-3", "trackly", "project", "", "2024-01-05 00:00:00", "2024-01-06 00:00:00", false},
		{4, "architecture", "DB schema", "content-4", "trackly", "project", "", "2024-01-07 00:00:00", "2024-01-08 00:00:00", false},
		// homelab: 1 live, empty type (to test type-exclusion in Types/TypesByProject)
		{6, "", "No type", "content-6", "homelab", "project", "", "2024-01-11 00:00:00", "2024-01-12 00:00:00", false},
		// NULL project: live row, should appear in List (no project filter) but NOT in Projects()
		{7, "decision", "Null-project row", "content-7", "", "project", "", "2024-01-13 00:00:00", "2024-01-14 00:00:00", false},
	}

	for _, f := range fixtures {
		// Insert project as NULL when the fixture string is empty.
		var projectVal any
		if f.project != "" {
			projectVal = f.project
		}
		var deletedAt any
		if f.deleted {
			deletedAt = "2024-01-11 00:00:00"
		}
		_, err = db.Exec(
			`INSERT INTO observations
			 (id, type, title, content, project, scope, topic_key, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.id, f.typ, f.title, f.content,
			projectVal, f.scope, f.topicKey,
			f.createdAt, f.updatedAt, deletedAt,
		)
		if err != nil {
			t.Fatalf("createTestDB: insert fixture id=%d: %v", f.id, err)
		}
	}

	return dir
}

func openTestDB(t *testing.T, dataDir string) *engramdb.DB {
	t.Helper()
	db, err := engramdb.Open(dataDir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dataDir, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- Projects ---

func TestProjects_ExcludesDeletedAndNullProject(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	projects, err := db.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}

	byName := map[string]int{}
	for _, p := range projects {
		byName[p.Name] = p.Count
	}

	cases := []struct {
		name string
		want int
	}{
		{"workly", 2},   // ids 1,2 live; id 5 deleted → excluded
		{"trackly", 2},  // ids 3,4
		{"homelab", 1},  // id 6
	}
	for _, tc := range cases {
		if got := byName[tc.name]; got != tc.want {
			t.Errorf("Projects[%q]: got %d, want %d", tc.name, got, tc.want)
		}
	}

	// NULL project row (id 7) must not appear.
	if _, ok := byName[""]; ok {
		t.Error("Projects: empty/null project should not appear in results")
	}
}

// --- TypesByProject ---

func TestTypesByProject_FiltersCorrectly(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	types, err := db.TypesByProject(ctx, "workly")
	if err != nil {
		t.Fatalf("TypesByProject: %v", err)
	}

	// workly live: id 1 (architecture), id 2 (decision); id 5 deleted.
	byName := map[string]int{}
	for _, tc := range types {
		byName[tc.Name] = tc.Count
	}

	if byName["architecture"] != 1 {
		t.Errorf("TypesByProject workly[architecture]: got %d, want 1", byName["architecture"])
	}
	if byName["decision"] != 1 {
		t.Errorf("TypesByProject workly[decision]: got %d, want 1 (deleted id 5 excluded)", byName["decision"])
	}
	// Soft-deleted id 5 must not inflate the count.
	if total := byName["architecture"] + byName["decision"]; total != 2 {
		t.Errorf("TypesByProject workly total: got %d, want 2", total)
	}
}

func TestTypesByProject_ExcludesEmptyType(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	// homelab has only id 6 with empty type; result should be empty.
	types, err := db.TypesByProject(ctx, "homelab")
	if err != nil {
		t.Fatalf("TypesByProject homelab: %v", err)
	}
	for _, tc := range types {
		if tc.Name == "" {
			t.Errorf("TypesByProject: empty type should not appear in results")
		}
	}
}

// --- Types ---

func TestTypes_CountsAcrossProjects(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	types, err := db.Types(ctx)
	if err != nil {
		t.Fatalf("Types: %v", err)
	}

	byName := map[string]int{}
	for _, tc := range types {
		byName[tc.Name] = tc.Count
	}

	// Live obs with types:
	//   architecture: ids 1 (workly), 4 (trackly) → 2
	//   decision:     ids 2 (workly), 7 (null-project) → 2  (id 5 deleted)
	//   bugfix:       id 3 (trackly) → 1
	//   empty type:   id 6 (homelab) → excluded
	if byName["architecture"] != 2 {
		t.Errorf("Types[architecture]: got %d, want 2", byName["architecture"])
	}
	if byName["decision"] != 2 {
		t.Errorf("Types[decision]: got %d, want 2 (id 5 deleted, id 7 null-project counted)", byName["decision"])
	}
	if byName["bugfix"] != 1 {
		t.Errorf("Types[bugfix]: got %d, want 1", byName["bugfix"])
	}
	if _, ok := byName[""]; ok {
		t.Error("Types: empty type must not appear in results")
	}
}

// --- List ---

func TestList_ProjectFilter(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	obs, err := db.List(ctx, engramdb.Filter{Project: "workly", Limit: 100})
	if err != nil {
		t.Fatalf("List(workly): %v", err)
	}
	// ids 1, 2 live; id 5 deleted → excluded.
	if len(obs) != 2 {
		t.Errorf("List(workly): got %d rows, want 2", len(obs))
	}
	for _, o := range obs {
		if o.Project != "workly" {
			t.Errorf("List(workly): got project %q, want 'workly'", o.Project)
		}
		if o.ID == 5 {
			t.Error("List: returned soft-deleted row id=5")
		}
	}
}

func TestList_ProjectAndTypeFilter(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	obs, err := db.List(ctx, engramdb.Filter{Project: "workly", Type: "architecture", Limit: 100})
	if err != nil {
		t.Fatalf("List(workly, architecture): %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("List(workly, architecture): got %d rows, want 1", len(obs))
	}
	if obs[0].ID != 1 {
		t.Errorf("List(workly, architecture): got ID %d, want 1", obs[0].ID)
	}
}

func TestList_ExcludesDeleted(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	obs, _ := db.List(ctx, engramdb.Filter{Limit: 100})
	for _, o := range obs {
		if o.ID == 5 {
			t.Error("List: returned soft-deleted row id=5")
		}
	}
}

func TestList_AllLiveCount(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	// Live: ids 1,2,3,4,6,7 (id 5 deleted) → 6 rows.
	obs, err := db.List(ctx, engramdb.Filter{Limit: 200})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(obs) != 6 {
		t.Errorf("List(all): got %d rows, want 6", len(obs))
	}
}

func TestList_LimitAndOffset(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	page1, err := db.List(ctx, engramdb.Filter{Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("List(page1): %v", err)
	}
	page2, err := db.List(ctx, engramdb.Filter{Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("List(page2): %v", err)
	}

	if len(page1) != 3 {
		t.Errorf("page1: got %d rows, want 3", len(page1))
	}
	if len(page1)+len(page2) != 6 {
		t.Errorf("page1+page2: got %d rows total, want 6", len(page1)+len(page2))
	}

	// Pages must not overlap (no duplicate IDs).
	seen := map[int]bool{}
	for _, o := range page1 {
		seen[o.ID] = true
	}
	for _, o := range page2 {
		if seen[o.ID] {
			t.Errorf("ID %d appears on both pages", o.ID)
		}
	}
}

func TestList_SQLInjectionTreatedAsLiteral(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	// A classic injection payload in the project field should return zero rows
	// (no project named "' OR '1'='1") and must not error.
	obs, err := db.List(ctx, engramdb.Filter{Project: "' OR '1'='1"})
	if err != nil {
		t.Errorf("injection attempt caused error: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("injection: got %d rows, want 0", len(obs))
	}
}

// --- Count ---

func TestCount_MatchesListLen(t *testing.T) {
	dir := createTestDB(t)
	db := openTestDB(t, dir)
	ctx := context.Background()

	cases := []struct {
		name   string
		filter engramdb.Filter
		want   int
	}{
		{"workly", engramdb.Filter{Project: "workly"}, 2},
		{"trackly", engramdb.Filter{Project: "trackly"}, 2},
		{"homelab", engramdb.Filter{Project: "homelab"}, 1},
		{"all", engramdb.Filter{}, 6},
		{"workly+architecture", engramdb.Filter{Project: "workly", Type: "architecture"}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := db.Count(ctx, tc.filter)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if n != tc.want {
				t.Errorf("Count(%q): got %d, want %d", tc.name, n, tc.want)
			}
		})
	}
}
