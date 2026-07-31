package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/velion/omnia/internal/diagnostic"
	"github.com/velion/omnia/internal/store"
)

func cmdDoctor(cfg store.Config) {
	if len(os.Args) > 2 && os.Args[2] == "repair" {
		cmdDoctorRepair(cfg)
		return
	}
	jsonOut := false
	project := ""
	check := ""
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--json":
			jsonOut = true
		case "--project":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "error: --project requires a value")
				exitFunc(1)
				return
			}
			project = os.Args[i+1]
			i++
		case "--check":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "error: --check requires a value")
				exitFunc(1)
				return
			}
			check = os.Args[i+1]
			i++
		case "--config":
			// finding #3: already consumed by main()'s prologue (before
			// dispatch) to build cfg's shared config-derived fields.
			// doctor's own stricter-than-search allowlist parser must
			// recognize and skip it — like every other value-taking flag
			// here — instead of rejecting it as an unknown argument.
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "error: --config requires a value")
				exitFunc(1)
				return
			}
			i++
		case "--help", "-h", "help":
			printDoctorUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown doctor argument %q\n", os.Args[i])
			printDoctorUsage()
			exitFunc(1)
			return
		}
	}

	project, _ = store.NormalizeProject(project)
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	report, err := runDiagnostics(context.Background(), s, strings.TrimSpace(project), strings.TrimSpace(check))
	if err != nil {
		report = diagnostic.ErrorReport(project, err)
		if jsonOut {
			writeDoctorJSON(report, cfg.EncryptionEnabled)
		} else {
			fmt.Fprintf(os.Stderr, "omnia doctor failed: %s\n", err)
		}
		if errors.Is(err, diagnostic.ErrInvalidCheck) {
			exitFunc(1)
		}
		return
	}

	if jsonOut {
		writeDoctorJSON(report, cfg.EncryptionEnabled)
		return
	}
	renderDoctorText(report, cfg.EncryptionEnabled)
}

// encryptionThreatModelText is v0.4 memory-at-rest-security's REQ-433
// fixed statement: it must be discoverable in `omnia doctor`/status output
// whenever encryption is active, and must state BOTH what it protects and
// what it explicitly does NOT.
const encryptionThreatModelText = "Encryption at rest protects against disk theft or a lost laptop " +
	"while the process is NOT running. It does NOT protect against a live-process memory dump " +
	"or an attacker who already has the unlocked OS keychain."

func printDoctorUsage() {
	fmt.Fprintln(os.Stdout, "usage: omnia doctor [--json] [--project PROJECT] [--check CODE]")
	fmt.Fprintln(os.Stdout, "       omnia doctor repair --project PROJECT --check CODE (--plan|--dry-run|--apply)")
	fmt.Fprintln(os.Stdout, "checks: "+strings.Join(diagnostic.RegisteredCodes(), ", "))
}

func cmdDoctorRepair(cfg store.Config) {
	project := ""
	check := ""
	mode := diagnostic.RepairMode("")
	modeCount := 0
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--project":
			if i+1 >= len(os.Args) {
				failDoctorRepair("--project requires a value")
				return
			}
			project = os.Args[i+1]
			i++
		case "--check":
			if i+1 >= len(os.Args) {
				failDoctorRepair("--check requires a value")
				return
			}
			check = os.Args[i+1]
			i++
		case "--plan":
			mode = diagnostic.RepairModePlan
			modeCount++
		case "--dry-run":
			mode = diagnostic.RepairModeDryRun
			modeCount++
		case "--apply":
			mode = diagnostic.RepairModeApply
			modeCount++
		case "--config":
			// finding #3: see the identical case in cmdDoctor above — already
			// consumed by main()'s prologue; skip it here instead of
			// rejecting it as unknown.
			if i+1 >= len(os.Args) {
				failDoctorRepair("--config requires a value")
				return
			}
			i++
		case "--help", "-h", "help":
			printDoctorUsage()
			return
		default:
			failDoctorRepair(fmt.Sprintf("unknown doctor repair argument %q", os.Args[i]))
			return
		}
	}

	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	check = strings.TrimSpace(check)
	if project == "" {
		failDoctorRepair("--project is required")
		return
	}
	if check == "" {
		failDoctorRepair("--check is required")
		return
	}
	if modeCount != 1 {
		failDoctorRepair("exactly one of --plan, --dry-run, or --apply is required")
		return
	}
	if !isSupportedDoctorRepairCheck(check) {
		failDoctorRepair("unsupported repair check " + check)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	ctx := context.Background()
	report, err := runDiagnostics(ctx, s, project, check)
	if err != nil {
		failDoctorRepair(err.Error())
		return
	}
	plan, err := diagnostic.BuildRepairPlan(ctx, diagnostic.Scope{Store: s, Project: project}, report, check, mode)
	if err != nil {
		failDoctorRepair(err.Error())
		return
	}
	actions := make([]store.SessionProjectReclassification, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		actions = append(actions, store.SessionProjectReclassification{SessionID: action.SessionID, FromProject: action.FromProject, ToProject: action.ToProject})
	}
	if mode == diagnostic.RepairModeApply && len(actions) > 0 {
		counts, err := s.EstimateSessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Counts.SessionsPlanned = counts.Sessions
		plan.Counts.ObservationsPlanned = counts.Observations
		plan.Counts.PromptsPlanned = counts.Prompts
		result, err := s.ApplySessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Status = "applied"
		plan.BackupPath = result.BackupPath
		plan.Counts.SessionsApplied = result.Counts.Sessions
		plan.Counts.ObservationsApplied = result.Counts.Observations
		plan.Counts.PromptsApplied = result.Counts.Prompts
	} else {
		counts, err := s.EstimateSessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Counts.SessionsPlanned = counts.Sessions
		plan.Counts.ObservationsPlanned = counts.Observations
		plan.Counts.PromptsPlanned = counts.Prompts
	}
	writeDoctorRepairJSON(plan)
}

func isSupportedDoctorRepairCheck(check string) bool {
	switch check {
	case diagnostic.CheckSessionProjectDirectoryMismatch, diagnostic.CheckManualSessionNameProjectMismatch:
		return true
	default:
		return false
	}
}

func failDoctorRepair(message string) {
	fmt.Fprintln(os.Stderr, "omnia doctor repair failed: "+message)
	printDoctorUsage()
	exitFunc(1)
}

func writeDoctorRepairJSON(plan diagnostic.RepairPlan) {
	out, err := jsonMarshalIndent(plan, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))
}

// writeDoctorJSON marshals report as-is when encryptionEnabled is false
// (byte-for-byte pre-v0.4 JSON shape, spec REQ-430's discipline extended to
// this output too). When encryption is active, an additive
// "encryption_threat_model" field is appended (spec REQ-433) — never
// changing the shape of the existing report fields.
func writeDoctorJSON(report diagnostic.Report, encryptionEnabled bool) {
	if !encryptionEnabled {
		out, err := jsonMarshalIndent(report, "", "  ")
		if err != nil {
			fatal(err)
			return
		}
		fmt.Println(string(out))
		return
	}
	wrapped := struct {
		diagnostic.Report
		EncryptionThreatModel string `json:"encryption_threat_model"`
	}{report, encryptionThreatModelText}
	out, err := jsonMarshalIndent(wrapped, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))
}

func renderDoctorText(report diagnostic.Report, encryptionEnabled bool) {
	fmt.Printf("Omnia Doctor: %s\n", report.Status)
	if report.Project != "" {
		fmt.Printf("Project: %s\n", report.Project)
	}
	fmt.Printf("Checks: %d ok=%d warnings=%d blocked=%d errors=%d\n\n", report.Summary.Total, report.Summary.OK, report.Summary.Warnings, report.Summary.Blocked, report.Summary.Errors)
	for _, check := range report.Checks {
		fmt.Printf("[%s] %s — %s\n", check.Result, check.CheckID, check.Message)
		if check.Why != "" {
			fmt.Printf("  why: %s\n", check.Why)
		}
		if check.SafeNextStep != "" {
			fmt.Printf("  next: %s\n", check.SafeNextStep)
		}
		for _, finding := range check.Findings {
			fmt.Printf("  - %s: %s\n", finding.ReasonCode, finding.Message)
			if len(finding.Evidence) > 0 {
				fmt.Printf("    evidence: %s\n", string(finding.Evidence))
			}
		}
	}
	// spec REQ-433: the explicit threat model MUST be discoverable in
	// `omnia doctor` output whenever encryption is active. Additive-only:
	// omitted entirely when encryption is disabled (the default), so the
	// disabled path stays byte-for-byte pre-v0.4 output.
	if encryptionEnabled {
		fmt.Printf("\nEncryption: %s\n", encryptionThreatModelText)
	}
}
