package embed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/vec1"
)

// This file implements v0.4's `memory-at-rest-security` migration (design
// capability 4, "mirrors datadir.Migrate", spec REQ-435) for
// embeddings.db — the sibling of internal/store/migrate_encryption.go's
// omnia.db migration. Same non-destructive, reversible, atomic-rename
// shape; independent implementation (not shared code) because
// internal/store and internal/embed must never import each other
// (existing architecture guardrail) and the two schemas differ (FTS5 vs
// Vec1 virtual tables).

// EncryptionMigrationResult reports the outcome of one MigrateToEncrypted/
// MigrateToPlaintext call.
type EncryptionMigrationResult struct {
	AlreadyEncrypted bool
	AlreadyPlaintext bool
	RowsBefore       int
	RowsAfter        int
	BackupPath       string
}

const embeddingsRowCountQuery = `SELECT COUNT(*) FROM embeddings`

// checkpointAndTruncateWAL mirrors internal/store's own helper of the same
// name (independent copy, not shared — see this file's doc above): runs
// `PRAGMA wal_checkpoint(TRUNCATE)` on db, merging any live write-ahead log
// fully into the main database file and truncating away the WAL sidecar.
// This MUST run before this file's migration functions read a row count
// off a migration SOURCE and copy it into a brand-new destination file: a
// source left with a live, un-checkpointed WAL (completely normal — e.g. a
// daemon stopped via SIGKILL/`launchctl bootout` without a final
// checkpoint) would otherwise leave a stale `dbPath-wal`/`dbPath-shm`
// sidecar pair sitting on disk, structurally incompatible with whatever
// NEW file later takes over dbPath's name via this file's atomic-rename
// step below — the confirmed root cause of a real production incident
// (see internal/store/migrate_encryption.go's identical helper for the
// full rationale). Checkpointing the source is safe here: the migration
// never writes new application data to the source, it only merges the
// source's OWN existing WAL into itself.
//
// `wal_checkpoint(TRUNCATE)` returns (busy, log, checkpointed); busy != 0
// means SQLITE_BUSY was hit — some other connection held a lock that
// prevented a full checkpoint. We abort loudly rather than proceed with a
// source that still has un-merged WAL data, exactly like every other
// failure mode in this file already aborts before touching anything.
func checkpointAndTruncateWAL(ctx context.Context, db *sql.DB) error {
	var busy, log, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return fmt.Errorf("wal_checkpoint(TRUNCATE): %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("wal_checkpoint(TRUNCATE) reported busy=%d (a concurrent connection prevented a full checkpoint); refusing to migrate a source with un-merged WAL data", busy)
	}
	return nil
}

// checkpointAndTruncateWALConn is checkpointAndTruncateWAL's twin for
// callers holding a raw *sqlite3.Conn (decryptToPlainFile's source
// connection, opened directly via sqlite3.OpenContext rather than
// database/sql) — same rationale, same abort-on-busy contract.
func checkpointAndTruncateWALConn(c *sqlite3.Conn) error {
	stmt, _, err := c.Prepare("PRAGMA wal_checkpoint(TRUNCATE)")
	if err != nil {
		return fmt.Errorf("wal_checkpoint(TRUNCATE): prepare: %w", err)
	}
	defer stmt.Close()
	if !stmt.Step() {
		if serr := stmt.Err(); serr != nil {
			return fmt.Errorf("wal_checkpoint(TRUNCATE): %w", serr)
		}
		return fmt.Errorf("wal_checkpoint(TRUNCATE): no result row")
	}
	if busy := stmt.ColumnInt(0); busy != 0 {
		return fmt.Errorf("wal_checkpoint(TRUNCATE) reported busy=%d (a concurrent connection prevented a full checkpoint); refusing to migrate a source with un-merged WAL data", busy)
	}
	return nil
}

// removeStaleWALSidecars best-effort removes any dbPath-wal/dbPath-shm
// files. Called immediately before this file's atomic rename-into-place
// steps, as defense-in-depth alongside checkpointAndTruncateWAL/
// checkpointAndTruncateWALConn above — see internal/store's identical
// helper for the full rationale. Any error (including a missing file) is
// ignored: this is best-effort cleanup, not verification.
func removeStaleWALSidecars(dbPath string) {
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}

// removeStaleTempFile best-effort removes path's own main file together
// with its `-wal`/`-shm` sidecars. Mirrors internal/store's own helper of
// the same name (independent copy, not shared — see file doc above): every
// temp/intermediate path this file re-creates (MigrateToEncrypted's and
// MigrateToPlaintext's own tmpPath, and BOTH of RotateKey's tmpPlainPath
// and tmpPath) uses a FIXED, deterministic name — never os.CreateTemp's
// randomized form — so that exact name gets reused verbatim across
// separate calls: a RotateKey retried after an aborted first attempt, or
// simply rotating the key twice in a row (both normal, supported
// sequences). Clearing only the main file is not enough — any live
// sidecars left behind by whatever previously wrote to that exact path
// must go too, before the next write reuses it. Best-effort: any error
// (including a missing file) is ignored.
func removeStaleTempFile(path string) {
	_ = os.Remove(path)
	removeStaleWALSidecars(path)
}

// checkpointPlainFileWAL opens a plain (non-adiantum) SQLite file at path
// via the ncruces driver (mirroring this file's own plaintext-read
// convention elsewhere — see countRowsWith's callers) and runs
// checkpointAndTruncateWAL on it, then closes. Used by RotateKey to
// explicitly checkpoint its plaintext intermediate (tmpPlainPath) between
// decryptToPlainFile writing it and encryptNewFile reading it back as a
// source — the same checkpoint discipline already applied to every other
// source-then-consume hop in this file — rather than relying on
// decryptToPlainFile's own Backup call having already left its destination
// connection fully checkpointed on close.
func checkpointPlainFileWAL(ctx context.Context, path string, includeVec1 bool) error {
	db, err := ncrucesdriver.Open("file:"+path, migrateInit("", includeVec1))
	if err != nil {
		return fmt.Errorf("open %s for checkpoint: %w", path, err)
	}
	defer db.Close()
	return checkpointAndTruncateWAL(ctx, db)
}

// isPlaintextSQLiteFile mirrors internal/store's own helper of the same
// name (independent copy, not shared — see file doc above).
func isPlaintextSQLiteFile(path string) (exists bool, plaintext bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	header := make([]byte, 16)
	n, rerr := f.Read(header)
	if rerr != nil && n == 0 {
		return true, false, nil
	}
	return true, n >= 15 && string(header[:15]) == "SQLite format 3", nil
}

// migrateInit builds the per-connection init callback shared by the
// migration helpers below: PRAGMA hexkey (when key != ""), then
// vec1.Register (when includeVec1) — same composition order OpenStore's
// own openEncryptedEmbedDB documents.
func migrateInit(key string, includeVec1 bool) func(*sqlite3.Conn) error {
	return func(c *sqlite3.Conn) error {
		if key != "" {
			if err := c.Exec("PRAGMA hexkey='" + key + "'"); err != nil {
				return err
			}
		}
		if includeVec1 {
			return vec1.Register(c)
		}
		return nil
	}
}

// encryptNewFile creates a brand-new adiantum-encrypted embeddings.db file
// at dstPath containing a full copy of the SQLite database readable
// (without any key) at plainSrcURI. The destination's encryption key is
// established via `PRAGMA hexkey=...` on a connection this function opens
// and controls directly, immediately after connecting — NEVER via a URI
// parameter (the adiantum package's own docs warn a URI-embedded key is
// visible via vfs.Filename.URIParameters to any code that inspects the
// open filename; the PRAGMA form never appears there, mirroring
// openEncryptedEmbedDB's own read-path pattern). includeVec1 registers the
// Vec1 extension on the destination when vector_index.enabled is ALSO
// true, so an existing vec_embeddings virtual table survives the copy. The
// actual copy uses SQLite's online backup/restore API (Conn.Restore) — the
// same mechanism VACUUM INTO is itself implemented on top of — so the
// resulting bytes are equivalent, but the destination connection (and its
// key) are ours to control from the very first byte, unlike VACUUM INTO's
// implicit, un-hookable target-file creation.
func encryptNewFile(ctx context.Context, dstPath, hexKey, plainSrcURI string, includeVec1 bool) error {
	dst, err := sqlite3.OpenContext(ctx, "file:"+dstPath+"?vfs=adiantum")
	if err != nil {
		return fmt.Errorf("open new encrypted target: %w", err)
	}
	defer dst.Close()
	if err := dst.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
		return fmt.Errorf("set encryption key on new target: %w", err)
	}
	if includeVec1 {
		if err := vec1.Register(dst); err != nil {
			return fmt.Errorf("register vec1 on new target: %w", err)
		}
	}
	if err := dst.Restore("main", plainSrcURI); err != nil {
		return fmt.Errorf("restore into new encrypted target: %w", err)
	}
	return nil
}

// decryptToPlainFile opens srcPath (adiantum-encrypted with hexKey) and
// backs its full content up into a brand-new plaintext file at dstPath.
// This is RotateKey's intermediate step: the OLD key is only ever
// established via a PRAGMA on a connection this function fully controls
// (never a URI parameter), and the NEW key (set separately by
// encryptNewFile) never has to coexist with the old key in the same
// connection, SQL statement, or URI.
func decryptToPlainFile(ctx context.Context, srcPath, hexKey, dstPath string, includeVec1 bool) error {
	src, err := sqlite3.OpenContext(ctx, "file:"+srcPath+"?vfs=adiantum")
	if err != nil {
		return fmt.Errorf("open encrypted source: %w", err)
	}
	defer src.Close()
	if err := src.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
		return fmt.Errorf("set encryption key on source: %w", err)
	}
	if includeVec1 {
		if err := vec1.Register(src); err != nil {
			return fmt.Errorf("register vec1 on source: %w", err)
		}
	}
	if err := checkpointAndTruncateWALConn(src); err != nil {
		return fmt.Errorf("checkpoint encrypted source WAL before backup: %w", err)
	}
	if err := src.Backup("main", "file:"+dstPath+"?vfs=os"); err != nil {
		return fmt.Errorf("back up source into plaintext target: %w", err)
	}
	return nil
}

// MigrateToEncrypted encrypts the embeddings.db file at dbPath in place,
// mirroring internal/store.MigrateToEncrypted exactly (backs it up into a
// brand-new adiantum-encrypted sibling temp file via encryptNewFile's
// Conn.Restore, never a VACUUM INTO URI-embedded key; verifies row count;
// atomic rename with a retained timestamped .bak). includeVec1 registers
// the Vec1 extension on the source connection when vector_index.enabled is
// ALSO true, so an existing vec_embeddings virtual table survives the copy.
//
// A missing dbPath and an already-encrypted dbPath are both no-ops.
func MigrateToEncrypted(ctx context.Context, dbPath, hexKey string, includeVec1 bool) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil
	}
	if !plaintext {
		return EncryptionMigrationResult{AlreadyEncrypted: true}, nil
	}

	var srcDB *sql.DB
	if includeVec1 {
		srcDB, err = ncrucesdriver.Open("file:"+dbPath, vec1.Register)
	} else {
		srcDB, err = ncrucesdriver.Open("file:" + dbPath)
	}
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: open source: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: checkpoint source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, embeddingsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: count source rows: %w", err)
	}
	srcDB.Close()

	tmpPath := dbPath + ".encrypting.tmp"
	removeStaleTempFile(tmpPath) // clear any stale temp (and its sidecars) from a previous aborted attempt
	if err := encryptNewFile(ctx, tmpPath, hexKey, "file:"+dbPath, includeVec1); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: %w", err)
	}

	after, verr := countRowsWith(ctx, "file:"+tmpPath+"?vfs=adiantum", migrateInit(hexKey, includeVec1), embeddingsRowCountQuery)
	if verr != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: verify encrypted target: %w", verr)
	}
	if after != before {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: back up original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to encrypted: finalize rename: %w", err)
	}
	// tmpPath's own name is now vacated — clear any sidecars orphaned under
	// that vacated name so a LATER migration never inherits them.
	removeStaleWALSidecars(tmpPath)

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// MigrateToPlaintext reverses MigrateToEncrypted (spec REQ-435: never locks
// the user out in either direction). An already-plaintext dbPath is a
// no-op. A wrong hexKey (or any other failure) aborts before the rename.
func MigrateToPlaintext(ctx context.Context, dbPath, hexKey string, includeVec1 bool) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil
	}
	if plaintext {
		return EncryptionMigrationResult{AlreadyPlaintext: true}, nil
	}

	srcDB, err := ncrucesdriver.Open("file:"+dbPath+"?vfs=adiantum", migrateInit(hexKey, includeVec1))
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: open encrypted source: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: checkpoint encrypted source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, embeddingsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: count source rows (wrong key?): %w", err)
	}

	tmpPath := dbPath + ".decrypting.tmp"
	removeStaleTempFile(tmpPath) // clear any stale temp (and its sidecars) from a previous aborted attempt
	// vfs=os is REQUIRED (see internal/store/migrate_encryption.go's
	// identical note): a connection whose current VFS is adiantum otherwise
	// tries to open the plaintext VACUUM INTO target under that same
	// encrypting VFS too.
	vacuumSQL := fmt.Sprintf(`VACUUM INTO 'file:%s?vfs=os'`, tmpPath)
	if _, err := srcDB.ExecContext(ctx, vacuumSQL); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: vacuum into plaintext target: %w", err)
	}

	after, verr := countRowsWith(ctx, "file:"+tmpPath, migrateInit("", includeVec1), embeddingsRowCountQuery)
	if verr != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: verify plaintext target: %w", verr)
	}
	if after != before {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}
	srcDB.Close()

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: back up original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: migrate to plaintext: finalize rename: %w", err)
	}
	// tmpPath's own name is now vacated — clear any sidecars orphaned under
	// that vacated name so a LATER migration never inherits them.
	removeStaleWALSidecars(tmpPath)

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// RotateKey re-encrypts the ALREADY-encrypted embeddings.db file at dbPath
// from oldHexKey to newHexKey: first backs it up (decryptToPlainFile, keyed
// via PRAGMA, never a URI) into a plaintext intermediate temp file, then
// re-encrypts that intermediate (encryptNewFile, newHexKey via PRAGMA) into
// a new-key sibling temp file — mirroring internal/store.RotateKey exactly
// (see its doc for the "call before updating the keychain" ordering
// contract, and for why a missing dbPath is a no-op rather than an error).
// includeVec1 mirrors MigrateToEncrypted's own parameter.
func RotateKey(ctx context.Context, dbPath, oldHexKey, newHexKey string, includeVec1 bool) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil
	}
	if plaintext {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: %s is not encrypted; nothing to rotate", dbPath)
	}

	// Defensively clear ANY residue — main file AND -wal/-shm sidecars —
	// left at either of this function's own fixed, non-randomized
	// temp/intermediate paths by a PRIOR call, right at the top before doing
	// anything else. RotateKey has no separate "already done" idempotency
	// marker: a retried call after an aborted first attempt, or simply
	// rotating the key twice in a row (both normal, supported sequences),
	// reuses these exact names — a fresh call must never inherit anything
	// from whatever previously occupied them.
	tmpPlainPath := dbPath + ".rotating.plain.tmp"
	tmpPath := dbPath + ".rotating.tmp"
	removeStaleTempFile(tmpPlainPath)
	removeStaleTempFile(tmpPath)

	srcDB, err := ncrucesdriver.Open("file:"+dbPath+"?vfs=adiantum", migrateInit(oldHexKey, includeVec1))
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: open source with old key: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: checkpoint old-key source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, embeddingsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: count source rows (wrong old key?): %w", err)
	}
	srcDB.Close()

	if err := decryptToPlainFile(ctx, dbPath, oldHexKey, tmpPlainPath, includeVec1); err != nil {
		removeStaleTempFile(tmpPlainPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: %w", err)
	}
	defer removeStaleTempFile(tmpPlainPath)

	// decryptToPlainFile's Backup destination connection is opened AND
	// closed entirely inside that call — checkpoint tmpPlainPath explicitly
	// here anyway, exactly like every other source-then-consume hop in this
	// file, rather than relying on Backup's own destination connection
	// having already left itself fully checkpointed on close. The very next
	// step reads tmpPlainPath back as encryptNewFile's source.
	if err := checkpointPlainFileWAL(ctx, tmpPlainPath, includeVec1); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: checkpoint plaintext intermediate WAL: %w", err)
	}

	if err := encryptNewFile(ctx, tmpPath, newHexKey, "file:"+tmpPlainPath, includeVec1); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: %w", err)
	}

	after, verr := countRowsWith(ctx, "file:"+tmpPath+"?vfs=adiantum", migrateInit(newHexKey, includeVec1), embeddingsRowCountQuery)
	if verr != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: verify new-key target: %w", verr)
	}
	if after != before {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		removeStaleTempFile(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: back up old-key original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("embed: rotate key: finalize rename: %w", err)
	}
	// tmpPath's own name is now vacated — clear any sidecars orphaned under
	// that vacated name so a LATER rotation never inherits them.
	removeStaleWALSidecars(tmpPath)

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// countRowsWith opens dsn via the ncruces driver with the given init
// callback and runs query — shared verification helper for both migration
// directions above.
func countRowsWith(ctx context.Context, dsn string, init func(*sqlite3.Conn) error, query string) (int, error) {
	db, err := ncrucesdriver.Open(dsn, init)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
