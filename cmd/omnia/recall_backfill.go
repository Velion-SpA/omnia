package main

import (
	"context"
	"fmt"
	"os"

	"github.com/velion/omnia/internal/store"
)

// ─── omnia recall-backfill (#1399 backfill) ──────────────────────────────────
//
// error_signature (internal/store/signature.go, design obs #1498) is
// computed at SAVE time — observations saved BEFORE that feature shipped (or
// imported/legacy data) have error_signature = NULL and can never surface
// through the signature lane in store.Search, regardless of how good a
// match they'd otherwise be. Since the whole point of #1399 is finding PAST
// fixes for recurring bugs, and those fixes by definition predate the
// feature, this is a one-shot, idempotent backfill: it derives + persists a
// signature for existing bugfix-family observations that still lack one,
// using the exact same extraction store.AddObservation uses at save time
// (store.BackfillErrorSignatures), so backfilled rows behave identically to
// freshly-saved ones.
func cmdRecallBackfill(cfg store.Config) {
	project := ""
	dryRun := false

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	ctx := context.Background()

	var scanned, updated int
	if dryRun {
		scanned, updated, err = s.PreviewBackfillErrorSignatures(ctx, project)
	} else {
		scanned, updated, err = s.BackfillErrorSignatures(ctx, project)
	}
	if err != nil {
		fatal(err)
		return
	}
	skipped := scanned - updated

	if dryRun {
		fmt.Printf("[dry-run] scanned %d bugfix memories, would backfill %d signatures (%d had no extractable error, skipped)\n", scanned, updated, skipped)
		return
	}
	fmt.Printf("scanned %d bugfix memories, backfilled %d signatures (%d had no extractable error, skipped)\n", scanned, updated, skipped)
}
