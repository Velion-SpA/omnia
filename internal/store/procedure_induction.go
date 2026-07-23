package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2 ───────
//
// InduceProject is the OFFLINE batch counterpart to mem_save's online
// candidate induction (internal/mcp's induceProcedureFromObservation): it
// walks historical outcome-tagged bugfix-family observations for a project,
// clusters them by (error_signature, outcome) — polarity is ALWAYS derived
// from outcome (design decision #2), so two clusters sharing a signature
// but differing in outcome must never be merged into one
// ambiguous-polarity procedure — and induces one candidate procedure per
// cluster, riding a bounded worker pool shaped after ScanProject's own
// semantic pass (relations.go).
//
// This intentionally does NOT extend ScanProject itself: clustering by
// error_signature and inducing procedures is a different unit of work than
// relation-candidate detection, and folding it into ScanProject's already
// large branch surface (SourceFTS / SourceAnchor / Semantic) would only
// entangle two orthogonal features. InduceProject reuses ScanProject's
// *shape* (bounded worker pool, dry-run-by-default, capped inserts) as a
// sibling method instead.

// ErrProcedureInducerRequired is returned by InduceProject when opts.Apply
// is true but opts.Inducer is nil — mirrors ErrSemanticRunnerRequired's role
// for ScanProject's Semantic mode.
var ErrProcedureInducerRequired = errors.New("InduceProject: --apply requires a non-nil Inducer")

// InducedProcedureResult mirrors llm.InducedProcedure's shape but lives in
// this package to avoid a store→llm import cycle (the same pattern
// SemanticVerdict/ObservationSnippet already use for the semantic-conflict
// scan in runner.go/relations.go).
type InducedProcedureResult struct {
	Trigger           string
	Steps             []ProcedureStep
	ExpectedOutcome   string
	PostconditionKind string
	PostconditionExpr string
	Model             string
}

// InduceFunc is the store-side seam for a single cluster's induction call.
// cmd/omnia's production wiring closes over llm.SafeInduce plus a resolved
// llm.ProcedureInducer, so a missing/erroring LLM CLI degrades to
// DeterministicInduce rather than failing the whole batch — mirroring
// SafeInduce's own graceful-degradation contract at the online mem_save
// boundary. A plain func type (rather than an interface) keeps this a
// single-method seam tests can substitute directly with a closure, no mock
// struct boilerplate required.
type InduceFunc func(ctx context.Context, trigger, trajectory string) InducedProcedureResult

// InduceOptions controls an InduceProject call.
type InduceOptions struct {
	// Project is required — scopes the observation walk, mirroring
	// ScanOptions.Project.
	Project string
	// Apply controls whether induced procedures are actually written. When
	// false (dry-run, default), InduceProject reports cluster counts only —
	// no Inducer call is made and no procedures.UpsertProcedure call happens
	// (spec: "Without --apply it MUST report counts only").
	Apply bool
	// Concurrency is the worker pool size for Inducer calls. Default 5 if 0.
	Concurrency int
	// TimeoutPerCall bounds each Inducer call's context. Default 60s if
	// zero — reserved for a real shell-out Inducer; a fake test closure may
	// ignore it entirely.
	TimeoutPerCall time.Duration
	// MaxClusters caps the number of clusters processed in a single Apply
	// run. Default 100 if 0 or negative.
	MaxClusters int
	// Inducer performs the actual induction call for one cluster. Required
	// when Apply is true; ignored (never called) in dry-run mode.
	Inducer InduceFunc
}

// InduceResult holds the output of an InduceProject call.
type InduceResult struct {
	Project             string `json:"project"`
	ObservationsScanned int    `json:"observations_scanned"`
	ClustersFound       int    `json:"clusters_found"`
	ProceduresInduced   int    `json:"procedures_induced"`
	Errors              int    `json:"errors"`
	Capped              bool   `json:"capped"`
	DryRun              bool   `json:"dry_run"`
}

// procedureCluster groups outcome-tagged bugfix-family observations that
// share the SAME (error_signature, outcome) pair.
type procedureCluster struct {
	signature  string
	outcome    string
	syncIDs    []string
	trigger    string
	trajectory string
}

// InduceProject scans opts.Project's outcome-tagged bugfix-family
// observations (the same vocabulary isBugfixFamilyType / normalizeOutcome
// already enforce at save time), clusters them by (error_signature,
// outcome), and — when opts.Apply is true — induces and upserts one
// candidate procedure per cluster via a bounded worker pool.
//
// error_signature is read directly from the observations.error_signature
// column rather than being recomputed: AddObservation already normalizes
// and persists it there for every bugfix-family save (signature.go), so
// re-deriving it here would risk silently drifting from the save-time
// value it needs to match.
func (s *Store) InduceProject(opts InduceOptions) (InduceResult, error) {
	if opts.Apply && opts.Inducer == nil {
		return InduceResult{}, ErrProcedureInducerRequired
	}

	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	maxClusters := opts.MaxClusters
	if maxClusters <= 0 {
		maxClusters = 100
	}
	timeoutPerCall := opts.TimeoutPerCall
	if timeoutPerCall <= 0 {
		timeoutPerCall = 60 * time.Second
	}

	result := InduceResult{Project: opts.Project, DryRun: !opts.Apply}

	rows, err := s.db.Query(`
		SELECT ifnull(sync_id,''), title, ifnull(content,''), error_signature, outcome
		FROM observations
		WHERE ifnull(project,'') = ?
		  AND deleted_at IS NULL
		  AND outcome IN (?, ?)
		  AND error_signature IS NOT NULL
		  AND error_signature != ''
		ORDER BY datetime(created_at) ASC
	`, opts.Project, OutcomeWorked, OutcomeDidNotWork)
	if err != nil {
		return result, fmt.Errorf("InduceProject: query observations: %w", err)
	}

	clusters := map[string]*procedureCluster{}
	var order []string
	for rows.Next() {
		var syncID, title, content, sig, outcome string
		if err := rows.Scan(&syncID, &title, &content, &sig, &outcome); err != nil {
			rows.Close()
			return result, fmt.Errorf("InduceProject: scan: %w", err)
		}
		result.ObservationsScanned++

		key := sig + "\x00" + outcome
		c, ok := clusters[key]
		if !ok {
			c = &procedureCluster{signature: sig, outcome: outcome, trigger: title}
			clusters[key] = c
			order = append(order, key)
		}
		if syncID != "" {
			c.syncIDs = append(c.syncIDs, syncID)
		}
		if strings.TrimSpace(content) != "" {
			if c.trajectory != "" {
				c.trajectory += "\n---\n"
			}
			c.trajectory += content
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("InduceProject: rows error: %w", err)
	}
	rows.Close()

	result.ClustersFound = len(order)

	if !opts.Apply {
		return result, nil
	}

	if len(order) > maxClusters {
		order = order[:maxClusters]
		result.Capped = true
	}

	jobCh := make(chan string, len(order))
	for _, key := range order {
		jobCh <- key
	}
	close(jobCh)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobCh {
				c := clusters[key]
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[store] InduceProject: Inducer panic cluster signature=%q outcome=%q: %v", c.signature, c.outcome, r)
							mu.Lock()
							result.Errors++
							mu.Unlock()
						}
					}()

					callCtx, cancel := context.WithTimeout(context.Background(), timeoutPerCall)
					defer cancel()

					induced := opts.Inducer(callCtx, c.trigger, c.trajectory)
					if len(induced.Steps) == 0 {
						mu.Lock()
						result.Errors++
						mu.Unlock()
						return
					}

					polarity := ProcedurePolarityPlaybook
					if c.outcome == OutcomeDidNotWork {
						polarity = ProcedurePolarityAntiPlaybook
					}
					postconditionKind := induced.PostconditionKind
					if !isValidPostconditionKind(postconditionKind) {
						postconditionKind = PostconditionCustom
					}

					if _, err := s.UpsertProcedure(Procedure{
						Project:           opts.Project,
						Scope:             "project",
						Polarity:          polarity,
						Trigger:           induced.Trigger,
						Steps:             induced.Steps,
						ExpectedOutcome:   induced.ExpectedOutcome,
						PostconditionKind: postconditionKind,
						PostconditionExpr: induced.PostconditionExpr,
						Confidence:        0.5,
						State:             ProcedureStateCandidate,
						SourceObsSyncIDs:  c.syncIDs,
						InducedByActor:    "engram",
						InducedByKind:     "system",
						InducedByModel:    induced.Model,
					}); err != nil {
						log.Printf("[store] InduceProject: UpsertProcedure cluster signature=%q outcome=%q: %v", c.signature, c.outcome, err)
						mu.Lock()
						result.Errors++
						mu.Unlock()
						return
					}

					mu.Lock()
					result.ProceduresInduced++
					mu.Unlock()
				}()
			}
		}()
	}

	wg.Wait()

	return result, nil
}
