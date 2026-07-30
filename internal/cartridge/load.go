package cartridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Load-degradation reasons (REQ-452/453/456). Every non-fresh reason
// degrades identically from the caller's point of view — cold-start, never
// an error — but the distinct string lets `omnia cartridge load` and a
// future `mem_cartridge` surface WHY, mirroring mem_blame's never-silently-
// hidden stale-anchor convention.
const (
	// ReasonMissing means no cartridge file exists for this repo at all.
	ReasonMissing = "missing"
	// ReasonStaleCommit means a cartridge was found but its HeadSHA no
	// longer matches the repo's live HEAD (REQ-452).
	ReasonStaleCommit = "stale-commit"
	// ReasonOldSchemaVersion means a cartridge was found but its
	// schema_version is older than this binary's SchemaVersion (REQ-456).
	ReasonOldSchemaVersion = "old-schema-version"
	// ReasonCorrupt means a cartridge file was found but could not be read
	// or parsed as valid JSON.
	ReasonCorrupt = "corrupt"
)

// LoadResult is Load's degradation-safe outcome. Fresh is true only when a
// cartridge was found, its schema_version matches, and its HeadSHA matches
// the live HEAD passed in — the ONLY case a caller may treat the digest as
// current. Cartridge is non-nil whenever a readable, correctly-versioned
// file was found (even when stale), so a caller that wants to inspect a
// stale cartridge's own HeadSHA (e.g. for a diagnostic message) still can;
// it is nil for ReasonMissing/ReasonCorrupt/ReasonOldSchemaVersion, where
// there is nothing safe to read.
type LoadResult struct {
	Cartridge *Cartridge
	Fresh     bool
	// Reason is empty when Fresh is true; otherwise one of the Reason*
	// constants above.
	Reason string
}

// Load resolves the most recently built cartridge for repoRoot under dir
// (design: "<dataDir>/cartridges/<repo-id>-<head-sha>.json") and compares
// its embedded HeadSHA against the caller-supplied, live headSHA. It never
// returns an error — every failure mode (missing directory/file, corrupt
// JSON, an older schema_version, or a commit mismatch) degrades to a
// LoadResult a caller can trivially treat as "cold start" (REQ-453).
func Load(dir, repoRoot, headSHA string) LoadResult {
	matches, err := filepath.Glob(filepath.Join(dir, RepoID(repoRoot)+"-*.json"))
	if err != nil || len(matches) == 0 {
		return LoadResult{Reason: ReasonMissing}
	}

	// Pick the most recently built file (by mtime) — the newest artifact
	// for this repo, regardless of which commit it was keyed to. This lets
	// Load distinguish "stale" (a real prior build exists, but the repo has
	// since moved on) from "missing" (no prior build at all) even though
	// the file name always encodes the BUILD-time commit, not the current
	// one.
	sort.Slice(matches, func(i, j int) bool {
		iInfo, iErr := os.Stat(matches[i])
		jInfo, jErr := os.Stat(matches[j])
		if iErr != nil || jErr != nil {
			return matches[i] < matches[j]
		}
		return iInfo.ModTime().After(jInfo.ModTime())
	})

	data, err := os.ReadFile(matches[0])
	if err != nil {
		return LoadResult{Reason: ReasonCorrupt}
	}
	var c Cartridge
	if err := json.Unmarshal(data, &c); err != nil {
		return LoadResult{Reason: ReasonCorrupt}
	}
	if c.SchemaVersion != SchemaVersion {
		return LoadResult{Reason: ReasonOldSchemaVersion}
	}
	if c.HeadSHA != headSHA {
		return LoadResult{Cartridge: &c, Reason: ReasonStaleCommit}
	}
	return LoadResult{Cartridge: &c, Fresh: true}
}
