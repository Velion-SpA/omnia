package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/velion/omnia/internal/datadir"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/projectname"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Engram       EngramConfig      `yaml:"engram"`
	Sources      SourcesConfig     `yaml:"sources"`
	BackfillDays int               `yaml:"backfill_days"`
	Routes       map[string]string `yaml:"routes"`
	// Projects is an explicit list of Engram project names to show in the
	// dashboard. The dashboard merges this list with "omnia" and the projects
	// derived from the routes map — whichever yields more projects wins.
	Projects []string `yaml:"projects"`
	// ProjectHidden lists canonical project names to hide from the dashboard entirely.
	// Values are matched after canonicalization (alias lookup + lowercase+trim).
	ProjectHidden []string `yaml:"project_hidden"`
	// ProjectAliases maps raw project variant names to their canonical name.
	// Used for non-case-fold merges (e.g., "NUDGE Sistema" -> "nudge").
	// Leave empty; add entries only when needed.
	ProjectAliases map[string]string `yaml:"project_aliases"`
	// ProjectGroups defines the two-level parent→children hierarchy.
	// Key = parent canonical; value = []child canonicals.
	// Children are hidden from the top-level overview and aggregated into the parent.
	// A child must not also be a parent; self-referential entries are ignored.
	ProjectGroups map[string][]string `yaml:"project_groups"`
	// Embeddings configures Omnia's own local semantic-search index ("capa propia").
	// Disabled by default so the production sync job is unaffected until opted in.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	// Recall configures Omnia's hybrid (lexical+semantic) recall fusion for
	// mem_search (design D1/D2/D6, human-like-memory PR3). Disabled by default:
	// off reproduces today's FTS5-only store.Search path byte-for-byte, so
	// enabling it is a pure config flip with zero engram.db migration (D7).
	Recall RecallConfig `yaml:"recall"`
	// RecallEnabledExplicit reports whether `recall.enabled` was present in
	// the loaded YAML (true or false), as opposed to Recall.Enabled's zero
	// value (false) meaning "never mentioned." Recall.Enabled is a plain
	// bool, so on its own it cannot distinguish "operator explicitly
	// disabled recall" from "operator never touched recall at all" — a
	// distinction issue #83's Ollama auto-detect needs: the auto-probe must
	// only decide the default when the operator never expressed an opinion,
	// and must never override an explicit `enabled: true` OR `enabled:
	// false`. Computed by Load; not itself part of the YAML schema
	// (yaml:"-").
	RecallEnabledExplicit bool `yaml:"-"`
	// StructuralForgetting configures the omnia-structural-forgetting living-
	// memory layer's RECALL-SIDE integration only (design obs #1594, spec
	// obs #1595). Disabled by default: `mem_save`'s optional `code_anchors`
	// capture and `omnia forget-scan` are explicit, user-initiated actions
	// that are NEVER gated by this flag either way — this config covers only
	// handleSearch's stale-anchor downrank + receipt line, so a fresh
	// install/upgrade that never mentions `structural_forgetting` sees ZERO
	// behavior change vs today (backward-compatible default, mirroring
	// Recall.Enabled/Ranking.Enabled's own off-by-default convention).
	StructuralForgetting StructuralForgettingConfig `yaml:"structural_forgetting"`
	// Procedural configures the omnia-procedural-memory playbook/anti-playbook
	// slot (design obs #1602, spec obs #1606). Disabled by default: with the
	// flag off (or entirely absent), nothing is induced from a bugfix
	// `outcome` and nothing auto-injects at retrieval — `mem_save`,
	// `mem_update`, `mem_search`, `mem_context` behave identically to their
	// pre-existing contracts (backward-compatibility domain, mirroring
	// Recall.Enabled/Ranking.Enabled/StructuralForgetting.Enabled's own
	// off-by-default convention).
	Procedural ProceduralConfig `yaml:"procedural"`
	// Injection configures Omnia v0.3's "Context Economy" family of optional,
	// gated re-sort/trim passes over already-ranked handleSearch results
	// (design obs #1643, spec obs #1642): a token-based injection budget
	// (this slice, PR2), FormatContext's own aggregate token budget (PR3),
	// MMR diversity (PR4), and situational type-as-lens boosting (PR5) all
	// live under this single parent block. Every sub-gate defaults to
	// disabled, mirroring Recall.Enabled/Ranking.Enabled/
	// StructuralForgetting.Enabled/Procedural.Enabled's own off-by-default
	// convention: a fresh install or upgrade that never mentions `injection`
	// sees ZERO behavior change from pre-v0.3.
	Injection InjectionConfig `yaml:"injection"`
}

// InjectionConfig is the parent block for every Context Economy sub-gate
// (design obs #1643). Each field is its own independently-gated pass; only
// Budget exists as of PR2 — ContextBudget/Diversity/TypeLens are added by
// PR3/PR4/PR5 respectively.
type InjectionConfig struct {
	// Budget gates handleSearch's token-based injection budget (spec
	// injection-budget, ApplyTokenBudget in internal/mcp/token_budget.go).
	// Disabled by default: handleSearch's response stays byte-for-byte
	// identical to pre-v0.3 output when unset (spec REQ6).
	Budget TokenBudgetConfig `yaml:"budget"`
}

// TokenBudgetConfig gates a single token-based budget pass (spec
// injection-budget). MaxTokens is the ceiling passed to
// internal/token.TrimToBudget — the same shared primitive used by
// handleSearch (PR2), FormatContext (PR3), and cmd/omnia/recall_fix.go
// (PR2), so all three can never drift out of sync (spec REQ4). Enabled
// defaults to false; when false (or MaxTokens<=0), the gated pass is a
// total no-op (spec REQ6).
type TokenBudgetConfig struct {
	Enabled   bool `yaml:"enabled"`
	MaxTokens int  `yaml:"max_tokens"`
}

// ProceduralConfig gates and tunes the procedural-memory governance gate
// (SSGM): TrustThreshold confirmed reuses promote a candidate procedure to
// trusted; ConfidenceFloor auto-retires a contradicted/decayed one;
// ReviewAfterDays is the spaced-repetition window an unused trusted
// procedure decays on. Defaults mirror internal/store's own
// defaultProceduralTrustThreshold/defaultProceduralConfidenceFloor/
// defaultProceduralReviewAfterDays constants (duplicated, not imported —
// internal/config stays a plain config leaf, same convention RecallConfig's
// doc comment documents for its own RRFK/DenseK/floor constants). Per
// design.md's Open Questions, these seed values need one empirical tuning
// pass against the live corpus.
type ProceduralConfig struct {
	Enabled         bool    `yaml:"enabled"`
	TrustThreshold  int     `yaml:"trust_threshold"`
	ConfidenceFloor float64 `yaml:"confidence_floor"`
	ReviewAfterDays int     `yaml:"review_after_days"`
}

// StructuralForgettingConfig gates memory-structural-forgetting's recall
// downrank (Requirement 6: "Retrieval Downranks Stale Memories"). See
// Config.StructuralForgetting's doc for what this flag does and does NOT
// cover.
type StructuralForgettingConfig struct {
	Enabled bool `yaml:"enabled"`
}

type EngramConfig struct {
	BaseURL        string `yaml:"base_url"`
	DefaultProject string `yaml:"default_project"`
}

// EmbeddingsConfig configures the local embeddings layer. When Enabled is false,
// `omnia embed` is a no-op and the dashboard serves keyword (FTS) search only.
// Omnia keeps its OWN writable vector store at DBPath; engram.db stays read-only.
type EmbeddingsConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"` // Ollama base URL, e.g. http://localhost:11434
	// Model selects the Ollama embedding model (EMBM-1: selectable without
	// code changes). Defaults to jina/jina-embeddings-v2-base-es (EMBM-2:
	// jina stays the shipped default until an eval-gated swap is recorded).
	// embeddinggemma:300m (Google's Matryoshka-trained 300M-parameter model,
	// see internal/embed.LookupModel) is a selectable MRL-capable
	// alternative; bge-m3 remains selectable but is not MRL-capable either.
	Model string `yaml:"model"`
	// Dim is the effective embedding dimension, e.g. 768 for jina-v2-es
	// (native, non-truncatable) or 1024 for bge-m3. For an MRL-capable model
	// (embeddinggemma:300m), Dim may be set below the model's native output
	// (e.g. 768/256/128) to truncate-and-renormalize (EMBM-4); ValidateEmbeddings
	// rejects that same shape for a non-MRL model (EMBM-3).
	Dim int `yaml:"dim"`
	// DBPath is the path to Omnia's own embeddings SQLite file. Left EMPTY by
	// Load/applyDefaults on purpose (unlike every other Embeddings* field):
	// the right default depends on the active data directory, which Load
	// does not know. Callers resolve the effective path via
	// ResolveEmbeddingsDBPath(cfg.Embeddings.DBPath, activeDataDir) instead
	// of reading this field directly. An explicit value here (set in
	// config.yaml) always wins regardless of data dir (see #82).
	DBPath string `yaml:"db_path"`
}

// RecallConfig configures Omnia's hybrid (lexical+semantic) recall fusion
// (design D1: reciprocal rank fusion; D2: adaptive relevance floor). Enabled
// defaults to false when the operator explicitly sets `recall.enabled:
// false`, OR when embeddings are disabled/unreachable — mem_search then
// calls store.Search directly, exactly as it always has (D7 rollback
// guarantee: byte-for-byte today's FTS5-only path, zero engram.db
// migration). When `recall.enabled` is never mentioned at all AND
// embeddings are enabled, callers should run a lightweight Ollama
// reachability probe (see Config.RecallEnabledExplicit) and enable recall
// only if Ollama answers — this is composition-root logic (cmd/omnia), not
// this leaf package's concern, since it involves a network call.
// RRFK/DenseK/StrongFloor/BaseFloor/MaxResults default to the same values
// internal/recall.DefaultFuseParams() already uses as the single source of
// truth (PR2) — duplicated here (not imported) so this package stays a
// plain config leaf, mirroring how EmbeddingsConfig's Dim default
// duplicates the embed package's convention rather than importing it.
// StrongFloor/BaseFloor are calibrated for the default embedding model,
// jina/jina-embeddings-v2-base-es: the original D2 constants (0.65/0.55)
// were tuned for a different model's score distribution and were
// empirically too high for jina, starving recall on a fresh install even
// with embeddings enabled (issue #83; see engram/omnia memory #1434 for the
// live-instance tuning that found 0.25/0.35 worked).
type RecallConfig struct {
	Enabled     bool    `yaml:"enabled"`
	RRFK        int     `yaml:"rrf_k"`
	DenseK      int     `yaml:"dense_k"`
	StrongFloor float32 `yaml:"strong_floor"`
	BaseFloor   float32 `yaml:"base_floor"`
	MaxResults  int     `yaml:"max_results"`
	// Ranking configures an optional recency x importance x relevance
	// ranking pass over already-fused/searched results (memory-recall-ranking
	// spec, Requirement: Ranking Combines Relevance, Recency, and
	// Importance). See RankingConfig's own doc for the D7-style rollback
	// guarantee this mirrors.
	Ranking RankingConfig `yaml:"ranking"`
}

// RankingConfig configures Omnia's optional recency x importance x relevance
// ranking pass (Generative Agents' weighted-sum retrieval shape) applied at
// the internal/mcp wiring boundary, never inside internal/recall or
// internal/store — both of those stay untouched, pure leaves this slice
// (memory-recall-ranking spec, design note D6). Enabled defaults to false
// (Requirement: Backward-Compatible Default Behavior): when unset,
// mem_search/omnia search order and scores stay byte-for-byte identical to
// today's relevance-only recall.Fuse/store.Search output.
type RankingConfig struct {
	Enabled bool `yaml:"enabled"`
	// RecencyHalfLifeDays controls how fast the recency component decays:
	// 1.0 at zero elapsed days, 0.5 at this many elapsed days, asymptotically
	// approaching (never reaching) zero — recency alone must never be able
	// to exclude a result (Requirement: Recency Decay Never Hard-Filters).
	RecencyHalfLifeDays float64 `yaml:"recency_half_life_days"`
	// Weights are the per-component multipliers applied to each normalized
	// [0,1] signal (relevance/recency/importance) before summing them into
	// the final ranking score.
	Weights RankingWeights `yaml:"weights"`
	// ImportanceOverrides lets an operator override
	// DefaultImportanceWeight's heuristic-by-type default per
	// Observation.Type (Requirement: Importance Heuristic By Observation
	// Type — "MUST allow per-type overrides via config").
	ImportanceOverrides map[string]float32 `yaml:"importance_overrides"`
}

// RankingWeights are the per-component multipliers RankScore applies to each
// normalized [0,1] signal. All three default to 1.0 (Requirement:
// Configurable Weights With Safe Defaults) — an equal-weight sum, matching
// the Generative Agents retrieval formula's shape.
type RankingWeights struct {
	Recency    float32 `yaml:"recency"`
	Importance float32 `yaml:"importance"`
	Relevance  float32 `yaml:"relevance"`
}

// DefaultImportanceWeight returns the heuristic-by-type importance weight
// (Requirement: Importance Heuristic By Observation Type) BEFORE any
// RankingConfig.ImportanceOverrides is applied: decision/architecture score
// highest (3), bugfix/pattern/manual are mid-tier (2), and everything else —
// including session-chatter types like tool_use/file_read/search — is the
// baseline (1).
func DefaultImportanceWeight(obsType string) float32 {
	switch obsType {
	case "decision", "architecture":
		return 3
	case "bugfix", "pattern", "manual":
		return 2
	default:
		return 1
	}
}

type SourcesConfig struct {
	Discord   DiscordConfig   `yaml:"discord"`
	GitHub    GitHubConfig    `yaml:"github"`
	Atlassian AtlassianConfig `yaml:"atlassian"`
}

// AtlassianConfig holds ONE shared Atlassian Cloud site + Basic-auth
// credential pair (email + API token), used by both the Jira and Confluence
// adapters (design decision: single shared Cloud token/site, not one token
// per adapter). Jira/Confluence keep their own Enabled flag, project/space
// keys, and Engram Project so each source can be toggled and routed
// independently even though auth is shared.
//
// Phase 2 (this skeleton) only declares the config shape and its defaults.
// Router.ResolveJira/ResolveConfluence and collect.go wiring land with the
// Jira/Confluence adapters themselves (later phases).
type AtlassianConfig struct {
	SiteURL    string           `yaml:"site_url"`
	Email      string           `yaml:"email"`
	Token      string           `yaml:"token"`
	Jira       JiraConfig       `yaml:"jira"`
	Confluence ConfluenceConfig `yaml:"confluence"`
}

type JiraConfig struct {
	Enabled bool `yaml:"enabled"`
	// ProjectKeys lists the Jira project keys to ingest (e.g. "ENG", "OPS").
	ProjectKeys []string `yaml:"project_keys"`
	// Project is the Engram project this source's items route to by default.
	Project string `yaml:"project"`
}

type ConfluenceConfig struct {
	Enabled bool `yaml:"enabled"`
	// SpaceKeys lists the Confluence space keys to ingest.
	SpaceKeys []string `yaml:"space_keys"`
	// Project is the Engram project this source's items route to by default.
	Project string `yaml:"project"`
}

type DiscordConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Token    string          `yaml:"token"`
	Channels []ChannelConfig `yaml:"channels"`
	Project  string          `yaml:"project"`
}

type ChannelConfig struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Guild string `yaml:"guild"`
}

type GitHubConfig struct {
	Enabled bool     `yaml:"enabled"`
	Token   string   `yaml:"token"`
	Repos   []string `yaml:"repos"`
	Project string   `yaml:"project"`
	// IncludeCommits also ingests commit history (sha, message, author login +
	// git name/email, date, url) as `github-commit` observations, in addition to
	// issues/PRs. Off by default: the first run backfills up to MaxCommitsPerRepo
	// per repo over the backfill window, which can be a lot of observations.
	IncludeCommits bool `yaml:"include_commits"`
	// MaxCommitsPerRepo caps commits fetched per repo per run (0 → default 300).
	MaxCommitsPerRepo int `yaml:"max_commits_per_repo"`
}

// Router resolves the Engram project for a given ingestion origin.
// Resolution order: explicit routes map → default derivation → fallback.
type Router struct {
	routes         map[string]string // from config.Routes
	defaultProject string            // config.Engram.DefaultProject (last-resort fallback only)
}

// NewRouter creates a Router from the config routes map and fallback default project.
func NewRouter(routes map[string]string, defaultProject string) *Router {
	r := &Router{
		routes:         make(map[string]string),
		defaultProject: normalizeProject(defaultProject),
	}
	for k, v := range routes {
		r.routes[k] = normalizeProject(v)
	}
	return r
}

// ResolveGitHub returns the project for a GitHub repo (format: "owner/repo").
// Key tried: "github/{owner}/{repo}" → falls back to repo name (without owner) → defaultProject.
func (r *Router) ResolveGitHub(ownerRepo string) string {
	key := "github/" + ownerRepo
	if p, ok := r.routes[key]; ok {
		return p
	}
	// Default: repo name without owner.
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) == 2 && parts[1] != "" {
		return normalizeProject(parts[1])
	}
	return r.defaultProject
}

// ResolveDiscord returns the project for a Discord channel (format: channel ID as string).
// Key tried: "discord/{channelID}" → falls back to guild slug → defaultProject.
func (r *Router) ResolveDiscord(channelID string, guildSlug string) string {
	key := "discord/" + channelID
	if p, ok := r.routes[key]; ok {
		return p
	}
	if guildSlug != "" {
		return normalizeProject(guildSlug)
	}
	return r.defaultProject
}

// ResolveJira returns the project for a Jira project key.
// Key tried: "jira/{projectKey}" → falls back to the (normalized) project key
// itself → defaultProject. Mirrors ResolveGitHub/ResolveDiscord's pattern.
func (r *Router) ResolveJira(projectKey string) string {
	key := "jira/" + projectKey
	if p, ok := r.routes[key]; ok {
		return p
	}
	if projectKey != "" {
		return normalizeProject(projectKey)
	}
	return r.defaultProject
}

// ResolveConfluence returns the project for a Confluence space key.
// Key tried: "confluence/{spaceKey}" → falls back to the (normalized) space
// key itself → defaultProject. Mirrors ResolveJira's pattern.
func (r *Router) ResolveConfluence(spaceKey string) string {
	key := "confluence/" + spaceKey
	if p, ok := r.routes[key]; ok {
		return p
	}
	if spaceKey != "" {
		return normalizeProject(spaceKey)
	}
	return r.defaultProject
}

// normalizeProject delegates to the canonical internal/projectname leaf
// package so config-sourced project names normalize identically to the
// store and project-detection paths (see internal/store.NormalizeProject
// and internal/project's normalize).
func normalizeProject(s string) string {
	return projectname.Normalize(s)
}

// DefaultPath returns the default config file path.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "omnia", "config.yaml")
}

// Load reads and parses the config file at the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.RecallEnabledExplicit = recallEnabledKeyPresent(data)
	applyDefaults(&cfg, data)
	return &cfg, nil
}

// recallEnabledKeyPresent reports whether the loaded YAML explicitly set
// `recall.enabled` (to either true or false), as opposed to the section or
// key being entirely absent. Uses a pointer field in a throwaway struct so
// "absent" and "false" are distinguishable — Config.Recall.Enabled itself
// stays a plain bool (its zero value IS the documented D7 default), so this
// second, best-effort unmarshal is the only way to recover the distinction
// without changing that field's type. The re-parse operates on already
// validated data (Load's own yaml.Unmarshal above already succeeded), so a
// failure here is not expected; if it somehow occurs, treat the key as
// absent (safest: falls through to the D7-preserving off-by-default path
// rather than risking a false positive that would auto-enable recall).
func recallEnabledKeyPresent(data []byte) bool {
	var probe struct {
		Recall struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"recall"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Recall.Enabled != nil
}

// injectionBudgetMaxTokensKeyPresent reports whether the loaded YAML
// explicitly set `injection.budget.max_tokens` (to any integer, including
// 0), as opposed to the key being entirely absent. Mirrors
// recallEnabledKeyPresent's own explicit-vs-absent probe (see that func's
// doc comment) for the same reason: TokenBudgetConfig.MaxTokens is a plain
// int, so its zero value cannot on its own distinguish "operator explicitly
// disabled the trim pass via max_tokens: 0" (ApplyTokenBudget's own
// MaxTokens<=0 branch) from "operator never mentioned max_tokens at all"
// (which must still default to 1500). A re-parse failure here is not
// expected since Load's own yaml.Unmarshal already succeeded; if it somehow
// occurs, treat the key as absent (safest: falls through to applying the
// 1500 default rather than risking a stuck zero-budget that silently drops
// every result).
func injectionBudgetMaxTokensKeyPresent(data []byte) bool {
	var probe struct {
		Injection struct {
			Budget struct {
				MaxTokens *int `yaml:"max_tokens"`
			} `yaml:"budget"`
		} `yaml:"injection"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Injection.Budget.MaxTokens != nil
}

func applyDefaults(cfg *Config, data []byte) {
	if cfg.Engram.BaseURL == "" {
		cfg.Engram.BaseURL = "http://127.0.0.1:7437"
	}
	if cfg.Engram.DefaultProject == "" {
		cfg.Engram.DefaultProject = "omnia"
	}
	if cfg.BackfillDays == 0 {
		cfg.BackfillDays = 30
	}
	if cfg.Sources.Discord.Project == "" {
		cfg.Sources.Discord.Project = cfg.Engram.DefaultProject
	}
	if cfg.Sources.GitHub.Project == "" {
		cfg.Sources.GitHub.Project = cfg.Engram.DefaultProject
	}
	if cfg.Sources.Atlassian.Jira.Project == "" {
		cfg.Sources.Atlassian.Jira.Project = cfg.Engram.DefaultProject
	}
	if cfg.Sources.Atlassian.Confluence.Project == "" {
		cfg.Sources.Atlassian.Confluence.Project = cfg.Engram.DefaultProject
	}
	if cfg.Embeddings.BaseURL == "" {
		cfg.Embeddings.BaseURL = "http://localhost:11434"
	}
	if cfg.Embeddings.Model == "" {
		// jina/jina-embeddings-v2-base-es: purpose-built ES<->EN shared-space
		// model, 768-dim (matches the prior nomic default, no schema change),
		// 8192-token context. See engram obs #1401 (supersedes design.md D3).
		cfg.Embeddings.Model = "jina/jina-embeddings-v2-base-es"
	}
	if cfg.Embeddings.Dim == 0 {
		cfg.Embeddings.Dim = 768
	}
	// Embeddings.DBPath intentionally gets NO default here (unlike BaseURL/
	// Model/Dim above): the right default depends on the active data
	// directory (OMNIA_DATA_DIR / --data-dir), which Load has no visibility
	// into. It stays "" unless the operator sets it explicitly; callers must
	// resolve the effective path via ResolveEmbeddingsDBPath (see #82 — a
	// single eagerly-defaulted global path is exactly what let `omnia embed`
	// prune the wrong instance's vectors when run under an alternate data
	// dir).
	// Recall.Enabled intentionally has NO default override — its zero value
	// (false) IS the default (D7: off = today's FTS5-only path). Only the
	// fusion params get defaults, so an operator who opts in by setting only
	// `recall: { enabled: true }` still gets the proven D1/D2 constants.
	if cfg.Recall.RRFK == 0 {
		cfg.Recall.RRFK = 60
	}
	if cfg.Recall.DenseK == 0 {
		cfg.Recall.DenseK = 5
	}
	// jina-calibrated floors (issue #83): the original 0.65/0.55 D2 constants
	// starved recall for the default model, jina/jina-embeddings-v2-base-es,
	// whose cosine-similarity distribution runs lower than what those
	// constants assumed. 0.35/0.25 is the value the live instance was
	// empirically tuned to (engram/omnia memory #1434) and is now the
	// shipped default for every fresh install, not just a manual tweak.
	if cfg.Recall.StrongFloor == 0 {
		cfg.Recall.StrongFloor = 0.35
	}
	if cfg.Recall.BaseFloor == 0 {
		cfg.Recall.BaseFloor = 0.25
	}
	if cfg.Recall.MaxResults == 0 {
		cfg.Recall.MaxResults = 50
	}
	// Ranking.Enabled intentionally has NO default override — its zero value
	// (false) IS the default, mirroring Recall.Enabled's own convention
	// above. Only the ranking params get defaults, so an operator who opts
	// in by setting only `recall: { ranking: { enabled: true } }` still gets
	// an equal-weight sum and a sane 14-day recency half-life instead of
	// zero-valued weights that would silently zero out every RankScore.
	if cfg.Recall.Ranking.Weights.Recency == 0 {
		cfg.Recall.Ranking.Weights.Recency = 1.0
	}
	if cfg.Recall.Ranking.Weights.Importance == 0 {
		cfg.Recall.Ranking.Weights.Importance = 1.0
	}
	if cfg.Recall.Ranking.Weights.Relevance == 0 {
		cfg.Recall.Ranking.Weights.Relevance = 1.0
	}
	if cfg.Recall.Ranking.RecencyHalfLifeDays == 0 {
		cfg.Recall.Ranking.RecencyHalfLifeDays = 14
	}
	// Procedural.Enabled intentionally has NO default override — its zero
	// value (false) IS the default, mirroring Recall.Enabled/Ranking.Enabled's
	// own convention above. Only the governance-tuning params get defaults, so
	// an operator who opts in by setting only `procedural: { enabled: true }`
	// still gets a sane trust_threshold/confidence_floor/review_after_days
	// instead of zero-valued governance constants that would promote every
	// candidate on its first reuse (threshold=0) or retire nothing (floor=0
	// looks like "never retire" only by accident).
	if cfg.Procedural.TrustThreshold == 0 {
		cfg.Procedural.TrustThreshold = 3
	}
	if cfg.Procedural.ConfidenceFloor == 0 {
		cfg.Procedural.ConfidenceFloor = 0.15
	}
	if cfg.Procedural.ReviewAfterDays == 0 {
		cfg.Procedural.ReviewAfterDays = 14
	}
	// Injection.Budget.Enabled intentionally has NO default override — its
	// zero value (false) IS the default, mirroring Recall.Enabled/
	// Ranking.Enabled/StructuralForgetting.Enabled/Procedural.Enabled's own
	// convention above. Only MaxTokens gets a default, so an operator who
	// opts in with only `injection: { budget: { enabled: true } }` still
	// gets a sane ceiling instead of a zero-valued budget that would trim
	// every result away (ApplyTokenBudget treats MaxTokens<=0 as "disabled",
	// not "budget of zero").
	//
	// PR2 review fix (WARNING): only apply that 1500 default when
	// `injection.budget.max_tokens` is entirely ABSENT from the YAML (see
	// injectionBudgetMaxTokensKeyPresent, mirroring recallEnabledKeyPresent's
	// own explicit-vs-absent precedent above). An operator who explicitly
	// writes `max_tokens: 0` is deliberately reaching ApplyTokenBudget's own
	// MaxTokens<=0 "disabled" branch, not accidentally leaving the key unset
	// — without this probe that branch is unreachable from real config.
	if cfg.Injection.Budget.MaxTokens == 0 && !injectionBudgetMaxTokensKeyPresent(data) {
		cfg.Injection.Budget.MaxTokens = 1500
	}
}

// legacyEmbeddingsDBPath is the historic global default for Omnia's own
// embeddings vector store, unchanged since before per-data-dir scoping
// existed. It stays the default for the canonical (no-override) data
// directory so upgrading to this fix never relocates or invalidates an
// existing install's embeddings.
func legacyEmbeddingsDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "omnia", "embeddings.db")
}

// ValidateEmbeddings checks an EmbeddingsConfig for the two
// internally-inconsistent shapes EMBM-3 forbids:
//
//  1. a configured Dim that would require truncating a model's native
//     output, for a model NOT flagged Matryoshka (MRL) capable in
//     internal/embed's model registry. Truncating a non-MRL model's output
//     silently degrades every stored/query vector with no error surfaced
//     otherwise.
//  2. a configured Dim GREATER than the model's native output dimension,
//     for ANY model (MRL-capable or not) — Matryoshka training only ever
//     supports truncating a model's native output, never expanding it, so a
//     Dim above NativeDim can never be satisfied and would otherwise only
//     surface as a generic runtime dim-mismatch error deep inside
//     embed.Client.Embed instead of at config-load time.
//
// This must be checked before any Ollama call — callers wire it in right
// after config.Load (cmd/omnia/embed.go, cmd/omnia/autoembed.go,
// cmd/omnia/recall.go, cmd/omnia/dashboard.go, cmd/omnia/eval.go).
//
// An unregistered model name (embed.LookupModel's second return value false)
// is NOT rejected here: EMBM-1 requires the active model stay selectable via
// config without code changes, so an operator picking a model this registry
// hasn't been taught about yet must not be blocked by this guard —
// embed.Client.Embed's own dim-mismatch check remains the safety net for
// that model at embed time.
func ValidateEmbeddings(cfg EmbeddingsConfig) error {
	info, ok := embed.LookupModel(cfg.Model)
	if !ok {
		return nil
	}
	if cfg.Dim <= 0 {
		return nil
	}
	if cfg.Dim > info.NativeDim {
		return fmt.Errorf(
			"config: embeddings.dim %d exceeds %s's native %d-dim output; a model cannot produce more dimensions than it was trained for",
			cfg.Dim, cfg.Model, info.NativeDim,
		)
	}
	if cfg.Dim < info.NativeDim && !info.MRL {
		return fmt.Errorf(
			"config: embeddings.dim %d truncates %s's native %d-dim output, but %s is not Matryoshka (MRL) capable; truncating a non-MRL model silently degrades its embeddings",
			cfg.Dim, cfg.Model, info.NativeDim, cfg.Model,
		)
	}
	return nil
}

// ResolveEmbeddingsDBPath returns the file path Omnia should use for its own
// embeddings vector store, given an optional explicit override (typically
// EmbeddingsConfig.DBPath as loaded from config.yaml) and the ACTIVE data
// directory this run resolved (e.g. via datadir.Resolve or
// engramdb.ResolveDataDir — same OMNIA_DATA_DIR/--data-dir precedence used
// to open the memory database).
//
// Resolution order:
//
//  1. explicit, if non-empty — an operator-set db_path always wins,
//     unconditionally, exactly as before this function existed. This is
//     the escape hatch for anyone who wants a fixed, shared, or otherwise
//     custom location regardless of data dir.
//  2. otherwise, when dataDir is the canonical, no-override data directory
//     (datadir.HomeDefault(), computed independently of OMNIA_DATA_DIR so an
//     alternate data dir set via env is NOT mistaken for the home default) —
//     legacyEmbeddingsDBPath(), the historic global default. This keeps
//     every existing install byte-for-byte unaffected: no relocation, no
//     forced re-embed, after upgrading to this fix.
//  3. otherwise (dataDir is an alternate directory — OMNIA_DATA_DIR or
//     --data-dir pointing elsewhere, e.g. an eval harness or a second
//     instance) — <dataDir>/embeddings.db, so that instance gets its OWN
//     vector store. This is the fix for #82: `omnia embed` run with an
//     alternate OMNIA_DATA_DIR used to reconcile against the SAME shared
//     global embeddings.db as the primary instance, and its prune step
//     (internal/embed.Reconcile → Store.Prune) deleted every vector not in
//     the tiny alt corpus's live set — silently wiping the primary
//     instance's real embeddings. Scoping the alt instance's store to its
//     own data dir makes that impossible: the two instances never touch
//     the same file.
func ResolveEmbeddingsDBPath(explicit, dataDir string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if dataDir == "" || dataDir == datadir.HomeDefault() {
		return legacyEmbeddingsDBPath()
	}
	return filepath.Join(dataDir, "embeddings.db")
}
