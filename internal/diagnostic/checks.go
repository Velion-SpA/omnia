package diagnostic

import (
	"context"
	"os"
	"strings"

	projectpkg "github.com/velion/omnia/internal/project"
	"github.com/velion/omnia/internal/store"
)

const (
	CheckSessionProjectDirectoryMismatch  = "session_project_directory_mismatch"
	CheckManualSessionNameProjectMismatch = "manual_session_name_project_mismatch"
	CheckSyncMutationRequiredFields       = "sync_mutation_required_fields"
	CheckSQLiteLockContention             = "sqlite_lock_contention"
	// CheckStoreExposure is the memory-provenance foundation
	// (omnia-provenance-foundation) hardening check: flags a store directory
	// living inside a recognized cloud-backup/sync folder or having
	// group/world-readable permissions. Encrypt-at-rest is deferred to 0.3
	// (see design.md) — this check is the 0.2 mitigation instead.
	CheckStoreExposure = "store_exposure"
)

type SessionProjectDirectoryMismatchCheck struct{}
type ManualSessionNameProjectMismatchCheck struct{}
type SyncMutationRequiredFieldsCheck struct{}
type SQLiteLockContentionCheck struct{}
type StoreExposureCheck struct{}

func (SessionProjectDirectoryMismatchCheck) Code() string {
	return CheckSessionProjectDirectoryMismatch
}
func (ManualSessionNameProjectMismatchCheck) Code() string {
	return CheckManualSessionNameProjectMismatch
}
func (SyncMutationRequiredFieldsCheck) Code() string { return CheckSyncMutationRequiredFields }
func (SQLiteLockContentionCheck) Code() string       { return CheckSQLiteLockContention }
func (StoreExposureCheck) Code() string              { return CheckStoreExposure }

// cloudSyncPathMarkers are path substrings indicating the store directory
// lives inside a recognized cloud-backup/sync folder, where a background
// sync agent could silently replicate the local SQLite file (raw memory
// content + embedding vectors) to a third-party cloud service — entirely
// outside Omnia's own sync/encryption posture.
var cloudSyncPathMarkers = []string{
	"Mobile Documents/com~apple~CloudDocs",
	"Dropbox",
	"OneDrive",
}

// storeFileStat abstracts os.Stat so tests could substitute a fake if ever
// needed; today it is always os.Stat (kept as a var for consistency with
// this file's other injectable seams, e.g. scope.ReadSQLiteLockSnapshot).
var storeFileStat = os.Stat

func (c StoreExposureCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	dir := scope.Store.DataDir()
	dbPath := scope.Store.DBPath()

	findings := make([]Finding, 0)

	for _, marker := range cloudSyncPathMarkers {
		if strings.Contains(dir, marker) {
			findings = append(findings, Finding{
				CheckID:              c.Code(),
				Severity:             SeverityWarning,
				ReasonCode:           "store_path_cloud_synced",
				Message:              "Store directory resolves inside a recognized cloud-backup/sync folder.",
				Why:                  "A background cloud-sync agent (iCloud Drive, Dropbox, OneDrive) can silently replicate the local SQLite database file to a third-party service, bypassing Omnia's own sync/encryption posture entirely.",
				Evidence:             mustJSON(map[string]any{"data_dir": dir, "matched_marker": marker}),
				SafeNextStep:         "Move the Omnia data directory (OMNIA_DATA_DIR) outside any cloud-sync folder.",
				RequiresConfirmation: true,
			})
			break
		}
	}

	if info, err := storeFileStat(dir); err == nil && info.Mode().Perm()&0o077 != 0 {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "store_dir_group_or_world_readable",
			Message:              "Store directory is group- or world-readable/writable.",
			Why:                  "A group/world-readable data directory lets other local accounts read or tamper with memory content and embeddings.",
			Evidence:             mustJSON(map[string]any{"data_dir": dir, "mode": info.Mode().Perm().String()}),
			SafeNextStep:         "Run `chmod 700` on the Omnia data directory.",
			RequiresConfirmation: true,
		})
	}

	if info, err := storeFileStat(dbPath); err == nil && info.Mode().Perm()&0o077 != 0 {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "store_db_file_group_or_world_readable",
			Message:              "Store database file is group- or world-readable/writable.",
			Why:                  "A group/world-readable database file lets other local accounts read raw memory content and embeddings directly.",
			Evidence:             mustJSON(map[string]any{"db_path": dbPath, "mode": info.Mode().Perm().String()}),
			SafeNextStep:         "Run `chmod 600` on the Omnia database file.",
			RequiresConfirmation: true,
		})
	}

	return resultFromFindings(c.Code(), map[string]any{"data_dir": dir}, findings), nil
}

func (c SessionProjectDirectoryMismatchCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	detected := make(map[string]DetectedProject)
	for _, session := range sessions {
		directory := strings.TrimSpace(session.Directory)
		directoryProject, ok := detectSessionDirectoryProject(scope, detected, directory)
		sessionProject := normalizeProjectName(session.Project)
		if !ok || directoryProject.Project == "" || sessionProject == "" || directoryProject.Project == sessionProject {
			continue
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "session_project_directory_mismatch",
			Message:              "Session project does not match the project inferred from its directory.",
			Why:                  "Project/directory drift can cause agents to retrieve or save memories under the wrong project scope.",
			Evidence:             mustJSON(map[string]any{"session_id": session.ID, "session_project": session.Project, "directory": session.Directory, "directory_project": directoryProject.Project, "directory_project_source": directoryProject.Source, "directory_project_path": directoryProject.Path}),
			SafeNextStep:         "Review the session evidence and use explicit `--project`/MCP project overrides until the project naming is consolidated.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

func detectSessionDirectoryProject(scope Scope, cache map[string]DetectedProject, directory string) (DetectedProject, bool) {
	if strings.TrimSpace(directory) == "" {
		return DetectedProject{}, false
	}
	if cached, ok := cache[directory]; ok {
		return cached, cached.Project != ""
	}
	if scope.DetectProject != nil {
		detected, ok := scope.DetectProject(directory)
		cache[directory] = detected
		return detected, ok && detected.Project != ""
	}
	if _, err := os.Stat(directory); err != nil {
		return DetectedProject{}, false
	}
	res := projectpkg.DetectProjectFull(directory)
	if res.Error != nil || (res.Source != projectpkg.SourceGitRemote && res.Source != projectpkg.SourceGitRoot) {
		return DetectedProject{}, false
	}
	detected := DetectedProject{Project: normalizeProjectName(res.Project), Source: res.Source, Path: res.Path}
	cache[directory] = detected
	return detected, detected.Project != ""
}

func (c ManualSessionNameProjectMismatchCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	knownProjects, err := knownSessionProjects(scope)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	for _, session := range sessions {
		if !strings.HasPrefix(session.Name, "manual-save-") {
			continue
		}
		nameProject := normalizeProjectName(strings.TrimPrefix(session.Name, "manual-save-"))
		sessionProject := normalizeProjectName(session.Project)
		if nameProject == "" || sessionProject == "" || nameProject == sessionProject || !knownProjects[nameProject] {
			continue
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "manual_session_name_project_mismatch",
			Message:              "Manual session name suffix does not match sessions.project.",
			Why:                  "Manual session naming drift can hide memories from project-scoped context retrieval.",
			Evidence:             mustJSON(map[string]any{"session_id": session.ID, "session_name": session.Name, "session_project": session.Project, "name_project": nameProject}),
			SafeNextStep:         "Use `omnia context --project <project>` or MCP `project` overrides explicitly before deciding whether to consolidate projects.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

func knownSessionProjects(scope Scope) (map[string]bool, error) {
	sessions, err := scope.Store.ListDiagnosticSessions("")
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project != "" {
			known[project] = true
		}
	}
	return known, nil
}

func (c SyncMutationRequiredFieldsCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	mutations, err := scope.Store.ListPendingProjectMutations(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	for _, mutation := range mutations {
		validation := store.ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey)
		if validation.ReasonCode == "" {
			continue
		}
		nextStep := "Run `omnia cloud upgrade doctor` and inspect the mutation payload before any manual repair."
		if strings.TrimSpace(scope.Project) != "" {
			nextStep = "Run `omnia cloud upgrade doctor --project " + scope.Project + "` and inspect the mutation payload before any manual repair."
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           validation.ReasonCode,
			Message:              validation.Message,
			Why:                  "A pending sync mutation with missing required fields can block safe cloud replication and must fail loudly instead of being silently dropped.",
			Evidence:             mustJSON(map[string]any{"seq": mutation.Seq, "target_key": mutation.TargetKey, "project": mutation.Project, "entity": mutation.Entity, "op": mutation.Op, "entity_key": mutation.EntityKey, "missing_fields": validation.MissingFields}),
			SafeNextStep:         nextStep,
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"pending_mutations_evaluated": len(mutations)}, findings), nil
}

func (c SQLiteLockContentionCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	readSnapshot := scope.Store.ReadSQLiteLockSnapshot
	if scope.ReadSQLiteLockSnapshot != nil {
		readSnapshot = scope.ReadSQLiteLockSnapshot
	}
	snapshot, err := readSnapshot(ctx)
	if err != nil {
		finding := Finding{CheckID: c.Code(), Severity: SeverityError, ReasonCode: "sqlite_lock_probe_failed", Message: err.Error(), Why: "Doctor could not read SQLite lock state, so contention cannot be ruled out.", Evidence: mustJSON(map[string]any{"error": err.Error()}), SafeNextStep: "Close other Engram processes and rerun `omnia doctor --check sqlite_lock_contention`.", RequiresConfirmation: false}
		return resultFromFindings(c.Code(), map[string]any{"probe": "failed"}, []Finding{finding}), nil
	}
	findings := make([]Finding, 0)
	if snapshot.CheckpointBusy > 0 || snapshot.BusyTimeoutMS <= 0 {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "sqlite_lock_contention_detected",
			Message:              "SQLite lock probe reported contention indicators.",
			Why:                  "Lock contention can cause writes or sync enrollment to fail; doctor only reports the condition and does not repair it.",
			Evidence:             mustJSON(snapshot),
			SafeNextStep:         "Stop other Engram processes, wait for active operations to finish, then rerun `omnia doctor --check sqlite_lock_contention`.",
			RequiresConfirmation: false,
		})
	}
	return resultFromFindings(c.Code(), snapshot, findings), nil
}

func normalizeProjectName(value string) string {
	normalized, _ := store.NormalizeProject(strings.TrimSpace(value))
	return strings.TrimSpace(normalized)
}
