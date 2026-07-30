package dashboard

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // register "sqlite" driver for the raw inspection below
)

func dbHasVecEmbeddingsTable(t *testing.T, dbPath string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s for inspection: %v", dbPath, err)
	}
	defer db.Close()
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'vec_embeddings'`).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master on %s: %v", dbPath, err)
	}
	return true
}

// TestNewLocalDataSource_VecIndexEnabledCreatesDerivedTable (task 2.17,
// design capability 7): newLocalDataSource must forward Config.VecIndexEnabled
// into embed.OpenStore, so the dashboard's semantic-search embeddings.db
// carries the derived Vec1 table when the capability is on.
func TestNewLocalDataSource_VecIndexEnabledCreatesDerivedTable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	cfg := Config{
		EngramURL:         "http://127.0.0.1:1", // unreachable is fine; only the embeddings store matters here
		EmbeddingsEnabled: true,
		EmbeddingsBaseURL: "http://127.0.0.1:11434",
		EmbeddingsModel:   "jina/jina-embeddings-v2-base-es",
		EmbeddingsDim:     768,
		EmbeddingsDBPath:  dbPath,
		VecIndexEnabled:   true,
	}

	ds := newLocalDataSource(cfg, logger)
	if ds.emb == nil {
		t.Fatal("newLocalDataSource: expected a non-nil embeddings store")
	}
	defer ds.Close()

	if !dbHasVecEmbeddingsTable(t, dbPath) {
		t.Error("newLocalDataSource(VecIndexEnabled=true) must create vec_embeddings in the dashboard's embeddings.db")
	}
}

// TestNewLocalDataSource_VecIndexDisabledStaysFTSSafe proves the default
// (VecIndexEnabled=false) path never creates the derived table — the
// dashboard's semantic/graph searches stay on their existing brute-force
// store methods, byte-for-byte, until those methods themselves route
// through Vec1.
func TestNewLocalDataSource_VecIndexDisabledStaysFTSSafe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	cfg := Config{
		EngramURL:         "http://127.0.0.1:1",
		EmbeddingsEnabled: true,
		EmbeddingsBaseURL: "http://127.0.0.1:11434",
		EmbeddingsModel:   "jina/jina-embeddings-v2-base-es",
		EmbeddingsDim:     768,
		EmbeddingsDBPath:  dbPath,
		// VecIndexEnabled intentionally omitted (zero value = false).
	}

	ds := newLocalDataSource(cfg, logger)
	if ds.emb == nil {
		t.Fatal("newLocalDataSource: expected a non-nil embeddings store")
	}
	defer ds.Close()

	if dbHasVecEmbeddingsTable(t, dbPath) {
		t.Error("newLocalDataSource(VecIndexEnabled=false, the default) must NOT create vec_embeddings")
	}
}
