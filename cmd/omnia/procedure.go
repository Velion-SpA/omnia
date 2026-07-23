package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/store"
)

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2 ───────
//
// Phase 6: offline batch induction (`omnia procedure-induct`) + Phase 6.5
// curation subcommands (`omnia procedure list/inspect/retire`). Mirrors
// conflicts.go's dispatcher + flag-parsing style (cmdConflicts,
// cmdConflictsScan).

// ─── resolveProcedureInducer / induceFuncFromInducer ─────────────────────────

// resolveProcedureInducer reads OMNIA_AGENT_CLI and resolves an
// llm.ProcedureInducer via the same llm.NewRunner factory
// resolveAgentRunner uses for the semantic-conflict scan — *llm.ClaudeRunner
// and *llm.OpenCodeRunner both satisfy ProcedureInducer (their Induce
// method) in addition to AgentRunner (Compare), so no separate CLI
// selection mechanism is needed. Returns nil (never an error) when no CLI
// is configured or the name is unrecognized: induceFuncFromInducer's
// closure degrades to llm.SafeInduce's deterministic fallback in that case,
// exactly like the online mem_save wiring's own graceful degradation —
// `--apply` never fails outright over a missing LLM CLI.
func resolveProcedureInducer() llm.ProcedureInducer {
	name := envx.Get("OMNIA_AGENT_CLI")
	if name == "" {
		return nil
	}
	runner, err := llm.NewRunner(name)
	if err != nil {
		return nil
	}
	inducer, ok := runner.(llm.ProcedureInducer)
	if !ok {
		return nil
	}
	return inducer
}

// induceFuncFromInducer adapts an llm.ProcedureInducer into the
// store.InduceFunc seam InduceProject calls per cluster, converting
// llm.InducedProcedure into store.InducedProcedureResult (mirrors
// llmRunnerAdapter's role for store.SemanticRunner in llm.go — a small,
// package-local adapter that avoids a store→llm import cycle without
// needing store to know about llm.ProcedureInducer at all).
func induceFuncFromInducer(inducer llm.ProcedureInducer) store.InduceFunc {
	return func(ctx context.Context, trigger, trajectory string) store.InducedProcedureResult {
		prompt := llm.BuildInducePrompt(trigger, trajectory)
		induced := llm.SafeInduce(ctx, inducer, trigger, trajectory, prompt)

		steps := make([]store.ProcedureStep, 0, len(induced.Steps))
		for _, st := range induced.Steps {
			steps = append(steps, store.ProcedureStep{Order: st.Order, Template: st.Template, Slots: st.Slots})
		}
		return store.InducedProcedureResult{
			Trigger:           induced.Trigger,
			Steps:             steps,
			ExpectedOutcome:   induced.ExpectedOutcome,
			PostconditionKind: induced.PostconditionKind,
			PostconditionExpr: induced.PostconditionExpr,
			Model:             induced.Model,
		}
	}
}

// ─── procedure-induct ─────────────────────────────────────────────────────────

// cmdProcedureInduct is `omnia procedure-induct [--project][--apply]`: the
// offline batch counterpart to mem_save's online candidate induction.
// Mirrors cmdConflictsScan's flag-parsing style (dry-run default, --apply
// opts in to writing).
func cmdProcedureInduct(cfg store.Config) {
	args := os.Args[2:]

	var projectFlag string
	apply := false
	concurrency := 5
	maxClusters := 100

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--apply":
			apply = true
		case "--dry-run":
			apply = false
		case "--concurrency":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					concurrency = n
				}
				i++
			}
		case "--max-clusters":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					maxClusters = n
				}
				i++
			}
		}
	}

	proj := resolveConflictsProject(projectFlag)

	opts := store.InduceOptions{
		Project:     proj,
		Apply:       apply,
		Concurrency: concurrency,
		MaxClusters: maxClusters,
	}

	if apply {
		opts.Inducer = induceFuncFromInducer(resolveProcedureInducer())
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	result, err := s.InduceProject(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Procedure Induct (project: %s)\n", proj)
	fmt.Printf("  observations_scanned: %d\n", result.ObservationsScanned)
	fmt.Printf("  clusters_found:       %d\n", result.ClustersFound)
	fmt.Printf("  procedures_induced:   %d\n", result.ProceduresInduced)
	fmt.Printf("  errors:               %d\n", result.Errors)
	fmt.Printf("  dry_run:              %v\n", result.DryRun)

	if result.Capped {
		fmt.Printf("WARNING: max-clusters cap of %d reached — stopped early. Re-run to continue.\n", maxClusters)
	}
}

// ─── procedure (list/inspect/retire) ──────────────────────────────────────────

// cmdProcedure is the top-level dispatcher for `omnia procedure <subcommand>`.
// Mirrors cmdConflicts' switch-on-os.Args[2] pattern.
func cmdProcedure(cfg store.Config) {
	if len(os.Args) < 3 {
		printProcedureUsage()
		exitFunc(1)
		return
	}
	switch os.Args[2] {
	case "list":
		cmdProcedureList(cfg)
	case "inspect":
		cmdProcedureInspect(cfg)
	case "retire":
		cmdProcedureRetire(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown procedure subcommand: %s\n", os.Args[2])
		printProcedureUsage()
		exitFunc(1)
	}
}

func printProcedureUsage() {
	fmt.Fprintln(os.Stderr, "usage: omnia procedure <subcommand> [options]")
	fmt.Fprintln(os.Stderr, "subcommands: list, inspect, retire")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  list     [--project P] [--polarity playbook|anti_playbook] [--state candidate|trusted|retired] [--limit N]")
	fmt.Fprintln(os.Stderr, "  inspect  <sync_id>")
	fmt.Fprintln(os.Stderr, "  retire   <sync_id>")
}

func cmdProcedureList(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag, polarityFlag, stateFlag string
	limit := 50

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--polarity":
			if i+1 < len(args) {
				polarityFlag = args[i+1]
				i++
			}
		case "--state":
			if i+1 < len(args) {
				stateFlag = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	procedures, err := s.ListProcedures(store.ListProceduresOptions{
		Project:  projectFlag,
		Polarity: polarityFlag,
		State:    stateFlag,
		Limit:    limit,
	})
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Procedures (project: %s)\n", projectFlag)
	fmt.Printf("  Showing: %d\n", len(procedures))
	if len(procedures) == 0 {
		fmt.Println("  No procedures found.")
		return
	}
	fmt.Println()
	for _, p := range procedures {
		fmt.Printf("  sync_id:    %s\n", p.SyncID)
		fmt.Printf("  polarity:   %s\n", p.Polarity)
		fmt.Printf("  state:      %s\n", p.State)
		fmt.Printf("  trigger:    %s\n", truncate(p.Trigger, 80))
		fmt.Printf("  confidence: %.2f\n", p.Confidence)
		fmt.Println()
	}
}

func cmdProcedureInspect(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia procedure inspect <sync_id>")
		exitFunc(1)
		return
	}
	syncID := os.Args[3]

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	p, err := s.GetProcedure(syncID)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Procedure Detail\n")
	fmt.Printf("  sync_id:            %s\n", p.SyncID)
	fmt.Printf("  polarity:           %s\n", p.Polarity)
	fmt.Printf("  state:              %s\n", p.State)
	fmt.Printf("  trigger:            %s\n", p.Trigger)
	fmt.Printf("  confidence:         %.2f\n", p.Confidence)
	fmt.Printf("  reuse_confirmed:    %d\n", p.ReuseConfirmed)
	fmt.Printf("  contradicted_count: %d\n", p.ContradictedCount)
	fmt.Printf("  postcondition_kind: %s\n", p.PostconditionKind)
	fmt.Println()
	fmt.Println("  steps:")
	for _, st := range p.Steps {
		fmt.Printf("    %d. %s\n", st.Order, st.Template)
	}
}

func cmdProcedureRetire(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia procedure retire <sync_id>")
		exitFunc(1)
		return
	}
	syncID := os.Args[3]

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	p, err := s.RetireProcedure(syncID)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Procedure retired: %s (state=%s)\n", p.SyncID, p.State)
}
