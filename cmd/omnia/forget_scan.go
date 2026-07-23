package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/store"
)

// ─── forgetScanAnchorAdapter ──────────────────────────────────────────────────

// forgetScanAnchorAdapter wraps *anchor.Probe to satisfy store.AnchorProvider
// for `omnia forget-scan` — the ONLY place in Omnia that re-blames stored
// code anchors against the live git working tree (design obs #1594's
// cgo-free shell-out decision). Every other anchor.Probe caller
// (internal/mcp's mem_save wiring, PR1) only CAPTURES; this is the sole
// RECHECK caller, keeping git shell-outs confined to an explicit,
// user-initiated reconcile pass — never inside mem_save or mem_search.
type forgetScanAnchorAdapter struct {
	probe *anchor.Probe
}

// Recheck re-blames a. Classification (unchanged / traveled / stale) is
// decided by store.scanProjectAnchors, NOT here — this adapter only reports
// what it currently finds at (and, on a content-hash mismatch, near) a's
// stored location; see store.AnchorRecheckResult's doc for the contract.
func (a *forgetScanAnchorAdapter) Recheck(ctx context.Context, anc store.MemoryAnchor) (store.AnchorRecheckResult, error) {
	// 1. Re-hash the stored range at its stored location. anchor.RangeHash
	// normalizes content before hashing, so a hash match here means "no
	// material change" regardless of whitespace (REQ-003) — no separate
	// materiality call is needed.
	if hash, err := a.probe.RangeHash(ctx, anc.RepoRoot, anc.FilePath, anc.LineStart, anc.LineEnd); err == nil && hash == anc.ContentHash {
		sha := anc.BlameSHA
		if br, blameErr := a.probe.Blame(ctx, anc.RepoRoot, anc.FilePath, anc.LineStart, anc.LineEnd); blameErr == nil {
			sha = br.SHA
		}
		return store.AnchorRecheckResult{
			Found:       true,
			LineStart:   anc.LineStart,
			LineEnd:     anc.LineEnd,
			BlameSHA:    sha,
			ContentHash: hash,
		}, nil
	}

	// 2. Range changed (or is no longer readable) — attempt relocation by
	// symbol BEFORE any staleness verdict (REQ-004: travel MUST be
	// attempted before staling).
	if strings.TrimSpace(anc.Symbol) == "" {
		// No symbol to relocate by — cannot verify. Proceed to staleness eval.
		return store.AnchorRecheckResult{}, nil
	}

	file, line, err := a.probe.Locate(ctx, anc.RepoRoot, anc.FilePath, anc.Symbol)
	if err != nil {
		if errors.Is(err, anchor.ErrSymbolNotFound) {
			// REQ-004 scenario: symbol not found -> proceeds to staleness eval.
			return store.AnchorRecheckResult{}, nil
		}
		return store.AnchorRecheckResult{}, err
	}
	if file != "" && file != anc.FilePath {
		// Locate found the symbol in a DIFFERENT file. UpdateAnchorRange (the
		// travel write) has no file_path column — cross-file relocation is
		// out of scope for this slice (documented limitation). Treat like
		// "not found": proceed to staleness eval rather than silently
		// reporting a wrong same-file range.
		return store.AnchorRecheckResult{}, nil
	}

	// Preserve the anchor's original range LENGTH at the new location —
	// Locate only resolves a single declaration line, not a full range.
	newStart := line
	newEnd := line + (anc.LineEnd - anc.LineStart)

	newHash, err := a.probe.RangeHash(ctx, anc.RepoRoot, anc.FilePath, newStart, newEnd)
	if err != nil {
		// Relocated range unreadable — cannot verify. Proceed to staleness eval.
		return store.AnchorRecheckResult{}, nil
	}

	sha := ""
	if br, blameErr := a.probe.Blame(ctx, anc.RepoRoot, anc.FilePath, newStart, newEnd); blameErr == nil {
		sha = br.SHA
	}

	return store.AnchorRecheckResult{
		Found:       true,
		LineStart:   newStart,
		LineEnd:     newEnd,
		BlameSHA:    sha,
		ContentHash: newHash,
	}, nil
}

// ─── cmdForgetScan ─────────────────────────────────────────────────────────────

// cmdForgetScan is `omnia forget-scan`: the reconcile pass for
// omnia-structural-forgetting. Mirrors cmdConflictsScan's flag-parsing style
// (--project --repo --semantic --apply, dry-run default).
func cmdForgetScan(cfg store.Config) {
	args := os.Args[2:]

	var projectFlag, repoFlag string
	apply := false
	semantic := false
	yesFlag := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--repo":
			if i+1 < len(args) {
				repoFlag = args[i+1]
				i++
			}
		case "--apply":
			apply = true
		case "--dry-run":
			apply = false
		case "--semantic":
			semantic = true
		case "--yes":
			yesFlag = true
		}
	}

	proj := resolveConflictsProject(projectFlag)

	// --semantic: accepted for interface parity with cmdConflictsScan's cost-
	// confirm gate, but store.AnchorProvider has no LLM hook this slice
	// (design obs #1594's Open Questions defers materiality-threshold
	// tuning; the 8 spec requirements never call for an LLM-assisted
	// staleness confirmation). resolveAgentRunner() is still invoked so the
	// flag fails loudly — same UX as conflicts scan — when no agent CLI is
	// configured, rather than silently ignoring --semantic.
	if semantic {
		if _, err := resolveAgentRunner(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			exitFunc(1)
			return
		}
		if !yesFlag {
			fmt.Println("--semantic is accepted but not yet implemented for forget-scan (deterministic hash/line-delta materiality only). Proceeding without LLM confirmation.")
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	opts := store.ScanOptions{
		Project:        proj,
		Source:         store.SourceAnchor,
		Apply:          apply,
		Repo:           repoFlag,
		AnchorProvider: &forgetScanAnchorAdapter{probe: anchor.NewProbe()},
	}

	result, err := s.ScanProject(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Forget Scan (project: %s)\n", proj)
	fmt.Printf("  checked:  %d\n", result.AnchorsChecked)
	fmt.Printf("  traveled: %d\n", result.AnchorsTraveled)
	fmt.Printf("  staled:   %d\n", result.AnchorsStaled)
	fmt.Printf("  errors:   %d\n", result.AnchorsErrors)
	fmt.Printf("  dry_run:  %v\n", result.DryRun)
}
