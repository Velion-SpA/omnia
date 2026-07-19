package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/velion/omnia/internal/datadir"
)

// cmdMigrate explicitly performs the safe, non-destructive migration of a legacy
// Engram data directory (~/.engram, engram.db) to the Omnia layout (~/.omnia,
// omnia.db). The same migration also runs automatically on first launch; this
// subcommand exists so users can trigger and inspect it on demand.
//
// The legacy directory is COPIED, never moved, and is left completely untouched
// as a backup.
//
// Usage: omnia migrate [--from ~/.engram] [--to ~/.omnia]
func cmdMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	from := fs.String("from", "", "legacy data directory to migrate from (default: ~/.engram)")
	to := fs.String("to", "", "destination data directory (default: ~/.omnia)")
	if err := fs.Parse(args); err != nil {
		fatal(err)
	}

	home, _ := os.UserHomeDir()
	src := *from
	dst := *to
	if src == "" {
		src = filepath.Join(home, datadir.LegacyDirName)
	}
	if dst == "" {
		dst = filepath.Join(home, datadir.DirName)
	}

	srcInfo, err := os.Stat(src)
	if err != nil || !srcInfo.IsDir() {
		fmt.Printf("omnia: no legacy data directory at %s — nothing to migrate\n", src)
		return
	}
	if _, err := os.Stat(dst); err == nil {
		fmt.Printf("omnia: %s already exists — leaving it untouched (no migration needed)\n", dst)
		return
	}

	if err := datadir.Migrate(src, dst); err != nil {
		fatal(fmt.Errorf("migrate %s → %s: %w", src, dst, err))
	}
	fmt.Printf("omnia: migrated %s → %s (original left untouched as backup)\n", src, dst)
}
