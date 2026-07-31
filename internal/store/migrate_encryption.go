package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// This file implements v0.4's `memory-at-rest-security` migration
// (design capability 4, "mirrors datadir.Migrate", spec REQ-435): a
// non-destructive, reversible, atomic-rename migration between the
// plaintext (modernc) and adiantum-encrypted (ncruces) on-disk formats for
// omnia.db. Mirrors internal/datadir.Migrate's own "copy to a sibling
// temp location, verify, atomic rename, never touch the original until the
// copy is proven good" shape.

// EncryptionMigrationResult reports the outcome of one MigrateToEncrypted/
// MigrateToPlaintext call.
type EncryptionMigrationResult struct {
	// AlreadyEncrypted/AlreadyPlaintext is true when the file was already in
	// the target format — a no-op, not an error (idempotent, safe to call
	// on every `omnia security encrypt`/`decrypt` invocation).
	AlreadyEncrypted bool
	AlreadyPlaintext bool
	// RowsBefore/RowsAfter are the observations row count measured on the
	// source and the migrated copy — MUST match (spec REQ-461-style
	// non-destructive verification, reused here for capability 4).
	RowsBefore int
	RowsAfter  int
	// BackupPath is the timestamped copy of the ORIGINAL file, retained
	// after a successful migration (design: "keep the plaintext as a
	// timestamped .bak until the next clean startup" — kept indefinitely
	// here; cleanup is an explicit, separate operator action, never
	// automatic, so a mistaken migration is always recoverable).
	BackupPath string
}

const observationsRowCountQuery = `SELECT COUNT(*) FROM observations`

// checkpointAndTruncateWAL runs `PRAGMA wal_checkpoint(TRUNCATE)` on db,
// merging any live write-ahead log fully into the main database file and
// truncating away the WAL sidecar. This MUST run before this file's
// migration functions read a row count off a migration SOURCE and copy it
// into a brand-new destination file: a source left with a live,
// un-checkpointed WAL (completely normal — e.g. a daemon stopped via
// SIGKILL/`launchctl bootout` without a final checkpoint) would otherwise
// leave a stale `dbPath-wal`/`dbPath-shm` sidecar pair sitting on disk,
// structurally incompatible with whatever NEW file later takes over
// dbPath's name via this file's atomic-rename step below — the confirmed
// root cause of a real production incident where an encryption migration
// reported success but left the result permanently unreadable by any
// subsequent fresh process (`sqlite3: file is not a database`). Checkpointing
// the source is safe here: the migration never writes new application data
// to the source, it only merges the source's OWN existing WAL into itself.
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
// checkpointAndTruncateWALConn above: even after an explicit checkpoint on
// the SOURCE connection, a subsequent read against dbPath (SQLite's own
// online backup/restore reading the source, or a VACUUM INTO source scan)
// can reopen it in WAL mode and recreate a fresh sidecar pair before the
// rename lands — so dbPath is guaranteed to start completely clean the
// moment the new file takes over that name. Any error (including a
// missing file) is ignored: this is best-effort cleanup, not verification —
// the checkpoint above is the primary defense, this is the backstop.
func removeStaleWALSidecars(dbPath string) {
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}

// encryptNewFile creates a brand-new adiantum-encrypted database file at
// dstPath containing a full copy of the SQLite database readable (without
// any key) at plainSrcURI. The destination's encryption key is established
// via `PRAGMA hexkey=...` on a connection this function opens and controls
// directly, immediately after connecting — NEVER via a URI parameter (the
// adiantum package's own docs warn a URI-embedded key is visible via
// vfs.Filename.URIParameters to any code that inspects the open filename;
// the PRAGMA form never appears there, mirroring openAdiantumDB's own
// read-path pattern). The actual copy uses SQLite's online backup/restore
// API (Conn.Restore) — the same mechanism VACUUM INTO is itself implemented
// on top of — so the resulting bytes are equivalent, but the destination
// connection (and its key) are ours to control from the very first byte,
// unlike VACUUM INTO's implicit, un-hookable target-file creation.
func encryptNewFile(ctx context.Context, dstPath, hexKey, plainSrcURI string) error {
	dst, err := sqlite3.OpenContext(ctx, "file:"+dstPath+"?vfs=adiantum")
	if err != nil {
		return fmt.Errorf("open new encrypted target: %w", err)
	}
	defer dst.Close()
	if err := dst.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
		return fmt.Errorf("set encryption key on new target: %w", err)
	}
	if err := fts5.Register(dst); err != nil {
		return fmt.Errorf("register fts5 on new target: %w", err)
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
func decryptToPlainFile(ctx context.Context, srcPath, hexKey, dstPath string) error {
	src, err := sqlite3.OpenContext(ctx, "file:"+srcPath+"?vfs=adiantum")
	if err != nil {
		return fmt.Errorf("open encrypted source: %w", err)
	}
	defer src.Close()
	if err := src.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
		return fmt.Errorf("set encryption key on source: %w", err)
	}
	if err := fts5.Register(src); err != nil {
		return fmt.Errorf("register fts5 on source: %w", err)
	}
	if err := checkpointAndTruncateWALConn(src); err != nil {
		return fmt.Errorf("checkpoint encrypted source WAL before backup: %w", err)
	}
	if err := src.Backup("main", "file:"+dstPath+"?vfs=os"); err != nil {
		return fmt.Errorf("back up source into plaintext target: %w", err)
	}
	return nil
}

// isPlaintextSQLiteFile reports whether path exists and starts with the
// literal, well-known SQLite file-format magic header — the same
// deterministic check used to decide "needs migration" vs "brand new" vs
// "already encrypted" without any ambiguous probing.
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
		return true, false, nil // empty/unreadable file — treat as not-plaintext
	}
	return true, n >= 15 && string(header[:15]) == "SQLite format 3", nil
}

// MigrateToEncrypted encrypts the omnia.db file at dbPath in place: back it
// up into a brand-new adiantum-encrypted sibling temp file (via
// encryptNewFile's Conn.Restore, never a VACUUM INTO URI-embedded key),
// verify the observations row count matches, atomically rename the
// original aside as a timestamped .bak, then rename the encrypted copy
// into place. Any failure aborts BEFORE the rename — the original
// plaintext file is never touched or left half-migrated (design:
// "abort-and-keep-plaintext on any step failure").
//
// A missing dbPath (brand-new store, nothing to migrate — the normal
// New/openEngramDB path already creates a fresh encrypted file directly)
// and an already-encrypted dbPath are both no-ops, not errors.
func MigrateToEncrypted(ctx context.Context, dbPath, hexKey string) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil // nothing to migrate — a fresh open will create it encrypted
	}
	if !plaintext {
		return EncryptionMigrationResult{AlreadyEncrypted: true}, nil
	}

	srcDB, err := ncrucesdriver.Open("file:"+dbPath, fts5.Register)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: open source: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: checkpoint source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, observationsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: count source rows: %w", err)
	}

	srcDB.Close()

	tmpPath := dbPath + ".encrypting.tmp"
	os.Remove(tmpPath) // best-effort: clear any stale temp from a previous aborted attempt
	if err := encryptNewFile(ctx, tmpPath, hexKey, "file:"+dbPath); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: %w", err)
	}

	after, verr := countRowsInEncryptedFile(ctx, tmpPath, hexKey, observationsRowCountQuery)
	if verr != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: verify encrypted target: %w", verr)
	}
	if after != before {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: back up original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		// Restore the original so the caller is never left without a
		// working database — never a half-migrated state.
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to encrypted: finalize rename: %w", err)
	}

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// MigrateToPlaintext reverses MigrateToEncrypted: VACUUM INTO a plain
// sibling temp file from the encrypted source, verify the row count
// matches, atomically rename the encrypted original aside as a timestamped
// .bak, then rename the plaintext copy into place (spec REQ-435: "A user
// MUST NEVER be locked out of their own memory by this transition in
// either direction").
//
// An already-plaintext dbPath is a no-op, not an error. A wrong hexKey (or
// any other failure) aborts BEFORE the rename — the encrypted file is
// never touched or left corrupted.
func MigrateToPlaintext(ctx context.Context, dbPath, hexKey string) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil
	}
	if plaintext {
		return EncryptionMigrationResult{AlreadyPlaintext: true}, nil
	}

	srcDSN := "file:" + dbPath + "?vfs=adiantum"
	srcInit := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		return fts5.Register(c)
	}
	srcDB, err := ncrucesdriver.Open(srcDSN, srcInit)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: open encrypted source: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: checkpoint encrypted source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, observationsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: count source rows (wrong key?): %w", err)
	}

	tmpPath := dbPath + ".decrypting.tmp"
	os.Remove(tmpPath)
	// vfs=os is REQUIRED here, not cosmetic: empirically, VACUUM INTO run
	// from a connection whose CURRENT vfs is adiantum otherwise tries to
	// open the plaintext target under that same encrypting VFS too (failing
	// with "unable to open database file", since no key was ever set for a
	// brand-new file under that VFS) — the target must explicitly select
	// ncruces' default VFS ("os", the reserved name vfs.Find("")/vfs.Find(
	// "os") both resolve to) to actually land as a plain file.
	vacuumSQL := fmt.Sprintf(`VACUUM INTO 'file:%s?vfs=os'`, tmpPath)
	if _, err := srcDB.ExecContext(ctx, vacuumSQL); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: vacuum into plaintext target: %w", err)
	}

	after, verr := countRowsInPlaintextFile(ctx, tmpPath, observationsRowCountQuery)
	if verr != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: verify plaintext target: %w", verr)
	}
	if after != before {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}
	srcDB.Close()

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: back up original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: migrate to plaintext: finalize rename: %w", err)
	}

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// RotateKey re-encrypts the ALREADY-encrypted omnia.db file at dbPath from
// oldHexKey to newHexKey: first backs it up (decryptToPlainFile, keyed via
// PRAGMA, never a URI) into a plaintext intermediate temp file, then
// re-encrypts that intermediate (encryptNewFile, newHexKey via PRAGMA) into
// a new-key sibling temp file — the old and new keys are never embedded in
// SQL/URI text, and never coexist on the same connection. Verifies the row
// count, atomically renames the old-key file aside as a timestamped .bak,
// then renames the new-key copy into place. `omnia security rotate-key` MUST
// call this BEFORE updating the keychain entry to newHexKey (never after):
// if this fails, the original file and the still-current keychain entry
// (oldHexKey) are both untouched, so the store remains fully readable.
//
// A missing dbPath is a no-op, not an error (mirrors MigrateToEncrypted/
// MigrateToPlaintext's own "nothing to migrate" convention) — a capability
// that was never enabled/created must never block rotating the OTHER
// file's key. Returns an error if dbPath EXISTS but is still plaintext:
// rotating a key only makes sense on an already-encrypted file.
func RotateKey(ctx context.Context, dbPath, oldHexKey, newHexKey string) (EncryptionMigrationResult, error) {
	existed, plaintext, err := isPlaintextSQLiteFile(dbPath)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: inspect %s: %w", dbPath, err)
	}
	if !existed {
		return EncryptionMigrationResult{}, nil
	}
	if plaintext {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: %s is not encrypted; nothing to rotate", dbPath)
	}

	srcInit := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + oldHexKey + "'"); err != nil {
			return err
		}
		return fts5.Register(c)
	}
	srcDB, err := ncrucesdriver.Open("file:"+dbPath+"?vfs=adiantum", srcInit)
	if err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: open source with old key: %w", err)
	}
	defer srcDB.Close()

	if err := checkpointAndTruncateWAL(ctx, srcDB); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: checkpoint old-key source WAL: %w", err)
	}

	var before int
	if err := srcDB.QueryRowContext(ctx, observationsRowCountQuery).Scan(&before); err != nil {
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: count source rows (wrong old key?): %w", err)
	}
	srcDB.Close()

	tmpPlainPath := dbPath + ".rotating.plain.tmp"
	os.Remove(tmpPlainPath)
	if err := decryptToPlainFile(ctx, dbPath, oldHexKey, tmpPlainPath); err != nil {
		os.Remove(tmpPlainPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: %w", err)
	}
	defer os.Remove(tmpPlainPath)

	tmpPath := dbPath + ".rotating.tmp"
	os.Remove(tmpPath)
	if err := encryptNewFile(ctx, tmpPath, newHexKey, "file:"+tmpPlainPath); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: %w", err)
	}

	after, verr := countRowsInEncryptedFile(ctx, tmpPath, newHexKey, observationsRowCountQuery)
	if verr != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: verify new-key target: %w", verr)
	}
	if after != before {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: row count mismatch (before=%d after=%d); aborting, original untouched", before, after)
	}

	backupPath := fmt.Sprintf("%s.bak-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(dbPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: back up old-key original: %w", err)
	}
	removeStaleWALSidecars(dbPath)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Rename(backupPath, dbPath)
		return EncryptionMigrationResult{}, fmt.Errorf("engram: rotate key: finalize rename: %w", err)
	}

	return EncryptionMigrationResult{RowsBefore: before, RowsAfter: after, BackupPath: backupPath}, nil
}

// countRowsInEncryptedFile opens path via ncruces+adiantum+hexKey and runs
// query, used to verify a just-written encrypted VACUUM INTO target before
// committing to the atomic rename.
func countRowsInEncryptedFile(ctx context.Context, path, hexKey, query string) (int, error) {
	init := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		return fts5.Register(c)
	}
	db, err := ncrucesdriver.Open("file:"+path+"?vfs=adiantum", init)
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

// countRowsInPlaintextFile opens path via the default (plain) modernc
// driver and runs query, used to verify a just-written plaintext VACUUM
// INTO target before committing to the atomic rename.
func countRowsInPlaintextFile(ctx context.Context, path, query string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+path)
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
