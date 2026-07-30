package cartridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/velion/omnia/internal/store"
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
	// ReasonProjectMismatch means a cartridge file was found at the
	// requested project's expected path, its schema_version and HeadSHA
	// both matched, but its own embedded Project field does not match the
	// requested project. This should never happen through normal Build/Save
	// use (FileName already keys by project), but Load re-verifies it as a
	// defense-in-depth check so a caller can never be silently served the
	// wrong project's digest even if an on-disk file was mislabeled or
	// hand-edited.
	ReasonProjectMismatch = "project-mismatch"
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

// Load resolves the most recently built cartridge for repoRoot AND project
// under dir (design: "<dataDir>/cartridges/<repo-id>-<project>-<head-sha>.
// json") and compares its embedded HeadSHA against the caller-supplied,
// live headSHA. It never returns an error — every failure mode (missing
// directory/file, corrupt JSON, an older schema_version, a commit mismatch,
// or a project mismatch) degrades to a LoadResult a caller can trivially
// treat as "cold start" (REQ-453).
//
// project is normalized the same way Build normalizes BuildParams.Project,
// so a caller does not need to pre-normalize it to match what was used at
// build time. Scoping the glob by project (rather than resolving any
// cartridge for the repo+commit regardless of project) is what makes the
// --project flag on `omnia cartridge load` actually filter/select, instead
// of being parsed and discarded.
func Load(dir, repoRoot, headSHA, project string) LoadResult {
	normalizedProject, _ := store.NormalizeProject(project)
	matches, err := filepath.Glob(filepath.Join(dir, RepoID(repoRoot)+"-"+projectSegment(normalizedProject)+"-*.json"))
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
	// Defense-in-depth (see ReasonProjectMismatch doc comment): the glob
	// above already scopes candidates by project, so this should never
	// trigger through normal Build/Save use — but Load never trusts the
	// filename alone for something as sensitive as "whose memories are
	// these."
	if normalizedCartridgeProject, _ := store.NormalizeProject(c.Project); normalizedCartridgeProject != normalizedProject {
		return LoadResult{Reason: ReasonProjectMismatch}
	}
	if c.HeadSHA != headSHA {
		return LoadResult{Cartridge: &c, Reason: ReasonStaleCommit}
	}
	return LoadResult{Cartridge: &c, Fresh: true}
}
