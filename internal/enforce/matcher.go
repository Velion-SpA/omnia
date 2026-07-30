// Package enforce implements Omnia's memory-enforcement-gate capability
// (design obs #1592's deferred compiler runtime): a pre-completion gate an
// agent calls before an edit/task is considered done, which mechanically
// selects the `trusted` procedures whose trigger/scope apply to the current
// change and runs their postconditions, returning a pass/flag/block verdict.
//
// Mechanical, not LLM-judged (locked product decision, proposal.md
// "memory-enforcement-gate"): nothing in this package ever calls an LLM.
// Every decision is derived from already machine-checkable state — trusted
// procedure rows (internal/store/procedures.go) and command exit codes
// (runner.go, ADR-6).
package enforce

import (
	"sort"
	"strings"

	"github.com/velion/omnia/internal/store"
)

// defaultMatchLimit mirrors ListProcedures/SearchProcedures' own "<= 0 means
// use the package default" convention.
const defaultMatchLimit = 20

// ProcedureSource is the narrow store surface the matcher needs — just
// ListProcedures and SearchProcedures — so tests (and future callers) can
// supply a fake without depending on a real sqlite-backed *store.Store.
// *store.Store already satisfies this interface.
type ProcedureSource interface {
	ListProcedures(store.ListProceduresOptions) ([]store.Procedure, error)
	SearchProcedures(query, polarity, state string, limit int) ([]store.Procedure, error)
}

// MatchTrustedProcedures selects the `trusted` procedures (REQ-411:
// candidate/retired procedures MUST NEVER gate a completion) whose
// trigger/steps_summary overlaps the touched file paths, scoped to project.
//
// Design (design.md, Capability 2, "Algorithm — change→procedure matching"):
// the candidate set is `ListProcedures{State:trusted, Project}` — this is
// what enforces project scoping, since SearchProcedures itself has no
// project filter — narrowed via SearchProcedures' FTS5 query
// (trigger+steps_summary), one query per touched file path so a single long
// AND-of-every-fragment query can never spuriously suppress a real match. A
// procedure only gates when it appears in BOTH result sets: project+trusted
// (from ListProcedures) AND FTS-matched for at least one touched file (from
// SearchProcedures, already filtered to state=trusted).
//
// Fail-safe by design: no touched files (nothing to scope a match against)
// or no trusted procedures in the project return an empty, error-free
// result — never an error, never a match. limit <= 0 uses defaultMatchLimit.
func MatchTrustedProcedures(src ProcedureSource, project string, filesTouched []string, limit int) ([]store.Procedure, error) {
	if len(filesTouched) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultMatchLimit
	}

	candidates, err := src.ListProcedures(store.ListProceduresOptions{
		Project: project,
		State:   store.ProcedureStateTrusted,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	inProjectScope := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		inProjectScope[c.SyncID] = struct{}{}
	}

	matchedByID := make(map[string]store.Procedure)
	for _, f := range filesTouched {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		hits, err := src.SearchProcedures(f, "", store.ProcedureStateTrusted, limit)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if _, ok := inProjectScope[h.SyncID]; ok {
				matchedByID[h.SyncID] = h
			}
		}
	}
	if len(matchedByID) == 0 {
		return nil, nil
	}

	matched := make([]store.Procedure, 0, len(matchedByID))
	for _, p := range matchedByID {
		matched = append(matched, p)
	}
	// Deterministic ordering (map iteration is not).
	sort.Slice(matched, func(i, j int) bool { return matched[i].SyncID < matched[j].SyncID })
	return matched, nil
}
