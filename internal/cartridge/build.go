package cartridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

// SchemaVersion is the current on-disk cartridge format version (REQ-456).
// Bumping it makes Load reject an older cartridge cleanly (falling back to
// cold-start) instead of misreading it.
const SchemaVersion = 1

// defaultTopMemories mirrors CartridgeConfig.TopMemories's own documented
// default (internal/config/config.go's applyDefaults) so a caller that
// forgets to thread the config value through still gets a sane digest size
// instead of an empty/zero-length one.
const defaultTopMemories = 50

// Cartridge is the versioned, per-repo warm-start digest (REQ-451): a
// project's most-relevant memories, its code-decision graph state
// (capability 1's CodeDecisionGraph), and its trusted procedures, keyed to a
// commit SHA. It is a local, on-disk artifact only (REQ-454) — never synced
// to the cloud, never sent through internal/store's sync_mutations pipeline
// — and its fields are strictly memory/graph digest data: never model
// weights, KV-cache state, or any other parametric-memory artifact
// (REQ-455). Field additions here must be reflected in the content-shape
// allowlist asserted by TestBuildContentShapeAssertion.
type Cartridge struct {
	SchemaVersion int    `json:"schema_version"`
	RepoRoot      string `json:"repo_root"`
	HeadSHA       string `json:"head_sha"`
	BuiltAt       string `json:"built_at"`
	// TopMemories is the top-N (default 50, CartridgeConfig.TopMemories)
	// most-relevant observations for the project, ranked by the active
	// learned ranker (capability 5) when enabled, or by RankResults'
	// recency x importance order otherwise (byte-for-byte the same
	// degradation the recall path itself uses when the ranker is off).
	TopMemories []store.Observation `json:"top_memories"`
	// Anchors is the project's active code-decision graph state
	// (capability 1's CodeDecisionGraph): one edge per active anchor, each
	// carrying the anchor's code location plus the memory it explains.
	Anchors []store.CodeDecisionEdge `json:"anchors"`
	// TrustedProcedures are the project's `trusted`-state procedures only
	// (candidate/retired never gate — mirrors the enforcement gate's own
	// feed convention, design.md Capability 2).
	TrustedProcedures []store.Procedure `json:"trusted_procedures"`
	// RankerModelVersion records which learned-ranker model (if any) ranked
	// TopMemories, for diagnostic/debugging purposes. Empty when the ranker
	// was disabled or no model was available at build time.
	RankerModelVersion string `json:"ranker_model_version,omitempty"`
}

// BuildParams are the inputs Build needs to assemble a Cartridge in memory.
// Ranking/RankerCfg/RankerModel are all optional: a zero-value RankingConfig
// (Enabled: false) or a nil RankerModel degrade to natural recency order,
// never an error — matching every other v0.4 capability's cold-start
// convention (design.md Capability 6 "Failure/degradation").
type BuildParams struct {
	Store    *store.Store
	Project  string
	RepoRoot string
	HeadSHA  string
	// TopN is the top_memories cap. <=0 defaults to defaultTopMemories.
	TopN        int
	Ranking     config.RankingConfig
	RankerCfg   config.RankerConfig
	RankerModel *ranker.Model
	// Now is injectable for deterministic tests; the zero value defaults to
	// time.Now().UTC().
	Now time.Time
}

// Build assembles a Cartridge in memory (REQ-451). It never writes to disk
// — callers persist the result via Save. It never returns a partial
// Cartridge on hard error: either the digest is fully assembled, or Build
// returns a non-nil error for the caller to treat as "cannot build right
// now" (never as "delete/replace the existing on-disk cartridge").
func Build(p BuildParams) (*Cartridge, error) {
	if p.Store == nil {
		return nil, fmt.Errorf("cartridge: Build: Store is required")
	}
	if strings.TrimSpace(p.HeadSHA) == "" {
		return nil, fmt.Errorf("cartridge: Build: HeadSHA is required")
	}
	topN := p.TopN
	if topN <= 0 {
		topN = defaultTopMemories
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Normalize once so all three reads (observations/anchors/procedures)
	// agree on the same project key, regardless of which underlying store
	// method does (ListActiveAnchors/ListProcedures) or doesn't
	// (AllObservations) normalize its own project filter internally.
	project, _ := store.NormalizeProject(p.Project)

	topMemories, err := rankedTopMemories(p, project, topN, now)
	if err != nil {
		return nil, fmt.Errorf("cartridge: Build: %w", err)
	}

	_, edges, err := p.Store.CodeDecisionGraph(project)
	if err != nil {
		return nil, fmt.Errorf("cartridge: Build: %w", err)
	}
	if edges == nil {
		edges = []store.CodeDecisionEdge{}
	}

	procedures, err := p.Store.ListProcedures(store.ListProceduresOptions{
		Project: project, State: store.ProcedureStateTrusted,
	})
	if err != nil {
		return nil, fmt.Errorf("cartridge: Build: %w", err)
	}
	if procedures == nil {
		procedures = []store.Procedure{}
	}

	var rankerVersion string
	if p.RankerCfg.Enabled && p.RankerModel != nil {
		rankerVersion = p.RankerModel.Version
	}

	return &Cartridge{
		SchemaVersion:      SchemaVersion,
		RepoRoot:           p.RepoRoot,
		HeadSHA:            p.HeadSHA,
		BuiltAt:            now.Format(time.RFC3339),
		TopMemories:        topMemories,
		Anchors:            edges,
		TrustedProcedures:  procedures,
		RankerModelVersion: rankerVersion,
	}, nil
}

// rankedTopMemories fetches the project's candidate observations and ranks
// them exactly the way the recall path itself would: RankResults' recency x
// importance x relevance pass (design.md D6/D7's shared primitive), then the
// optional learned-ranker re-rank (capability 5) on top, then truncates to
// topN. With no live query, relevance is uniform across candidates (nil
// map), so — exactly like a cold-start caller of ApplyLearnedRanker/
// RankResults — recency and importance alone decide order when ranking is
// enabled; with ranking disabled too, AllObservations' own natural
// recency-DESC order is preserved untouched (byte-for-byte the same
// no-op fallback every other v0.4 capability uses when its gate is off).
func rankedTopMemories(p BuildParams, project string, topN int, now time.Time) ([]store.Observation, error) {
	observations, err := p.Store.AllObservations(project, "", 0)
	if err != nil {
		return nil, err
	}
	candidates := make([]store.SearchResult, 0, len(observations))
	for _, obs := range observations {
		candidates = append(candidates, store.SearchResult{Observation: obs})
	}
	ranked := mcp.RankResults(candidates, nil, p.Ranking, now)
	ranked = mcp.ApplyLearnedRanker(ranked, nil, p.RankerCfg, p.RankerModel, p.Ranking, now)
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	out := make([]store.Observation, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.Observation)
	}
	return out, nil
}

// RepoID derives the stable, filesystem-safe repo identifier used in the
// cartridge filename: a short hash of the (cleaned) repo root path (design:
// "repo-id = short hash of the repo root").
func RepoID(repoRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return hex.EncodeToString(sum[:])[:12]
}

// fileName is the on-disk cartridge artifact name: <repo-id>-<head-sha>.json.
func fileName(repoRoot, headSHA string) string {
	return RepoID(repoRoot) + "-" + headSHA + ".json"
}

// Save writes c to dir/<repo-id>-<head-sha>.json, creating dir if needed,
// and returns the written path. Save never mutates any other file in dir —
// a cartridge built at a different commit keeps its own file (non-
// destructive: rebuilding at a new HEAD never deletes the prior artifact,
// it only becomes stale per Load's HeadSHA comparison).
func Save(dir string, c *Cartridge) (string, error) {
	if c == nil {
		return "", fmt.Errorf("cartridge: Save: Cartridge is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cartridge: Save: %w", err)
	}
	path := filepath.Join(dir, fileName(c.RepoRoot, c.HeadSHA))
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("cartridge: Save: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("cartridge: Save: %w", err)
	}
	return path, nil
}
