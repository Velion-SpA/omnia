// Package datadir owns resolution of the Omnia data directory and the explicit,
// non-destructive migration from a legacy ~/.engram directory.
//
// Omnia was previously named "Engram" and stored everything under ~/.engram with
// a database file named engram.db. After the rebrand the home is ~/.omnia and the
// database is omnia.db. Real user memories still live in ~/.engram, so a
// pre-rebrand install is used IN PLACE: resolution prefers ~/.omnia but
// transparently falls back to an existing ~/.engram, reading and writing it
// directly so the data never diverges. Moving to ~/.omnia is opt-in via the
// explicit `omnia migrate` command, which COPIES (never moves) and leaves
// ~/.engram untouched as a backup.
//
// Resolution order (see Resolve):
//
//  1. explicit argument (e.g. a --data-dir flag), if non-empty
//  2. OMNIA_DATA_DIR (falls back to legacy ENGRAM_DATA_DIR via envx)
//  3. ~/.omnia if it already exists
//  4. legacy ~/.engram used in place, when ~/.omnia is absent but ~/.engram exists
//  5. ~/.omnia (to be created by the caller) otherwise
package datadir

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Velion-SpA/omnia/internal/envx"
)

const (
	// DirName is the canonical Omnia data directory name under the user's home.
	DirName = ".omnia"
	// LegacyDirName is the pre-rebrand data directory name.
	LegacyDirName = ".engram"

	// DBFilename is the canonical Omnia SQLite database filename.
	DBFilename = "omnia.db"
	// LegacyDBFilename is the pre-rebrand SQLite database filename.
	LegacyDBFilename = "engram.db"

	// DataDirEnv is the canonical environment variable that overrides the data dir.
	DataDirEnv = "OMNIA_DATA_DIR"
)

// dbFileBases are the SQLite database files (main DB plus its WAL/SHM sidecars)
// whose names change from engram.db* to omnia.db* during migration. Copying the
// three together keeps the snapshot internally consistent without checkpointing
// (and therefore without writing to the source).
var dbFileSuffixes = []string{"", "-wal", "-shm"}

// Resolve returns the Omnia data directory. It never mutates the filesystem: it
// only stats candidate directories, never creating, copying, or migrating. A
// legacy ~/.engram is therefore used IN PLACE — read and written directly — so a
// pre-rebrand install keeps working against its real data with no copy and no
// divergence. Moving to ~/.omnia is reserved for the explicit `omnia migrate`.
//
// Resolution order:
//
//  1. explicit argument, if non-empty
//  2. OMNIA_DATA_DIR (or legacy ENGRAM_DATA_DIR)
//  3. ~/.omnia if it already exists
//  4. legacy ~/.engram used in place, when only it exists
//  5. ~/.omnia (the canonical default, to be created by the caller) otherwise
func Resolve(explicit string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return resolveWithHome(explicit, home)
}

// resolveWithHome is the testable core of Resolve.
func resolveWithHome(explicit, home string) string {
	if dir := strings.TrimSpace(explicit); dir != "" {
		return dir
	}
	if env := strings.TrimSpace(envx.Get(DataDirEnv)); env != "" {
		return env
	}
	if home == "" {
		return DirName
	}
	omniaDir := filepath.Join(home, DirName)
	// Prefer the canonical ~/.omnia. When it does not yet exist but a legacy
	// ~/.engram does, use the legacy dir in place (no copy) so a pre-rebrand
	// install keeps working against its real data and never diverges.
	if !isDir(omniaDir) {
		if legacyDir := filepath.Join(home, LegacyDirName); isDir(legacyDir) {
			return legacyDir
		}
	}
	return omniaDir
}

// DBPath returns the database file to open inside dir. It prefers the canonical
// omnia.db, but transparently falls back to a legacy engram.db when only the old
// file is present, so an un-migrated data directory still opens without error.
func DBPath(dir string) string {
	omnia := filepath.Join(dir, DBFilename)
	legacy := filepath.Join(dir, LegacyDBFilename)
	if !fileExists(omnia) && fileExists(legacy) {
		return legacy
	}
	return omnia
}

// Migrate copies a legacy data directory (src) to the new Omnia directory (dst),
// renaming engram.db* files to omnia.db* along the way. It is:
//
//   - Non-destructive: src is only ever read; it is never modified or removed.
//   - Consistent: the SQLite main/WAL/SHM files are copied together, so the
//     snapshot is valid even while the source is in WAL mode.
//   - Atomic: the copy lands in a sibling temp directory and is renamed into
//     place, so a crash mid-copy never leaves a half-populated dst that later
//     resolution would mistake for a completed migration.
//
// If dst already exists Migrate is a no-op and returns nil (idempotent).
func Migrate(src, dst string) error {
	if !isDir(src) {
		return fmt.Errorf("source %q is not a directory", src)
	}
	if exists(dst) {
		return nil // already migrated (or user-created) — never clobber
	}

	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent %q: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".omnia-migrating-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	// Best-effort cleanup if we fail before the final rename.
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := copyTree(src, tmp); err != nil {
		return err
	}

	if err := os.Rename(tmp, dst); err != nil {
		// A concurrent run may have created dst between our exists() check and
		// the rename; treat that as success rather than failing the migration.
		if exists(dst) {
			return nil
		}
		return fmt.Errorf("finalize %q: %w", dst, err)
	}
	committed = true
	return nil
}

// copyTree recursively copies the directory tree rooted at src into dst, applying
// the engram.db* → omnia.db* rename to top-level database files.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read dir %q: %w", src, err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstName := renameDBFile(e.Name())
		dstPath := filepath.Join(dst, dstName)

		switch {
		case e.IsDir():
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", dstPath, err)
			}
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
		case e.Type()&os.ModeSymlink != 0:
			// Skip symlinks: a data dir should not contain them, and copying a
			// link target verbatim is safer skipped than blindly dereferenced.
			continue
		default:
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// renameDBFile maps a legacy database filename to its Omnia equivalent, leaving
// every other filename unchanged. It handles engram.db, engram.db-wal and
// engram.db-shm.
func renameDBFile(name string) string {
	for _, suffix := range dbFileSuffixes {
		if name == LegacyDBFilename+suffix {
			return DBFilename + suffix
		}
	}
	return name
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", src, err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", dst, err)
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
