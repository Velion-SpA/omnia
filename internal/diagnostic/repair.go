package diagnostic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	RepairModePlan   RepairMode = "plan"
	RepairModeDryRun RepairMode = "dry_run"
	RepairModeApply  RepairMode = "apply"
)

type RepairMode string

type ProjectReclassifyAction struct {
	SessionID      string `json:"session_id"`
	FromProject    string `json:"from_project"`
	ToProject      string `json:"to_project"`
	ReasonCode     string `json:"reason_code"`
	EvidenceSource string `json:"evidence_source,omitempty"`
	EvidencePath   string `json:"evidence_path,omitempty"`
}

type RepairSkip struct {
	SessionID  string `json:"session_id,omitempty"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

// SyncMutationDeleteAction is one pending sync_mutations row planned for
// deletion because its payload fails store.ValidateSyncMutationPayload —
// the repair action for CheckSyncMutationRequiredFields. Delete only: there
// is no PayloadFix field, deliberately — repairing this check never mutates
// payload content, only removes the row that cannot be trusted.
//
// Carries enough for a human to recognize the exact row before it disappears
// (seq/target_key/entity/entity_key/op/occurred_at), plus the validation
// failure that earned it a spot on this list. Two actions sharing the same
// EntityKey under different TargetKey are the SAME logical mutation,
// duplicated by multi-cloud fan-out (see ListPendingProjectMutations) — the
// plan is sorted by EntityKey then TargetKey so that correspondence is
// visible directly in the printed output, not just inferable from raw seqs.
type SyncMutationDeleteAction struct {
	Seq           int64    `json:"seq"`
	TargetKey     string   `json:"target_key"`
	Entity        string   `json:"entity"`
	EntityKey     string   `json:"entity_key,omitempty"`
	Op            string   `json:"op"`
	OccurredAt    string   `json:"occurred_at"`
	ReasonCode    string   `json:"reason_code"`
	Message       string   `json:"message"`
	MissingFields []string `json:"missing_fields,omitempty"`
}

type RepairCounts struct {
	SessionsPlanned      int64 `json:"sessions_planned"`
	ObservationsPlanned  int64 `json:"observations_planned"`
	PromptsPlanned       int64 `json:"prompts_planned"`
	SessionsApplied      int64 `json:"sessions_applied"`
	ObservationsApplied  int64 `json:"observations_applied"`
	PromptsApplied       int64 `json:"prompts_applied"`
	SyncMutationsPlanned int64 `json:"sync_mutations_planned,omitempty"`
	SyncMutationsApplied int64 `json:"sync_mutations_applied,omitempty"`
}

type RepairPlan struct {
	Project               string                     `json:"project"`
	Check                 string                     `json:"check"`
	Mode                  RepairMode                 `json:"mode"`
	Status                string                     `json:"status"`
	Actions               []ProjectReclassifyAction  `json:"actions"`
	SyncMutationDeletions []SyncMutationDeleteAction `json:"sync_mutation_deletions,omitempty"`
	Skipped               []RepairSkip               `json:"skipped,omitempty"`
	Counts                RepairCounts               `json:"counts"`
	BackupPath            string                     `json:"backup_path,omitempty"`
}

func BuildRepairPlan(ctx context.Context, scope Scope, report Report, check string, mode RepairMode) (RepairPlan, error) {
	_ = ctx
	project := normalizeProjectName(scope.Project)
	check = strings.TrimSpace(check)
	plan := RepairPlan{Project: project, Check: check, Mode: mode, Status: "planned", Actions: []ProjectReclassifyAction{}}
	switch mode {
	case RepairModePlan:
		plan.Status = "planned"
	case RepairModeDryRun:
		plan.Status = "dry_run"
	case RepairModeApply:
		plan.Status = "planned"
	default:
		return RepairPlan{}, fmt.Errorf("unsupported repair mode %q", mode)
	}

	switch check {
	case CheckSessionProjectDirectoryMismatch:
		planDirectoryMismatchRepair(&plan, report)
	case CheckManualSessionNameProjectMismatch:
		if err := planManualSessionRepair(&plan, scope); err != nil {
			return RepairPlan{}, err
		}
	case CheckSyncMutationRequiredFields:
		planSyncMutationRequiredFieldsRepair(&plan, report)
	default:
		return RepairPlan{}, fmt.Errorf("unsupported repair check %q", check)
	}

	dedupeAndSortRepairPlan(&plan)
	if len(plan.Actions) == 0 && len(plan.SyncMutationDeletions) == 0 {
		plan.Status = "noop"
	}
	return plan, nil
}

// planSyncMutationRequiredFieldsRepair reads the already-computed findings
// from report (built by SyncMutationRequiredFieldsCheck.Run, which itself
// calls the fan-out-aware Store.ListPendingProjectMutations — see that
// check's doc comment) rather than re-querying the store, the same pattern
// planDirectoryMismatchRepair already uses for CheckSessionProjectDirectoryMismatch.
// One finding maps to exactly one queue row: multi-cloud fan-out means the
// SAME logical mutation can appear here twice under different TargetKey
// values (e.g. "cloud" and "work:umbral"), and both must be planned for
// deletion independently — each is its own row in sync_mutations with its
// own seq.
func planSyncMutationRequiredFieldsRepair(plan *RepairPlan, report Report) {
	for _, check := range report.Checks {
		for _, finding := range check.Findings {
			var ev struct {
				Seq           int64    `json:"seq"`
				TargetKey     string   `json:"target_key"`
				Project       string   `json:"project"`
				Entity        string   `json:"entity"`
				Op            string   `json:"op"`
				EntityKey     string   `json:"entity_key"`
				OccurredAt    string   `json:"occurred_at"`
				MissingFields []string `json:"missing_fields"`
			}
			if err := json.Unmarshal(finding.Evidence, &ev); err != nil {
				plan.Skipped = append(plan.Skipped, RepairSkip{ReasonCode: "invalid_doctor_evidence", Message: err.Error()})
				continue
			}
			if ev.Seq == 0 || strings.TrimSpace(ev.TargetKey) == "" {
				plan.Skipped = append(plan.Skipped, RepairSkip{ReasonCode: "invalid_sync_mutation_evidence", Message: "doctor evidence does not describe a deletable sync_mutations row"})
				continue
			}
			plan.SyncMutationDeletions = append(plan.SyncMutationDeletions, SyncMutationDeleteAction{
				Seq:           ev.Seq,
				TargetKey:     ev.TargetKey,
				Entity:        ev.Entity,
				EntityKey:     ev.EntityKey,
				Op:            ev.Op,
				OccurredAt:    ev.OccurredAt,
				ReasonCode:    finding.ReasonCode,
				Message:       finding.Message,
				MissingFields: ev.MissingFields,
			})
		}
	}
}

func planDirectoryMismatchRepair(plan *RepairPlan, report Report) {
	for _, check := range report.Checks {
		for _, finding := range check.Findings {
			var ev struct {
				SessionID              string `json:"session_id"`
				SessionProject         string `json:"session_project"`
				DirectoryProject       string `json:"directory_project"`
				DirectoryProjectSource string `json:"directory_project_source"`
				DirectoryProjectPath   string `json:"directory_project_path"`
			}
			if err := json.Unmarshal(finding.Evidence, &ev); err != nil {
				plan.Skipped = append(plan.Skipped, RepairSkip{ReasonCode: "invalid_doctor_evidence", Message: err.Error()})
				continue
			}
			from := normalizeProjectName(ev.SessionProject)
			to := normalizeProjectName(ev.DirectoryProject)
			if !isTrustedDirectoryEvidence(ev.DirectoryProjectSource) {
				plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: ev.SessionID, ReasonCode: "untrusted_directory_evidence", Message: "directory evidence is not git_remote or git_root"})
				continue
			}
			if ev.SessionID == "" || from == "" || to == "" || from == to || from != plan.Project {
				plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: ev.SessionID, ReasonCode: "invalid_reclassification_evidence", Message: "doctor evidence does not describe a supported project move"})
				continue
			}
			plan.Actions = append(plan.Actions, ProjectReclassifyAction{SessionID: ev.SessionID, FromProject: from, ToProject: to, ReasonCode: finding.ReasonCode, EvidenceSource: ev.DirectoryProjectSource, EvidencePath: ev.DirectoryProjectPath})
		}
	}
}

func planManualSessionRepair(plan *RepairPlan, scope Scope) error {
	sessions, err := scope.Store.ListDiagnosticSessions("")
	if err != nil {
		return err
	}
	known := map[string]bool{}
	byProject := make([]string, 0)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project != "" && !known[project] {
			known[project] = true
			byProject = append(byProject, project)
		}
	}
	_ = byProject
	for _, session := range sessions {
		from := normalizeProjectName(session.Project)
		if from != plan.Project {
			continue
		}
		name := strings.TrimSpace(session.Name)
		if !strings.HasPrefix(name, "manual-save-") {
			continue
		}
		to := normalizeProjectName(strings.TrimPrefix(name, "manual-save-"))
		if to == "" || from == to {
			continue
		}
		if !known[to] {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: session.ID, ReasonCode: "manual_name_unknown_project", Message: "manual session suffix is not a known local project"})
			continue
		}
		if detected, ok := detectSessionDirectoryProject(scope, map[string]DetectedProject{}, session.Directory); ok && isTrustedDirectoryEvidence(detected.Source) && normalizeProjectName(detected.Project) != to {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: session.ID, ReasonCode: "trusted_directory_contradicts_manual_name", Message: "trusted directory evidence points at a different project"})
			continue
		}
		plan.Actions = append(plan.Actions, ProjectReclassifyAction{SessionID: session.ID, FromProject: from, ToProject: to, ReasonCode: CheckManualSessionNameProjectMismatch})
	}
	return nil
}

func isTrustedDirectoryEvidence(source string) bool {
	switch strings.TrimSpace(source) {
	case "git_remote", "git_root":
		return true
	default:
		return false
	}
}

func dedupeAndSortRepairPlan(plan *RepairPlan) {
	seen := map[string]ProjectReclassifyAction{}
	for _, action := range plan.Actions {
		key := action.SessionID + "\x00" + action.FromProject + "\x00" + action.ToProject
		seen[key] = action
	}
	plan.Actions = plan.Actions[:0]
	for _, action := range seen {
		plan.Actions = append(plan.Actions, action)
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].SessionID < plan.Actions[j].SessionID })

	// Dedupe by seq (the unique identifier for a sync_mutations row) rather
	// than by entity_key: two rows with the SAME entity_key are exactly the
	// fan-out case (docs/conversational-retrieval-plan.md Bug B) and both
	// must survive deduping as independent deletions. Sorted by entity_key
	// then target_key, not seq, so fan-out siblings land next to each other
	// in the printed plan — that adjacency is what makes "one logical
	// mutation, N queue rows" visible to a human reviewing --plan output.
	seenSeqs := map[int64]SyncMutationDeleteAction{}
	for _, action := range plan.SyncMutationDeletions {
		seenSeqs[action.Seq] = action
	}
	plan.SyncMutationDeletions = plan.SyncMutationDeletions[:0]
	for _, action := range seenSeqs {
		plan.SyncMutationDeletions = append(plan.SyncMutationDeletions, action)
	}
	sort.Slice(plan.SyncMutationDeletions, func(i, j int) bool {
		a, b := plan.SyncMutationDeletions[i], plan.SyncMutationDeletions[j]
		if a.EntityKey != b.EntityKey {
			return a.EntityKey < b.EntityKey
		}
		if a.TargetKey != b.TargetKey {
			return a.TargetKey < b.TargetKey
		}
		return a.Seq < b.Seq
	})

	sort.Slice(plan.Skipped, func(i, j int) bool {
		if plan.Skipped[i].SessionID == plan.Skipped[j].SessionID {
			return plan.Skipped[i].ReasonCode < plan.Skipped[j].ReasonCode
		}
		return plan.Skipped[i].SessionID < plan.Skipped[j].SessionID
	})
}
