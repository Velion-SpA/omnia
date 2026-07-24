// Package mcp implements the Model Context Protocol server for Omnia.
//
// This exposes memory tools via MCP stdio transport so ANY agent
// (OpenCode, Claude Code, Cursor, Windsurf, etc.) can use Omnia's
// persistent memory just by adding it as an MCP server.
//
// Tool profiles allow agents to load only the tools they need:
//
//	omnia mcp                    → all 19 tools (default)
//	omnia mcp --tools=agent      → 15 tools agents actually use (per skill files)
//	omnia mcp --tools=admin      → 4 tools for TUI/CLI (delete, stats, timeline, merge)
//	omnia mcp --tools=agent,admin → combine profiles
//	omnia mcp --tools=mem_save,mem_search → individual tool names
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/diagnostic"
	"github.com/velion/omnia/internal/embed"
	projectpkg "github.com/velion/omnia/internal/project"
	"github.com/velion/omnia/internal/purge"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/timeutil"
)

const sourceProcessOverride = "process_override"

// MCPConfig holds configuration for the MCP server.
type MCPConfig struct {
	// DefaultProject is a trusted process-level project override supplied by
	// long-lived MCP hosts (for example, `omnia mcp --project NAME` or
	// ENGRAM_PROJECT). When set, it is used before cwd detection for MCP
	// auto-resolution; per-call project arguments remain separately validated.
	DefaultProject string

	// BM25Floor overrides the default BM25 score floor used by FindCandidates
	// during conflict candidate detection (REQ-001). The floor is the minimum
	// acceptable BM25 rank (negative; closer to 0 = better match). Candidates
	// whose score falls below this threshold are excluded.
	//
	// nil means "use the store default" (-2.0). An explicit pointer value
	// (including 0.0) is forwarded directly. Using a pointer avoids the
	// zero-value ambiguity where 0.0 would otherwise be indistinguishable
	// from "not set".
	BM25Floor *float64

	// Limit overrides the maximum number of conflict candidates returned per
	// mem_save call (REQ-001). nil means "use the store default" (3).
	// An explicit pointer value (including 0) is forwarded directly.
	Limit *int

	// Recall, when non-nil, enables hybrid (lexical+semantic) recall fusion
	// for mem_search (design D1/D2/D6, human-like-memory PR3). nil (the
	// default) means recall.enabled=false in config — handleSearch calls
	// store.Search directly, exactly as it always has (D7 rollback
	// guarantee: byte-for-byte today's FTS5-only path, zero data migration).
	// Built by cmd/omnia/main.go from RecallConfig + EmbeddingsConfig when
	// recall.enabled is true.
	Recall *recall.Service

	// AutoEmbed, when non-nil, is the async worker that embeds a memory
	// out-of-band right after it is saved, so it becomes semantically
	// searchable within seconds without a manual `omnia embed` run
	// (human-like-memory PR4). nil (the default) means embeddings are
	// disabled in config — mem_save enqueues nothing and the save path is
	// byte-for-byte today's. Enqueue is non-blocking, so the save never waits
	// on the embedding.
	AutoEmbed *embed.Worker

	// RecallRanking configures the optional recency x importance x relevance
	// ranking pass over handleSearch's results (memory-recall-ranking spec,
	// recall_ranking.go's RankResults). The zero value (Enabled=false) is the
	// default: handleSearch's response stays byte-for-byte identical to
	// today's relevance-only order (Requirement: Backward-Compatible Default
	// Behavior) even though RankResults is unconditionally called — it is a
	// pure no-op when RecallRanking.Enabled is false.
	RecallRanking config.RankingConfig

	// AnchorProbe resolves optional mem_save `code_anchors` into persisted
	// memory_anchors rows (omnia-structural-forgetting, PR1 — the write
	// side only). nil (the default) falls back to a lazily-constructed real
	// anchor.NewProbe() (real git shell-out) inside handleSave. Declared as
	// the AnchorCapturer interface — not the concrete *anchor.Probe type —
	// so tests can inject a fake without needing anchor.Probe's unexported
	// runGit field. Anchor capture is unconditional whenever a mem_save call
	// supplies code_anchors, and is best-effort: any capture failure (no
	// git, not a repo, bad range, symbol not found, etc.) is logged and
	// swallowed — mem_save itself must NEVER fail because of anchoring
	// (REQ-002 graceful degradation). Omitting code_anchors leaves the save
	// byte-identical to today regardless of this field.
	AnchorProbe AnchorCapturer

	// StructuralForgetting gates handleSearch's stale-anchor downrank +
	// receipt (memory-structural-forgetting spec, Requirement 6;
	// omnia-structural-forgetting PR2). The zero value (Enabled=false) is
	// the default: handleSearch's response — including whether
	// anchor_receipt is ever attached and whether score_breakdown's
	// staleness_penalty is ever non-zero — stays byte-for-byte identical to
	// today (Regression: memory with no anchor unaffected, extended to
	// "flag off"). Anchor CAPTURE (mem_save's code_anchors) and `omnia
	// forget-scan` are never gated by this flag either way — see
	// config.Config.StructuralForgetting's doc.
	StructuralForgetting config.StructuralForgettingConfig

	// Procedural, when non-nil, enables the omnia-procedural-memory
	// playbook/anti-playbook slot (design obs #1602, spec obs #1606):
	// mem_save/mem_update's online candidate induction + SSGM
	// applied_procedure reuse feedback, and mem_search/mem_context's
	// contrastive procedure card. nil (the default, mirroring AutoEmbed's
	// own nil-means-disabled convention) means procedural.enabled=false in
	// config.yaml — every one of those call sites is then a total no-op,
	// keeping mem_save/mem_update/mem_search/mem_context byte-for-byte
	// identical to their pre-existing contracts (spec: backward-compatibility
	// domain). Built by cmd/omnia/main.go from config.ProceduralConfig only
	// when Enabled is true.
	Procedural *ProceduralWiring

	// Injection configures Omnia v0.3's "Context Economy" family of optional
	// re-sort/trim passes over handleSearch's already-ranked results (design
	// obs #1643, spec obs #1642): ApplyMMR (PR4) runs after
	// RankResults/ApplyStalenessDownrank to remove near-duplicates, then
	// ApplyTokenBudget (PR2) is the LAST pass in the pipeline. The zero value
	// (Injection.Budget.Enabled=false, Injection.Diversity.Enabled=false) is
	// the default: handleSearch's response stays byte-for-byte identical to
	// pre-v0.3 output even though both passes are unconditionally called —
	// each is a pure no-op when its own Enabled flag is false (spec REQ6),
	// mirroring RecallRanking/StructuralForgetting's own convention above.
	Injection config.InjectionConfig
}

var suggestTopicKey = store.SuggestTopicKey

var addPromptIfMissing = func(s *store.Store, params store.AddPromptParams) (int64, bool, error) {
	return s.AddPromptIfMissing(params)
}

var loadMCPStats = func(s *store.Store) (*store.Stats, error) {
	return s.Stats()
}

// auditAppend is the injectable seam over audit.Append (memory-provenance
// foundation, omnia-provenance-foundation) so save/delete audit-append call
// sites are testable without touching the real ~/.local/state/omnia/audit.jsonl
// file. audit.Append itself never returns an error and swallows failures
// internally (logs to stderr) — this seam does not change that contract, it
// only makes the call observable in tests via a stub.
var auditAppend = audit.Append

func currentWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// enqueueAutoEmbed schedules an out-of-band embedding for a just-saved memory
// so it becomes semantically searchable within seconds, without a manual
// `omnia embed` run. It is a no-op when the worker is disabled (nil). Enqueue
// is non-blocking, and a fetch or full-queue miss is intentionally swallowed:
// the periodic Reconcile backstop re-embeds anything missed, so a save must
// never fail or slow down because of embedding.
func enqueueAutoEmbed(cfg MCPConfig, s *store.Store, obsID int64) {
	if cfg.AutoEmbed == nil {
		return
	}
	obs, err := s.GetObservation(obsID)
	if err != nil {
		return
	}
	cfg.AutoEmbed.Enqueue(embed.Job{
		SyncID:    obs.SyncID,
		ObsID:     int(obs.ID),
		Project:   strFromPtr(obs.Project),
		Type:      obs.Type,
		TopicKey:  strFromPtr(obs.TopicKey),
		Title:     obs.Title,
		Content:   obs.Content,
		UpdatedAt: obs.UpdatedAt,
	})
}

func strFromPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ensureImplicitSessionWithCWD(s *store.Store, sessionID, project string) error {
	return s.CreateSession(sessionID, project, currentWorkingDirectory())
}

// ─── Tool Profiles ───────────────────────────────────────────────────────────
//
// "agent" — tools AI agents use during coding sessions:
//   mem_save, mem_search, mem_context, mem_session_summary,
//   mem_session_start, mem_session_end, mem_get_observation,
//   mem_suggest_topic_key, mem_capture_passive, mem_save_prompt
//
// "admin" — tools for manual curation, TUI, and dashboards:
//   mem_update, mem_delete, mem_stats, mem_timeline, mem_merge_projects
//
// "all" (default) — every tool registered.

// ProfileAgent contains the tool names that AI agents need.
// Sourced from actual skill files and memory protocol instructions
// across all 4 supported agents (Claude Code, OpenCode, Gemini CLI, Codex).
var ProfileAgent = map[string]bool{
	"mem_save":              true, // proactive save — referenced 17 times across protocols
	"mem_search":            true, // search past memories — referenced 6 times
	"mem_context":           true, // recent context from previous sessions — referenced 10 times
	"mem_session_summary":   true, // end-of-session summary — referenced 16 times
	"mem_session_start":     true, // register session start
	"mem_session_end":       true, // mark session completed
	"mem_get_observation":   true, // full observation content after search — referenced 4 times
	"mem_suggest_topic_key": true, // stable topic key for upserts — referenced 3 times
	"mem_capture_passive":   true, // extract learnings from text — referenced in Gemini/Codex protocol
	"mem_save_prompt":       true, // save user prompts
	"mem_update":            true, // update observation by ID — skills say "use mem_update when you have an exact ID to correct"
	"mem_current_project":   true, // detect current project — recommended first call for agents (REQ-313)
	"mem_judge":             true, // record verdict on a pending memory conflict (REQ-003, Phase D)
	"mem_compare":           true, // persist an agent-judged semantic verdict via JudgeBySemantic (REQ-011, Phase G)
	"mem_doctor":            true, // read-only operational diagnostics for agents
	"mem_review":            true, // list/mark observations whose review_after lifecycle is stale
	"mem_pin":               true, // local pin for context priority
	"mem_unpin":             true, // local unpin for context priority
}

// ProfileAdmin contains tools for TUI, dashboards, and manual curation
// that are NOT referenced in any agent skill or memory protocol.
var ProfileAdmin = map[string]bool{
	"mem_delete":         true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_stats":          true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_timeline":       true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_merge_projects": true, // destructive curation tool — not for agent use
}

// Profiles maps profile names to their tool sets.
var Profiles = map[string]map[string]bool{
	"agent": ProfileAgent,
	"admin": ProfileAdmin,
}

// ResolveTools takes a comma-separated string of profile names and/or
// individual tool names and returns the set of tool names to register.
// An empty input means "all" — every tool is registered.
func ResolveTools(input string) map[string]bool {
	input = strings.TrimSpace(input)
	if input == "" || input == "all" {
		return nil // nil means register everything
	}

	result := make(map[string]bool)
	for _, token := range strings.Split(input, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "all" {
			return nil
		}
		if profile, ok := Profiles[token]; ok {
			for tool := range profile {
				result[tool] = true
			}
		} else {
			// Treat as individual tool name
			result[token] = true
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// NewServer creates an MCP server with ALL tools registered (backwards compatible).
func NewServer(s *store.Store) *server.MCPServer {
	return NewServerWithConfig(s, MCPConfig{}, nil)
}

// serverInstructions tells MCP clients when to use Omnia's tools.
// 7 core tools are eager (always in context). The rest are deferred
// and require ToolSearch to load.
const serverInstructions = `Omnia provides persistent memory that survives across sessions and compactions.

CORE TOOLS (always available — use without ToolSearch):
  mem_save — save decisions, bugs, discoveries, conventions PROACTIVELY (do not wait to be asked)
  mem_search — find past work, decisions, or context from previous sessions
  mem_context — get recent session history (call at session start or after compaction)
  mem_session_summary — save end-of-session summary (MANDATORY before saying "done")
  mem_get_observation — get full untruncated content of a search result by ID
  mem_save_prompt — save user prompt for context
  mem_current_project — detect current project from cwd (recommended first call)

DEFERRED TOOLS (use ToolSearch when needed):
  mem_update, mem_review, mem_pin, mem_unpin, mem_suggest_topic_key, mem_session_start, mem_session_end,
  mem_stats, mem_delete, mem_timeline, mem_capture_passive, mem_merge_projects

PROACTIVE SAVE RULE: Call mem_save immediately after ANY decision, bug fix, discovery, or convention — not just when asked.

## CONFLICT SURFACING — when mem_save returns candidates

After every mem_save call, check the response envelope for judgment_required.

IF judgment_required IS TRUE:
  Iterate candidates[] and call mem_judge once per candidate using the
  judgment_id from that candidate entry. Do NOT use the top-level judgment_id
  for multiple candidates — each candidate has its own.

  HEURISTIC — when to ask the user vs. resolve autonomously:

  ASK the user (conversationally, as part of your next reply) when:
    - confidence is below 0.7, OR
    - the relation you would choose is supersedes or conflicts_with AND the
      observation type is architecture, policy, or decision

  RESOLVE silently (call mem_judge without asking) when:
    - confidence >= 0.7 AND the relation is not supersedes/conflicts_with, OR
    - the relation is related, compatible, scoped, or not_conflict

  HOW TO ASK (conversationally — never via blocking CLI or dashboard prompt):
    Raise it naturally in your next reply to the user. Example phrasing:
    "I noticed memory #abc123 might conflict with what we just saved.
     Want me to mark the new one as superseding it, or are they about
     different scopes? I can also mark them as compatible if both still apply."

  AFTER RESOLUTION (both paths):
    Call mem_judge with the chosen relation, a reason, and if the user gave
    explicit direction, include their words as the evidence field. This persists
    the verdict and closes the pending conflict row.`

// NewServerWithTools creates an MCP server registering only the tools in
// the allowlist. If allowlist is nil, all tools are registered.
func NewServerWithTools(s *store.Store, allowlist map[string]bool) *server.MCPServer {
	return NewServerWithConfig(s, MCPConfig{}, allowlist)
}

// NewServerWithConfig creates an MCP server with full configuration including
// default project detection and optional tool allowlist.
func NewServerWithConfig(s *store.Store, cfg MCPConfig, allowlist map[string]bool) *server.MCPServer {
	return newServerWithActivity(s, cfg, allowlist, NewSessionActivity(10*time.Minute))
}

func newServerWithActivity(s *store.Store, cfg MCPConfig, allowlist map[string]bool, activity *SessionActivity) *server.MCPServer {
	srv := server.NewMCPServer(
		"omnia",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)

	registerTools(srv, s, cfg, allowlist, activity)
	return srv
}

// shouldRegister returns true if the tool should be registered given the
// allowlist. If allowlist is nil, everything is allowed.
func shouldRegister(name string, allowlist map[string]bool) bool {
	if allowlist == nil {
		return true
	}
	return allowlist[name]
}

func registerTools(srv *server.MCPServer, s *store.Store, cfg MCPConfig, allowlist map[string]bool, activity *SessionActivity) {
	writeQueue := newWriteQueue(defaultMCPWriteQueueSize)

	// ─── mem_search (profile: agent, core — always in context) ─────────
	if shouldRegister("mem_search", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_search",
				mcp.WithDescription("Search your persistent memory across all sessions. Use this to find past decisions, bugs fixed, patterns used, files changed, or any context from previous coding sessions."),
				mcp.WithTitleAnnotation("Search Memory"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("query",
					mcp.Required(),
					mcp.Description("Search query — natural language or keywords"),
				),
				mcp.WithString("type",
					mcp.Description("Filter by type: tool_use, file_change, command, file_read, search, manual, decision, architecture, bugfix, pattern"),
				),
				mcp.WithString("project",
					mcp.Description("Filter by project name. Ignored when all_projects=true."),
				),
				mcp.WithBoolean("all_projects",
					mcp.Description("Search across every project instead of the current one. When true, the project argument is ignored and results may come from any project. Useful for recalling decisions logged elsewhere when you don't know the project key."),
				),
				mcp.WithString("scope",
					mcp.Description("Filter by scope: project (default) or personal"),
				),
				mcp.WithNumber("limit",
					mcp.Description("Max results (default: 10, max: 20)"),
				),
				mcp.WithBoolean("explain",
					mcp.Description("Attach a per-hit score breakdown (lexical, semantic, fusion, recency, importance, final, staleness_penalty) to each result for transparency into why it ranked where it did. Omitted by default — response shape is unchanged unless explicitly requested."),
				),
			),
			handleSearch(s, cfg, activity),
		)
	}

	// ─── mem_save (profile: agent, core — always in context) ───────────
	if shouldRegister("mem_save", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_save",
				mcp.WithTitleAnnotation("Save Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Save an important observation to persistent memory. Call this PROACTIVELY after completing significant work — don't wait to be asked.

WHEN to save (call this after each of these):
- Architectural decisions or tradeoffs
- Bug fixes (what was wrong, why, how you fixed it)
- New patterns or conventions established
- Configuration changes or environment setup
- Important discoveries or gotchas
- File structure changes

FORMAT for content — use this structured format:
  **What**: [concise description of what was done]
  **Why**: [the reasoning, user request, or problem that drove it]
  **Where**: [files/paths affected, e.g. src/auth/middleware.ts, internal/store/store.go]
  **Learned**: [any gotchas, edge cases, or decisions made — omit if none]

TITLE should be short and searchable, like: "JWT auth middleware", "FTS5 query sanitization", "Fixed N+1 in user list"

Examples:
  title: "Switched from sessions to JWT"
  type: "decision"
  content: "**What**: Replaced express-session with jsonwebtoken for auth\n**Why**: Session storage doesn't scale across multiple instances\n**Where**: src/middleware/auth.ts, src/routes/login.ts\n**Learned**: Must set httpOnly and secure flags on the cookie, refresh tokens need separate rotation logic"

  title: "Fixed FTS5 syntax error on special chars"
  type: "bugfix"
  content: "**What**: Wrapped each search term in quotes before passing to FTS5 MATCH\n**Why**: Users typing queries like 'fix auth bug' would crash because FTS5 interprets special chars as operators\n**Where**: internal/store/store.go — sanitizeFTS() function\n**Learned**: FTS5 MATCH syntax is NOT the same as LIKE — always sanitize user input"`),
				mcp.WithString("title",
					mcp.Required(),
					mcp.Description("Short, searchable title (e.g. 'JWT auth middleware', 'Fixed N+1 query')"),
				),
				mcp.WithString("content",
					mcp.Description("Structured content using **What**, **Why**, **Where**, **Learned** format. Required unless observation alias is provided."),
				),
				mcp.WithString("observation",
					mcp.Description("Backward-compatible alias for content. Prefer content for new clients."),
				),
				mcp.WithString("type",
					mcp.Description("Category: decision, architecture, bugfix, pattern, config, discovery, learning (default: manual)"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID to associate with (default: manual-save-{project})"),
				),
				mcp.WithString("scope",
					mcp.Description("Scope for this observation: project (default) or personal"),
				),
				mcp.WithString("topic_key",
					mcp.Description("Optional topic identifier for upserts (e.g. architecture/auth-model). Reuses and updates the latest observation in same project+scope."),
				),
				mcp.WithString("project",
					mcp.Description("Optional explicit project for this memory. Accepted only when backed by known context (existing project, matching session, repo config, or ambiguous-project recovery); invalid or unbacked names fail loudly."),
				),
				mcp.WithString("project_choice_reason",
					mcp.Description("Must be user_selected_after_ambiguous_project, and only after the user explicitly chose one of available_projects from an ambiguous_project error."),
				),
				mcp.WithString("recovery_token",
					mcp.Description("Short-lived token returned by an ambiguous_project error. Required with project_choice_reason=user_selected_after_ambiguous_project."),
				),
				mcp.WithBoolean("capture_prompt",
					mcp.Description("Automatically capture the current user prompt when available (default: true). Set false for SDD artifacts or automated saves."),
				),
				mcp.WithString("error_signature",
					mcp.Description("Optional raw error text (a stack trace, panic, or log line) to derive a normalized bug signature from. When omitted and type is bugfix-family (bug, bugfix, fix, incident, hotfix), the signature is auto-derived from content instead. Lets a recurring bug be reliably re-found later even when its title/content wording differs (design #1399)."),
				),
				mcp.WithString("outcome",
					mcp.Description("Optional outcome for a bugfix-family save: worked | did_not_work | unknown (default). A fix marked worked ranks above unrecorded/unknown fixes in mem_search; did_not_work ranks below. Use mem_update to set this later once a fix has been verified."),
				),
				mcp.WithString("applied_procedure",
					mcp.Description("Optional sync_id of a procedure (playbook or anti_playbook) you applied before this save's outcome. worked confirms/promotes it (SSGM UPVOTE); did_not_work contradicts it (SSGM DOWNVOTE). Only takes effect when procedural memory is enabled."),
				),
				mcp.WithString("source",
					mcp.Description("Write-time provenance class: user | agent | ingest:tool | ingest:web | ingest:doc. Classified into a trust_tag at save time (attribution, not authentication — never blocks or alters the save). Omitted or unrecognized values default to trust_tag=unverified."),
				),
				mcp.WithArray("code_anchors",
					mcp.Description(`Optional list of code anchors to link this memory to: [{file, symbol, line_start, line_end}]. Each entry resolves a git-blame line range, blame SHA, and content hash via the local git repo and persists it linked to this memory (living-memory / structural-forgetting). Omitting this field leaves mem_save unchanged. A missing git binary, a non-repo directory, or a malformed entry never fails the save — that entry (or all of them) is silently skipped.`),
					mcp.Items(map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file":       map[string]any{"type": "string", "description": "File path, relative to the repo root (e.g. 'internal/auth/middleware.go')."},
							"symbol":     map[string]any{"type": "string", "description": "Optional symbol name (function/type/etc) this anchor targets."},
							"line_start": map[string]any{"type": "number", "description": "1-based start line of the anchored range."},
							"line_end":   map[string]any{"type": "number", "description": "1-based end line of the anchored range (inclusive)."},
						},
						"required": []string{"file", "line_start", "line_end"},
					}),
				),
			),
			queuedWriteHandler(writeQueue, handleSave(s, cfg, activity)),
		)
	}

	// ─── mem_update (profile: agent, deferred) ──────────────────────────
	if shouldRegister("mem_update", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_update",
				mcp.WithDescription("Update an existing observation by ID. Only provided fields are changed."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Update Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("Observation ID to update"),
				),
				mcp.WithString("title",
					mcp.Description("New title"),
				),
				mcp.WithString("content",
					mcp.Description("New content"),
				),
				mcp.WithString("type",
					mcp.Description("New type/category"),
				),
				mcp.WithString("scope",
					mcp.Description("New scope: project or personal"),
				),
				mcp.WithString("topic_key",
					mcp.Description("New topic key (normalized internally)"),
				),
				mcp.WithString("outcome",
					mcp.Description("Mark a bugfix's outcome after the fact: worked | did_not_work | unknown. Use this once a fix has been verified in practice — mem_search ranks worked fixes above unknown ones for the same match."),
				),
				mcp.WithString("applied_procedure",
					mcp.Description("Optional sync_id of a procedure (playbook or anti_playbook) you applied. worked confirms/promotes it (SSGM UPVOTE); did_not_work contradicts it (SSGM DOWNVOTE). Only takes effect when procedural memory is enabled."),
				),
			),
			queuedWriteHandler(writeQueue, handleUpdate(s, cfg)),
		)
	}

	// ─── mem_review (profile: agent, deferred) ──────────────────────────
	if shouldRegister("mem_review", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_review",
				mcp.WithDescription("Review observation lifecycle state. action=list returns observations whose review_after has passed; action=mark_reviewed resets one observation's review_after using its type decay policy."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Review Memories"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("action", mcp.Required(), mcp.Description("Action: list | mark_reviewed")),
				mcp.WithString("project", mcp.Description("Optional project filter for action=list; omit to list all projects.")),
				mcp.WithNumber("limit", mcp.Description("Max results for action=list (default: 10).")),
				mcp.WithNumber("observation_id", mcp.Description("Observation id for action=mark_reviewed.")),
				mcp.WithNumber("id", mcp.Description("Backward-compatible alias for observation_id.")),
			),
			queuedWriteHandler(writeQueue, handleReview(s, cfg)),
		)
	}

	// ─── mem_suggest_topic_key (profile: agent, deferred) ───────────────
	if shouldRegister("mem_suggest_topic_key", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_suggest_topic_key",
				mcp.WithDescription("Suggest a stable topic_key for memory upserts. Use this before mem_save when you want evolving topics (like architecture decisions) to update a single observation over time."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Suggest Topic Key"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("type",
					mcp.Description("Observation type/category, e.g. architecture, decision, bugfix"),
				),
				mcp.WithString("title",
					mcp.Description("Observation title (preferred input for stable keys)"),
				),
				mcp.WithString("content",
					mcp.Description("Observation content used as fallback if title is empty"),
				),
			),
			handleSuggestTopicKey(),
		)
	}

	// ─── mem_delete (profile: admin, deferred) ──────────────────────────
	if shouldRegister("mem_delete", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_delete",
				mcp.WithDescription("Delete an observation by ID. Soft-delete by default; set hard_delete=true for permanent deletion."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Delete Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("Observation ID to delete"),
				),
				mcp.WithBoolean("hard_delete",
					mcp.Description("If true, permanently deletes the observation"),
				),
			),
			queuedWriteHandler(writeQueue, handleDelete(s, cfg)),
		)
	}

	// ─── mem_save_prompt (profile: agent, eager) ────────────────────────
	if shouldRegister("mem_save_prompt", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_save_prompt",
				mcp.WithDescription("Save a user prompt to persistent memory. Use this to record what the user asked — their intent, questions, and requests — so future sessions have context about the user's goals."),
				mcp.WithTitleAnnotation("Save User Prompt"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("The user's prompt text"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID to associate with (default: manual-save-{project})"),
				),
				mcp.WithString("project",
					mcp.Description("Optional recovery target only after ambiguous_project. Ignored unless project_choice_reason is user_selected_after_ambiguous_project."),
				),
				mcp.WithString("project_choice_reason",
					mcp.Description("Must be user_selected_after_ambiguous_project, and only after the user explicitly chose one of available_projects from an ambiguous_project error."),
				),
				mcp.WithString("recovery_token",
					mcp.Description("Short-lived token returned by an ambiguous_project error. Required with project_choice_reason=user_selected_after_ambiguous_project."),
				),
			),
			queuedWriteHandler(writeQueue, handleSavePrompt(s, cfg, activity)),
		)
	}

	// ─── mem_pin / mem_unpin (profile: agent, deferred) ──────────────────
	if shouldRegister("mem_pin", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_pin",
				mcp.WithDescription("Pin a local observation so it appears before recent observations in memory context. Pinned state is local to this device and is not synced."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Pin Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id", mcp.Required(), mcp.Description("Observation ID to pin")),
			),
			handlePin(s, true),
		)
	}
	if shouldRegister("mem_unpin", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_unpin",
				mcp.WithDescription("Unpin a local observation so it only appears in normal recency order. Pinned state is local to this device and is not synced."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Unpin Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id", mcp.Required(), mcp.Description("Observation ID to unpin")),
			),
			handlePin(s, false),
		)
	}

	// ─── mem_context (profile: agent, core — always in context) ────────
	if shouldRegister("mem_context", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_context",
				mcp.WithDescription("Get recent memory context from previous sessions. Shows recent sessions and observations to understand what was done before."),
				mcp.WithTitleAnnotation("Get Memory Context"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project",
					mcp.Description("Filter by project (omit for all projects)"),
				),
				mcp.WithString("scope",
					mcp.Description("Filter observations by scope: project (default) or personal"),
				),
				// JW7: limit param removed — schema advertised it but handleContext never read it.
			),
			handleContext(s, cfg, activity),
		)
	}

	// ─── mem_stats (profile: admin, deferred) ───────────────────────────
	if shouldRegister("mem_stats", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_stats",
				mcp.WithDescription("Show memory system statistics — total sessions, observations, and projects tracked."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Memory Stats"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project",
					mcp.Description("Project to echo in envelope context (omit for auto-detect; stats themselves are global aggregates)"),
				),
			),
			handleStats(s, cfg),
		)
	}

	// ─── mem_timeline (profile: admin, deferred) ────────────────────────
	if shouldRegister("mem_timeline", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_timeline",
				mcp.WithDescription("Show chronological context around a specific observation. Use after mem_search to drill into the timeline of events surrounding a search result. This is the progressive disclosure pattern: search first, then timeline to understand context."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Memory Timeline"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("observation_id",
					mcp.Required(),
					mcp.Description("The observation ID to center the timeline on (from mem_search results)"),
				),
				mcp.WithNumber("before",
					mcp.Description("Number of observations to show before the focus (default: 5)"),
				),
				mcp.WithNumber("after",
					mcp.Description("Number of observations to show after the focus (default: 5)"),
				),
				mcp.WithString("project",
					mcp.Description("Filter by project name (omit for auto-detect)"),
				),
			),
			handleTimeline(s, cfg),
		)
	}

	// ─── mem_get_observation (profile: agent, eager) ────────────────────
	if shouldRegister("mem_get_observation", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_get_observation",
				mcp.WithDescription("Get the full content of a specific observation by ID. Use when you need the complete, untruncated content of an observation found via mem_search or mem_timeline."),
				mcp.WithTitleAnnotation("Get Observation"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("The observation ID to retrieve"),
				),
			),
			handleGetObservation(s, cfg),
		)
	}

	// ─── mem_session_summary (profile: agent, core — always in context) ─
	if shouldRegister("mem_session_summary", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_summary",
				mcp.WithTitleAnnotation("Save Session Summary"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Save a comprehensive end-of-session summary. Call this when a session is ending or when significant work is complete. This creates a structured summary that future sessions will use to understand what happened.

FORMAT — use this exact structure in the content field:

## Goal
[One sentence: what were we building/working on in this session]

## Instructions
[User preferences, constraints, or context discovered during this session. Things a future agent needs to know about HOW the user wants things done. Skip if nothing notable.]

## Discoveries
- [Technical finding, gotcha, or learning 1]
- [Technical finding 2]
- [Important API behavior, config quirk, etc.]

## Accomplished
- ✅ [Completed task 1 — with key implementation details]
- ✅ [Completed task 2 — mention files changed]
- 🔲 [Identified but not yet done — for next session]

## Next Steps
- [What remains to be done — for the next session]

## Relevant Files
- path/to/file.ts — [what it does or what changed]
- path/to/other.go — [role in the architecture]

GUIDELINES:
- Be CONCISE but don't lose important details (file paths, error messages, decisions)
- Focus on WHAT and WHY, not HOW (the code itself is in the repo)
- Include things that would save a future agent time
- The Discoveries section is the most valuable — capture gotchas and non-obvious learnings
- Relevant Files should only include files that were significantly changed or are important for context`),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("Full session summary using the Goal/Instructions/Discoveries/Accomplished/Next Steps/Relevant Files format"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID (default: manual-save-{project})"),
				),
				// project field intentionally omitted — auto-detect only (REQ-308 write-tool contract)
			),
			queuedWriteHandler(writeQueue, handleSessionSummary(s, cfg, activity)),
		)
	}

	// ─── mem_session_start (profile: agent, deferred) ───────────────────
	if shouldRegister("mem_session_start", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_start",
				mcp.WithDescription("Register the start of a new coding session. Call this at the beginning of a session to track activity."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Start Session"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("id",
					mcp.Required(),
					mcp.Description("Unique session identifier"),
				),
				mcp.WithString("directory",
					mcp.Description("Working directory"),
				),
			),
			queuedWriteHandler(writeQueue, handleSessionStart(s, cfg, activity)),
		)
	}

	// ─── mem_session_end (profile: agent, deferred) ─────────────────────
	if shouldRegister("mem_session_end", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_end",
				mcp.WithDescription("Mark a coding session as completed with an optional summary."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("End Session"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("id",
					mcp.Required(),
					mcp.Description("Session identifier to close"),
				),
				mcp.WithString("summary",
					mcp.Description("Summary of what was accomplished"),
				),
			),
			queuedWriteHandler(writeQueue, handleSessionEnd(s, cfg, activity)),
		)
	}

	// ─── mem_capture_passive (profile: agent, deferred) ─────────────────
	if shouldRegister("mem_capture_passive", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_capture_passive",
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Capture Learnings"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Extract and save structured learnings from text output. Use this at the end of a task to capture knowledge automatically.

The tool looks for sections like "## Key Learnings:" or "## Aprendizajes Clave:" and extracts numbered or bulleted items. Each item is saved as a separate observation.

Duplicates are automatically detected and skipped — safe to call multiple times with the same content.`),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("The text output containing a '## Key Learnings:' section with numbered or bulleted items"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID (default: manual-save-{project})"),
				),
				mcp.WithString("source",
					mcp.Description("Source identifier (e.g. 'subagent-stop', 'session-end')"),
				),
			),
			queuedWriteHandler(writeQueue, handleCapturePassive(s, cfg, activity)),
		)
	}

	// ─── mem_merge_projects (profile: admin, deferred) ──────────────────
	if shouldRegister("mem_merge_projects", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_merge_projects",
				mcp.WithDescription("Merge memories from multiple project name variants into one canonical name. Use when you discover project name drift (e.g. 'Engram' and 'engram' should be the same project). DESTRUCTIVE — moves all records from source names to the canonical name."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Merge Projects"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("from",
					mcp.Required(),
					mcp.Description("Comma-separated list of project names to merge FROM (e.g. 'Engram,engram-memory,ENGRAM')"),
				),
				mcp.WithString("to",
					mcp.Required(),
					mcp.Description("The canonical project name to merge INTO (e.g. 'engram')"),
				),
			),
			queuedWriteHandler(writeQueue, handleMergeProjects(s)),
		)
	}

	// ─── mem_current_project (profile: agent) ────────────────────────────
	if shouldRegister("mem_current_project", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_current_project",
				mcp.WithDescription("Detect the current project from the working directory. Returns project name, source (how it was detected), path, and available alternatives. NEVER errors — use this for discovery before writing. Recommended as the first call when starting a new session."),
				mcp.WithTitleAnnotation("Detect Current Project"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			handleCurrentProject(s, cfg),
		)
	}

	// ─── mem_doctor (profile: agent, deferred) ──────────────────────────
	if shouldRegister("mem_doctor", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_doctor",
				mcp.WithDescription("Run read-only operational diagnostics. Returns the same structured envelope as `omnia doctor --json`."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Run Omnia Doctor"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Project to diagnose (omit for auto-detect)")),
				mcp.WithString("check", mcp.Description("Optional diagnostic check code to run")),
			),
			handleDoctor(s, cfg),
		)
	}

	// ─── mem_judge (profile: agent, eager) — REQ-003, Design §6 ─────────
	if shouldRegister("mem_judge", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_judge",
				mcp.WithDescription(`Record a verdict on a pending memory conflict surfaced by mem_save.

WHEN TO CALL: After mem_save returns judgment_required=true, iterate candidates[] and call mem_judge once per entry using that entry's judgment_id.

PARAMS:
  judgment_id (required) — from candidates[].judgment_id in the mem_save response
  relation    (required) — one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict
  reason      (optional) — free-text explanation of the verdict
  evidence    (optional) — JSON or text supporting the verdict
  confidence  (optional) — float 0..1; default 1.0

WHEN TO ASK USER:
  - confidence < 0.7: surface to user before judging
  - relation in {supersedes, conflicts_with} AND type in {architecture, policy, decision}: ask user
  - otherwise: resolve silently

SUCCESS: Returns the updated relation row with judgment_status="judged".
ERROR: Returns IsError=true if judgment_id is unknown or relation verb is invalid. Row is NOT mutated on error.

Re-judging an already-judged ID overwrites the verdict (deliberate revision).`),
				mcp.WithTitleAnnotation("Judge Memory Conflict"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("judgment_id",
					mcp.Required(),
					mcp.Description("The judgment_id from candidates[] in the mem_save response (format: rel-<hex>)"),
				),
				mcp.WithString("relation",
					mcp.Required(),
					mcp.Description("Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict"),
				),
				mcp.WithString("reason",
					mcp.Description("Free-text explanation of the verdict"),
				),
				mcp.WithString("evidence",
					mcp.Description("Supporting evidence (JSON or free text)"),
				),
				mcp.WithNumber("confidence",
					mcp.Description("Confidence score 0.0..1.0 (default: 1.0)"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID for provenance (default: auto)"),
				),
			),
			queuedWriteHandler(writeQueue, handleJudge(s, activity)),
		)
	}

	// ─── mem_compare (profile: agent, eager) — REQ-011, Design §9 ────────
	if shouldRegister("mem_compare", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_compare",
				mcp.WithDescription(`Persist a semantic verdict you have already judged externally (with your LLM) into Omnia.

WHEN TO CALL: After you have evaluated two memories and reached a verdict, call mem_compare to PERSIST that verdict into the relation store. You do the judgment; mem_compare records it.

PARAMS:
  memory_id_a  (required) — integer id of the first observation (from mem_search or mem_get_observation)
  memory_id_b  (required) — integer id of the second observation
  relation     (required) — one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict
  confidence   (required) — float 0..1; your self-reported confidence in the verdict
  reasoning    (required) — explanation of the verdict, max 200 chars
  model        (optional) — your model identifier, stored for provenance (e.g. "claude-haiku-4-5")

BEHAVIOR:
  - Persists the verdict via JudgeBySemantic with system provenance (marked_by_actor="engram").
  - not_conflict: no row is inserted; tool returns success with empty sync_id (the verdict is recorded but not stored — it means "we evaluated these and they do not conflict").
  - Idempotent: calling again for the same pair updates the existing row.
  - Cross-project pairs are rejected.

SUCCESS: Returns { "sync_id": "rel-..." } on persist, { "sync_id": "" } on not_conflict.
ERROR: Returns IsError=true if IDs are unknown, relation is invalid, or cross-project pair.`),
				mcp.WithTitleAnnotation("Compare Memory Pair (Persist Semantic Verdict)"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("memory_id_a",
					mcp.Required(),
					mcp.Description("Integer id of the first observation (from mem_search #id)"),
				),
				mcp.WithNumber("memory_id_b",
					mcp.Required(),
					mcp.Description("Integer id of the second observation (from mem_search #id)"),
				),
				mcp.WithString("relation",
					mcp.Required(),
					mcp.Description("Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict"),
				),
				mcp.WithNumber("confidence",
					mcp.Required(),
					mcp.Description("Confidence score 0.0..1.0"),
				),
				mcp.WithString("reasoning",
					mcp.Required(),
					mcp.Description("Brief explanation of the verdict (max 200 chars)"),
				),
				mcp.WithString("model",
					mcp.Description("Your model identifier for provenance (e.g. \"claude-haiku-4-5\")"),
				),
			),
			handleCompare(s, activity),
		)
	}
}

// ─── Tool Handlers ───────────────────────────────────────────────────────────

// handleCurrentProject implements mem_current_project. It NEVER returns an error
// even on ambiguous cwd — it always returns a success result with whatever
// detection info is available (REQ-313).
func handleCurrentProject(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cwd, _ := os.Getwd()
		res := projectpkg.DetectProjectFull(cwd)
		if processRes, ok := processProjectResult(cfg.DefaultProject); ok {
			res = processRes
		}

		envelope := map[string]any{
			"project":            res.Project,
			"project_source":     res.Source,
			"project_path":       res.Path,
			"cwd":                cwd,
			"available_projects": res.AvailableProjects,
		}
		if res.Warning != "" {
			envelope["warning"] = res.Warning
		}
		if res.Error != nil {
			// REQ-313: not an error response — just surface the info.
			envelope["error_hint"] = res.Error.Error()
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func handleSearch(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.GetArguments()["query"].(string)
		typ, _ := req.GetArguments()["type"].(string)
		projectOverride, _ := req.GetArguments()["project"].(string)
		scope, _ := req.GetArguments()["scope"].(string)
		allProjects := boolArg(req, "all_projects", false)
		limit := intArg(req, "limit", 10)
		explain := boolArg(req, "explain", false)

		// all_projects=true short-circuits project resolution: we search globally
		// regardless of the project override or any auto-detected project. This
		// keeps the cross-project flow independent of cwd-based detection so the
		// agent can recall context from any project without knowing its key.
		var detRes projectpkg.DetectionResult
		var project string
		if allProjects {
			detRes = projectpkg.DetectionResult{Source: projectpkg.SourceAllProjects}
		} else {
			// Resolve project: validate override or auto-detect (REQ-310, REQ-311)
			res, err := resolveReadProjectWithProcessOverride(s, projectOverride, cfg.DefaultProject)
			if err != nil {
				var upe *unknownProjectError
				if errors.As(err, &upe) {
					return errorWithMeta("unknown_project",
						fmt.Sprintf("Project %q not found in store", upe.Name),
						upe.AvailableProjects,
					), nil
				}
				return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
			}
			detRes = res
			project = detRes.Project
			project, _ = store.NormalizeProject(project)
			detRes.Project = project // JR2-1: keep envelope in sync with normalized query project
		}

		// REQ-391: personal scope is cross-project by definition. When scope=personal
		// and no explicit project override was provided, clear the project filter so
		// memories from all projects are visible (not just the cwd-detected one).
		searchProject := project
		if scope == "personal" && strings.TrimSpace(projectOverride) == "" {
			searchProject = ""
		}

		sessionID := defaultSessionID(project)
		activity.RecordToolCall(sessionID)

		// Recall wiring (design D6/D7, PR3): when cfg.Recall is non-nil
		// (recall.enabled=true in config), fuse lexical + semantic hits via
		// recall.Service and hydrate the fused, ranked ID list back into full
		// store.SearchResult records ("rank then hydrate" — recall.Fuse never
		// carries Project/Type/Content). When nil (the default), this branch
		// is skipped entirely: the else branch below is byte-for-byte today's
		// FTS5-only store.Search call, unchanged, so rollback is always safe.
		// relevance carries each result's un-normalized relevance signal,
		// keyed by Observation.ID, for the memory-recall-ranking pass below
		// (RankResults) — RRF fusion Score for the hybrid recall path
		// (higher is already more relevant), or negated FTS5 bm25 Rank for
		// the FTS5-only path (bm25's rank is a small negative "cost," so
		// negating it makes a better match a larger positive relevance,
		// matching the fused path's "higher is better" convention). Built
		// alongside results below so both branches populate it consistently.
		relevance := make(map[int64]float64)

		var results []store.SearchResult
		if cfg.Recall != nil {
			// RecallFetchLimit over-fetches candidates (beyond the caller's
			// real limit) so HydrateFusedResults's project/scope/type
			// re-check below has enough headroom to still fill up to limit
			// valid results after filtering out any cross-project semantic
			// noise (bugfix: see RecallScopeFilter's doc in recall_adapter.go).
			fused, ferr := cfg.Recall.Search(ctx, query, recall.LexicalSearchOptions{
				Type:    typ,
				Project: searchProject,
				Scope:   scope,
				Limit:   RecallFetchLimit(limit),
			})
			if ferr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Search error: %s. Try simpler keywords.", ferr)), nil
			}
			for _, fr := range fused {
				relevance[fr.ID] = fr.Score
			}
			results = HydrateFusedResults(s, fused, limit, RecallScopeFilter{
				Type:    typ,
				Project: searchProject,
				Scope:   scope,
			})
		} else {
			r, serr := s.Search(query, store.SearchOptions{
				Type:    typ,
				Project: searchProject,
				Scope:   scope,
				Limit:   limit,
			})
			if serr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Search error: %s. Try simpler keywords.", serr)), nil
			}
			results = r
			for _, rr := range r {
				if rr.Rank == exactSentinelRank {
					continue
				}
				relevance[rr.ID] = -rr.Rank
			}
		}

		if len(results) == 0 {
			// JW4: use respondWithProject even for empty results.
			return respondWithProject(detRes, fmt.Sprintf("No memories found for: %q", query), nil), nil
		}

		// memory-recall-ranking (design D6 wiring boundary): re-sorts by
		// recency x importance x relevance when cfg.RecallRanking.Enabled.
		// A pure no-op when it's not (the default), so this call never
		// changes handleSearch's response when ranking is unconfigured
		// (Requirement: Backward-Compatible Default Behavior).
		now := time.Now()
		results = RankResults(results, relevance, cfg.RecallRanking, now)

		// Batch-load relations for all results (REQ-002). Avoids N+1.
		syncIDs := make([]string, 0, len(results))
		for _, r := range results {
			if r.SyncID != "" {
				syncIDs = append(syncIDs, r.SyncID)
			}
		}
		relationsMap := map[string]store.ObservationRelations{}
		if len(syncIDs) > 0 {
			if rm, relErr := s.GetRelationsForObservations(syncIDs); relErr == nil {
				relationsMap = rm
			}
			// Errors from relation loading are swallowed — search must not fail.
		}

		// memory-structural-forgetting (design D6 wiring boundary, PR2
		// Requirement 6): downranks memories carrying at least one stale code
		// anchor and attaches an anchor_receipt line. Gated behind
		// cfg.StructuralForgetting.Enabled (default false) so the ENTIRE
		// block — including the anchor batch-load itself — is skipped when
		// the flag is off, keeping handleSearch's response byte-for-byte
		// identical to before this slice existed (Regression: memory with no
		// anchor unaffected, extended to "flag off" too).
		anchorsByObs := map[string][]store.MemoryAnchor{}
		if cfg.StructuralForgetting.Enabled && len(syncIDs) > 0 {
			if am, aerr := s.GetAnchorsForObservations(syncIDs); aerr == nil {
				anchorsByObs = am
			}
			// Errors from anchor loading are swallowed — search must not fail.
			results = ApplyStalenessDownrank(results, anchorsByObs)
		}

		// explain (Requirement: Per-Hit Score Breakdown) needs the SAME
		// batch-normalized relevance RankResults scores against, so a
		// receipt's "final" is honest about what RankResults actually
		// computed (or would compute, if ranking is currently disabled) for
		// that row — not a value normalized differently. Sentinel AND
		// signature-match rows are excluded from normalization here too,
		// mirroring RankResults' own pre-emption of both, and treated as
		// maximally relevant (1.0) below (BuildResultReceipt's preempted
		// check) — otherwise a signature row's outlier relevance (FTS5-only
		// path's relevance[id] = -rank, ~500 for signatureMatchRank) would
		// pin the batch's max and crush every other row's normalized
		// relevance toward 0.
		var normalizedRelevance map[int64]float64
		if explain {
			nonSentinel := make([]store.SearchResult, 0, len(results))
			for _, r := range results {
				if r.Rank != exactSentinelRank && !r.SignatureMatch {
					nonSentinel = append(nonSentinel, r)
				}
			}
			normalizedRelevance = MinMaxNormalizeRelevance(nonSentinel, relevance)
		}

		// Omnia v0.3 Context Economy — MMR diversity (design obs #1643
		// section 3.3, spec obs #1642 injection-diversity domain, PR4): a
		// read-time token-set-Jaccard MMR re-rank that demotes/hard-drops
		// rows near-duplicating an already-selected higher-ranked row. Runs
		// AFTER RankResults/ApplyStalenessDownrank (priority is already
		// decided) and AFTER explain's normalizedRelevance above (same
		// "score_breakdown reflects the full pre-trim batch" rationale
		// ApplyTokenBudget's own comment below documents), but BEFORE
		// ApplyTokenBudget (dedup before trim — MMR removes redundancy so
		// the budget is never spent on near-duplicates).
		//
		// PIPELINE-POSITION NOTE (task 4.9): design section 2's final order
		// is RankResults -> ApplyStalenessDownrank -> ApplyTypeLens ->
		// ApplyMMR -> ApplyTokenBudget. ApplyTypeLens does not exist yet
		// (PR5, not merged) — ApplyMMR is wired directly after
		// ApplyStalenessDownrank/explain for now. When PR5 lands, insert its
		// ApplyTypeLens call BETWEEN this comment and the ApplyMMR call
		// below (never after it) to reconcile the final order — a one-line
		// insertion, not a reshuffle, since every pass here is an
		// independent gated no-op over the same []store.SearchResult shape.
		//
		// A pure no-op when cfg.Injection.Diversity.Enabled is false (the
		// default) or fewer than 2 results, keeping handleSearch's response
		// byte-for-byte identical to pre-v0.3 output (spec: Disabled by
		// Default, No-Op When Off).
		results = ApplyMMR(results, relevance, cfg.Injection.Diversity)

		// Omnia v0.3 Context Economy (design obs #1643, spec obs #1642
		// injection-budget domain): ApplyTokenBudget is the LAST pass in the
		// pipeline, after RankResults/ApplyStalenessDownrank/ApplyMMR above
		// and before the display/preview loop below, so trimming reflects
		// the FINAL ranked/downranked/diversified/(PR5: lens-boosted) order.
		// A pure no-op when cfg.Injection.Budget.Enabled is false (the
		// default), keeping handleSearch's response byte-for-byte identical
		// to pre-v0.3 output (Requirement: Disabled by Default, No-Op When
		// Off). Runs AFTER explain's normalizedRelevance above so a
		// score_breakdown reflects what RankResults computed over the full
		// (pre-trim) batch, not a batch already narrowed by the budget or
		// MMR.
		//
		// PR2 review fix (WARNING): capture the pre-trim count so a genuine
		// budget-driven cut can be surfaced below — "Found 0 memories" after
		// ApplyTokenBudget silently drops every real hit is otherwise
		// indistinguishable from a true no-match response.
		preTrimCount := len(results)
		results = ApplyTokenBudget(results, cfg.Injection.Budget)
		budgetTrimmed := 0
		if cfg.Injection.Budget.Enabled {
			budgetTrimmed = preTrimCount - len(results)
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Found %d memories:\n\n", len(results))
		anyTruncated := false
		structuredResults := make([]map[string]any, 0, len(results))
		for i, r := range results {
			projectDisplay := ""
			if r.Project != nil {
				projectDisplay = fmt.Sprintf(" | project: %s", *r.Project)
			}
			preview := truncate(r.Content, 300)
			if len(r.Content) > 300 {
				anyTruncated = true
				preview += " [preview]"
			}
			stateDisplay := ""
			if r.State() == store.ObservationStateNeedsReview {
				stateDisplay = " | state: needs_review"
			}
			fmt.Fprintf(&b, "[%d] #%d (%s) — %s\n    %s\n    %s%s | scope: %s%s\n",
				i+1, r.ID, r.Type, r.Title,
				preview,
				timeutil.FormatLocal(r.CreatedAt), projectDisplay, r.Scope, stateDisplay)
			entry := map[string]any{
				"id":      r.ID,
				"sync_id": r.SyncID,
				"title":   r.Title,
				"type":    r.Type,
				"state":   r.State(),
				"scope":   r.Scope,
				"pinned":  r.Pinned,
			}
			if r.Project != nil {
				entry["project"] = *r.Project
			}
			if r.ReviewAfter != nil {
				entry["review_after"] = *r.ReviewAfter
			}
			// Recall reliability #1399, slice 1: surface outcome feedback and
			// whether this hit came from the error-signature lane (a proven
			// prior fix) rather than a loose lexical/BM25 match.
			if r.Outcome != nil {
				entry["outcome"] = *r.Outcome
			}
			if r.SignatureMatch {
				entry["signature_match"] = true
			}
			// memory-structural-forgetting (Requirement 6): attach the
			// "anchor <file>:<lines> changed <old->new sha>" receipt line
			// (both to the structured entry and, below, the human-readable
			// text block) when this result carries a stale anchor, and
			// compute the staleness_penalty that feeds into score_breakdown
			// below. stalenessPenalty stays 0 (its pre-PR2 reserved default)
			// when the flag is off or this result has no anchors at all.
			var stalenessPenalty float64
			var anchorReceiptLine string
			if cfg.StructuralForgetting.Enabled {
				if anchors, ok := anchorsByObs[r.SyncID]; ok {
					stalenessPenalty = StalenessPenaltyFor(anchors)
					if stale, found := firstStaleAnchor(anchors); found {
						if receipt := BuildStaleReceipt(stale); receipt != "" {
							anchorReceiptLine = receipt
							entry["anchor_receipt"] = receipt
						}
					}
				}
			}
			if explain {
				// cfg.Recall != nil is always an accurate "did fusion run"
				// signal at this call site: unlike cmd/omnia's cmdSearch,
				// handleSearch never silently falls back to the FTS5-only
				// path on a fusion error above — it returns an error result
				// instead (see the cfg.Recall != nil branch), so there is no
				// mid-query fallback case to distinguish here.
				entry["score_breakdown"] = BuildResultReceipt(r, cfg.Recall != nil, cfg.RecallRanking, relevance, normalizedRelevance, now, stalenessPenalty)
			}
			structuredResults = append(structuredResults, entry)

			// Append relation annotations. Skip orphaned (filtered by store).
			//
			// Annotation format contract (REQ-012, Design §7):
			//   supersedes: #<id> (<title>)            judged supersedes
			//   superseded_by: #<id> (<title>)         judged superseded_by
			//   conflicts: #<id> (<title>)             judged conflicts_with
			//   conflict: contested by #<id> (pending) pending (UNCHANGED from Phase 1)
			//
			// <id> is the observation's integer primary key. <title> is the related
			// observation's title; "(deleted)" when the observation is missing or soft-deleted.
			// Prefixes (supersedes:, superseded_by:, conflicts:) are stable across Phase 3.
			if rels, ok := relationsMap[r.SyncID]; ok {
				for _, rel := range rels.AsSource {
					switch {
					case rel.Relation == store.RelationSupersedes && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.TargetTitle
						if rel.TargetMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    supersedes: #%d (%s)\n", rel.TargetIntID, title)
					case rel.Relation == store.RelationConflictsWith && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.TargetTitle
						if rel.TargetMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    conflicts: #%d (%s)\n", rel.TargetIntID, title)
					case rel.JudgmentStatus == store.JudgmentStatusPending:
						// UNCHANGED from Phase 1 — byte-for-byte preserved.
						fmt.Fprintf(&b, "    conflict: contested by #%s (pending)\n", rel.TargetID)
					}
				}
				for _, rel := range rels.AsTarget {
					switch {
					case rel.Relation == store.RelationSupersedes && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.SourceTitle
						if rel.SourceMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    superseded_by: #%d (%s)\n", rel.SourceIntID, title)
					case rel.JudgmentStatus == store.JudgmentStatusPending:
						// UNCHANGED from Phase 1 — byte-for-byte preserved.
						fmt.Fprintf(&b, "    conflict: contested by #%s (pending)\n", rel.SourceID)
					}
				}
			}
			if anchorReceiptLine != "" {
				fmt.Fprintf(&b, "    %s\n", anchorReceiptLine)
			}
			b.WriteString("\n")
		}
		if anyTruncated {
			fmt.Fprintf(&b, "---\nResults above are previews (300 chars). To read the full content of a specific memory, call mem_get_observation(id: <ID>).\n")
		}

		// PR2 review fix (WARNING): trim-transparency footer + envelope
		// field, ONLY when the budget pass is enabled AND actually trimmed
		// something — flag-off and enabled-but-nothing-trimmed stay
		// byte-for-byte identical to pre-v0.3 output.
		if budgetTrimmed > 0 {
			fmt.Fprintf(&b, "---\n%d more result(s) matched but were trimmed by the injection token budget (injection.budget.max_tokens). Raise the budget or refine the query.\n", budgetTrimmed)
		}

		if nudge := activity.NudgeIfNeeded(sessionID); nudge != "" {
			b.WriteString(nudge)
		}

		envelope := map[string]any{"results": structuredResults}
		if budgetTrimmed > 0 {
			envelope["budget_trimmed"] = budgetTrimmed
		}

		// omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2
		// Phase 7: attach a contrastive procedure card (top trusted playbook
		// + top trusted anti_playbook matching query) when procedural memory
		// is enabled. Gated by cfg.Procedural != nil, mirroring
		// cfg.StructuralForgetting.Enabled's own "byte-identical when off"
		// contract — internal/recall stays untouched/pure either way.
		if cfg.Procedural != nil {
			if card := BuildProcedureCard(s, query); card != nil {
				envelope["procedure_card"] = card
			}
		}

		// JW4: use respondWithProject for the success path (REQ-314).
		return respondWithProject(detRes, b.String(), envelope), nil
	}
}

func handlePin(s *store.Store, pinned bool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		var err error
		if pinned {
			err = s.PinObservation(id)
		} else {
			err = s.UnpinObservation(id)
		}
		if err != nil {
			return mcp.NewToolResultError("Failed to update pin state: " + err.Error()), nil
		}

		obs, err := s.GetObservation(id)
		if err != nil {
			return mcp.NewToolResultError("Updated pin state but failed to reload observation: " + err.Error()), nil
		}
		state := "unpinned"
		if pinned {
			state = "pinned"
		}
		out, _ := jsonMarshal(map[string]any{
			"result":  fmt.Sprintf("Memory #%d %s", id, state),
			"id":      obs.ID,
			"sync_id": obs.SyncID,
			"pinned":  obs.Pinned,
		})
		return mcp.NewToolResultText(string(out)), nil
	}
}

// searchabilityHint runs conservative, non-blocking lint checks over a
// mem_save request so agents get nudged toward findable memories. It NEVER
// fails or alters the save — callers only attach the result to the response
// envelope as an informational field. Returns "" when nothing fires.
func searchabilityHint(title, content string) string {
	var hints []string
	if !contentHasKeywordsLine(content) {
		hints = append(hints, "hint: add a 'Keywords:' line (exact error strings, file paths, flags, EN+ES synonyms) so this memory can be found later")
	}
	if titleIsVague(title) {
		hints = append(hints, "hint: vague title — name the component and the symptom/action")
	}
	return strings.Join(hints, " | ")
}

// keywordsOrderedListPrefixRe strips a leading ordered-list marker ("1.",
// "2)") so "1. Keywords: ..." is still recognized.
var keywordsOrderedListPrefixRe = regexp.MustCompile(`^\d+[.)]\s*`)

// contentHasKeywordsLine reports whether content contains a line that
// declares "Keywords:", allowing common markdown decoration around it
// (bold/italic markers, bullet points, headings, ordered-list numbering).
func contentHasKeywordsLine(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if lineDeclaresKeywords(line) {
			return true
		}
	}
	return false
}

func lineDeclaresKeywords(line string) bool {
	s := strings.NewReplacer("*", "", "_", "").Replace(line)
	s = strings.TrimSpace(s)
	s = keywordsOrderedListPrefixRe.ReplaceAllString(s, "")
	s = strings.TrimLeft(s, "-+# \t")
	s = strings.TrimSpace(s)
	return strings.HasPrefix(strings.ToLower(s), "keywords:")
}

// concreteIdentifierRe matches an embedded path/flag/identifier separator
// (word char, then . / or -, then word char) so titles naming a real
// component (e.g. "internal/mcp handleSave", "mem_save.go", "--daemon-url")
// are never flagged as vague, regardless of word count.
var concreteIdentifierRe = regexp.MustCompile(`\w[./-]\w`)

// genericOnlyTitles are titles that name no concrete component regardless of
// how they're capitalized — kept short and conservative to avoid false
// positives on legitimate short-but-specific titles.
var genericOnlyTitles = map[string]bool{
	"fix":       true,
	"fixes":     true,
	"fix bug":   true,
	"fixed bug": true,
	"bug fix":   true,
	"bugfix":    true,
	"update":    true,
	"updates":   true,
	"change":    true,
	"changes":   true,
	"mejora":    true,
	"mejoras":   true,
	"arreglo":   true,
	"arreglos":  true,
}

func isGenericOnlyTitle(title string) bool {
	normalized := strings.ToLower(strings.Trim(title, ".!¡¿? "))
	return genericOnlyTitles[normalized]
}

// titleIsVague flags titles that are too short or generic to be findable
// later. A title escapes the check if it names a concrete component (a
// path, or an identifier containing a dot/slash/dash) or has 4+ words.
func titleIsVague(title string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return false // absence of a title is a separate concern from vagueness
	}
	if isGenericOnlyTitle(trimmed) {
		return true
	}
	if concreteIdentifierRe.MatchString(trimmed) {
		return false
	}
	return len(strings.Fields(trimmed)) < 4
}

func handleSave(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title, _ := req.GetArguments()["title"].(string)
		content, _ := req.GetArguments()["content"].(string)
		if strings.TrimSpace(content) == "" {
			if observation, _ := req.GetArguments()["observation"].(string); strings.TrimSpace(observation) != "" {
				content = observation
			}
		}
		if strings.TrimSpace(content) == "" {
			return mcp.NewToolResultError("content is required for mem_save (use content, or observation for backward-compatible clients)"), nil
		}
		typ, _ := req.GetArguments()["type"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		scope, _ := req.GetArguments()["scope"].(string)
		topicKey, _ := req.GetArguments()["topic_key"].(string)
		errorSignature, _ := req.GetArguments()["error_signature"].(string)
		outcome, _ := req.GetArguments()["outcome"].(string)
		source, _ := req.GetArguments()["source"].(string)
		appliedProcedure, _ := req.GetArguments()["applied_procedure"].(string)
		projectChoice, _ := req.GetArguments()["project"].(string)
		_, explicitProjectProvided := req.GetArguments()["project"]
		projectChoiceReason, _ := req.GetArguments()["project_choice_reason"].(string)
		recoveryToken, _ := req.GetArguments()["recovery_token"].(string)
		capturePrompt := boolArg(req, "capture_prompt", true)
		recoverySessionID := sessionID
		if strings.TrimSpace(recoverySessionID) == "" {
			recoverySessionID = defaultSessionID("")
		}
		validateRecoveryToken := func(res projectpkg.DetectionResult, choice string) (bool, bool) {
			if strings.TrimSpace(recoveryToken) == "" {
				return false, false
			}
			return true, activity.ValidateAmbiguousProjectRecoveryToken(recoverySessionID, recoveryToken, strings.TrimSpace(choice), res.AvailableProjects, res.Path)
		}

		// Resolve write project using the full MCP precedence: explicit request,
		// existing session association, process override, repo config/directory detection, then cwd fallback.
		detRes, err := resolveSaveWriteProjectWithProcessOverride(s, projectChoice, explicitProjectProvided, projectChoiceReason, sessionID, validateRecoveryToken, cfg.DefaultProject)
		if err != nil {
			return writeProjectErrorResult(activity, recoverySessionID, detRes, err), nil
		}
		project := detRes.Project

		// Normalize project name and capture warning
		normalized, normWarning := store.NormalizeProject(project)
		project = normalized

		if typ == "" {
			typ = "manual"
		}
		if sessionID == "" {
			sessionID = resolveFallbackSessionID(s, project)
		}
		suggestedTopicKey := suggestTopicKey(typ, title, content)

		// Check for similar existing projects (only when this project has no existing observations)
		var similarWarning string
		if project != "" {
			existingNames, _ := s.ListProjectNames()
			isNew := true
			for _, e := range existingNames {
				if e == project {
					isNew = false
					break
				}
			}
			if isNew && len(existingNames) > 0 {
				matches := projectpkg.FindSimilar(project, existingNames, 3)
				if len(matches) > 0 {
					bestMatch := matches[0].Name
					obsCount, _ := s.CountObservationsForProject(bestMatch)
					similarWarning = fmt.Sprintf("⚠️ Project %q has no memories. Similar project found: %q (%d memories). Consider using that name instead.", project, bestMatch, obsCount)
				}
			}
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		truncated := len(content) > s.MaxObservationLength()

		savedID, err := s.AddObservation(store.AddObservationParams{
			SessionID:      sessionID,
			Type:           typ,
			Title:          title,
			Content:        content,
			Project:        project,
			Scope:          scope,
			TopicKey:       topicKey,
			ErrorSignature: errorSignature,
			Outcome:        outcome,
			Source:         source,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save: " + err.Error()), nil
		}

		// Auto-embed the just-saved memory out-of-band (non-blocking): it
		// becomes semantically searchable within seconds without slowing the
		// save. No-op when embeddings are disabled.
		enqueueAutoEmbed(cfg, s, savedID)

		// omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2
		// Phases 4-5: online candidate induction + SSGM applied_procedure
		// reuse feedback. No-op when procedural.enabled is false (the
		// default) — see runProceduralHooksForObservation's own doc.
		runProceduralHooksForObservation(cfg, s, savedID, appliedProcedure)

		// Write-time provenance audit (memory-provenance foundation,
		// omnia-provenance-foundation): source was already classified into
		// trust_tag inside AddObservation (store.classifyTrust); read the
		// persisted values back here so the audit entry always reflects what
		// was actually stored, not what the caller asked for. Reuses
		// internal/audit — no parallel log. auditAppend/audit.Append never
		// returns an error and swallows failures internally (logs to
		// stderr), so a failing audit write can never block or roll back
		// this save.
		if savedObs, obsErr := s.GetObservation(savedID); obsErr == nil {
			auditAppend(audit.Entry{
				Ts:            audit.Now(),
				Actor:         "mcp",
				Action:        audit.ActionWrite,
				ObservationID: int(savedID),
				Project:       project,
				Summary:       title,
				Result:        "ok",
				Source:        strFromPtr(savedObs.Source),
				TrustTag:      strFromPtr(savedObs.TrustTag),
				SyncID:        savedObs.SyncID,
				SessionID:     sessionID,
			})
		}

		if capturePrompt && activity != nil {
			if prompt, ok := activity.CurrentPrompt(sessionID, project); ok {
				if _, _, promptErr := addPromptIfMissing(s, store.AddPromptParams{
					SessionID: sessionID,
					Content:   prompt,
					Project:   project,
				}); promptErr != nil {
					fmt.Fprintf(os.Stderr, "engram: auto prompt capture error (non-fatal): %v\n", promptErr)
				}
			}
		}

		if activity != nil {
			activity.RecordSave(sessionID)
		}

		msg := fmt.Sprintf("Memory saved: %q (%s)", title, typ)
		if topicKey == "" && suggestedTopicKey != "" {
			msg += fmt.Sprintf("\nSuggested topic_key: %s", suggestedTopicKey)
		}
		if truncated {
			msg += fmt.Sprintf("\n⚠ WARNING: Content was truncated from %d to %d chars. Consider splitting into smaller observations.", len(content), s.MaxObservationLength())
		}
		if normWarning != "" {
			msg += "\n" + normWarning
		}
		if similarWarning != "" {
			msg += "\n" + similarWarning
		}

		// Post-transaction conflict candidate detection (REQ-001).
		// Errors are logged and swallowed — detection failure never fails the save.
		extra := map[string]any{}

		// Soft searchability lint (non-blocking): nudge toward findable memories.
		// The save has already succeeded above — this only annotates the envelope.
		if hint := searchabilityHint(title, content); hint != "" {
			extra["searchability_hint"] = hint
		}

		// Build CandidateOptions, forwarding any MCPConfig overrides.
		// nil fields mean "use store defaults"; explicit pointer values override.
		candOpts := store.CandidateOptions{
			Project:   project,
			Scope:     scope,
			BM25Floor: cfg.BM25Floor, // nil → store default (-2.0); explicit value overrides
		}
		if cfg.Limit != nil {
			candOpts.Limit = *cfg.Limit
		}
		candidates, candErr := s.FindCandidates(savedID, candOpts)
		if candErr != nil {
			// Log only — do not fail the save.
			fmt.Fprintf(os.Stderr, "engram: FindCandidates error (non-fatal): %v\n", candErr)
		}

		// Fetch the saved observation's sync_id for the envelope (REQ-001).
		var savedSyncID string
		if obs, obsErr := s.GetObservation(savedID); obsErr == nil {
			savedSyncID = obs.SyncID
			extra["id"] = savedID
			extra["sync_id"] = savedSyncID
			extra["state"] = obs.State()
			if obs.ReviewAfter != nil {
				extra["review_after"] = *obs.ReviewAfter
			}
			if obs.ErrorSignature != nil {
				extra["error_signature"] = *obs.ErrorSignature
			}
			if obs.Outcome != nil {
				extra["outcome"] = *obs.Outcome
			}
		}

		// Optional code anchor capture (omnia-structural-forgetting, PR1 —
		// write side only). Unconditional whenever code_anchors is supplied;
		// omitting it leaves the envelope byte-identical to today. Capture
		// failures (no git, not a repo, bad range, symbol not found, etc.)
		// are logged and swallowed — mem_save MUST NEVER fail because of
		// anchoring (REQ-002 graceful degradation).
		if anchorInputs := parseCodeAnchorsArg(req); len(anchorInputs) > 0 && savedSyncID != "" {
			probe := cfg.AnchorProbe
			if probe == nil {
				probe = anchor.NewProbe()
			}
			adapter := &AnchorCaptureAdapter{Probe: probe, Store: s}
			captured, capErrs := adapter.Capture(ctx, currentWorkingDirectory(), savedSyncID, anchorInputs)
			for _, capErr := range capErrs {
				fmt.Fprintf(os.Stderr, "engram: code_anchors capture error (non-fatal): %v\n", capErr)
			}
			extra["anchors_captured"] = captured
		}

		if len(candidates) > 0 {
			extra["judgment_required"] = true
			extra["judgment_status"] = "pending"
			extra["judgment_id"] = candidates[0].JudgmentID // first candidate's rel sync_id (design convenience)

			candList := make([]map[string]any, 0, len(candidates))
			for _, c := range candidates {
				entry := map[string]any{
					"id":          c.ID,
					"sync_id":     c.SyncID,
					"title":       c.Title,
					"type":        c.Type,
					"score":       c.Score,
					"judgment_id": c.JudgmentID,
				}
				if c.TopicKey != nil {
					entry["topic_key"] = *c.TopicKey
				}
				candList = append(candList, entry)
			}
			extra["candidates"] = candList

			msg += fmt.Sprintf("\nCONFLICT REVIEW PENDING — %d candidate(s); use mem_judge to record verdicts.", len(candidates))
		} else {
			extra["judgment_required"] = false
		}

		// Update detRes to reflect normalized project for envelope accuracy
		detRes.Project = project
		return respondWithProject(detRes, msg, extra), nil
	}
}

func handleSuggestTopicKey() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		typ, _ := req.GetArguments()["type"].(string)
		title, _ := req.GetArguments()["title"].(string)
		content, _ := req.GetArguments()["content"].(string)

		if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" {
			return mcp.NewToolResultError("provide title or content to suggest a topic_key"), nil
		}

		topicKey := suggestTopicKey(typ, title, content)
		if topicKey == "" {
			return mcp.NewToolResultError("could not suggest topic_key from input"), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Suggested topic_key: %s", topicKey)), nil
	}
}

func handleUpdate(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		appliedProcedure, _ := req.GetArguments()["applied_procedure"].(string)

		update := store.UpdateObservationParams{}
		if v, ok := req.GetArguments()["title"].(string); ok {
			update.Title = &v
		}
		if v, ok := req.GetArguments()["content"].(string); ok {
			update.Content = &v
		}
		if v, ok := req.GetArguments()["type"].(string); ok {
			update.Type = &v
		}
		if v, ok := req.GetArguments()["scope"].(string); ok {
			update.Scope = &v
		}
		if v, ok := req.GetArguments()["topic_key"].(string); ok {
			update.TopicKey = &v
		}
		if v, ok := req.GetArguments()["outcome"].(string); ok {
			update.Outcome = &v
		}

		if update.Title == nil && update.Content == nil && update.Type == nil && update.Project == nil && update.Scope == nil && update.TopicKey == nil && update.Outcome == nil {
			return mcp.NewToolResultError("provide at least one field to update"), nil
		}

		var contentLen int
		if update.Content != nil {
			contentLen = len(*update.Content)
		}

		obs, err := s.UpdateObservation(id, update)
		if err != nil {
			return mcp.NewToolResultError("Failed to update memory: " + err.Error()), nil
		}

		// omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2
		// Phases 4-5: online candidate induction + SSGM applied_procedure
		// reuse feedback — mem_update can record/confirm an outcome after
		// the fact, exactly like mem_save. No-op when procedural.enabled is
		// false (the default).
		runProceduralHooksForObservation(cfg, s, obs.ID, appliedProcedure)

		msg := fmt.Sprintf("Memory updated: #%d %q (%s, scope=%s)", obs.ID, obs.Title, obs.Type, obs.Scope)
		if contentLen > s.MaxObservationLength() {
			msg += fmt.Sprintf("\n⚠ WARNING: Content was truncated from %d to %d chars. Consider splitting into smaller observations.", contentLen, s.MaxObservationLength())
		}

		// Auto-detect for envelope; tolerant — don't fail update on resolution error
		detRes, detErr := resolveWriteProject()
		if detErr != nil {
			// Still return success for the update itself.
			return mcp.NewToolResultText(msg), nil
		}
		return respondWithProject(detRes, msg, nil), nil
	}
}

func handleReview(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action, _ := req.GetArguments()["action"].(string)
		switch strings.TrimSpace(action) {
		case "list":
			projectFilter, _ := req.GetArguments()["project"].(string)
			limit := intArg(req, "limit", 10)
			detRes := projectpkg.DetectionResult{Project: projectFilter, Source: projectpkg.SourceAllProjects}
			if strings.TrimSpace(projectFilter) != "" {
				var err error
				detRes, err = resolveReadProject(s, projectFilter)
				if err != nil {
					var upe *unknownProjectError
					if errors.As(err, &upe) {
						return errorWithMeta("unknown_project",
							fmt.Sprintf("Project %q not found in store", upe.Name),
							upe.AvailableProjects,
						), nil
					}
					return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
				}
				projectFilter = detRes.Project
			} else if res, err := resolveReadProjectWithProcessOverride(s, "", cfg.DefaultProject); err == nil {
				detRes = res
				detRes.Source = projectpkg.SourceAllProjects
			}

			observations, err := s.ObservationsNeedingReview(projectFilter, limit)
			if err != nil {
				return mcp.NewToolResultError("Review list error: " + err.Error()), nil
			}

			structured := make([]map[string]any, 0, len(observations))
			if len(observations) == 0 {
				return respondWithProject(detRes, "No memories need review.", map[string]any{"observations": structured}), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Found %d memories needing review:\n\n", len(observations))
			for i, obs := range observations {
				projectDisplay := ""
				if obs.Project != nil {
					projectDisplay = fmt.Sprintf(" | project: %s", *obs.Project)
				}
				reviewDisplay := ""
				if obs.ReviewAfter != nil {
					reviewDisplay = fmt.Sprintf(" | review_after: %s", *obs.ReviewAfter)
				}
				fmt.Fprintf(&b, "[%d] #%d (%s) — %s\n    state: %s%s%s\n",
					i+1, obs.ID, obs.Type, obs.Title, obs.State(), projectDisplay, reviewDisplay)

				entry := map[string]any{
					"id":      obs.ID,
					"sync_id": obs.SyncID,
					"title":   obs.Title,
					"type":    obs.Type,
					"state":   obs.State(),
				}
				if obs.Project != nil {
					entry["project"] = *obs.Project
				}
				if obs.ReviewAfter != nil {
					entry["review_after"] = *obs.ReviewAfter
				}
				structured = append(structured, entry)
			}
			return respondWithProject(detRes, b.String(), map[string]any{"observations": structured}), nil

		case "mark_reviewed":
			id := int64(intArg(req, "observation_id", 0))
			if id == 0 {
				id = int64(intArg(req, "id", 0))
			}
			if id == 0 {
				return mcp.NewToolResultError("observation_id is required for mark_reviewed"), nil
			}
			if err := s.MarkReviewed(id); err != nil {
				return mcp.NewToolResultError("Failed to mark reviewed: " + err.Error()), nil
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				return mcp.NewToolResultError("Marked reviewed but failed to reload observation: " + err.Error()), nil
			}
			extra := map[string]any{"id": obs.ID, "sync_id": obs.SyncID, "state": obs.State()}
			if obs.ReviewAfter != nil {
				extra["review_after"] = *obs.ReviewAfter
			}
			detRes, detErr := resolveReadProjectWithProcessOverride(s, "", cfg.DefaultProject)
			msg := fmt.Sprintf("Memory marked reviewed: #%d %q (%s)", obs.ID, obs.Title, obs.Type)
			if detErr != nil {
				out, _ := jsonMarshal(map[string]any{"result": msg, "id": obs.ID, "sync_id": obs.SyncID, "state": obs.State()})
				return mcp.NewToolResultText(string(out)), nil
			}
			return respondWithProject(detRes, msg, extra), nil

		default:
			return mcp.NewToolResultError("action must be one of: list, mark_reviewed"), nil
		}
	}
}

func handleDelete(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		hardDelete := boolArg(req, "hard_delete", false)

		if hardDelete {
			// Hard delete ONLY: store purge + tombstone, embed vector purge
			// fan-out, and the audit entry are all owned by the shared
			// internal/purge orchestration helper so this path can never
			// silently regress on any of the three (omnia-provenance-foundation
			// review fix, Blocking 1) — the exact same helper HTTP
			// DELETE /observations/{id}?hard=true and `omnia delete --hard`
			// go through. A disabled/absent embeddings worker makes the
			// vector purge a no-op, mirroring enqueueAutoEmbed's
			// cfg.AutoEmbed nil-guard: there is nothing to purge if nothing
			// was ever embedded.
			var embedStore purge.EmbedPurger
			if cfg.AutoEmbed != nil {
				embedStore = cfg.AutoEmbed.Store()
			}
			if err := purge.HardDeleteWithPurge(ctx, s, embedStore, auditAppend, "mcp", id); err != nil {
				if errors.Is(err, purge.ErrEmbedPurgeFailed) {
					// The row + tombstone already committed — only the
					// vector purge failed (Blocking 2: report honestly
					// instead of swallowing it as an unconditional "ok").
					return mcp.NewToolResultText(fmt.Sprintf("Memory #%d permanently deleted (warning: %v)", id, err)), nil
				}
				return mcp.NewToolResultError("Failed to delete memory: " + err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Memory #%d permanently deleted", id)), nil
		}

		// Soft delete stays out of the purge helper entirely — it is out of
		// physical-purge scope per spec (no tombstone, no vector purge) and
		// only needs its own audit entry.
		//
		// Fetch the observation BEFORE deleting it so we still have its
		// sync_id/project/title for the audit entry below — after
		// DeleteObservation the row's deleted_at is set (still readable, but
		// no need to re-fetch).
		preDelete, preErr := s.GetObservation(id)

		if err := s.DeleteObservation(id, false); err != nil {
			return mcp.NewToolResultError("Failed to delete memory: " + err.Error()), nil
		}

		// Audit soft delete via the existing audit log (no parallel log) —
		// auditAppend/audit.Append never returns an error and swallows
		// failures internally, so this can never block the delete that
		// already succeeded above.
		if preErr == nil {
			auditAppend(audit.Entry{
				Ts:            audit.Now(),
				Actor:         "mcp",
				Action:        audit.ActionSoftDelete,
				ObservationID: int(id),
				Project:       strFromPtr(preDelete.Project),
				Summary:       preDelete.Title,
				Result:        "ok",
				Source:        strFromPtr(preDelete.Source),
				TrustTag:      strFromPtr(preDelete.TrustTag),
				SyncID:        preDelete.SyncID,
			})
		}

		return mcp.NewToolResultText(fmt.Sprintf("Memory #%d soft-deleted", id)), nil
	}
}

func handleSavePrompt(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		projectChoice, _ := req.GetArguments()["project"].(string)
		projectChoiceReason, _ := req.GetArguments()["project_choice_reason"].(string)
		recoveryToken, _ := req.GetArguments()["recovery_token"].(string)
		recoverySessionID := sessionID
		if strings.TrimSpace(recoverySessionID) == "" {
			recoverySessionID = defaultSessionID("")
		}
		validateRecoveryToken := func(res projectpkg.DetectionResult, choice string) (bool, bool) {
			if strings.TrimSpace(recoveryToken) == "" {
				return false, false
			}
			return true, activity.ValidateAmbiguousProjectRecoveryToken(recoverySessionID, recoveryToken, strings.TrimSpace(choice), res.AvailableProjects, res.Path)
		}

		detRes, err := resolveWriteProjectWithChoiceAndProcessOverride(projectChoice, projectChoiceReason, validateRecoveryToken, cfg.DefaultProject)
		if err != nil {
			return writeProjectErrorResult(activity, recoverySessionID, detRes, err), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if sessionID == "" {
			sessionID = resolveFallbackSessionID(s, project)
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		_, err = s.AddPrompt(store.AddPromptParams{
			SessionID: sessionID,
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save prompt: " + err.Error()), nil
		}

		if activity != nil {
			activity.RecordPrompt(sessionID, project, content)
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Prompt saved: %q", truncate(content, 80)), nil), nil
	}
}

func handleContext(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)
		scope, _ := req.GetArguments()["scope"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311)
		detRes, err := resolveReadProjectWithProcessOverride(s, projectOverride, cfg.DefaultProject)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}
		project := detRes.Project
		project, _ = store.NormalizeProject(project)
		detRes.Project = project // JR2-1: keep envelope in sync with normalized query project

		// REQ-391: personal scope is cross-project by definition. When scope=personal
		// and no explicit project override was provided, clear the project filter so
		// observations from all projects are returned (not just the cwd-detected one).
		contextProject := project
		if scope == "personal" && strings.TrimSpace(projectOverride) == "" {
			contextProject = ""
		}

		sessionID := defaultSessionID(project)
		activity.RecordToolCall(sessionID)

		contextResult, err := s.FormatContext(contextProject, scope)
		if err != nil {
			return mcp.NewToolResultError("Failed to get context: " + err.Error()), nil
		}

		if contextResult == "" {
			return respondWithProject(detRes, "No previous session memories found.", nil), nil
		}

		stats, _ := s.Stats()
		var projects string
		if len(stats.Projects) > 0 {
			projects = strings.Join(stats.Projects, ", ")
		} else {
			projects = "none"
		}

		result := fmt.Sprintf("%s\n---\nMemory stats: %d sessions, %d observations across projects: %s",
			contextResult, stats.TotalSessions, stats.TotalObservations, projects)

		if nudge := activity.NudgeIfNeeded(sessionID); nudge != "" {
			result += nudge
		}

		// omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2
		// Phase 7: mem_context has no free-text query, so it surfaces the
		// most-recently-updated trusted procedure of each polarity for the
		// project instead of a query match (BuildProcedureCardForProject's
		// own doc). Gated by cfg.Procedural != nil, same as handleSearch.
		var extra map[string]any
		if cfg.Procedural != nil {
			if card := BuildProcedureCardForProject(s, contextProject, scope); card != nil {
				extra = map[string]any{"procedure_card": card}
			}
		}

		return respondWithProject(detRes, result, extra), nil
	}
}

func handleStats(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311, REQ-314)
		detRes, err := resolveReadProjectWithProcessOverride(s, projectOverride, cfg.DefaultProject)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}

		stats, err := loadMCPStats(s)
		if err != nil {
			return mcp.NewToolResultError("Failed to get stats: " + err.Error()), nil
		}

		var projects string
		if len(stats.Projects) > 0 {
			projects = strings.Join(stats.Projects, ", ")
		} else {
			projects = "none yet"
		}

		result := fmt.Sprintf("Memory System Stats:\n- Sessions: %d\n- Observations: %d\n- Prompts: %d\n- Projects: %s",
			stats.TotalSessions, stats.TotalObservations, stats.TotalPrompts, projects)

		return respondWithProject(detRes, result, nil), nil
	}
}

func DoctorToolHandler(s *store.Store) server.ToolHandlerFunc {
	return handleDoctor(s, MCPConfig{})
}

func handleDoctor(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)
		check, _ := req.GetArguments()["check"].(string)
		detRes, err := resolveReadProjectWithProcessOverride(s, projectOverride, cfg.DefaultProject)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project", fmt.Sprintf("Project %q not found in store", upe.Name), upe.AvailableProjects), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}
		project := detRes.Project
		project, _ = store.NormalizeProject(project)
		runner := diagnostic.NewRunner()
		scope := diagnostic.Scope{Store: s, Project: project, Now: time.Now()}
		var report diagnostic.Report
		if strings.TrimSpace(check) != "" {
			report, err = runner.RunOne(ctx, scope, check)
		} else {
			report, err = runner.RunAll(ctx, scope)
		}
		if err != nil {
			report = diagnostic.ErrorReport(project, err)
		}
		out, marshalErr := jsonMarshal(report)
		if marshalErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Doctor JSON error: %s", marshalErr)), nil
		}
		result := mcp.NewToolResultText(string(out))
		if report.Status == diagnostic.StatusError {
			result.IsError = true
		}
		return result, nil
	}
}

func handleTimeline(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		observationID := int64(intArg(req, "observation_id", 0))
		if observationID == 0 {
			return mcp.NewToolResultError("observation_id is required"), nil
		}
		before := intArg(req, "before", 5)
		after := intArg(req, "after", 5)
		projectOverride, _ := req.GetArguments()["project"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311, REQ-314)
		detRes, err := resolveReadProjectWithProcessOverride(s, projectOverride, cfg.DefaultProject)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}

		result, err := s.Timeline(observationID, before, after)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Timeline error: %s", err)), nil
		}

		var b strings.Builder

		// Session header
		if result.SessionInfo != nil {
			summary := ""
			if result.SessionInfo.Summary != nil {
				summary = fmt.Sprintf(" — %s", truncate(*result.SessionInfo.Summary, 100))
			}
			fmt.Fprintf(&b, "Session: %s (%s)%s\n", result.SessionInfo.Project, result.SessionInfo.StartedAt, summary)
			fmt.Fprintf(&b, "Total observations in session: %d\n\n", result.TotalInRange)
		}

		// Before entries
		if len(result.Before) > 0 {
			b.WriteString("─── Before ───\n")
			for _, e := range result.Before {
				fmt.Fprintf(&b, "  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
			}
			b.WriteString("\n")
		}

		// Focus observation (highlighted)
		fmt.Fprintf(&b, ">>> #%d [%s] %s <<<\n", result.Focus.ID, result.Focus.Type, result.Focus.Title)
		fmt.Fprintf(&b, "    %s\n", truncate(result.Focus.Content, 500))
		fmt.Fprintf(&b, "    %s\n\n", timeutil.FormatLocal(result.Focus.CreatedAt))

		// After entries
		if len(result.After) > 0 {
			b.WriteString("─── After ───\n")
			for _, e := range result.After {
				fmt.Fprintf(&b, "  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
			}
		}

		return respondWithProject(detRes, b.String(), nil), nil
	}
}

func handleGetObservation(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		obs, err := s.GetObservation(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Observation #%d not found", id)), nil
		}

		// Resolve project from process override/cwd (REQ-310, REQ-314). No per-call
		// override possible for get-by-ID. Tolerant: don't fail the fetch on
		// resolution error; degrade to plain text.
		detRes, detErr := resolveReadProjectWithProcessOverride(s, "", cfg.DefaultProject)

		obsProject := ""
		if obs.Project != nil {
			obsProject = fmt.Sprintf("\nProject: %s", *obs.Project)
		}
		scope := fmt.Sprintf("\nScope: %s", obs.Scope)
		topic := ""
		if obs.TopicKey != nil {
			topic = fmt.Sprintf("\nTopic: %s", *obs.TopicKey)
		}
		toolName := ""
		if obs.ToolName != nil {
			toolName = fmt.Sprintf("\nTool: %s", *obs.ToolName)
		}
		duplicateMeta := fmt.Sprintf("\nDuplicates: %d", obs.DuplicateCount)
		revisionMeta := fmt.Sprintf("\nRevisions: %d", obs.RevisionCount)

		result := fmt.Sprintf("#%d [%s] %s\n%s\nSession: %s%s%s\nCreated: %s",
			obs.ID, obs.Type, obs.Title,
			obs.Content,
			obs.SessionID, obsProject+scope+topic, toolName+duplicateMeta+revisionMeta,
			timeutil.FormatLocal(obs.CreatedAt),
		)

		if detErr != nil {
			// Degraded path: resolution failed (e.g. ambiguous cwd). Return
			// the observation content without envelope rather than erroring.
			return mcp.NewToolResultText(result), nil
		}
		return respondWithProject(detRes, result, nil), nil
	}
}

func handleSessionSummary(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		// project field intentionally not read — auto-detect only (REQ-308 write-tool contract)

		// Reject empty/whitespace-only content before any project resolution (#393).
		if strings.TrimSpace(content) == "" {
			return mcp.NewToolResultError("content is required for mem_session_summary"), nil
		}

		// Honour process-level project override (cfg.DefaultProject) set via
		// OMNIA_PROJECT or `omnia mcp --project` (#403/#413). Falls back to cwd
		// detection when no override is configured.
		detRes, err := resolveWriteProjectWithProcessOverride(cfg.DefaultProject)
		if err != nil {
			return writeProjectErrorResult(nil, "", detRes, err), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if sessionID == "" {
			sessionID = resolveFallbackSessionID(s, project)
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		_, err = s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      "session_summary",
			Title:     fmt.Sprintf("Session summary: %s", project),
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save session summary: " + err.Error()), nil
		}

		msg := fmt.Sprintf("Session summary saved for project %q", project)
		if score := activity.ActivityScore(defaultSessionID(project)); score != "" {
			msg += "\n" + score
		}
		detRes.Project = project
		return respondWithProject(detRes, msg, nil), nil
	}
}

func handleSessionStart(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		directory, _ := req.GetArguments()["directory"].(string)
		resolvedDirectory := strings.TrimSpace(directory)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveSessionStartProject(resolvedDirectory)
		if err != nil {
			return writeProjectErrorResult(nil, "", detRes, err), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		activity.RecordToolCall(defaultSessionID(project))
		if resolvedDirectory == "" {
			resolvedDirectory = strings.TrimSpace(detRes.Path)
			if resolvedDirectory == "" {
				resolvedDirectory = strings.TrimSpace(currentWorkingDirectory())
			}
		}

		if err := s.CreateSession(id, project, resolvedDirectory); err != nil {
			return mcp.NewToolResultError("Failed to start session: " + err.Error()), nil
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Session %q started for project %q", id, project), nil), nil
	}
}

func resolveSessionStartProject(explicitDirectory string) (projectpkg.DetectionResult, error) {
	if explicitDirectory == "" {
		return resolveWriteProject()
	}
	res := projectpkg.DetectProjectFull(explicitDirectory)
	if res.Error != nil {
		return res, res.Error
	}
	return res, nil
}

func handleSessionEnd(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		summary, _ := req.GetArguments()["summary"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveWriteProject()
		if err != nil {
			if errors.Is(err, projectpkg.ErrInvalidConfig) {
				return writeProjectErrorResult(nil, "", detRes, err), nil
			}
			// For session end, still complete the operation even if project resolution fails.
			// Use basename fallback.
			cwd, _ := os.Getwd()
			detRes = projectpkg.DetectionResult{
				Project: projectpkg.DetectProject(cwd),
				Source:  "dir_basename",
				Path:    cwd,
			}
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if err := s.EndSession(id, summary); err != nil {
			return mcp.NewToolResultError("Failed to end session: " + err.Error()), nil
		}

		activity.ClearSession(defaultSessionID(project))

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Session %q completed", id), nil), nil
	}
}

func handleCapturePassive(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		source, _ := req.GetArguments()["source"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveWriteProject()
		if err != nil {
			return writeProjectErrorResult(nil, "", detRes, err), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		activity.RecordToolCall(defaultSessionID(project))

		if content == "" {
			return mcp.NewToolResultError("content is required — include text with a '## Key Learnings:' section"), nil
		}

		if sessionID == "" {
			sessionID = resolveFallbackSessionID(s, project)
			_ = ensureImplicitSessionWithCWD(s, sessionID, project)
		}

		if source == "" {
			source = "mcp-passive"
		}

		result, err := s.PassiveCapture(store.PassiveCaptureParams{
			SessionID: sessionID,
			Content:   content,
			Project:   project,
			Source:    source,
		})
		if err != nil {
			return mcp.NewToolResultError("Passive capture failed: " + err.Error()), nil
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf(
			"Passive capture complete: extracted=%d saved=%d duplicates=%d",
			result.Extracted, result.Saved, result.Duplicates,
		), nil), nil
	}
}

// handleJudge implements mem_judge. It validates params, calls JudgeRelation,
// and returns the updated relation row as JSON.
//
// Tool description contract (Design §6.1):
// "Record a verdict on a pending memory conflict surfaced by mem_save.
// When mem_save returns judgment_required=true, call mem_judge once per
// candidate (judgment_id is in candidates[]). Use to mark SUPERSEDES,
// CONFLICTS_WITH, NOT_CONFLICT, RELATED, COMPATIBLE, or SCOPED.
// Ask the user when ambiguous."
func handleJudge(s *store.Store, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		judgmentID, _ := req.GetArguments()["judgment_id"].(string)
		relation, _ := req.GetArguments()["relation"].(string)

		if judgmentID == "" {
			return mcp.NewToolResultError("judgment_id is required"), nil
		}
		if relation == "" {
			return mcp.NewToolResultError("relation is required"), nil
		}

		// Collect optional fields.
		var reason *string
		if v, ok := req.GetArguments()["reason"].(string); ok && v != "" {
			reason = &v
		}
		var evidence *string
		if v, ok := req.GetArguments()["evidence"].(string); ok && v != "" {
			evidence = &v
		}
		var confidence *float64
		if v, ok := req.GetArguments()["confidence"].(float64); ok {
			if v < 0 || v > 1 {
				return mcp.NewToolResultError("confidence must be between 0.0 and 1.0"), nil
			}
			confidence = &v
		}

		// Session context for provenance.
		sessionID, _ := req.GetArguments()["session_id"].(string)
		// Actor defaults to "agent" kind for MCP tool calls.
		markedByActor := "agent"
		markedByKind := "agent"
		markedByModel := "" // No model ID available at MCP layer without explicit param.

		result, err := s.JudgeRelation(store.JudgeRelationParams{
			JudgmentID:    judgmentID,
			Relation:      relation,
			Reason:        reason,
			Evidence:      evidence,
			Confidence:    confidence,
			MarkedByActor: markedByActor,
			MarkedByKind:  markedByKind,
			MarkedByModel: markedByModel,
			SessionID:     sessionID,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		envelope := map[string]any{
			"relation": result,
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleCompare implements mem_compare. The agent has already judged two
// observations externally; this handler persists the verdict via JudgeBySemantic.
//
// Tool description contract (REQ-011, Design §9):
// "Persist a semantic verdict you have already judged externally into Omnia.
// Accepts int IDs for both observations, resolves them to sync_ids, then
// calls JudgeBySemantic. Returns the persisted relation's sync_id."
func handleCompare(s *store.Store, _ *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// --- required numeric IDs ---
		rawA, okA := req.GetArguments()["memory_id_a"].(float64)
		rawB, okB := req.GetArguments()["memory_id_b"].(float64)
		if !okA {
			return mcp.NewToolResultError("memory_id_a is required (integer observation id)"), nil
		}
		if !okB {
			return mcp.NewToolResultError("memory_id_b is required (integer observation id)"), nil
		}
		idA := int64(rawA)
		idB := int64(rawB)

		// --- required string fields ---
		relation, _ := req.GetArguments()["relation"].(string)
		if relation == "" {
			return mcp.NewToolResultError("relation is required"), nil
		}
		reasoning, _ := req.GetArguments()["reasoning"].(string)
		if reasoning == "" {
			return mcp.NewToolResultError("reasoning is required"), nil
		}

		// --- required confidence ---
		rawConf, okConf := req.GetArguments()["confidence"].(float64)
		if !okConf {
			return mcp.NewToolResultError("confidence is required (float 0.0..1.0)"), nil
		}
		if rawConf < 0 || rawConf > 1 {
			return mcp.NewToolResultError("confidence must be between 0.0 and 1.0"), nil
		}

		// --- optional model ---
		model, _ := req.GetArguments()["model"].(string)

		// Resolve integer IDs to sync_ids.
		obsA, err := s.GetObservation(idA)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("observation id=%d not found: %s", idA, err)), nil
		}
		obsB, err := s.GetObservation(idB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("observation id=%d not found: %s", idB, err)), nil
		}

		syncID, err := s.JudgeBySemantic(store.JudgeBySemanticParams{
			SourceID:   obsA.SyncID,
			TargetID:   obsB.SyncID,
			Relation:   relation,
			Confidence: rawConf,
			Reasoning:  reasoning,
			Model:      model,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// syncID is "" when relation == "not_conflict" (JudgeBySemantic no-op).
		envelope := map[string]any{
			"sync_id": syncID,
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func handleMergeProjects(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fromStr, _ := req.GetArguments()["from"].(string)
		to, _ := req.GetArguments()["to"].(string)

		if fromStr == "" || to == "" {
			return mcp.NewToolResultError("both 'from' and 'to' are required"), nil
		}

		var sources []string
		for _, src := range strings.Split(fromStr, ",") {
			src = strings.TrimSpace(src)
			if src != "" {
				sources = append(sources, src)
			}
		}

		if len(sources) == 0 {
			return mcp.NewToolResultError("at least one source project name is required in 'from'"), nil
		}

		result, err := s.MergeProjects(sources, to)
		if err != nil {
			return mcp.NewToolResultError("Merge failed: " + err.Error()), nil
		}

		msg := fmt.Sprintf("Merged %d source(s) into %q:\n", len(result.SourcesMerged), result.Canonical)
		msg += fmt.Sprintf("  Observations moved: %d\n", result.ObservationsUpdated)
		msg += fmt.Sprintf("  Sessions moved:     %d\n", result.SessionsUpdated)
		msg += fmt.Sprintf("  Prompts moved:      %d\n", result.PromptsUpdated)

		return mcp.NewToolResultText(msg), nil
	}
}

// ─── Project Resolution Helpers ──────────────────────────────────────────────

// unknownProjectError is returned when a read tool receives a project override
// that does not exist in the store.
type unknownProjectError struct {
	Name              string
	AvailableProjects []string
}

func (e *unknownProjectError) Error() string {
	return "unknown project: " + e.Name
}

type invalidProjectChoiceError struct {
	Name              string
	AvailableProjects []string
}

func (e *invalidProjectChoiceError) Error() string {
	return "invalid project choice: " + e.Name
}

type missingRecoveryTokenError struct {
	Name              string
	AvailableProjects []string
}

func (e *missingRecoveryTokenError) Error() string {
	return "missing ambiguous project recovery token for project choice: " + e.Name
}

type invalidRecoveryTokenError struct {
	Name              string
	AvailableProjects []string
}

func (e *invalidRecoveryTokenError) Error() string {
	return "invalid ambiguous project recovery token for project choice: " + e.Name
}

type invalidExplicitProjectError struct {
	Name   string
	Reason string
}

func (e *invalidExplicitProjectError) Error() string {
	if e.Reason == "" {
		return "invalid project: " + e.Name
	}
	return "invalid project: " + e.Name + " (" + e.Reason + ")"
}

type normalizedProjectCollisionError struct {
	Name              string
	Normalized        string
	CollidingProjects []string
}

func (e *normalizedProjectCollisionError) Error() string {
	return fmt.Sprintf("project %q collides after normalization to %q", e.Name, e.Normalized)
}

type unknownSessionError struct {
	SessionID string
}

func (e *unknownSessionError) Error() string {
	return "unknown session: " + e.SessionID
}

type sessionProjectMismatchError struct {
	SessionID       string
	SessionProject  string
	ExplicitProject string
}

func (e *sessionProjectMismatchError) Error() string {
	return fmt.Sprintf("session %q belongs to project %q, not %q", e.SessionID, e.SessionProject, e.ExplicitProject)
}

// resolveWriteProject detects the current project from the process working
// directory. Returns ErrAmbiguousProject if cwd is a parent of multiple repos.
func resolveWriteProject() (projectpkg.DetectionResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	res := projectpkg.DetectProjectFull(cwd)
	if res.Error != nil {
		return res, res.Error
	}
	return res, nil
}

func processProjectResult(project string) (projectpkg.DetectionResult, bool) {
	project = strings.TrimSpace(project)
	if project == "" {
		return projectpkg.DetectionResult{}, false
	}
	normalized, warning := store.NormalizeProject(project)
	return projectpkg.DetectionResult{
		Project: normalized,
		Source:  sourceProcessOverride,
		Path:    "",
		Warning: warning,
	}, true
}

func resolveWriteProjectWithProcessOverride(defaultProject string) (projectpkg.DetectionResult, error) {
	if res, ok := processProjectResult(defaultProject); ok {
		return res, nil
	}
	return resolveWriteProject()
}

type ambiguousRecoveryTokenValidator func(projectpkg.DetectionResult, string) (provided bool, valid bool)

func resolveWriteProjectWithChoiceAndProcessOverride(projectChoice, reason string, validateToken ambiguousRecoveryTokenValidator, defaultProject string) (projectpkg.DetectionResult, error) {
	if strings.TrimSpace(projectChoice) == "" {
		return resolveWriteProjectWithProcessOverride(defaultProject)
	}
	return resolveWriteProjectWithChoice(projectChoice, reason, validateToken)
}

// resolveWriteProjectWithChoice preserves normal write resolution authority and
// only uses an explicit project choice as a recovery path from ErrAmbiguousProject.
func resolveWriteProjectWithChoice(projectChoice, reason string, validateToken ambiguousRecoveryTokenValidator) (projectpkg.DetectionResult, error) {
	res, err := resolveWriteProject()
	if err == nil {
		// Non-ambiguous config/git/autodetect remains authoritative. Ignore any
		// supplied project choice so agents cannot drift writes to arbitrary buckets.
		return res, nil
	}
	if !errors.Is(err, projectpkg.ErrAmbiguousProject) {
		return res, err
	}

	if strings.TrimSpace(reason) != projectpkg.SourceUserSelectedAfterAmbiguousProject {
		return res, err
	}

	choice := strings.TrimSpace(projectChoice)
	if choice == "" || !containsProjectChoice(res.AvailableProjects, choice) {
		return res, &invalidProjectChoiceError{
			Name:              choice,
			AvailableProjects: res.AvailableProjects,
		}
	}
	if normalized, colliding := normalizedProjectCollisions(res.AvailableProjects, choice); len(colliding) > 1 {
		return res, &normalizedProjectCollisionError{
			Name:              choice,
			Normalized:        normalized,
			CollidingProjects: colliding,
		}
	}
	provided, valid := false, false
	if validateToken != nil {
		provided, valid = validateToken(res, choice)
	}
	if !provided {
		return res, &missingRecoveryTokenError{
			Name:              choice,
			AvailableProjects: res.AvailableProjects,
		}
	}
	if !valid {
		return res, &invalidRecoveryTokenError{
			Name:              choice,
			AvailableProjects: res.AvailableProjects,
		}
	}

	res.Project = choice
	res.Source = projectpkg.SourceUserSelectedAfterAmbiguousProject
	res.Path = resolveAmbiguousChoicePath(res.Path, choice)
	res.Warning = "project selected by user after ambiguous_project recovery"
	return res, nil
}

func resolveSaveWriteProjectWithProcessOverride(s *store.Store, projectChoice string, explicitProjectProvided bool, reason, sessionID string, validateToken ambiguousRecoveryTokenValidator, defaultProject string) (projectpkg.DetectionResult, error) {
	if !explicitProjectProvided && strings.TrimSpace(projectChoice) == "" && strings.TrimSpace(sessionID) == "" && strings.TrimSpace(reason) == "" {
		if processRes, ok := processProjectResult(defaultProject); ok {
			return processRes, nil
		}
	}
	return resolveSaveWriteProject(s, projectChoice, explicitProjectProvided, reason, sessionID, validateToken)
}

func resolveSaveWriteProject(s *store.Store, projectChoice string, explicitProjectProvided bool, reason, sessionID string, validateToken ambiguousRecoveryTokenValidator) (projectpkg.DetectionResult, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	trimmedProjectChoice := strings.TrimSpace(projectChoice)
	trimmedReason := strings.TrimSpace(reason)
	var sessionProject string
	var sessionPath string
	if trimmedSessionID != "" {
		sess, err := s.GetSession(trimmedSessionID)
		if err != nil {
			return projectpkg.DetectionResult{}, &unknownSessionError{SessionID: trimmedSessionID}
		}
		sessionProject, err = normalizeExplicitWriteProject(sess.Project)
		if err != nil {
			return projectpkg.DetectionResult{}, err
		}
		sessionPath = strings.TrimSpace(sess.Directory)
	}

	if explicitProjectProvided && trimmedProjectChoice == "" {
		return projectpkg.DetectionResult{}, &invalidExplicitProjectError{Name: projectChoice, Reason: "project is required"}
	}

	if trimmedProjectChoice != "" {
		cwdRes, cwdErr := resolveWriteProject()
		if cwdErr != nil {
			if errors.Is(cwdErr, projectpkg.ErrInvalidConfig) {
				return cwdRes, cwdErr
			}
			if errors.Is(cwdErr, projectpkg.ErrAmbiguousProject) {
				if normalized, colliding := normalizedProjectCollisions(cwdRes.AvailableProjects, trimmedProjectChoice); len(colliding) > 1 {
					return cwdRes, &normalizedProjectCollisionError{
						Name:              trimmedProjectChoice,
						Normalized:        normalized,
						CollidingProjects: colliding,
					}
				}
			} else {
				return cwdRes, cwdErr
			}
		}

		project, err := normalizeExplicitWriteProject(projectChoice)
		if err != nil {
			return projectpkg.DetectionResult{}, err
		}
		if collisionErr := explicitWriteProjectCollision(trimmedProjectChoice, project, sessionProject, cwdRes); collisionErr != nil {
			return cwdRes, collisionErr
		}
		if sessionProject != "" && project != sessionProject {
			return projectpkg.DetectionResult{}, &sessionProjectMismatchError{
				SessionID:       trimmedSessionID,
				SessionProject:  sessionProject,
				ExplicitProject: project,
			}
		}

		exists, err := s.ProjectExists(project)
		if err != nil {
			return projectpkg.DetectionResult{}, err
		}
		if exists {
			if explicitProjectHasSeparatorCollapse(trimmedProjectChoice, project) {
				return cwdRes, &normalizedProjectCollisionError{
					Name:              trimmedProjectChoice,
					Normalized:        project,
					CollidingProjects: []string{trimmedProjectChoice, project},
				}
			}
			return projectpkg.DetectionResult{
				Project: project,
				Source:  projectpkg.SourceExplicitOverride,
				Path:    "",
			}, nil
		}

		if sessionProject != "" {
			return projectpkg.DetectionResult{
				Project: project,
				Source:  projectpkg.SourceExplicitOverride,
				Path:    sessionPath,
			}, nil
		}

		if cwdErr != nil {
			if errors.Is(cwdErr, projectpkg.ErrInvalidConfig) {
				return cwdRes, cwdErr
			}
			if errors.Is(cwdErr, projectpkg.ErrAmbiguousProject) {
				if trimmedReason == projectpkg.SourceUserSelectedAfterAmbiguousProject {
					return resolveWriteProjectWithChoice(projectChoice, reason, validateToken)
				}
				return cwdRes, cwdErr
			}
			return cwdRes, cwdErr
		}

		if cwdRes.Source == projectpkg.SourceConfig {
			resolvedProject, err := normalizeExplicitWriteProject(cwdRes.Project)
			if err != nil {
				return projectpkg.DetectionResult{}, err
			}
			if resolvedProject == project {
				return projectpkg.DetectionResult{
					Project: project,
					Source:  projectpkg.SourceExplicitOverride,
					Path:    cwdRes.Path,
				}, nil
			}
		}

		return projectpkg.DetectionResult{AvailableProjects: knownWriteProjects(s, cwdRes)}, &unknownProjectError{
			Name:              project,
			AvailableProjects: knownWriteProjects(s, cwdRes),
		}
	}

	if trimmedReason == projectpkg.SourceUserSelectedAfterAmbiguousProject && trimmedProjectChoice != "" {
		res, err := resolveWriteProjectWithChoice(projectChoice, reason, validateToken)
		if err != nil {
			return res, err
		}
		if sessionProject != "" {
			resolvedProject, err := normalizeExplicitWriteProject(res.Project)
			if err != nil {
				return projectpkg.DetectionResult{}, err
			}
			if resolvedProject != sessionProject {
				return projectpkg.DetectionResult{}, &sessionProjectMismatchError{
					SessionID:       trimmedSessionID,
					SessionProject:  sessionProject,
					ExplicitProject: resolvedProject,
				}
			}
		}
		return res, nil
	}

	if sessionProject != "" {
		return projectpkg.DetectionResult{
			Project: sessionProject,
			Source:  projectpkg.SourceSessionProject,
			Path:    sessionPath,
		}, nil
	}

	return resolveWriteProject()
}

func explicitWriteProjectCollision(trimmedRawProject, normalizedProject, sessionProject string, cwdRes projectpkg.DetectionResult) *normalizedProjectCollisionError {
	trimmedRawProject = strings.TrimSpace(trimmedRawProject)
	if trimmedRawProject == "" || normalizedProject == "" || !explicitProjectHasSeparatorCollapse(trimmedRawProject, normalizedProject) {
		return nil
	}

	if sessionProject != "" && sessionProject == normalizedProject {
		return &normalizedProjectCollisionError{
			Name:              trimmedRawProject,
			Normalized:        normalizedProject,
			CollidingProjects: []string{trimmedRawProject, normalizedProject},
		}
	}

	if cwdRes.Source == projectpkg.SourceConfig {
		canonical := strings.TrimSpace(cwdRes.Project)
		if canonical == trimmedRawProject {
			return nil
		}
		canonicalNormalized, _ := store.NormalizeProject(canonical)
		if canonicalNormalized == normalizedProject {
			return &normalizedProjectCollisionError{
				Name:              trimmedRawProject,
				Normalized:        normalizedProject,
				CollidingProjects: uniqueTrimmedProjects(trimmedRawProject, canonical, normalizedProject),
			}
		}
	}

	return nil
}

func explicitProjectHasSeparatorCollapse(trimmedRawProject, normalizedProject string) bool {
	lowerTrimmed := strings.TrimSpace(strings.ToLower(trimmedRawProject))
	return lowerTrimmed != "" && lowerTrimmed != normalizedProject
}

func uniqueTrimmedProjects(names ...string) []string {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func knownWriteProjects(s *store.Store, context projectpkg.DetectionResult) []string {
	seen := make(map[string]struct{})
	projects := make([]string, 0)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		projects = append(projects, name)
	}

	stats, err := s.Stats()
	if err == nil {
		for _, project := range stats.Projects {
			add(project)
		}
	}
	add(context.Project)
	for _, project := range context.AvailableProjects {
		add(project)
	}

	return projects
}

func normalizeExplicitWriteProject(projectName string) (string, error) {
	trimmed := strings.TrimSpace(projectName)
	if trimmed == "" {
		return "", &invalidExplicitProjectError{Name: projectName, Reason: "project is required"}
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", &invalidExplicitProjectError{Name: projectName, Reason: "project must be a name, not a path"}
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", &invalidExplicitProjectError{Name: projectName, Reason: "project contains control characters"}
		}
	}
	project, _ := store.NormalizeProject(trimmed)
	if project == "" {
		return "", &invalidExplicitProjectError{Name: projectName, Reason: "project is required"}
	}
	return project, nil
}

func containsProjectChoice(available []string, choice string) bool {
	choice = strings.TrimSpace(choice)
	for _, candidate := range available {
		if strings.TrimSpace(candidate) == choice {
			return true
		}
	}
	return false
}

func normalizedProjectCollisions(candidates []string, choice string) (string, []string) {
	normalized, _ := store.NormalizeProject(strings.TrimSpace(choice))
	if normalized == "" {
		return "", nil
	}

	colliding := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		candidateNormalized, _ := store.NormalizeProject(trimmed)
		if candidateNormalized != normalized {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		colliding = append(colliding, trimmed)
	}
	if len(colliding) < 2 {
		return normalized, nil
	}
	return normalized, colliding
}

func resolveAmbiguousChoicePath(ambiguousParent, choice string) string {
	parent := strings.TrimSpace(ambiguousParent)
	if parent == "" || strings.TrimSpace(choice) == "" {
		return ""
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Match the same name shape used by project.DetectProjectFull for
		// available_projects: trim + lowercase only. Do not use store.NormalizeProject
		// here because it collapses repeated '-'/'_' and can create collisions.
		if strings.TrimSpace(strings.ToLower(entry.Name())) != choice {
			continue
		}
		childPath := filepath.Join(parent, entry.Name())
		if _, err := os.Stat(filepath.Join(childPath, ".git")); err != nil {
			continue
		}
		absChild, err := filepath.Abs(childPath)
		if err != nil {
			return childPath
		}
		return absChild
	}
	return ""
}

// resolveReadProject validates an optional project override against the store.
// If override is empty, falls back to auto-detection from cwd.
// JW2: normalizes the override (lowercase+trim) before ProjectExists lookup so
// that e.g. "MyApp" and "  myapp  " both resolve to the stored "myapp".
func resolveReadProjectWithProcessOverride(s *store.Store, override, defaultProject string) (projectpkg.DetectionResult, error) {
	if strings.TrimSpace(override) == "" {
		if res, ok := processProjectResult(defaultProject); ok {
			return res, nil
		}
	}
	return resolveReadProject(s, override)
}

func resolveReadProject(s *store.Store, override string) (projectpkg.DetectionResult, error) {
	override = strings.TrimSpace(override)
	if override == "" {
		return resolveWriteProject()
	}
	normalized, _ := store.NormalizeProject(override)
	exists, err := s.ProjectExists(normalized)
	if err != nil {
		return projectpkg.DetectionResult{}, err
	}
	if !exists {
		// Collect available projects for the error.
		stats, _ := s.Stats()
		return projectpkg.DetectionResult{}, &unknownProjectError{
			Name:              normalized,
			AvailableProjects: stats.Projects,
		}
	}
	return projectpkg.DetectionResult{
		Project: normalized,
		Source:  projectpkg.SourceExplicitOverride, // JR2-2: use named constant
		Path:    "",
	}, nil
}

// respondWithProject wraps a tool result by prepending the project envelope
// fields (project, project_source, project_path) to the text output.
// extra is an optional map of additional fields to include.
func respondWithProject(res projectpkg.DetectionResult, text string, extra map[string]any) *mcp.CallToolResult {
	envelope := map[string]any{
		"project":        res.Project,
		"project_source": res.Source,
		"project_path":   res.Path,
		"result":         text,
	}
	if res.Warning != "" {
		envelope["warning"] = res.Warning
	}
	for k, v := range extra {
		envelope[k] = v
	}
	out, _ := jsonMarshal(envelope)
	return mcp.NewToolResultText(string(out))
}

func writeProjectErrorResult(activity *SessionActivity, sessionID string, res projectpkg.DetectionResult, err error) *mcp.CallToolResult {
	code := "ambiguous_project"
	if errors.Is(err, projectpkg.ErrInvalidConfig) {
		code = "invalid_project_config"
	}
	var choiceErr *invalidProjectChoiceError
	if errors.As(err, &choiceErr) {
		if choiceErr.Name == "" {
			return errorWithMeta("invalid_project_choice",
				"Project choice is empty; choose exactly one value from available_projects and retry with project_choice_reason=user_selected_after_ambiguous_project",
				choiceErr.AvailableProjects,
			)
		}
		return errorWithMeta("invalid_project_choice",
			fmt.Sprintf("Project choice %q is not one of available_projects", choiceErr.Name),
			choiceErr.AvailableProjects,
		)
	}
	var missingTokenErr *missingRecoveryTokenError
	if errors.As(err, &missingTokenErr) {
		return errorWithMeta("missing_recovery_token",
			fmt.Sprintf("project_choice_reason=user_selected_after_ambiguous_project for %q requires the recovery_token from the ambiguous_project error", missingTokenErr.Name),
			missingTokenErr.AvailableProjects,
		)
	}
	var invalidTokenErr *invalidRecoveryTokenError
	if errors.As(err, &invalidTokenErr) {
		return errorWithMeta("invalid_recovery_token",
			fmt.Sprintf("recovery_token is invalid, stale, or not valid for selected project %q", invalidTokenErr.Name),
			invalidTokenErr.AvailableProjects,
		)
	}
	var explicitErr *invalidExplicitProjectError
	if errors.As(err, &explicitErr) {
		return errorWithMeta("invalid_project",
			fmt.Sprintf("Project %q is invalid: %s", explicitErr.Name, explicitErr.Reason),
			res.AvailableProjects,
		)
	}
	var collisionErr *normalizedProjectCollisionError
	if errors.As(err, &collisionErr) {
		message := fmt.Sprintf(
			"Project %q collapses to stored bucket %q, but multiple exact candidates would share that bucket: %s. Refuse write until the colliding project names are disambiguated.",
			collisionErr.Name,
			collisionErr.Normalized,
			strings.Join(collisionErr.CollidingProjects, ", "),
		)
		return errorWithMeta("project_name_collision", message, res.AvailableProjects)
	}
	var unknownSessionErr *unknownSessionError
	if errors.As(err, &unknownSessionErr) {
		return errorWithMeta("unknown_session",
			fmt.Sprintf("Session %q was provided but does not exist", unknownSessionErr.SessionID),
			res.AvailableProjects,
		)
	}
	var unknownProjectErr *unknownProjectError
	if errors.As(err, &unknownProjectErr) {
		return errorWithMeta("unknown_project",
			fmt.Sprintf("Project %q is not backed by known context. Use an existing project, a matching session, repo .engram/config.json, or ambiguous-project recovery.", unknownProjectErr.Name),
			unknownProjectErr.AvailableProjects,
		)
	}
	var mismatchErr *sessionProjectMismatchError
	if errors.As(err, &mismatchErr) {
		return errorWithMeta("session_project_mismatch",
			fmt.Sprintf("Session %q belongs to project %q, but request targeted %q", mismatchErr.SessionID, mismatchErr.SessionProject, mismatchErr.ExplicitProject),
			res.AvailableProjects,
		)
	}
	result := errorWithMeta(code, fmt.Sprintf("Cannot determine project: %s", err), res.AvailableProjects)
	if code == "ambiguous_project" && activity != nil {
		if strings.TrimSpace(sessionID) == "" {
			sessionID = defaultSessionID("")
		}
		addErrorMetadata(result, map[string]any{
			"recovery_token":    activity.IssueAmbiguousProjectRecoveryToken(sessionID, res.AvailableProjects, res.Path),
			"token_ttl_seconds": int(ambiguousProjectRecoveryTTL.Seconds()),
		})
	}
	return result
}

func addErrorMetadata(result *mcp.CallToolResult, metadata map[string]any) {
	if result == nil || len(result.Content) == 0 || len(metadata) == 0 {
		return
	}
	text, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		return
	}
	for k, v := range metadata {
		envelope[k] = v
	}
	out, err := jsonMarshal(envelope)
	if err != nil {
		return
	}
	result.Content[0] = mcp.NewTextContent(string(out))
}

// errorWithMeta returns a structured tool error result with error_code,
// message, available_projects, and a hint for resolution.
func errorWithMeta(code, msg string, availableProjects []string) *mcp.CallToolResult {
	envelope := map[string]any{
		"error_code":         code,
		"message":            msg,
		"available_projects": availableProjects,
	}
	switch code {
	case "ambiguous_project":
		envelope["hint"] = "Ask the user to choose one of available_projects, then retry mem_save or mem_save_prompt with project and project_choice_reason=user_selected_after_ambiguous_project; alternatively cd into the target repo or add repo .engram/config.json."
	case "invalid_project_choice":
		envelope["hint"] = "Use exactly one of available_projects after asking the user, or cd into the target repo, or add repo .engram/config.json."
	case "missing_recovery_token":
		envelope["hint"] = "Retry with the recovery_token returned by the ambiguous_project error after the user selects one available_projects value."
	case "invalid_recovery_token":
		envelope["hint"] = "Request a fresh ambiguous_project recovery_token and retry with the same session, cwd context, and selected available_projects value before it expires."
	case "unknown_project":
		envelope["hint"] = "Use one of the available_projects values, or omit project to auto-detect."
	case "invalid_project_config":
		envelope["hint"] = "Fix .engram/config.json so project_name is a non-empty project name."
	case "invalid_project":
		envelope["hint"] = "Use a non-empty project name, not a path."
	case "unknown_session":
		envelope["hint"] = "Start the session first, omit session_id, or retry with an existing session_id."
	case "session_project_mismatch":
		envelope["hint"] = "Use a project that matches the existing session, or omit session_id and write to a different project."
	}
	out, _ := jsonMarshal(envelope)
	result := mcp.NewToolResultText(string(out))
	result.IsError = true
	return result
}

// jsonMarshal marshals v to JSON. Named to allow test injection if needed.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// defaultSessionID returns a project-scoped default session ID.
// If project is non-empty: "manual-save-{project}"
// If project is empty: "manual-save"
func defaultSessionID(project string) string {
	if project == "" {
		return "manual-save"
	}
	return "manual-save-" + project
}

// resolveFallbackSessionID resolves the session a write should attach to when
// the caller did not provide an explicit session_id.
//
// It first consults the persisted sessions table for the most recent active
// (un-ended) session of the project (issue #386). The SessionStart hook
// registers a UUID session via the HTTP server, a SEPARATE process from this
// MCP (stdio) server; the two share only the SQLite store, so the active
// session must be resolved from disk rather than from any in-process map.
//
// When no active session exists for the project (or the store query fails for
// any reason), it falls back to the manual-save-{project} session, preserving
// the prior behavior for projects with no live session.
func resolveFallbackSessionID(s *store.Store, project string) string {
	if s != nil {
		if id, ok, err := s.MostRecentActiveSession(project); err == nil && ok {
			return id
		}
	}
	return defaultSessionID(project)
}

func intArg(req mcp.CallToolRequest, key string, defaultVal int) int {
	v, ok := req.GetArguments()[key].(float64)
	if !ok {
		return defaultVal
	}
	return int(v)
}

func boolArg(req mcp.CallToolRequest, key string, defaultVal bool) bool {
	v, ok := req.GetArguments()[key].(bool)
	if !ok {
		return defaultVal
	}
	return v
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
