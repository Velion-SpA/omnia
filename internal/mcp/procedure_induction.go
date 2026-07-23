package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/store"
)

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2 ───────
//
// This file wires PR1's procedures store + governance gate + LLM inducer
// into mem_save/mem_update: Phase 4 (online candidate induction on a
// recorded outcome) and Phase 5 (SSGM applied_procedure reuse feedback).
// Both are gated by MCPConfig.Procedural != nil (procedural.enabled) and are
// unconditionally best-effort — neither may ever fail or alter the
// save/update they're attached to (spec: induction domain, "Induction
// failure...MUST NOT fail the originating save").
//
// Both phases run in a bounded, fire-and-forget goroutine (review fix, see
// runProceduralHooksForObservation's doc): mem_save/mem_update must return
// promptly regardless of the LLM inducer's latency, mirroring
// enqueueAutoEmbed's own non-blocking contract (mcp.go).

// defaultProceduralHookTimeout bounds a single async induction +
// reuse-feedback pass when ProceduralWiring.HookTimeout is unset — mirrors
// the offline induction path's own per-cluster timeout
// (internal/store/procedure_induction.go) and ScanProject's per-pair callCtx
// (internal/store/relations.go).
const defaultProceduralHookTimeout = 60 * time.Second

// ProceduralWiring holds the pieces mem_save/mem_update's procedural-memory
// hooks need, built by cmd/omnia/main.go only when procedural.enabled is
// true in config.yaml.
type ProceduralWiring struct {
	// Inducer performs the actual LLM shell-out (internal/llm's
	// ProcedureInducer interface — *llm.ClaudeRunner/*llm.OpenCodeRunner
	// both satisfy it via their Induce method). nil is valid: SafeInduce
	// degrades to DeterministicInduce instead of erroring, so a
	// misconfigured/missing CLI only degrades induction quality, never
	// disables it outright.
	Inducer llm.ProcedureInducer

	// HookTimeout bounds the online fire-and-forget induction +
	// reuse-feedback goroutine runProceduralHooksForObservation launches per
	// observation. Zero/negative uses defaultProceduralHookTimeout (60s).
	// Production wiring (cmd/omnia/main.go) never sets this — it exists so a
	// hung LLM CLI can never block the goroutine forever, and so tests can
	// shrink the bound instead of waiting out the real 60s default.
	HookTimeout time.Duration

	// afterHook, when set, is invoked at the very end of the async goroutine
	// (whether it returned early, finished normally, or recovered a panic)
	// purely so tests can synchronize on completion instead of
	// polling/sleeping. Never set by production wiring.
	afterHook func()
}

// validProcedurePostconditionKinds is a local copy of the locked
// postcondition_kind vocabulary (internal/store's own validProcedureKinds
// map is unexported) — an LLM-induced value outside this set falls back to
// "custom" rather than being rejected outright, since store.UpsertProcedure
// would otherwise reject the entire candidate over a cosmetic model
// deviation.
var validProcedurePostconditionKinds = map[string]bool{
	store.PostconditionTestsPass:  true,
	store.PostconditionLintClean:  true,
	store.PostconditionBuildGreen: true,
	store.PostconditionCustom:     true,
}

// procedurePolarityForOutcome derives a procedure's polarity from a
// persisted observation outcome — worked→playbook, did_not_work→
// anti_playbook (design decision #2: the CALLER derives polarity from the
// source outcome, never the model). Any other outcome value (including
// empty/"unknown") is not induction-eligible.
func procedurePolarityForOutcome(outcome string) (string, bool) {
	switch outcome {
	case store.OutcomeWorked:
		return store.ProcedurePolarityPlaybook, true
	case store.OutcomeDidNotWork:
		return store.ProcedurePolarityAntiPlaybook, true
	default:
		return "", false
	}
}

// runProceduralHooksForObservation performs Phase 4 (online candidate
// induction) + Phase 5 (SSGM applied_procedure reuse feedback) for a single
// observation that just had its outcome persisted, via either mem_save or
// mem_update. Gated by cfg.Procedural != nil; nil means
// procedural.enabled=false — a total no-op restoring today's exact
// behavior (spec: backward-compatibility domain).
//
// Review fix (blocking): this used to run llm.SafeInduce SYNCHRONOUSLY on
// the caller's own goroutine. handleSave/handleUpdate run inside
// writeQueue's single worker goroutine, so a hung/slow LLM CLI blocked
// EVERY write tool for the whole session and added unbounded latency to
// every bugfix-outcome save — the design/spec/docs all call for this to be
// async/best-effort, mirroring enqueueAutoEmbed. It now launches a bounded,
// fire-and-forget goroutine instead: handleSave/handleUpdate return
// promptly regardless of the inducer's latency, and a context.WithTimeout
// (ProceduralWiring.HookTimeout, default 60s) keeps a hung CLI from
// blocking the goroutine forever. Recovers from any panic and swallows
// every error: this must NEVER surface a failure to its caller.
func runProceduralHooksForObservation(cfg MCPConfig, s *store.Store, obsID int64, appliedProcedure string) {
	if cfg.Procedural == nil {
		return
	}
	wiring := cfg.Procedural

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "engram: procedural memory hook panic (non-fatal): %v\n", r)
			}
			if wiring.afterHook != nil {
				wiring.afterHook()
			}
		}()

		timeout := wiring.HookTimeout
		if timeout <= 0 {
			timeout = defaultProceduralHookTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		obs, err := s.GetObservation(obsID)
		if err != nil || obs.Outcome == nil {
			return
		}
		outcome := *obs.Outcome
		if outcome != store.OutcomeWorked && outcome != store.OutcomeDidNotWork {
			return
		}

		induceProcedureFromObservation(ctx, cfg, s, obs, outcome)
		applyProcedureReuseFeedback(s, appliedProcedure, outcome, obs.SessionID, obs.Title, obs.SyncID)
	}()
}

// induceProcedureFromObservation implements Phase 4: on a bugfix-family
// save/update recording outcome=worked/did_not_work, best-effort induces a
// CANDIDATE procedure via llm.SafeInduce (which never errors — a missing or
// failing Inducer degrades to a deterministic fallback) and upserts it.
// ctx is the caller's bounded per-hook context (runProceduralHooksForObservation's
// context.WithTimeout) — SafeInduce's underlying Inducer.Induce call is
// cancelled with it, so a hung LLM CLI can't run past the configured
// timeout. Errors from UpsertProcedure itself (e.g. an empty induced
// trigger) are logged and swallowed — never propagated.
func induceProcedureFromObservation(ctx context.Context, cfg MCPConfig, s *store.Store, obs *store.Observation, outcome string) {
	polarity, ok := procedurePolarityForOutcome(outcome)
	if !ok {
		return
	}

	trigger := obs.Title
	trajectory := obs.Content
	prompt := llm.BuildInducePrompt(trigger, trajectory)

	induced := llm.SafeInduce(ctx, cfg.Procedural.Inducer, trigger, trajectory, prompt)
	if len(induced.Steps) == 0 {
		// Nothing substantive to persist — a degraded/empty fallback is not
		// worth storing as a candidate procedure.
		return
	}

	steps := make([]store.ProcedureStep, 0, len(induced.Steps))
	for _, st := range induced.Steps {
		steps = append(steps, store.ProcedureStep{Order: st.Order, Template: st.Template, Slots: st.Slots})
	}

	postconditionKind := induced.PostconditionKind
	if !validProcedurePostconditionKinds[postconditionKind] {
		postconditionKind = store.PostconditionCustom
	}

	project, _ := store.NormalizeProject(strFromPtr(obs.Project))

	// Dedup (review nit): UpsertProcedure mints a brand-new sync_id whenever
	// its input SyncID is empty, and this caller never set one — every
	// bugfix-outcome save was therefore minting an unbounded new candidate
	// row, even for the exact same recurring bug. Reuse an existing
	// procedure's sync_id for the SAME (error_signature-or-title, polarity)
	// pair instead so this becomes an UPDATE (UpsertProcedure's own
	// contract: on conflict, governance fields — state, reuse_confirmed,
	// contradicted_count, confidence, etc. — are left untouched; only
	// content is refreshed). This bounds row growth and makes recurrence a
	// real signal instead of noise.
	dedupKey := strings.TrimSpace(strFromPtr(obs.ErrorSignature))
	if dedupKey == "" {
		dedupKey = strings.TrimSpace(trigger)
	}
	sourceObsSyncIDs := []string{obs.SyncID}
	existingSyncID := ""
	if existing, found := findDedupProcedure(s, project, polarity, dedupKey); found {
		existingSyncID = existing.SyncID
		sourceObsSyncIDs = appendUniqueSyncID(existing.SourceObsSyncIDs, obs.SyncID)
	}

	if _, err := s.UpsertProcedure(store.Procedure{
		SyncID:            existingSyncID,
		Project:           project,
		Scope:             obs.Scope,
		Polarity:          polarity,
		Trigger:           induced.Trigger,
		Steps:             steps,
		ExpectedOutcome:   induced.ExpectedOutcome,
		PostconditionKind: postconditionKind,
		PostconditionExpr: induced.PostconditionExpr,
		Confidence:        0.5, // seed confidence for a freshly induced candidate; ignored by UpsertProcedure when this is an existing-row UPDATE
		State:             store.ProcedureStateCandidate,
		SourceObsSyncIDs:  sourceObsSyncIDs,
		InducedByActor:    "engram",
		InducedByKind:     "system",
		InducedByModel:    induced.Model,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "engram: procedure induction UpsertProcedure error (non-fatal): %v\n", err)
	}
}

// findDedupProcedure looks up an already-induced procedure for the same
// recurring (error_signature-or-title, polarity) pair within the same
// project, so a repeat bugfix-outcome save UPDATEs it instead of minting a
// brand-new candidate row every time. Matching is FTS5-based
// (procedures_fts over trigger+steps_summary via SearchProcedures), not
// exact-string — a false negative here only costs one extra candidate row
// (it self-heals as recurrence accrues its own reuse signal), never data
// loss.
func findDedupProcedure(s *store.Store, project, polarity, dedupKey string) (store.Procedure, bool) {
	if strings.TrimSpace(dedupKey) == "" {
		return store.Procedure{}, false
	}
	candidates, err := s.SearchProcedures(dedupKey, polarity, "", 5)
	if err != nil {
		return store.Procedure{}, false
	}
	for _, c := range candidates {
		if c.Project == project {
			return c, true
		}
	}
	return store.Procedure{}, false
}

// appendUniqueSyncID appends syncID to existing unless it is already
// present, preserving a dedup'd procedure's full source-observation history
// across repeated inductions instead of clobbering it down to one entry.
func appendUniqueSyncID(existing []string, syncID string) []string {
	for _, id := range existing {
		if id == syncID {
			return existing
		}
	}
	return append(existing, syncID)
}

// applyProcedureReuseFeedback implements Phase 5: mem_save/mem_update's
// optional applied_procedure argument drives the SSGM governance gate —
// outcome=worked → ConfirmReuse (UPVOTE), outcome=did_not_work → Contradict
// (DOWNVOTE). When appliedProcedure is empty, falls back to a best-effort
// same-session trigger-vs-error_signature match (spec: reuse-attribution).
// currentObsSyncID is the observation this outcome was just recorded
// against — see resolveSessionProcedureMatch's doc for why the fallback
// match needs it. Errors are logged and swallowed — never propagated.
func applyProcedureReuseFeedback(s *store.Store, appliedProcedure, outcome, sessionID, triggerText, currentObsSyncID string) {
	syncID := strings.TrimSpace(appliedProcedure)
	if syncID == "" {
		syncID = resolveSessionProcedureMatch(s, sessionID, triggerText, currentObsSyncID, outcome)
	}
	if syncID == "" {
		return
	}

	var err error
	switch outcome {
	case store.OutcomeWorked:
		_, err = s.ConfirmReuse(syncID, "engram")
	case store.OutcomeDidNotWork:
		_, err = s.Contradict(syncID, "engram")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: procedure reuse feedback error (non-fatal): %v\n", err)
	}
}

// resolveSessionProcedureMatch is the reuse-attribution fallback (spec:
// "When absent, the system MUST attempt a best-effort same-session
// trigger-vs-error_signature match"): among procedures whose trigger
// text-matches triggerText, it returns the first one that was itself
// induced from an observation in THIS session (mirrors
// resolveFallbackSessionID's "same session" framing) — i.e. one of its
// source_obs_sync_ids belongs to a recent observation of sessionID.
// Returns "" when nothing qualifies (a safe no-op, never an error).
//
// Review fix (blocking): induction (Phase 4, above) runs BEFORE this
// fallback and — for a bugfix-family outcome save — creates (or refreshes)
// a candidate procedure sourced from THIS SAME observation. Without the
// three guards below, this fallback would then FTS-match that just-created
// row (it's in this session's observations) and call
// ConfirmReuse/Contradict on it — self-crediting a fake reuse and
// corrupting the SSGM signal:
//  1. currentObsSyncID exclusion — a procedure can never be "reused" by the
//     very observation that (may have) just created/refreshed it.
//  2. trusted-state only — you can only reuse something already PROMOTED;
//     a brand-new candidate was never reused by definition, so it can't be
//     self-credited.
//  3. matching polarity — a worked outcome only reuse-confirms a playbook,
//     did_not_work only an anti_playbook; a trigger-text collision must
//     never cross-credit the other polarity.
func resolveSessionProcedureMatch(s *store.Store, sessionID, triggerText, currentObsSyncID, outcome string) string {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(triggerText) == "" {
		return ""
	}
	requiredPolarity, ok := procedurePolarityForOutcome(outcome)
	if !ok {
		return ""
	}

	sessionObs, err := s.SessionObservations(sessionID, 50)
	if err != nil || len(sessionObs) == 0 {
		return ""
	}
	sessionSyncIDs := make(map[string]bool, len(sessionObs))
	for _, o := range sessionObs {
		if o.SyncID != "" {
			sessionSyncIDs[o.SyncID] = true
		}
	}

	candidates, err := s.SearchProcedures(triggerText, requiredPolarity, store.ProcedureStateTrusted, 10)
	if err != nil {
		return ""
	}
	for _, p := range candidates {
		if procedureSourcedFrom(p, currentObsSyncID) {
			continue // can't reuse the memory that (may have) created/refreshed it
		}
		for _, src := range p.SourceObsSyncIDs {
			if sessionSyncIDs[src] {
				return p.SyncID
			}
		}
	}
	return ""
}

// procedureSourcedFrom reports whether p's source_obs_sync_ids includes
// obsSyncID — i.e. whether p was (among possibly others) induced/refreshed
// from that observation.
func procedureSourcedFrom(p store.Procedure, obsSyncID string) bool {
	if obsSyncID == "" {
		return false
	}
	for _, src := range p.SourceObsSyncIDs {
		if src == obsSyncID {
			return true
		}
	}
	return false
}
