package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606) ────────────
//
// A Procedure is a verifiable, parameterized program induced from a bugfix
// `outcome`: worked → playbook (steps-to-follow), did_not_work →
// anti_playbook (guardrail, steps-to-avoid). Polarity is ALWAYS set by the
// caller from the source outcome, never by the inducing model (design
// decision #2) — this file only stores/retrieves/governs procedures; it
// never decides polarity itself.
//
// Governance is a small state machine (SSGM): candidate → trusted at
// procedural.trust_threshold confirmed reuses (ConfirmReuse, ExpEL-style
// UPVOTE); Contradict (DOWNVOTE) decays confidence and, at
// procedural.confidence_floor, auto-retires (never hard-deletes — the row
// stays queryable). Unused trusted procedures decay via the review_after
// spaced-repetition field (DecayProcedures).
//
// Local-only this slice (design decision #6): procedures/procedures_fts are
// intentionally NEVER written to sync_mutations — no SyncEntityProcedure
// const, no enqueueSyncMutationTx call anywhere in this file.

// ─── Vocabulary (locked) ──────────────────────────────────────────────────────

// Procedure polarity values.
const (
	ProcedurePolarityPlaybook     = "playbook"
	ProcedurePolarityAntiPlaybook = "anti_playbook"
)

// Procedure governance state values.
const (
	ProcedureStateCandidate = "candidate"
	ProcedureStateTrusted   = "trusted"
	ProcedureStateRetired   = "retired"
)

// Procedure postcondition kind values — the machine-checkable verification
// vocabulary a procedure's expected outcome is authored against. Free-form
// verification logic (not yet a fixed enum member) uses "custom" plus
// PostconditionExpr; this slice only STORES these values, it never executes
// them (the deferred compiler runtime, design obs #1592, does that later).
const (
	PostconditionTestsPass  = "tests_pass"
	PostconditionLintClean  = "lint_clean"
	PostconditionBuildGreen = "build_green"
	PostconditionCustom     = "custom"
)

// validProcedurePolarities is the locked set of polarity values UpsertProcedure accepts.
var validProcedurePolarities = map[string]bool{
	ProcedurePolarityPlaybook:     true,
	ProcedurePolarityAntiPlaybook: true,
}

// isValidProcedurePolarity mirrors isValidRelationVerb's guard shape.
func isValidProcedurePolarity(v string) bool {
	return validProcedurePolarities[v]
}

// validPostconditionKinds is the locked set of postcondition_kind values UpsertProcedure accepts.
var validPostconditionKinds = map[string]bool{
	PostconditionTestsPass:  true,
	PostconditionLintClean:  true,
	PostconditionBuildGreen: true,
	PostconditionCustom:     true,
}

// isValidPostconditionKind mirrors isValidRelationVerb's guard shape.
func isValidPostconditionKind(v string) bool {
	return validPostconditionKinds[v]
}

// ─── Governance defaults (seed values — Open Question in design.md: these ──
// need one empirical tuning pass against the live corpus, same caveat
// RecallConfig's jina floors document for their own initial constants).
const (
	defaultProceduralTrustThreshold  = 3
	defaultProceduralConfidenceFloor = 0.15
	defaultProceduralReviewAfterDays = 14

	// confirmReuseBump/contradictDecay are the ExpEL-style UPVOTE/DOWNVOTE
	// confidence deltas. Confidence is always clamped to [0.0, 1.0].
	confirmReuseBump = 0.15
	contradictDecay  = 0.20
	// decayStepConfidence is DecayProcedures' per-pass confidence decrement
	// for an unused trusted procedure past its review_after date.
	decayStepConfidence = 0.10
)

func (s *Store) proceduralTrustThreshold() int {
	if s.cfg.ProceduralTrustThreshold > 0 {
		return s.cfg.ProceduralTrustThreshold
	}
	return defaultProceduralTrustThreshold
}

func (s *Store) proceduralConfidenceFloor() float64 {
	if s.cfg.ProceduralConfidenceFloor > 0 {
		return s.cfg.ProceduralConfidenceFloor
	}
	return defaultProceduralConfidenceFloor
}

func (s *Store) proceduralReviewAfterDays() int {
	if s.cfg.ProceduralReviewAfterDays > 0 {
		return s.cfg.ProceduralReviewAfterDays
	}
	return defaultProceduralReviewAfterDays
}

// ─── Types ────────────────────────────────────────────────────────────────────

// ProcedureStep is one ordered, slot-templated step of a Procedure's program
// (ASI: programmatic > prose). Slots name the parameters a caller/executor
// must bind before the template step is actionable.
type ProcedureStep struct {
	Order    int      `json:"order"`
	Template string   `json:"template"`
	Slots    []string `json:"slots,omitempty"`
}

// Procedure is a row of the `procedures` table: a verifiable, parameterized
// program with a polarity, a governance state, and confidence — see this
// file's package doc comment for the full lifecycle.
type Procedure struct {
	ID                int64
	SyncID            string
	Project           string
	Scope             string
	Polarity          string
	Trigger           string
	Steps             []ProcedureStep
	ExpectedOutcome   string
	PostconditionKind string
	PostconditionExpr string
	Confidence        float64
	State             string
	ReuseConfirmed    int
	ContradictedCount int
	SourceObsSyncIDs  []string
	InducedByActor    string
	InducedByKind     string
	InducedByModel    string
	CreatedAt         string
	UpdatedAt         string
	LastReusedAt      *string
	ReviewAfter       *string
	RetiredAt         *string
}

// ListProceduresOptions filters ListProcedures.
type ListProceduresOptions struct {
	Project  string
	Scope    string
	Polarity string // "" means no filter
	State    string // "" means no filter
	Limit    int    // <= 0 means default (50)
}

// joinStepTemplates renders steps into the plain-text steps_summary column
// procedures_fts indexes alongside trigger (design.md: "FTS5 over
// trigger+steps_summary, same pattern as observations/observations_fts").
// Storing a derived plain-text column (instead of indexing the raw JSON)
// keeps FTS5 tokenization free of JSON punctuation noise.
func joinStepTemplates(steps []ProcedureStep) string {
	if len(steps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		if strings.TrimSpace(st.Template) != "" {
			parts = append(parts, st.Template)
		}
	}
	return strings.Join(parts, " \n ")
}

// ─── UpsertProcedure ──────────────────────────────────────────────────────────

// UpsertProcedure inserts or updates a procedure keyed by p.SyncID (mirrors
// JudgeBySemantic's insert-or-update-by-sync_id pattern). If p.SyncID is
// empty, a new one is minted via newSyncID("proc"). Rejects an unknown
// Polarity or PostconditionKind, or an empty Trigger.
//
// On conflict (re-upserting an existing sync_id — e.g. re-running offline
// induction over the same signature cluster), governance-owned fields
// (state, reuse_confirmed, contradicted_count, confidence, last_reused_at,
// review_after, retired_at) are DELIBERATELY left untouched: those are only
// ever mutated by ConfirmReuse/Contradict/RetireProcedure/DecayProcedures,
// never by re-induction. Only the procedure's content (trigger, steps,
// expected outcome, postcondition, source observations, induction
// provenance) is refreshed.
func (s *Store) UpsertProcedure(p Procedure) (string, error) {
	if !isValidProcedurePolarity(p.Polarity) {
		return "", fmt.Errorf("UpsertProcedure: invalid polarity %q — must be %q or %q", p.Polarity, ProcedurePolarityPlaybook, ProcedurePolarityAntiPlaybook)
	}
	if !isValidPostconditionKind(p.PostconditionKind) {
		return "", fmt.Errorf("UpsertProcedure: invalid postcondition_kind %q — must be one of %q, %q, %q, %q", p.PostconditionKind, PostconditionTestsPass, PostconditionLintClean, PostconditionBuildGreen, PostconditionCustom)
	}
	if strings.TrimSpace(p.Trigger) == "" {
		return "", fmt.Errorf("UpsertProcedure: trigger is required")
	}

	stepsJSON, err := json.Marshal(p.Steps)
	if err != nil {
		return "", fmt.Errorf("UpsertProcedure: marshal steps: %w", err)
	}
	sourceJSON, err := json.Marshal(p.SourceObsSyncIDs)
	if err != nil {
		return "", fmt.Errorf("UpsertProcedure: marshal source_obs_sync_ids: %w", err)
	}
	stepsSummary := joinStepTemplates(p.Steps)

	syncID := strings.TrimSpace(p.SyncID)
	if syncID == "" {
		syncID = newSyncID("proc")
	}

	state := p.State
	if state == "" {
		state = ProcedureStateCandidate
	}

	// PR1 review fix: clamp the incoming confidence now that PR2's
	// inducers (online mem_save wiring, offline procedure-induct) feed
	// real, caller-supplied values instead of only being reachable via
	// ConfirmReuse/Contradict/DecayProcedures (which already clamp
	// internally). Without this, a misbehaving inducer could persist an
	// out-of-range confidence that the governance gate's own thresholds
	// were never designed to compare against.
	confidence := clampConfidence(p.Confidence)

	scope := normalizeScope(p.Scope)
	project, _ := NormalizeProject(p.Project)

	if err := s.withTx(func(tx *sql.Tx) error {
		_, execErr := tx.Exec(`
			INSERT INTO procedures
				(sync_id, project, scope, polarity, "trigger", steps, steps_summary,
				 expected_outcome, postcondition_kind, postcondition_expr, confidence,
				 state, reuse_confirmed, contradicted_count, source_obs_sync_ids,
				 induced_by_actor, induced_by_kind, induced_by_model,
				 created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
			ON CONFLICT(sync_id) DO UPDATE SET
				project             = excluded.project,
				scope               = excluded.scope,
				polarity            = excluded.polarity,
				"trigger"           = excluded."trigger",
				steps               = excluded.steps,
				steps_summary       = excluded.steps_summary,
				expected_outcome    = excluded.expected_outcome,
				postcondition_kind  = excluded.postcondition_kind,
				postcondition_expr  = excluded.postcondition_expr,
				source_obs_sync_ids = excluded.source_obs_sync_ids,
				induced_by_actor    = excluded.induced_by_actor,
				induced_by_kind     = excluded.induced_by_kind,
				induced_by_model    = excluded.induced_by_model,
				updated_at          = datetime('now')
		`, syncID, nullableString(project), scope, p.Polarity, p.Trigger, string(stepsJSON), stepsSummary,
			nullableString(p.ExpectedOutcome), p.PostconditionKind, nullableString(p.PostconditionExpr), confidence,
			state, p.ReuseConfirmed, p.ContradictedCount, string(sourceJSON),
			nullableString(p.InducedByActor), nullableString(p.InducedByKind), nullableString(p.InducedByModel),
		)
		return execErr
	}); err != nil {
		return "", fmt.Errorf("UpsertProcedure: %w", err)
	}

	return syncID, nil
}

// ─── GetProcedure / ListProcedures / SearchProcedures ────────────────────────

// procedureScanFields is the shared SELECT column list for GetProcedure,
// ListProcedures, and SearchProcedures, so all three stay in lockstep.
const procedureSelectColumns = `
	id, sync_id, ifnull(project,''), scope, polarity, "trigger", steps,
	ifnull(expected_outcome,''), postcondition_kind, ifnull(postcondition_expr,''),
	confidence, state, reuse_confirmed, contradicted_count, source_obs_sync_ids,
	ifnull(induced_by_actor,''), ifnull(induced_by_kind,''), ifnull(induced_by_model,''),
	created_at, updated_at, last_reused_at, review_after, retired_at
`

// scanProcedureRow scans one procedures row matching procedureSelectColumns'
// column order into a Procedure, decoding the steps/source_obs_sync_ids JSON
// columns.
func scanProcedureRow(scan func(dest ...any) error) (Procedure, error) {
	var p Procedure
	var stepsJSON, sourceJSON string
	if err := scan(
		&p.ID, &p.SyncID, &p.Project, &p.Scope, &p.Polarity, &p.Trigger, &stepsJSON,
		&p.ExpectedOutcome, &p.PostconditionKind, &p.PostconditionExpr,
		&p.Confidence, &p.State, &p.ReuseConfirmed, &p.ContradictedCount, &sourceJSON,
		&p.InducedByActor, &p.InducedByKind, &p.InducedByModel,
		&p.CreatedAt, &p.UpdatedAt, &p.LastReusedAt, &p.ReviewAfter, &p.RetiredAt,
	); err != nil {
		return Procedure{}, err
	}
	if stepsJSON != "" {
		if err := json.Unmarshal([]byte(stepsJSON), &p.Steps); err != nil {
			return Procedure{}, fmt.Errorf("scanProcedureRow: unmarshal steps: %w", err)
		}
	}
	if sourceJSON != "" {
		if err := json.Unmarshal([]byte(sourceJSON), &p.SourceObsSyncIDs); err != nil {
			return Procedure{}, fmt.Errorf("scanProcedureRow: unmarshal source_obs_sync_ids: %w", err)
		}
	}
	return p, nil
}

// GetProcedure retrieves a single procedure by its sync_id.
func (s *Store) GetProcedure(syncID string) (*Procedure, error) {
	row := s.db.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
	p, err := scanProcedureRow(row.Scan)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("GetProcedure: procedure %q not found", syncID)
	}
	if err != nil {
		return nil, fmt.Errorf("GetProcedure: %w", err)
	}
	return &p, nil
}

// ListProcedures returns procedures matching opts, most recently updated first.
func (s *Store) ListProcedures(opts ListProceduresOptions) ([]Procedure, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT ` + procedureSelectColumns + ` FROM procedures WHERE 1=1`
	var args []any

	if opts.Project != "" {
		project, _ := NormalizeProject(opts.Project)
		query += ` AND ifnull(project,'') = ?`
		args = append(args, project)
	}
	if opts.Scope != "" {
		query += ` AND scope = ?`
		args = append(args, normalizeScope(opts.Scope))
	}
	if opts.Polarity != "" {
		query += ` AND polarity = ?`
		args = append(args, opts.Polarity)
	}
	if opts.State != "" {
		query += ` AND state = ?`
		args = append(args, opts.State)
	}
	query += ` ORDER BY datetime(updated_at) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListProcedures: query: %w", err)
	}
	defer rows.Close()

	procedures := []Procedure{}
	for rows.Next() {
		p, err := scanProcedureRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListProcedures: scan: %w", err)
		}
		procedures = append(procedures, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListProcedures: rows error: %w", err)
	}
	return procedures, nil
}

// SearchProcedures runs an FTS5 query over procedures_fts (trigger +
// steps_summary), optionally filtered by polarity and/or state, ordered by
// BM25 rank. limit <= 0 defaults to 20. An empty (post-sanitization) query
// returns no results rather than the whole table, mirroring FindCandidates'
// own "no query, no results" guard.
func (s *Store) SearchProcedures(query, polarity, state string, limit int) ([]Procedure, error) {
	if limit <= 0 {
		limit = 20
	}
	ftsQuery := sanitizeFTS(query)
	if strings.TrimSpace(ftsQuery) == "" {
		return []Procedure{}, nil
	}

	sqlQuery := `
		SELECT ` + procedureColumnsPrefixed("p") + `
		FROM procedures_fts fts
		JOIN procedures p ON p.id = fts.rowid
		WHERE procedures_fts MATCH ?
	`
	args := []any{ftsQuery}

	if polarity != "" {
		sqlQuery += ` AND p.polarity = ?`
		args = append(args, polarity)
	}
	if state != "" {
		sqlQuery += ` AND p.state = ?`
		args = append(args, state)
	}
	sqlQuery += ` ORDER BY fts.rank LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("SearchProcedures: FTS5 query: %w", err)
	}
	defer rows.Close()

	procedures := []Procedure{}
	for rows.Next() {
		p, err := scanProcedureRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("SearchProcedures: scan: %w", err)
		}
		procedures = append(procedures, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SearchProcedures: rows error: %w", err)
	}
	return procedures, nil
}

// procedureColumnsPrefixed returns the same column set as
// procedureSelectColumns, qualified by the given table alias, for queries
// that JOIN procedures against another table (procedures_fts) and would
// otherwise be ambiguous. Columns wrapped in ifnull(...) need the alias
// INSIDE the call, e.g. ifnull(p.project, empty-string) — applying the
// alias as a naive outer prefix instead (e.g. p.ifnull(project, ...)) is
// not valid SQL.
func procedureColumnsPrefixed(alias string) string {
	cols := []string{
		alias + ".id",
		alias + ".sync_id",
		"ifnull(" + alias + ".project,'')",
		alias + ".scope",
		alias + ".polarity",
		alias + `."trigger"`,
		alias + ".steps",
		"ifnull(" + alias + ".expected_outcome,'')",
		alias + ".postcondition_kind",
		"ifnull(" + alias + ".postcondition_expr,'')",
		alias + ".confidence",
		alias + ".state",
		alias + ".reuse_confirmed",
		alias + ".contradicted_count",
		alias + ".source_obs_sync_ids",
		"ifnull(" + alias + ".induced_by_actor,'')",
		"ifnull(" + alias + ".induced_by_kind,'')",
		"ifnull(" + alias + ".induced_by_model,'')",
		alias + ".created_at",
		alias + ".updated_at",
		alias + ".last_reused_at",
		alias + ".review_after",
		alias + ".retired_at",
	}
	return strings.Join(cols, ", ")
}

// ─── Governance gate (SSGM) ───────────────────────────────────────────────────

// ConfirmReuse records a confirmed (worked) reuse of a procedure — the SSGM
// UPVOTE: reuse_confirmed increments by 1, confidence bumps by
// confirmReuseBump (clamped to 1.0), and last_reused_at is stamped to now.
// At procedural.trust_threshold confirmed reuses, a candidate promotes to
// trusted and gets its first review_after (spaced-repetition) deadline.
// Idempotent-safe: calling ConfirmReuse repeatedly past the threshold keeps
// incrementing reuse_confirmed and refreshing review_after (each reuse
// "resets the clock" on an already-trusted procedure), it never demotes.
//
// actor identifies the caller for audit/logging parity with JudgeBySemantic's
// system-provenance convention (marked_by_kind="system"); this slice's
// `procedures` schema has no per-call provenance column (only aggregate
// governance counters), so actor is validated but not yet persisted to a
// dedicated column — a natural extension point for a later slice.
func (s *Store) ConfirmReuse(syncID, actor string) (Procedure, error) {
	if strings.TrimSpace(syncID) == "" {
		return Procedure{}, fmt.Errorf("ConfirmReuse: syncID is required")
	}
	if strings.TrimSpace(actor) == "" {
		return Procedure{}, fmt.Errorf("ConfirmReuse: actor is required")
	}

	threshold := s.proceduralTrustThreshold()
	reviewDays := s.proceduralReviewAfterDays()

	var result Procedure
	if err := s.withTx(func(tx *sql.Tx) error {
		row := tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		p, err := scanProcedureRow(row.Scan)
		if err == sql.ErrNoRows {
			return fmt.Errorf("ConfirmReuse: procedure %q not found", syncID)
		}
		if err != nil {
			return fmt.Errorf("ConfirmReuse: %w", err)
		}

		p.ReuseConfirmed++
		p.Confidence = clampConfidence(p.Confidence + confirmReuseBump)
		promoting := p.State == ProcedureStateCandidate && p.ReuseConfirmed >= threshold
		if promoting {
			p.State = ProcedureStateTrusted
		}

		var reviewAfter *string
		if p.State == ProcedureStateTrusted {
			ra := reviewAfterTimestamp(reviewDays)
			reviewAfter = &ra
		}

		if _, execErr := tx.Exec(`
			UPDATE procedures
			SET reuse_confirmed = ?,
			    confidence      = ?,
			    state           = ?,
			    last_reused_at  = datetime('now'),
			    review_after    = COALESCE(?, review_after),
			    updated_at      = datetime('now')
			WHERE sync_id = ?
		`, p.ReuseConfirmed, p.Confidence, p.State, reviewAfter, syncID); execErr != nil {
			return fmt.Errorf("ConfirmReuse: update: %w", execErr)
		}

		row = tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		result, err = scanProcedureRow(row.Scan)
		return err
	}); err != nil {
		return Procedure{}, err
	}

	return result, nil
}

// Contradict records a contradicted (did_not_work) reuse of a procedure —
// the SSGM DOWNVOTE: contradicted_count increments by 1, confidence decays
// by contradictDecay (clamped to 0.0). At procedural.confidence_floor, the
// procedure auto-retires (RetireProcedure semantics: never hard-deleted, row
// stays queryable). Idempotent: calling Contradict again on an
// already-retired procedure keeps incrementing contradicted_count and
// leaves state retired — it never un-retires.
//
// actor mirrors ConfirmReuse's own doc — accepted for audit/logging parity,
// not yet persisted to a dedicated column.
func (s *Store) Contradict(syncID, actor string) (Procedure, error) {
	if strings.TrimSpace(syncID) == "" {
		return Procedure{}, fmt.Errorf("Contradict: syncID is required")
	}
	if strings.TrimSpace(actor) == "" {
		return Procedure{}, fmt.Errorf("Contradict: actor is required")
	}

	floor := s.proceduralConfidenceFloor()

	var result Procedure
	if err := s.withTx(func(tx *sql.Tx) error {
		row := tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		p, err := scanProcedureRow(row.Scan)
		if err == sql.ErrNoRows {
			return fmt.Errorf("Contradict: procedure %q not found", syncID)
		}
		if err != nil {
			return fmt.Errorf("Contradict: %w", err)
		}

		p.ContradictedCount++
		p.Confidence = clampConfidence(p.Confidence - contradictDecay)

		shouldRetire := p.State == ProcedureStateRetired || p.Confidence <= floor
		var retiredAt *string
		if shouldRetire {
			p.State = ProcedureStateRetired
			if p.RetiredAt != nil {
				retiredAt = p.RetiredAt
			} else {
				now := nowTimestamp()
				retiredAt = &now
			}
		}

		if _, execErr := tx.Exec(`
			UPDATE procedures
			SET contradicted_count = ?,
			    confidence         = ?,
			    state              = ?,
			    retired_at         = COALESCE(?, retired_at),
			    updated_at         = datetime('now')
			WHERE sync_id = ?
		`, p.ContradictedCount, p.Confidence, p.State, retiredAt, syncID); execErr != nil {
			return fmt.Errorf("Contradict: update: %w", execErr)
		}

		row = tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		result, err = scanProcedureRow(row.Scan)
		return err
	}); err != nil {
		return Procedure{}, err
	}

	return result, nil
}

// RetireProcedure marks a procedure retired directly (e.g. an operator's
// manual "no longer useful" call from DecayProcedures' review surface — see
// design.md's Data Flow: "mem_review surfaces review_after procedures").
// Never hard-deletes; idempotent (retiring an already-retired procedure is a
// safe no-op that still returns the current row).
func (s *Store) RetireProcedure(syncID string) (Procedure, error) {
	if strings.TrimSpace(syncID) == "" {
		return Procedure{}, fmt.Errorf("RetireProcedure: syncID is required")
	}

	var result Procedure
	if err := s.withTx(func(tx *sql.Tx) error {
		row := tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		p, err := scanProcedureRow(row.Scan)
		if err == sql.ErrNoRows {
			return fmt.Errorf("RetireProcedure: procedure %q not found", syncID)
		}
		if err != nil {
			return fmt.Errorf("RetireProcedure: %w", err)
		}

		var retiredAt *string
		if p.RetiredAt != nil {
			retiredAt = p.RetiredAt
		} else {
			now := nowTimestamp()
			retiredAt = &now
		}

		if _, execErr := tx.Exec(`
			UPDATE procedures
			SET state      = ?,
			    retired_at = COALESCE(?, retired_at),
			    updated_at = datetime('now')
			WHERE sync_id = ?
		`, ProcedureStateRetired, retiredAt, syncID); execErr != nil {
			return fmt.Errorf("RetireProcedure: update: %w", execErr)
		}

		row = tx.QueryRow(`SELECT `+procedureSelectColumns+` FROM procedures WHERE sync_id = ?`, syncID)
		result, err = scanProcedureRow(row.Scan)
		return err
	}); err != nil {
		return Procedure{}, err
	}

	return result, nil
}

// DecayProcedures scans `trusted` procedures whose review_after deadline has
// passed (i.e., not reused since — the spaced-repetition "unused" signal)
// and decays each one's confidence by decayStepConfidence. A procedure whose
// decayed confidence drops to/below procedural.confidence_floor auto-retires
// (mirrors Contradict's floor-retire); otherwise it stays trusted with
// review_after pushed forward another procedural.review_after_days, so the
// same procedure is not immediately re-flagged on the next call. Returns the
// procedures that were flagged/decayed by this pass.
func (s *Store) DecayProcedures() ([]Procedure, error) {
	floor := s.proceduralConfidenceFloor()
	reviewDays := s.proceduralReviewAfterDays()

	rows, err := s.db.Query(`
		SELECT `+procedureSelectColumns+`
		FROM procedures
		WHERE state = ?
		  AND review_after IS NOT NULL
		  AND datetime(review_after) <= datetime('now')
	`, ProcedureStateTrusted)
	if err != nil {
		return nil, fmt.Errorf("DecayProcedures: query: %w", err)
	}
	var due []Procedure
	for rows.Next() {
		p, err := scanProcedureRow(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("DecayProcedures: scan: %w", err)
		}
		due = append(due, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("DecayProcedures: rows error: %w", err)
	}
	rows.Close()

	decayed := make([]Procedure, 0, len(due))
	for _, p := range due {
		newConfidence := clampConfidence(p.Confidence - decayStepConfidence)
		newState := p.State
		var retiredAt *string
		var reviewAfter *string
		if newConfidence <= floor {
			newState = ProcedureStateRetired
			now := nowTimestamp()
			retiredAt = &now
		} else {
			ra := reviewAfterTimestamp(reviewDays)
			reviewAfter = &ra
		}

		if err := s.withTx(func(tx *sql.Tx) error {
			_, execErr := tx.Exec(`
				UPDATE procedures
				SET confidence   = ?,
				    state        = ?,
				    retired_at   = COALESCE(?, retired_at),
				    review_after = COALESCE(?, review_after),
				    updated_at   = datetime('now')
				WHERE sync_id = ?
			`, newConfidence, newState, retiredAt, reviewAfter, p.SyncID)
			return execErr
		}); err != nil {
			return nil, fmt.Errorf("DecayProcedures: update %q: %w", p.SyncID, err)
		}

		p.Confidence = newConfidence
		p.State = newState
		if retiredAt != nil {
			p.RetiredAt = retiredAt
		}
		if reviewAfter != nil {
			p.ReviewAfter = reviewAfter
		}
		decayed = append(decayed, p)
	}

	return decayed, nil
}

// ─── Small helpers ────────────────────────────────────────────────────────────

// clampConfidence keeps a confidence score within [0.0, 1.0].
func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// nowTimestamp formats the current instant in the same
// "YYYY-MM-DD HH:MM:SS" (UTC) shape store.go's other review_after writers use
// (see AddObservation's review_after assignment).
func nowTimestamp() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// reviewAfterTimestamp returns a spaced-repetition deadline `days` days from
// now, in the same timestamp shape nowTimestamp uses.
func reviewAfterTimestamp(days int) string {
	return time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02 15:04:05")
}
