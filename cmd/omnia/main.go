// Engram — Persistent memory for AI coding agents.
//
// Usage:
//
//	omnia serve          Start HTTP + MCP server
//	omnia mcp            Start MCP server only (stdio transport)
//	omnia search <query> Search memories from CLI
//	omnia save           Save a memory from CLI
//	omnia context        Show recent context
//	omnia stats          Show memory stats
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/velion/omnia/internal/cloud/autosync"
	"github.com/velion/omnia/internal/cloud/constants"
	"github.com/velion/omnia/internal/cloud/remote"
	"github.com/velion/omnia/internal/cloud/syncguidance"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/datadir"
	"github.com/velion/omnia/internal/diagnostic"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/obsidian"
	"github.com/velion/omnia/internal/project"
	"github.com/velion/omnia/internal/server"
	"github.com/velion/omnia/internal/setup"
	"github.com/velion/omnia/internal/store"
	engramsync "github.com/velion/omnia/internal/sync"
	"github.com/velion/omnia/internal/timeutil"
	"github.com/velion/omnia/internal/tui"
	versioncheck "github.com/velion/omnia/internal/version"

	tea "github.com/charmbracelet/bubbletea"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// version is set via ldflags at build time by goreleaser.
// Falls back to "dev" for local builds; init() tries Go module info first.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
}

var (
	storeNew      = store.New
	newHTTPServer = server.New
	startHTTP     = (*server.Server).Start

	newMCPServer           = mcp.NewServer
	newMCPServerWithTools  = mcp.NewServerWithTools
	newMCPServerWithConfig = mcp.NewServerWithConfig
	resolveMCPTools        = mcp.ResolveTools
	serveMCP               = mcpserver.ServeStdio

	// detectProject is injectable for testing; wraps project.DetectProject.
	detectProject = project.DetectProject

	newTUIModel   = func(s *store.Store) tui.Model { return tui.New(s, version) }
	newTeaProgram = tea.NewProgram
	runTeaProgram = (*tea.Program).Run

	checkForUpdates = versioncheck.CheckLatest

	setupSupportedAgents        = setup.SupportedAgents
	setupInstallAgent           = setup.Install
	setupAddClaudeCodeAllowlist = setup.AddClaudeCodeAllowlist
	scanInputLine               = fmt.Scanln

	storeSearch = func(s *store.Store, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
		return s.Search(query, opts)
	}
	storeAddObservation    = func(s *store.Store, p store.AddObservationParams) (int64, error) { return s.AddObservation(p) }
	storeDeleteObservation = func(s *store.Store, id int64, hard bool) error { return s.DeleteObservation(id, hard) }
	storeDeleteSession     = func(s *store.Store, id string) error { return s.DeleteSession(id) }
	storeDeletePrompt      = func(s *store.Store, id int64) error { return s.DeletePrompt(id) }
	storeDeleteProject     = func(s *store.Store, name string, hard bool) (*store.DeleteProjectResult, error) {
		return s.DeleteProject(name, hard)
	}
	storeTimeline = func(s *store.Store, observationID int64, before, after int) (*store.TimelineResult, error) {
		return s.Timeline(observationID, before, after)
	}
	storeFormatContext = func(s *store.Store, project, scope string) (string, error) { return s.FormatContext(project, scope) }
	storeStats         = func(s *store.Store) (*store.Stats, error) { return s.Stats() }
	storeExport        = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
	jsonMarshalIndent  = json.MarshalIndent
	runDiagnostics     = func(ctx context.Context, s *store.Store, project, check string) (diagnostic.Report, error) {
		runner := diagnostic.NewRunner()
		scope := diagnostic.Scope{Store: s, Project: project, Now: time.Now()}
		if strings.TrimSpace(check) != "" {
			return runner.RunOne(ctx, scope, check)
		}
		return runner.RunAll(ctx, scope)
	}

	syncStatus = func(sy *engramsync.Syncer) (localChunks int, remoteChunks int, pendingImport int, err error) {
		return sy.Status()
	}
	syncImport = func(sy *engramsync.Syncer) (*engramsync.ImportResult, error) { return sy.Import() }
	syncExport = func(sy *engramsync.Syncer, createdBy, project string) (*engramsync.SyncResult, error) {
		return sy.Export(createdBy, project)
	}
	newCloudAutosyncManager = func(s *store.Store, _ any) cloudAutosyncManager {
		mgr := autosync.New(s, nil, autosync.DefaultConfig())
		return autosyncManagerAdapter{manager: mgr}
	}

	// newAutosyncManager is the injectable factory used by tryStartAutosync.
	// BR2-3: Returns startableAutosyncManager (not *autosync.Manager) so tests can
	// inject a deterministic fake — preventing racy wg.Add/wg.Wait interleaving.
	newAutosyncManager = func(s *store.Store, transport autosync.CloudTransport, cfg autosync.Config) startableAutosyncManager {
		return autosync.New(s, transport, cfg)
	}

	exitFunc = os.Exit

	stdinScanner = func() *bufio.Scanner { return bufio.NewScanner(os.Stdin) }
	userHomeDir  = os.UserHomeDir

	// newObsidianExporter is injectable for testing.
	newObsidianExporter = obsidian.NewExporter

	// newObsidianWatcher is injectable for testing.
	newObsidianWatcher = obsidian.NewWatcher

	// agentRunnerFactory is injectable for testing. In production it delegates to
	// llm.NewRunner; tests substitute a fake to avoid real CLI invocations.
	agentRunnerFactory = defaultAgentRunnerFactory
)

type cloudSyncStatus struct {
	Phase               string
	LastError           string
	ConsecutiveFailures int
	BackoffUntil        *time.Time
	LastSyncAt          *time.Time
	ReasonCode          string
	ReasonMessage       string
}

type cloudAutosyncManager interface {
	Run(context.Context)
	NotifyDirty()
	Status() cloudSyncStatus
}

// startableAutosyncManager is the interface implemented by *autosync.Manager and used
// by tryStartAutosync. It combines autosyncStatusProvider with Run and Stop so that
// the factory variable newAutosyncManager can be stubbed in tests without spawning
// real goroutines — eliminating the racy wg.Add/wg.Wait interleaving.
// BR2-3: Using an interface return type (not *autosync.Manager) makes the factory
// injectable with deterministic fakes.
type startableAutosyncManager interface {
	autosyncStatusProvider // Status() autosync.Status
	Run(context.Context)
	Stop()
}

type autosyncManagerAdapter struct {
	manager *autosync.Manager
}

func (a autosyncManagerAdapter) Run(ctx context.Context) {
	a.manager.Run(ctx)
}

func (a autosyncManagerAdapter) NotifyDirty() {
	a.manager.NotifyDirty()
}

func (a autosyncManagerAdapter) Status() cloudSyncStatus {
	status := a.manager.Status()
	return cloudSyncStatus{
		Phase:               status.Phase,
		LastError:           status.LastError,
		ConsecutiveFailures: status.ConsecutiveFailures,
		BackoffUntil:        status.BackoffUntil,
		LastSyncAt:          status.LastSyncAt,
		ReasonCode:          status.ReasonCode,
		ReasonMessage:       status.ReasonMessage,
	}
}

// mutationTransportAdapter adapts remote.MutationTransport to autosync.CloudTransport.
// This bridges the type gap between packages without creating a circular import.
type mutationTransportAdapter struct {
	remote *remote.MutationTransport
}

func (a *mutationTransportAdapter) PushMutations(entries []autosync.MutationEntry) (*autosync.PushMutationsResult, error) {
	remoteEntries := make([]remote.MutationEntry, len(entries))
	for i, e := range entries {
		remoteEntries[i] = remote.MutationEntry{
			Project:   e.Project,
			Entity:    e.Entity,
			EntityKey: e.EntityKey,
			Op:        e.Op,
			Payload:   e.Payload,
		}
	}
	seqs, err := a.remote.PushMutations(remoteEntries)
	if err != nil {
		return nil, err
	}
	return &autosync.PushMutationsResult{AcceptedSeqs: seqs}, nil
}

func (a *mutationTransportAdapter) PullMutations(sinceSeq int64, limit int) (*autosync.PullMutationsResponse, error) {
	resp, err := a.remote.PullMutations(sinceSeq, limit)
	if err != nil {
		return nil, err
	}
	mutations := make([]autosync.PulledMutation, len(resp.Mutations))
	for i, m := range resp.Mutations {
		mutations[i] = autosync.PulledMutation{
			Seq:        m.Seq,
			Entity:     m.Entity,
			EntityKey:  m.EntityKey,
			Op:         m.Op,
			Payload:    m.Payload,
			OccurredAt: m.OccurredAt,
		}
	}
	return &autosync.PullMutationsResponse{
		Mutations: mutations,
		HasMore:   resp.HasMore,
		LatestSeq: resp.LatestSeq,
	}, nil
}

type storeSyncStatusProvider struct {
	store          *store.Store
	defaultProject string
	cfg            store.Config
}

func (p storeSyncStatusProvider) Status(project string) server.SyncStatus {
	resolvedProject, _ := store.NormalizeProject(project)
	resolvedProject = strings.TrimSpace(resolvedProject)
	if resolvedProject == "" {
		resolvedProject, _ = store.NormalizeProject(p.defaultProject)
		resolvedProject = strings.TrimSpace(resolvedProject)
	}
	upgradeStage, upgradeCode, upgradeMessage := p.upgradeStatus(resolvedProject)
	enabled, disabledCode, disabledMessage := p.cloudSyncEnabled(resolvedProject)
	targetKey := cloudTargetKeyForProject(resolvedProject)
	if !enabled {
		if disabledCode == "cloud_not_configured" && resolvedProject != "" {
			enrolled, err := p.store.IsProjectEnrolled(resolvedProject)
			if err != nil {
				return server.SyncStatus{
					Enabled:              false,
					Phase:                store.SyncLifecycleIdle,
					ReasonCode:           "status_unavailable",
					ReasonMessage:        fmt.Sprintf("cloud enrollment status is unavailable: %v", err),
					UpgradeStage:         upgradeStage,
					UpgradeReasonCode:    upgradeCode,
					UpgradeReasonMessage: upgradeMessage,
				}
			}
			if !enrolled {
				return server.SyncStatus{
					Enabled:              false,
					Phase:                store.SyncLifecycleIdle,
					ReasonCode:           constants.ReasonBlockedUnenrolled,
					ReasonMessage:        fmt.Sprintf("project %q is not enrolled for cloud sync", resolvedProject),
					UpgradeStage:         upgradeStage,
					UpgradeReasonCode:    upgradeCode,
					UpgradeReasonMessage: upgradeMessage,
				}
			}
			state, err := p.store.GetSyncState(targetKey)
			if err == nil && hasMeaningfulSyncState(state) {
				status := syncStatusFromState(state)
				status.Enabled = true
				status.UpgradeStage = upgradeStage
				status.UpgradeReasonCode = upgradeCode
				status.UpgradeReasonMessage = upgradeMessage
				return status
			}
		}
		return server.SyncStatus{
			Enabled:              false,
			Phase:                store.SyncLifecycleIdle,
			ReasonCode:           disabledCode,
			ReasonMessage:        disabledMessage,
			UpgradeStage:         upgradeStage,
			UpgradeReasonCode:    upgradeCode,
			UpgradeReasonMessage: upgradeMessage,
		}
	}
	state, err := p.store.GetSyncState(targetKey)
	if err != nil {
		reason := "sync state is unavailable"
		lastErr := fmt.Sprintf("read sync state: %v", err)
		return server.SyncStatus{
			Enabled:              true,
			Phase:                store.SyncLifecycleDegraded,
			ReasonCode:           "status_unavailable",
			ReasonMessage:        reason,
			LastError:            lastErr,
			UpgradeStage:         upgradeStage,
			UpgradeReasonCode:    upgradeCode,
			UpgradeReasonMessage: upgradeMessage,
		}
	}
	status := syncStatusFromState(state)
	status.Enabled = true
	status.UpgradeStage = upgradeStage
	status.UpgradeReasonCode = upgradeCode
	status.UpgradeReasonMessage = upgradeMessage
	return status
}

func (p storeSyncStatusProvider) upgradeStatus(project string) (string, string, string) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", "", ""
	}
	state, err := p.store.GetCloudUpgradeState(project)
	if err != nil {
		return "", "upgrade_status_unavailable", fmt.Sprintf("cloud upgrade status is unavailable: %v", err)
	}
	if state == nil {
		return "", "", ""
	}
	return state.Stage, strings.TrimSpace(state.LastErrorCode), strings.TrimSpace(state.LastErrorMessage)
}

func (p storeSyncStatusProvider) cloudSyncEnabled(project string) (bool, string, string) {
	cc, err := resolveCloudRuntimeConfig(p.cfg)
	if err != nil {
		return false, "cloud_config_error", fmt.Sprintf("cloud config error: %v", err)
	}
	if cc == nil || strings.TrimSpace(cc.ServerURL) == "" {
		return false, "cloud_not_configured", "cloud sync is not configured"
	}
	if _, err := validateCloudServerURL(cc.ServerURL); err != nil {
		return false, "cloud_config_error", fmt.Sprintf("cloud config error: invalid cloud runtime server URL: %v", err)
	}
	if strings.TrimSpace(project) == "" {
		return false, "project_required", "cloud sync status requires an explicit project scope"
	}
	enrolled, err := p.store.IsProjectEnrolled(project)
	if err != nil {
		return false, "status_unavailable", fmt.Sprintf("cloud enrollment status is unavailable: %v", err)
	}
	if !enrolled {
		return false, constants.ReasonBlockedUnenrolled, fmt.Sprintf("project %q is not enrolled for cloud sync", project)
	}
	return true, "", ""
}

func syncStatusFromState(state *store.SyncState) server.SyncStatus {
	var lastSyncAt *time.Time
	if state != nil && state.Lifecycle == store.SyncLifecycleHealthy {
		lastSyncAt = parseSyncStateTimestamp(state.UpdatedAt)
	}
	return server.SyncStatus{
		Phase:               state.Lifecycle,
		LastError:           derefString(state.LastError),
		ConsecutiveFailures: state.ConsecutiveFailures,
		BackoffUntil:        parseRFC3339Ptr(state.BackoffUntil),
		LastSyncAt:          lastSyncAt,
		ReasonCode:          derefString(state.ReasonCode),
		ReasonMessage:       derefString(state.ReasonMessage),
	}
}

func hasMeaningfulSyncState(state *store.SyncState) bool {
	if state == nil {
		return false
	}
	if state.Lifecycle != "" && state.Lifecycle != store.SyncLifecycleIdle {
		return true
	}
	if state.LastEnqueuedSeq > 0 || state.LastAckedSeq > 0 || state.LastPulledSeq > 0 {
		return true
	}
	if state.ConsecutiveFailures > 0 {
		return true
	}
	if state.BackoffUntil != nil || state.LeaseOwner != nil || state.LeaseUntil != nil {
		return true
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil {
		return true
	}
	return false
}

func parseSyncStateTimestamp(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return &parsed
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, time.UTC); err == nil {
		return &parsed
	}
	return nil
}

func parseRFC3339Ptr(value *string) *time.Time {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil
	}
	return &parsed
}

func derefString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func envBool(key string) bool {
	v := strings.TrimSpace(strings.ToLower(envx.Get(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// resolveCloudRuntimeConfig resolves the runtime config for the DEFAULT cloud,
// honouring the ENGRAM_CLOUD_SERVER / ENGRAM_CLOUD_TOKEN env overrides (legacy
// behavior). It is the alias-agnostic entry point used by status/upgrade paths.
func resolveCloudRuntimeConfig(cfg store.Config) (*cloudConfig, error) {
	return resolveCloudRuntimeConfigForAlias(cfg, "", true)
}

// resolveCloudRuntimeConfigForAlias resolves the runtime connection config
// (server URL + bearer token) for a specific cloud alias.
//
//   - alias == "" → the default cloud entry (legacy single-cloud behavior).
//   - alias != "" → the named cloud entry from cloud.json.
//
// applyEnvOverrides controls whether ENGRAM_CLOUD_SERVER / ENGRAM_CLOUD_TOKEN
// override the resolved entry. They are applied for the default/legacy target (so
// an env-only configuration keeps working — fix for issue #343) but MUST NOT be
// applied when the caller explicitly targeted a named cloud (`--cloud-name`):
// otherwise a stray ENGRAM_CLOUD_TOKEN in the shell would silently send the wrong
// credentials to a named cloud.
func resolveCloudRuntimeConfigForAlias(cfg store.Config, alias string, applyEnvOverrides bool) (*cloudConfig, error) {
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		return nil, fmt.Errorf("read cloud config: %w", err)
	}
	cc := &cloudConfig{}
	if v2 != nil {
		var entry *cloudEntry
		if strings.TrimSpace(alias) != "" {
			entry, _ = v2.getCloud(alias)
		} else {
			entry = v2.defaultCloudEntry()
		}
		if entry != nil {
			cc.ServerURL = entry.ServerURL
			cc.Token = entry.Token
		}
	}
	if applyEnvOverrides {
		if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_SERVER")); v != "" {
			cc.ServerURL = v
		}
		if v := strings.TrimSpace(envx.Get("OMNIA_CLOUD_TOKEN")); v != "" {
			cc.Token = v
		}
	}
	return cc, nil
}

// preflightCloudSync validates the DEFAULT cloud for a project sync. Retained for
// callers/tests that target the default cloud; delegates to the alias-aware form.
func preflightCloudSync(s *store.Store, cfg store.Config, project string, mutateState bool) (*cloudConfig, error) {
	return preflightCloudSyncForAlias(s, cfg, project, "", true, mutateState)
}

// preflightCloudSyncForAlias validates a project sync against a specific cloud
// alias and computes the alias-prefixed target key (cloudnFor) used for any
// blocked-state marking, so preflight and the success path always agree on the key.
func preflightCloudSyncForAlias(s *store.Store, cfg store.Config, project, alias string, applyEnvOverrides, mutateState bool) (*cloudConfig, error) {
	project = strings.TrimSpace(project)
	if project != "" {
		project, _ = store.NormalizeProject(project)
	}
	targetKey := cloudnFor(alias, project)

	cc, err := resolveCloudRuntimeConfigForAlias(cfg, alias, applyEnvOverrides)
	if err != nil {
		return nil, fmt.Errorf("cloud sync config error: %w", err)
	}
	hasServer := strings.TrimSpace(cc.ServerURL) != ""
	if !hasServer {
		message := "cloud server is missing: configure server URL with `omnia cloud config --server <url>`"
		if mutateState {
			_ = s.MarkSyncBlocked(targetKey, constants.ReasonCloudConfigError, message)
		}
		return nil, fmt.Errorf("cloud sync %s: %s", constants.ReasonCloudConfigError, message)
	}
	if _, err := validateCloudServerURL(cc.ServerURL); err != nil {
		message := fmt.Sprintf("invalid cloud runtime server URL: %v", err)
		if mutateState {
			_ = s.MarkSyncBlocked(targetKey, constants.ReasonCloudConfigError, message)
		}
		return nil, fmt.Errorf("cloud sync %s: %s", constants.ReasonCloudConfigError, message)
	}
	if project != "" {
		enrolled, err := s.IsProjectEnrolled(project)
		if err != nil {
			return nil, fmt.Errorf("cloud sync enrollment check: %w", err)
		}
		if !enrolled {
			message := fmt.Sprintf("project %q is not enrolled for cloud sync", project)
			if mutateState {
				_ = s.MarkSyncBlocked(targetKey, constants.ReasonBlockedUnenrolled, message)
			}
			return nil, fmt.Errorf("cloud sync blocked_unenrolled: %s", message)
		}
		if err := preflightCloudSyncLegacyMutations(s, project, targetKey, mutateState); err != nil {
			return nil, err
		}
	}
	return cc, nil
}

func preflightCloudSyncLegacyMutations(s *store.Store, project, targetKey string, mutateState bool) error {
	report, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		return fmt.Errorf("cloud sync legacy mutation preflight: %w", err)
	}
	if report.BlockedCount == 0 && report.RepairableCount == 0 {
		return nil
	}

	reasonCode := store.UpgradeReasonRepairableLegacyMutationPayload
	message := fmt.Sprintf(
		"legacy mutation payloads require repair before cloud sync for project %q: run `omnia cloud upgrade doctor --project %s` then `omnia cloud upgrade repair --project %s --apply`",
		project, project, project,
	)
	if report.BlockedCount > 0 {
		reasonCode = store.UpgradeReasonBlockedLegacyMutationManual
		first := firstBlockedLegacyMutationFinding(report)
		message = fmt.Sprintf(
			"legacy mutation payloads require manual action before cloud sync for project %q (seq=%d entity=%s op=%s): %s; inspect with `omnia cloud upgrade doctor --project %s` and run `omnia cloud upgrade repair --project %s --apply` for deterministic repairs",
			project, first.Seq, first.Entity, first.Op, first.Message, project, project,
		)
	}
	if mutateState {
		_ = s.MarkSyncBlocked(targetKey, reasonCode, message)
	}
	return fmt.Errorf("cloud sync %s: %s", reasonCode, message)
}

func firstBlockedLegacyMutationFinding(report store.CloudUpgradeLegacyMutationReport) store.CloudUpgradeLegacyMutationFinding {
	for _, finding := range report.Findings {
		if !finding.Repairable {
			return finding
		}
	}
	if len(report.Findings) > 0 {
		return report.Findings[0]
	}
	return store.CloudUpgradeLegacyMutationFinding{}
}

func cloudTargetKeyForProject(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return constants.TargetKeyCloud
	}
	project, _ = store.NormalizeProject(project)
	if strings.TrimSpace(project) == "" {
		return constants.TargetKeyCloud
	}
	return fmt.Sprintf("%s:%s", constants.TargetKeyCloud, project)
}

// cloudnFor returns the sync target key for a given cloud alias and project.
// When alias is "cloud" (the legacy default), this produces exactly the same
// keys as cloudTargetKeyForProject, guaranteeing no orphaned sync_state rows.
// Multi-cloud callers use this instead of cloudTargetKeyForProject.
func cloudnFor(alias, project string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = constants.TargetKeyCloud // "cloud"
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return alias
	}
	project, _ = store.NormalizeProject(project)
	if strings.TrimSpace(project) == "" {
		return alias
	}
	return fmt.Sprintf("%s:%s", alias, project)
}

// isDefaultCloudAlias reports whether the alias resolves to the legacy default
// cloud, which consumes the shared "cloud" mutation queue rather than a dedicated
// fan-out queue. The empty alias (env-only), the canonical "cloud" name, and the
// configured default alias all map to the default queue.
func isDefaultCloudAlias(alias, defaultAlias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" || strings.EqualFold(alias, constants.TargetKeyCloud) {
		return true
	}
	defaultAlias = strings.TrimSpace(defaultAlias)
	return defaultAlias != "" && strings.EqualFold(alias, defaultAlias)
}

func markCloudSyncFailure(s *store.Store, targetKey string, syncErr error) {
	if syncErr == nil {
		return
	}
	message := cloudSyncFailureMessage(syncguidance.ProjectFromTargetKey(targetKey), syncErr)
	var statusErr *remote.HTTPStatusError
	if errors.As(syncErr, &statusErr) {
		switch {
		case statusErr.IsAuthFailure():
			_ = s.MarkSyncAuthRequired(targetKey, message)
			return
		case statusErr.IsPolicyFailure():
			_ = s.MarkSyncBlocked(targetKey, constants.ReasonPolicyForbidden, message)
			return
		}
	}
	_ = s.MarkSyncFailure(targetKey, message, time.Now().UTC().Add(30*time.Second))
}

func cloudSyncFailureMessage(project string, syncErr error) string {
	if syncErr == nil {
		return ""
	}
	return syncguidance.AppendGuidance(syncErr.Error(), project, syncErr)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		exitFunc(1)
	}

	if shouldCheckForUpdates(os.Args[1:]) {
		printUpdateCheckResult(checkForUpdates(version))
	}
	if handleConfigFreeCommand(os.Args[1:]) {
		return
	}

	// A legacy ~/.engram directory is used IN PLACE (see datadir.Resolve): the
	// store opens it directly so a pre-rebrand install keeps working against its
	// real data with no copy and no divergence. Moving to ~/.omnia is opt-in via
	// the explicit `omnia migrate` subcommand; startup never migrates implicitly.
	cfg, cfgErr := store.DefaultConfig()
	if cfgErr != nil {
		// Fallback: try to resolve home directory from environment variables
		// that os.UserHomeDir() might have missed (e.g. MCP subprocesses on
		// Windows where %USERPROFILE% is not propagated).
		if home := resolveHomeFallback(); home != "" {
			log.Printf("[omnia] UserHomeDir failed, using fallback: %s", home)
			cfg = store.FallbackConfig(filepath.Join(home, datadir.DirName))
		} else {
			fatal(cfgErr)
		}
	}

	// Allow overriding data dir via env (OMNIA_DATA_DIR, legacy ENGRAM_DATA_DIR).
	if dir := envx.Get(datadir.DataDirEnv); dir != "" {
		cfg.DataDir = dir
	}

	// Migrate orphaned databases that ended up in wrong locations
	// (e.g. drive root on Windows due to previous bug).
	migrateOrphanedDB(cfg.DataDir)

	switch os.Args[1] {
	case "serve":
		cmdServe(cfg)
	case "mcp":
		cmdMCP(cfg)
	case "tui":
		cmdTUI(cfg)
	case "search":
		cmdSearch(cfg)
	case "save":
		cmdSave(cfg)
	case "delete":
		cmdDelete(cfg)
	case "timeline":
		cmdTimeline(cfg)
	case "conflicts":
		cmdConflicts(cfg)
	case "doctor":
		cmdDoctor(cfg)
	case "context":
		cmdContext(cfg)
	case "stats":
		cmdStats(cfg)
	case "export":
		cmdExport(cfg)
	case "import":
		cmdImport(cfg)
	case "sync":
		cmdSync(cfg)
	case "cloud":
		cmdCloud(cfg)
	case "obsidian-export":
		cmdObsidianExport(cfg)
	case "projects":
		cmdProjects(cfg)
	case "setup":
		cmdSetup()
	case "dashboard":
		cmdDashboard(os.Args[2:])
	case "embed":
		cmdEmbed(os.Args[2:])
	case "collect":
		cmdCollect(os.Args[2:])
	case "migrate":
		cmdMigrate(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("omnia %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		exitFunc(1)
	}
}

func shouldCheckForUpdates(args []string) bool {
	if len(args) == 0 {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	switch command {
	case "mcp", "serve":
		return false
	case "cloud":
		return len(args) < 2 || strings.ToLower(strings.TrimSpace(args[1])) != "serve"
	}
	return true
}

func handleConfigFreeCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "version", "--version", "-v":
		fmt.Printf("omnia %s\n", version)
		return true
	case "help", "--help", "-h":
		printUsage()
		return true
	case "cloud":
		if len(args) >= 2 {
			subcommand := strings.ToLower(strings.TrimSpace(args[1]))
			if subcommand == "--help" || subcommand == "-h" || subcommand == "help" {
				cmdCloud(store.Config{})
				return true
			}
		}
	}
	return false
}

func printUpdateCheckResult(result versioncheck.CheckResult) {
	if result.Status != versioncheck.StatusUpToDate && result.Message != "" {
		fmt.Fprintln(os.Stderr, result.Message)
		fmt.Fprintln(os.Stderr)
	}
}

// ─── Commands ────────────────────────────────────────────────────────────────

func cmdServe(cfg store.Config) {
	port := 7437 // "ENGR" on phone keypad vibes
	if p := envx.Get("OMNIA_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	// Allow: omnia serve 8080
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			port = n
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	srv := newHTTPServer(s, port)

	// Wire the semantic runner factory and prompt builder for POST /conflicts/scan.
	// Both live in cmd/omnia so internal/server avoids a direct dependency on internal/llm.
	srv.SetRunnerFactory(agentRunnerFactory)
	srv.SetPromptBuilder(func(a, b store.ObservationSnippet) string {
		return llmBuildPrompt(a, b)
	})

	// Graceful shutdown context — cancelled on SIGINT/SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Auto-embed-on-save (human-like-memory PR4): when embeddings are enabled,
	// run the worker on this shutdown ctx so POST /observations embeds new
	// memories out-of-band. nil (disabled) leaves the save path byte-for-byte
	// today's. Hoisted to function scope so the SIGINT/SIGTERM handler below
	// can Stop() it (graceful drain) before the process exits.
	var autoEmbedWorker *embed.Worker
	if appCfg, cfgErr := config.Load(config.DefaultPath()); cfgErr == nil {
		if worker := buildAutoEmbedWorker(appCfg.Embeddings, s); worker != nil {
			worker.Start(ctx)
			srv.SetAutoEmbed(worker)
			autoEmbedWorker = worker
		}
	}

	// Try to start autosync (opt-in via ENGRAM_CLOUD_AUTOSYNC=1).
	// BW7: tryStartAutosync returns (status provider, stop func) so the signal
	// handler can call mgrStop() before os.Exit, giving the manager time to
	// release its sync lease.
	fallback := storeSyncStatusProvider{store: s, defaultProject: resolveServeSyncStatusProject(), cfg: cfg}
	mgr, mgrStop := tryStartAutosync(ctx, s, cfg)
	if mgr != nil {
		srv.SetSyncStatus(&autosyncStatusAdapter{mgr: mgr, fallback: fallback})
	} else {
		srv.SetSyncStatus(fallback)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[omnia] shutting down...")
		cancel()
		if mgrStop != nil {
			mgrStop() // BW7: wait for Manager to release lease before exiting
		}
		// Drain the auto-embed worker (human-like-memory PR4 review fix):
		// ctx is already cancelled above, so Stop() waits for the in-flight
		// job (if any) to finish rather than letting os.Exit kill it mid-embed.
		if autoEmbedWorker != nil {
			autoEmbedWorker.Stop()
		}
		exitFunc(0)
	}()

	if err := startHTTP(srv); err != nil {
		fatal(err)
	}
}

func resolveServeSyncStatusProject() string {
	projectName := strings.TrimSpace(envx.Get("OMNIA_PROJECT"))
	if projectName == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectName = detectProject(cwd)
		}
	}
	projectName, _ = store.NormalizeProject(projectName)
	return strings.TrimSpace(projectName)
}

// tryStartAutosync starts the autosync Manager(s) if ENGRAM_CLOUD_AUTOSYNC=1 and
// the default cloud has both a token and a server URL configured.
// REQ-210: only exact "1" is accepted. REQ-211: missing token/server → log+skip.
// Never fatal — autosync is optional.
// BW7: Returns (status provider, stop func) so the caller can invoke stop
// before os.Exit to ensure the Manager(s) release their sync lease(s).
//
// OBL-07 (requires OBL-06's per-alias mutation fan-out queue): in addition to
// the default cloud's Manager — whose Status() backs the returned
// autosyncStatusProvider exactly as before — this also starts one Manager per
// additional configured, non-default cloud alias, so autosync keeps EVERY
// configured cloud in sync, not just the default. Each named alias gets:
//   - one Manager for its project-less bucket (cloudnFor(alias, "")), and
//   - one Manager per project enrolled AT STARTUP TIME (cloudnFor(alias, project)),
//     since OBL-06's fan-out queue is project-scoped per alias (unlike the
//     default cloud's single global "cloud" bucket, which already spans every
//     project). A project enrolled after `omnia serve`/`omnia mcp` starts needs
//     a restart to gain named-cloud autosync coverage — the same restart
//     already required today for a newly `cloud add`-ed alias, since cloud
//     config is likewise only read once at startup.
//
// Named aliases are NEVER subject to the ENGRAM_CLOUD_TOKEN/SERVER env-override
// footgun — only the default/empty alias is (see resolveCloudRuntimeConfigForAlias
// / cloudSyncApplyEnv, main.go:456-495, 1749-1754).
func tryStartAutosync(ctx context.Context, s *store.Store, cfg store.Config) (autosyncStatusProvider, func()) {
	// REQ-210: opt-in requires exact "1".
	if strings.TrimSpace(envx.Get("OMNIA_CLOUD_AUTOSYNC")) != "1" {
		return nil, nil
	}

	defaultMgrs := startAutosyncManagersForAlias(ctx, s, cfg, "", true, nil)
	if len(defaultMgrs) == 0 {
		return nil, nil
	}
	allMgrs := append([]startableAutosyncManager{}, defaultMgrs...)

	if v2, err := loadCloudConfigV2(cfg); err != nil {
		log.Printf("[autosync] WARNING: could not read cloud config for multi-cloud fan-out: %v", err)
	} else if aliases := nonDefaultCloudAliases(v2); len(aliases) > 0 {
		var enrolledProjects []string
		if enrolled, err := s.ListEnrolledProjects(); err != nil {
			log.Printf("[autosync] WARNING: could not list enrolled projects for multi-cloud fan-out: %v", err)
		} else {
			for _, ep := range enrolled {
				enrolledProjects = append(enrolledProjects, ep.Project)
			}
		}
		for _, alias := range aliases {
			mgrs := startAutosyncManagersForAlias(ctx, s, cfg, alias, false, enrolledProjects)
			allMgrs = append(allMgrs, mgrs...)
		}
	}

	stopAll := func() {
		for _, mgr := range allMgrs {
			mgr.Stop()
		}
	}
	return defaultMgrs[0], stopAll
}

// startAutosyncManagersForAlias resolves runtime config for one cloud alias
// and, if a token and server are both configured, starts one background
// autosync.Manager per target key in {cloudnFor(alias, ""), cloudnFor(alias,
// project) for each project in extraProjects} (deduplicated). Returns an empty
// slice — never fatal — if the alias has no server/token configured, matching
// the original single-cloud gating (REQ-211).
func startAutosyncManagersForAlias(ctx context.Context, s *store.Store, cfg store.Config, alias string, applyEnvOverrides bool, extraProjects []string) []startableAutosyncManager {
	label := cloudAliasLabel(alias)

	cc, err := resolveCloudRuntimeConfigForAlias(cfg, alias, applyEnvOverrides)
	if err != nil {
		log.Printf("[autosync] ERROR: cannot read cloud config for %q: %v", label, err)
		return nil
	}

	token := strings.TrimSpace(cc.Token)
	serverURL := strings.TrimSpace(cc.ServerURL)

	// REQ-211: token required. The token is resolved from cloud.json first and
	// overridden by ENGRAM_CLOUD_TOKEN when set (default alias only), so both
	// sources are tried. On Windows (Task Scheduler), the env var is often
	// absent — the file path is the expected source (issue #421).
	if token == "" {
		log.Printf("[autosync] ERROR: cloud token is not configured for %q (set ENGRAM_CLOUD_TOKEN or store token in cloud.json via `omnia cloud config`); autosync disabled for this cloud", label)
		return nil
	}
	// REQ-211: server URL required. Resolved from cloud.json or ENGRAM_CLOUD_SERVER.
	if serverURL == "" {
		log.Printf("[autosync] ERROR: cloud server URL is not configured for %q (set ENGRAM_CLOUD_SERVER or run `omnia cloud config --server <url>`); autosync disabled for this cloud", label)
		return nil
	}

	remoteMT, err := remote.NewMutationTransport(serverURL, token)
	if err != nil {
		log.Printf("[autosync] ERROR: invalid server URL %q for %q: %v; autosync disabled for this cloud", serverURL, label, err)
		return nil
	}
	transport := &mutationTransportAdapter{remote: remoteMT}

	targets := make([]string, 0, len(extraProjects)+1)
	targets = append(targets, cloudnFor(alias, ""))
	for _, project := range extraProjects {
		targets = append(targets, cloudnFor(alias, project))
	}

	seen := make(map[string]bool, len(targets))
	mgrs := make([]startableAutosyncManager, 0, len(targets))
	for _, targetKey := range targets {
		if seen[targetKey] {
			continue
		}
		seen[targetKey] = true

		mgrCfg := autosync.DefaultConfig()
		if targetKey != store.DefaultSyncTargetKey {
			// Named/project-scoped target: distinguish from the default cloud's
			// queue and lease so multiple Managers never collide.
			mgrCfg.TargetKey = targetKey
			mgrCfg.LeaseOwner = fmt.Sprintf("autosync-%s-%d", targetKey, time.Now().UnixNano())
		}
		// BR2-3: Call newAutosyncManager (injectable) instead of autosync.New directly,
		// so tests can stub the factory and avoid real goroutine/network side effects.
		mgr := newAutosyncManager(s, transport, mgrCfg)
		go mgr.Run(ctx)
		log.Printf("[autosync] started for cloud %q (server=%s, target=%s)", label, serverURL, mgrCfg.TargetKey)
		mgrs = append(mgrs, mgr)
	}

	// Start the proactive token refresher for this alias. All managers above
	// share the same underlying MutationTransport (remoteMT), so passing it once
	// is sufficient — SetToken updates the token for every request in flight on
	// any of the target-key managers.
	startTokenRefresher(ctx, cfg, alias, applyEnvOverrides, []*remote.MutationTransport{remoteMT})

	return mgrs
}

func cmdMCP(cfg store.Config) {
	toolsFilter := ""
	projectOverride := strings.TrimSpace(envx.Get("OMNIA_PROJECT"))
	for i := 2; i < len(os.Args); i++ {
		if strings.HasPrefix(os.Args[i], "--tools=") {
			toolsFilter = strings.TrimPrefix(os.Args[i], "--tools=")
		} else if os.Args[i] == "--tools" && i+1 < len(os.Args) {
			toolsFilter = os.Args[i+1]
			i++
		} else if strings.HasPrefix(os.Args[i], "--project=") {
			projectOverride = strings.TrimSpace(strings.TrimPrefix(os.Args[i], "--project="))
			if projectOverride == "" {
				fatal(fmt.Errorf("--project requires a value"))
			}
		} else if os.Args[i] == "--project" {
			if i+1 >= len(os.Args) {
				fatal(fmt.Errorf("--project requires a value"))
			}
			projectOverride = strings.TrimSpace(os.Args[i+1])
			if projectOverride == "" {
				fatal(fmt.Errorf("--project requires a value"))
			}
			i++
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	// Match `omnia serve` autosync startup semantics for stdio MCP agents.
	// Autosync remains opt-in via ENGRAM_CLOUD_AUTOSYNC=1 and never makes MCP
	// startup fatal when cloud config is missing or invalid.
	ctx, cancel := context.WithCancel(context.Background())
	_, mgrStop := tryStartAutosync(ctx, s, cfg)
	// Hoisted to function scope (human-like-memory PR4 review fix) so
	// stopAutosync below can Stop() it — draining the worker's in-flight
	// job instead of leaving it to be killed mid-embed by process exit.
	var autoEmbedWorker *embed.Worker
	autosyncStopped := false
	stopAutosync := func() {
		if autosyncStopped {
			return
		}
		autosyncStopped = true
		cancel()
		if mgrStop != nil {
			mgrStop()
		}
		if autoEmbedWorker != nil {
			autoEmbedWorker.Stop()
		}
	}
	defer stopAutosync()

	mcpCfg := mcp.MCPConfig{DefaultProject: projectOverride}
	// Recall wiring (design D6/D7, human-like-memory PR3): only constructs
	// the embeddings store/Ollama client/recall.Service when recall.enabled
	// is true in config.yaml. A missing/unparseable config file degrades
	// silently (mcpCfg.Recall stays nil), matching every other `omnia`
	// subcommand's config.Load graceful-degradation convention.
	if appCfg, cfgErr := config.Load(config.DefaultPath()); cfgErr == nil {
		mcpCfg.Recall = buildRecallService(s, appCfg.Recall, appCfg.Embeddings)
		// Auto-embed-on-save (human-like-memory PR4): when embeddings are
		// enabled, run the worker on the same ctx cancelled at shutdown so
		// mem_save embeds new memories out-of-band. nil when disabled.
		if worker := buildAutoEmbedWorker(appCfg.Embeddings, s); worker != nil {
			worker.Start(ctx)
			mcpCfg.AutoEmbed = worker
			autoEmbedWorker = worker
		}
	}
	allowlist := resolveMCPTools(toolsFilter)
	mcpSrv := newMCPServerWithConfig(s, mcpCfg, allowlist)

	if err := serveMCP(mcpSrv); err != nil {
		stopAutosync()
		fatal(err)
	}
}

func cmdTUI(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	model := newTUIModel(s)
	p := newTeaProgram(model)
	if _, err := runTeaProgram(p); err != nil {
		fatal(err)
	}
}

func cmdSearch(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia search <query> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]")
		exitFunc(1)
	}

	// Collect the query (everything that's not a flag)
	var queryParts []string
	opts := store.SearchOptions{Limit: 10}

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--type":
			if i+1 < len(os.Args) {
				opts.Type = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				opts.Project = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					opts.Limit = n
				}
				i++
			}
		case "--scope":
			if i+1 < len(os.Args) {
				opts.Scope = os.Args[i+1]
				i++
			}
		default:
			queryParts = append(queryParts, os.Args[i])
		}
	}

	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: search query is required")
		exitFunc(1)
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	results, err := storeSearch(s, query, opts)
	if err != nil {
		fatal(err)
		return
	}

	if len(results) == 0 {
		fmt.Printf("No memories found for: %q\n", query)
		return
	}

	fmt.Printf("Found %d memories:\n\n", len(results))
	for i, r := range results {
		project := ""
		if r.Project != nil {
			project = fmt.Sprintf(" | project: %s", *r.Project)
		}
		fmt.Printf("[%d] #%d (%s) — %s\n    %s\n    %s%s | scope: %s\n\n",
			i+1, r.ID, r.Type, r.Title,
			truncate(r.Content, 300),
			timeutil.FormatLocal(r.CreatedAt), project, r.Scope)
	}
}

func cmdSave(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia save <title> <content> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--topic TOPIC_KEY]")
		exitFunc(1)
	}

	title := os.Args[2]
	content := os.Args[3]
	typ := "manual"
	project := ""
	scope := "project"
	topicKey := ""

	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--type":
			if i+1 < len(os.Args) {
				typ = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--scope":
			if i+1 < len(os.Args) {
				scope = os.Args[i+1]
				i++
			}
		case "--topic":
			if i+1 < len(os.Args) {
				topicKey = os.Args[i+1]
				i++
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	sessionID := "manual-save"
	if project != "" {
		sessionID = "manual-save-" + project
	}
	cwd, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	if err := s.CreateSession(sessionID, project, cwd); err != nil {
		fatal(err)
	}
	id, err := storeAddObservation(s, store.AddObservationParams{
		SessionID: sessionID,
		Type:      typ,
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     scope,
		TopicKey:  topicKey,
	})
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Memory saved: #%d %q (%s)\n", id, title, typ)
}

func cmdDelete(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia delete <observation_id> [--hard]")
		fmt.Fprintln(os.Stderr, "       omnia delete session  <id>")
		fmt.Fprintln(os.Stderr, "       omnia delete prompt   <id>")
		fmt.Fprintln(os.Stderr, "       omnia delete project  <name> [--hard]")
		exitFunc(1)
		return
	}

	sub := os.Args[2]
	switch sub {
	case "session":
		cmdDeleteSession(cfg)
	case "prompt":
		cmdDeletePrompt(cfg)
	case "project":
		cmdDeleteProject(cfg)
	default:
		// Backward-compat: treat the second arg as a numeric observation ID.
		cmdDeleteObservation(cfg)
	}
}

func cmdDeleteObservation(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia delete <observation_id> [--hard]")
		exitFunc(1)
		return
	}

	id, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid observation id %q\n", os.Args[2])
		exitFunc(1)
		return
	}

	hard := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--hard" {
			hard = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if err := storeDeleteObservation(s, id, hard); err != nil {
		fatal(err)
		return
	}

	kind := "soft-deleted"
	if hard {
		kind = "hard-deleted"
	}
	fmt.Printf("Observation #%d %s\n", id, kind)
}

func cmdDeleteSession(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia delete session <id>")
		exitFunc(1)
		return
	}

	id := os.Args[3]

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if err := storeDeleteSession(s, id); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("Session %q deleted\n", id)
}

func cmdDeletePrompt(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia delete prompt <id>")
		exitFunc(1)
		return
	}

	id, err := strconv.ParseInt(os.Args[3], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid prompt id %q\n", os.Args[3])
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if err := storeDeletePrompt(s, id); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("Prompt #%d deleted\n", id)
}

func cmdDeleteProject(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: omnia delete project <name> [--hard]")
		exitFunc(1)
		return
	}

	name := os.Args[3]
	hard := false
	for i := 4; i < len(os.Args); i++ {
		if os.Args[i] == "--hard" {
			hard = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	result, err := storeDeleteProject(s, name, hard)
	if err != nil {
		fatal(err)
		return
	}

	kind := "soft-deleted"
	if hard {
		kind = "hard-deleted"
	}
	fmt.Printf("Project %q %s: %d observation(s), %d prompt(s), %d session(s)\n",
		result.Project, kind, result.ObservationsDeleted, result.PromptsDeleted, result.SessionsDeleted)
}

func cmdTimeline(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia timeline <observation_id> [--before N] [--after N]")
		exitFunc(1)
	}

	obsID, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid observation id %q\n", os.Args[2])
		exitFunc(1)
	}

	before, after := 5, 5
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--before":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					before = n
				}
				i++
			}
		case "--after":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					after = n
				}
				i++
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	result, err := storeTimeline(s, obsID, before, after)
	if err != nil {
		fatal(err)
	}

	// Session header
	if result.SessionInfo != nil {
		summary := ""
		if result.SessionInfo.Summary != nil {
			summary = fmt.Sprintf(" — %s", truncate(*result.SessionInfo.Summary, 100))
		}
		fmt.Printf("Session: %s (%s)%s\n", result.SessionInfo.Project, result.SessionInfo.StartedAt, summary)
		fmt.Printf("Total observations in session: %d\n\n", result.TotalInRange)
	}

	// Before
	if len(result.Before) > 0 {
		fmt.Println("─── Before ───")
		for _, e := range result.Before {
			fmt.Printf("  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
		}
		fmt.Println()
	}

	// Focus
	fmt.Printf(">>> #%d [%s] %s <<<\n", result.Focus.ID, result.Focus.Type, result.Focus.Title)
	fmt.Printf("    %s\n", truncate(result.Focus.Content, 500))
	fmt.Printf("    %s\n\n", timeutil.FormatLocal(result.Focus.CreatedAt))

	// After
	if len(result.After) > 0 {
		fmt.Println("─── After ───")
		for _, e := range result.After {
			fmt.Printf("  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
		}
	}
}

func cmdContext(cfg store.Config) {
	project := ""
	scope := ""

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--scope":
			if i+1 < len(os.Args) {
				scope = os.Args[i+1]
				i++
			}
		default:
			if project == "" {
				project = os.Args[i]
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	ctx, err := storeFormatContext(s, project, scope)
	if err != nil {
		fatal(err)
	}

	if ctx == "" {
		fmt.Println("No previous session memories found.")
		return
	}

	fmt.Print(ctx)
}

func cmdStats(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	stats, err := storeStats(s)
	if err != nil {
		fatal(err)
	}

	projects := "none yet"
	if len(stats.Projects) > 0 {
		projects = strings.Join(stats.Projects, ", ")
	}

	fmt.Printf("Omnia Memory Stats\n")
	fmt.Printf("  Sessions:     %d\n", stats.TotalSessions)
	fmt.Printf("  Observations: %d\n", stats.TotalObservations)
	fmt.Printf("  Prompts:      %d\n", stats.TotalPrompts)
	fmt.Printf("  Projects:     %s\n", projects)
	fmt.Printf("  Database:     %s\n", datadir.DBPath(cfg.DataDir))
}

func cmdExport(cfg store.Config) {
	outFile := "omnia-export.json"
	if len(os.Args) > 2 {
		outFile = os.Args[2]
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	data, err := storeExport(s)
	if err != nil {
		fatal(err)
	}

	out, err := jsonMarshalIndent(data, "", "  ")
	if err != nil {
		fatal(err)
	}

	if err := os.WriteFile(outFile, out, 0644); err != nil {
		fatal(err)
	}

	fmt.Printf("Exported to %s\n", outFile)
	fmt.Printf("  Sessions:     %d\n", len(data.Sessions))
	fmt.Printf("  Observations: %d\n", len(data.Observations))
	fmt.Printf("  Prompts:      %d\n", len(data.Prompts))
}

func cmdImport(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia import <file.json>")
		exitFunc(1)
	}

	inFile := os.Args[2]
	raw, err := os.ReadFile(inFile)
	if err != nil {
		fatal(fmt.Errorf("read %s: %w", inFile, err))
	}

	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		fatal(fmt.Errorf("parse %s: %w", inFile, err))
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	result, err := s.Import(&data)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Imported from %s\n", inFile)
	fmt.Printf("  Sessions:     %d\n", result.SessionsImported)
	fmt.Printf("  Observations: %d\n", result.ObservationsImported)
	fmt.Printf("  Prompts:      %d\n", result.PromptsImported)
}

func cmdSync(cfg store.Config) {
	// Parse flags
	doImport := false
	doStatus := false
	doAll := false
	doCloud := false
	cloudName := ""
	project := ""
	projectProvided := false
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--help", "-h", "help":
			printSyncUsage()
			return
		case "--import":
			doImport = true
		case "--status":
			doStatus = true
		case "--all":
			doAll = true
		case "--cloud":
			doCloud = true
		case "--cloud-name":
			if i+1 < len(os.Args) {
				cloudName = strings.TrimSpace(os.Args[i+1])
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				projectProvided = true
				i++
			}
		}
	}

	// Default project using git detection (so sync only exports
	// memories for THIS project, not everything in the global DB).
	// --all skips project filtering entirely — exports everything.
	if !doAll && project == "" {
		if cwd, err := os.Getwd(); err == nil {
			project = detectProject(cwd)
		}
	}
	if project != "" {
		normalizedProject, warning := store.NormalizeProject(project)
		project = normalizedProject
		if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
	}

	syncDir := ".engram"

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	cloudEnabled := doCloud || cloudName != "" || envBool("OMNIA_CLOUD_SYNC")
	if cloudEnabled {
		if doAll {
			fatal(fmt.Errorf("cloud sync requires a single explicit --project scope; --all is not supported"))
		}
		if !projectProvided || strings.TrimSpace(project) == "" {
			fatal(fmt.Errorf("cloud sync requires an explicit non-empty --project value"))
		}
		runCloudSync(cfg, s, project, syncDir, cloudName, doStatus, doImport)
		return
	}

	// Local sync path: export/import project-scoped chunks to .engram/.
	sy := engramsync.NewLocal(s, syncDir)

	if doStatus {
		local, remote, pending, err := syncStatus(sy)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("Sync status:\n")
		fmt.Printf("  Local chunks:    %d\n", local)
		fmt.Printf("  Remote chunks:   %d\n", remote)
		fmt.Printf("  Pending import:  %d\n", pending)
		return
	}

	if doImport {
		result, err := syncImport(sy)
		if err != nil {
			fatal(err)
		}
		if result.ChunksImported == 0 {
			fmt.Println("No new chunks to import.")
			if result.ChunksSkipped > 0 {
				fmt.Printf("  (%d chunks already imported)\n", result.ChunksSkipped)
			}
			return
		}
		fmt.Printf("Imported %d new chunk(s) from .engram/\n", result.ChunksImported)
		fmt.Printf("  Sessions:     %d\n", result.SessionsImported)
		fmt.Printf("  Observations: %d\n", result.ObservationsImported)
		fmt.Printf("  Prompts:      %d\n", result.PromptsImported)
		if result.ChunksSkipped > 0 {
			fmt.Printf("  Skipped:      %d (already imported)\n", result.ChunksSkipped)
		}
		return
	}

	// Export: DB → new chunk
	username := engramsync.GetUsername()
	if doAll {
		fmt.Println("Exporting ALL memories (all projects)...")
	} else {
		fmt.Printf("Exporting memories for project %q...\n", project)
	}
	result, err := syncExport(sy, username, project)
	if err != nil {
		fatal(err)
	}
	if result.IsEmpty {
		if doAll {
			fmt.Println("Nothing new to sync — all memories already exported.")
		} else {
			fmt.Printf("Nothing new to sync for project %q — all memories already exported.\n", project)
		}
		return
	}

	fmt.Printf("Created chunk %s\n", result.ChunkID)
	fmt.Printf("  Sessions:     %d\n", result.SessionsExported)
	fmt.Printf("  Observations: %d\n", result.ObservationsExported)
	fmt.Printf("  Prompts:      %d\n", result.PromptsExported)
	if result.MutationsExported > 0 {
		fmt.Printf("  Mutations:    %d\n", result.MutationsExported)
	}
	fmt.Println()
	fmt.Println("Add to git:")
	fmt.Printf("  git add .engram/ && git commit -m \"sync omnia memories\"\n")
}

// runCloudSync pushes/pulls a project to one or more configured clouds, recording
// per-(cloud,project) sync state under alias-prefixed target keys
// ("<alias>:<project>", via cloudnFor).
//
//   - With explicitAlias (`--cloud-name <alias>`): sync to exactly that named cloud.
//   - Without it: sync to EVERY cloud configured in cloud.json, each under its own
//     alias key, so one local can replicate the same project to several clouds and
//     the dashboard can show where each project lives. When no clouds are configured
//     (env-only setup), it falls back to the legacy default target ("cloud:<project>").
//
// Backward compatibility: the default cloud is named "cloud" (v1 migration and
// `omnia cloud config` with no alias), and cloudnFor("cloud", project) ==
// cloudTargetKeyForProject(project). So the common single-cloud case keeps writing
// the exact same key it always did; no migration of old "cloud:<project>" rows is
// performed (a renamed default keeps its legacy row, which the dashboard still maps
// to the default cloud, while new syncs converge to the alias key).
func runCloudSync(cfg store.Config, s *store.Store, project, syncDir, explicitAlias string, doStatus, doImport bool) {
	aliases, defaultAlias, err := resolveCloudSyncAliases(cfg, explicitAlias)
	if err != nil {
		fatal(err)
		return
	}

	// Keep the store's fan-out registry aligned with cloud.json so future local
	// writes enqueue one pending row per non-default cloud (best-effort — config
	// commands are the primary registration point). Only reconcile the whole set
	// when syncing every cloud; a targeted --cloud-name run must not drop siblings.
	if explicitAlias == "" {
		reconcileCloudFanoutTargets(cfg, s)
	}

	if len(aliases) == 1 {
		alias := aliases[0]
		if err := runCloudSyncTarget(cfg, s, project, syncDir, alias, defaultAlias, cloudSyncApplyEnv(explicitAlias, alias, defaultAlias), doStatus, doImport); err != nil {
			fatal(err)
		}
		return
	}

	var failed []string
	for _, alias := range aliases {
		label := cloudAliasLabel(alias)
		fmt.Printf("== cloud %q ==\n", label)
		if err := runCloudSyncTarget(cfg, s, project, syncDir, alias, defaultAlias, cloudSyncApplyEnv(explicitAlias, alias, defaultAlias), doStatus, doImport); err != nil {
			fmt.Fprintf(os.Stderr, "cloud %q: %v\n", label, err)
			failed = append(failed, label)
		}
	}
	if len(failed) > 0 {
		fatal(fmt.Errorf("cloud sync failed for %d of %d cloud(s): %s", len(failed), len(aliases), strings.Join(failed, ", ")))
	}
}

// resolveCloudSyncAliases returns the cloud aliases to sync to and the resolved
// default alias. With explicitAlias set, the alias must exist in cloud.json (a
// clear error otherwise, mirroring `omnia cloud login`). Without it, every
// configured cloud is returned (sorted); when none are configured, a single empty
// alias is returned to drive the legacy/env-only default target.
func resolveCloudSyncAliases(cfg store.Config, explicitAlias string) (aliases []string, defaultAlias string, err error) {
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("cloud sync config error: %w", err)
	}
	if v2 != nil {
		if v2.Default != "" {
			defaultAlias = v2.Default
		} else if len(v2.Clouds) == 1 {
			for a := range v2.Clouds {
				defaultAlias = a
			}
		}
	}
	if explicitAlias != "" {
		if v2 == nil {
			return nil, "", fmt.Errorf("cloud %q not found; run `omnia cloud add %s --server <url>` first", explicitAlias, explicitAlias)
		}
		if _, ok := v2.getCloud(explicitAlias); !ok {
			return nil, "", fmt.Errorf("cloud %q not found; run `omnia cloud add %s --server <url>` first", explicitAlias, explicitAlias)
		}
		return []string{explicitAlias}, defaultAlias, nil
	}
	if v2 != nil {
		aliases = v2.listClouds()
	}
	if len(aliases) == 0 {
		aliases = []string{""}
	}
	return aliases, defaultAlias, nil
}

// cloudAliasLabel renders an alias for display, mapping the empty (legacy/default)
// alias to the canonical "cloud" target name.
func cloudAliasLabel(alias string) string {
	if strings.TrimSpace(alias) == "" {
		return constants.TargetKeyCloud
	}
	return alias
}

// cloudSyncApplyEnv reports whether ENGRAM_CLOUD_SERVER / ENGRAM_CLOUD_TOKEN env
// overrides apply to this target. They apply only to the legacy/default cloud when
// no explicit --cloud-name was given. An explicitly named cloud is NEVER overridden
// by these env vars, so a stray ENGRAM_CLOUD_TOKEN exported in the shell cannot
// silently redirect credentials to the wrong named cloud.
func cloudSyncApplyEnv(explicitAlias, alias, defaultAlias string) bool {
	if explicitAlias != "" {
		return false
	}
	return alias == "" || alias == defaultAlias
}

// runCloudSyncTarget performs a single cloud's push/pull for project, recording
// sync state under cloudnFor(alias, project). It returns a formatted error instead
// of exiting, so callers can either fatal (single cloud) or aggregate (multi-cloud).
func runCloudSyncTarget(cfg store.Config, s *store.Store, project, syncDir, alias, defaultAlias string, applyEnvOverrides, doStatus, doImport bool) error {
	targetKey := cloudnFor(alias, project)

	// The default cloud keeps consuming the legacy global "cloud" mutation queue
	// (and its derived "cloud:<project>" state), preserving single-cloud/env-only
	// behavior. Every other named cloud drains its OWN alias-scoped fan-out queue,
	// so multi-cloud sync delivers each local write to all clouds independently.
	isDefaultQueue := isDefaultCloudAlias(alias, defaultAlias)
	var mutationKey, chunkKey string
	if !isDefaultQueue {
		mutationKey = targetKey
		chunkKey = targetKey
	}

	cc, err := preflightCloudSyncForAlias(s, cfg, project, alias, applyEnvOverrides, !doStatus)
	if err != nil {
		return err
	}
	transport, err := remote.NewRemoteTransport(cc.ServerURL, cc.Token, project)
	if err != nil {
		if !doStatus {
			markCloudSyncFailure(s, targetKey, err)
		}
		return errors.New(cloudSyncFailureMessage(project, err))
	}
	sy := engramsync.NewCloudWithTransport(s, transport, project)
	sy.SetCloudTargetKeys(mutationKey, chunkKey)

	markCloudHealthy := func() error {
		if err := s.MarkSyncHealthy(targetKey); err != nil {
			return fmt.Errorf("cloud sync health update: %w", err)
		}
		return nil
	}
	markCloudSyncOutcome := func() error {
		// Only this cloud's OWN queue may keep it pending — never a sibling alias
		// that already emptied a shared queue.
		var hasPending bool
		var err error
		if isDefaultQueue {
			hasPending, err = s.HasPendingSyncMutationsForProject(project)
		} else {
			hasPending, err = s.HasPendingSyncMutationsForTarget(mutationKey, project)
		}
		if err != nil {
			return fmt.Errorf("cloud sync state update: %w", err)
		}
		pendingImports := 0
		remoteStatusVerified := false
		if _, _, pending, statusErr := syncStatus(sy); statusErr == nil {
			pendingImports = pending
			remoteStatusVerified = true
		}
		if hasPending || (remoteStatusVerified && pendingImports > 0) {
			if err := s.MarkSyncPending(targetKey); err != nil {
				return fmt.Errorf("cloud sync pending-state update: %w", err)
			}
			return nil
		}
		if !remoteStatusVerified {
			return nil
		}
		return markCloudHealthy()
	}

	if doStatus {
		local, remote, pending, err := syncStatus(sy)
		if err != nil {
			return err
		}
		fmt.Printf("Cloud sync status (project=%q):\n", project)
		fmt.Printf("  Local chunks:    %d\n", local)
		fmt.Printf("  Remote chunks:   %d\n", remote)
		fmt.Printf("  Pending import:  %d\n", pending)
		return nil
	}

	if doImport {
		result, err := syncImport(sy)
		if err != nil {
			markCloudSyncFailure(s, targetKey, err)
			return errors.New(cloudSyncFailureMessage(project, err))
		}
		if err := markCloudSyncOutcome(); err != nil {
			return err
		}
		if result.ChunksImported == 0 {
			fmt.Println("No new chunks to import.")
			if result.ChunksSkipped > 0 {
				fmt.Printf("  (%d chunks already imported)\n", result.ChunksSkipped)
			}
			return nil
		}
		fmt.Printf("Imported %d new remote chunk(s) for project %q\n", result.ChunksImported, project)
		fmt.Printf("  Sessions:     %d\n", result.SessionsImported)
		fmt.Printf("  Observations: %d\n", result.ObservationsImported)
		fmt.Printf("  Prompts:      %d\n", result.PromptsImported)
		if result.ChunksSkipped > 0 {
			fmt.Printf("  Skipped:      %d (already imported)\n", result.ChunksSkipped)
		}
		return nil
	}

	// Export: DB → new chunk
	username := engramsync.GetUsername()
	fmt.Printf("Exporting memories for project %q to cloud...\n", project)
	result, err := syncExport(sy, username, project)
	if err != nil {
		markCloudSyncFailure(s, targetKey, err)
		return errors.New(cloudSyncFailureMessage(project, err))
	}
	if err := markCloudSyncOutcome(); err != nil {
		return err
	}
	if result.IsEmpty {
		fmt.Printf("Nothing new to sync for project %q — all memories already exported.\n", project)
		return nil
	}
	fmt.Printf("Created chunk %s\n", result.ChunkID)
	fmt.Printf("  Sessions:     %d\n", result.SessionsExported)
	fmt.Printf("  Observations: %d\n", result.ObservationsExported)
	fmt.Printf("  Prompts:      %d\n", result.PromptsExported)
	if result.MutationsExported > 0 {
		fmt.Printf("  Mutations:    %d\n", result.MutationsExported)
	}
	fmt.Printf("Cloud sync complete for project %q.\n", project)
	return nil
}

func printSyncUsage() {
	fmt.Println("usage: omnia sync [--import | --status] [--all] [--cloud [--cloud-name ALIAS] --project PROJECT]")
	fmt.Println("Local sync exports project-scoped chunks to .engram/ by default.")
	fmt.Println("Cloud sync requires an explicit --project and never runs from --help.")
	fmt.Println("--cloud-name ALIAS targets one named cloud; with --cloud and no --cloud-name the")
	fmt.Println("project is synced to every configured cloud (see `omnia cloud list`).")
}

// storeAdapter wraps *store.Store to satisfy obsidian.StoreReader.
// The real store.Stats() returns (*store.Stats, error); the interface expects *store.Stats.
type storeAdapter struct{ s *store.Store }

func (a *storeAdapter) Export() (*store.ExportData, error) { return a.s.Export() }
func (a *storeAdapter) Stats() *store.Stats {
	st, _ := a.s.Stats()
	return st
}

func cmdObsidianExport(cfg store.Config) {
	// Parse flags
	var (
		vault       string
		project     string
		limit       int
		since       string
		force       bool
		graphConfig string
		watch       bool
		interval    string
	)

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--vault":
			if i+1 < len(os.Args) {
				vault = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		case "--since":
			if i+1 < len(os.Args) {
				since = os.Args[i+1]
				i++
			}
		case "--force":
			force = true
		case "--graph-config":
			if i+1 < len(os.Args) {
				graphConfig = os.Args[i+1]
				i++
			}
		case "--watch":
			watch = true
		case "--interval":
			if i+1 < len(os.Args) {
				interval = os.Args[i+1]
				i++
			}
		default:
			fmt.Fprintf(os.Stderr, "omnia: unknown flag: %s\n", os.Args[i])
			exitFunc(1)
		}
	}

	if vault == "" {
		fmt.Fprintln(os.Stderr, "error: flag --vault is required")
		exitFunc(1)
	}

	// Default --graph-config to "preserve"
	if graphConfig == "" {
		graphConfig = string(obsidian.GraphConfigPreserve)
	}

	graphMode, err := obsidian.ParseGraphConfigMode(graphConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --graph-config value: %s (accepted: preserve, force, skip)\n", graphConfig)
		exitFunc(1)
	}

	// Validate --interval requires --watch
	if interval != "" && !watch {
		fmt.Fprintln(os.Stderr, "error: --interval requires --watch")
		exitFunc(1)
	}

	// Parse and validate --interval (default 10m when --watch is set)
	var watchInterval time.Duration
	if watch {
		intervalStr := interval
		if intervalStr == "" {
			intervalStr = "10m"
		}
		d, parseErr := time.ParseDuration(intervalStr)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --interval value %q: %v\n", intervalStr, parseErr)
			exitFunc(1)
		}
		if d < time.Minute {
			fmt.Fprintf(os.Stderr, "error: --interval must be at least 1m (minimum), got %v\n", d)
			exitFunc(1)
		}
		watchInterval = d
	}

	exportCfg := obsidian.ExportConfig{
		VaultPath:   vault,
		Project:     project,
		Limit:       limit,
		Force:       force,
		GraphConfig: graphMode,
	}

	if since != "" {
		// Try common date formats: full RFC3339, date-only (YYYY-MM-DD)
		var sinceTime time.Time
		var parseErr error
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			sinceTime, parseErr = time.Parse(layout, since)
			if parseErr == nil {
				break
			}
		}
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --since value %q (expected YYYY-MM-DD or RFC3339)\n", since)
			exitFunc(1)
		}
		exportCfg.Since = sinceTime
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	exp := newObsidianExporter(&storeAdapter{s: s}, exportCfg)

	if watch {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		w := newObsidianWatcher(obsidian.WatcherConfig{
			Exporter: exp,
			Interval: watchInterval,
			Logf:     log.Printf,
		})

		if w != nil {
			if runErr := w.Run(ctx); runErr != nil {
				log.Printf("[omnia] shutting down watch mode: %v", runErr)
			} else {
				log.Printf("[omnia] shutting down watch mode")
			}
		}
		exitFunc(0)
		return
	}

	result, err := exp.Export()
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Obsidian export complete\n")
	fmt.Printf("  Created: %d\n", result.Created)
	fmt.Printf("  Updated: %d\n", result.Updated)
	fmt.Printf("  Deleted: %d\n", result.Deleted)
	fmt.Printf("  Skipped: %d\n", result.Skipped)
	fmt.Printf("  Hubs:    %d\n", result.HubsCreated)
	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "  Errors: %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "    - %v\n", e)
		}
	}
}

func cmdProjects(cfg store.Config) {
	// Route: omnia projects list | omnia projects consolidate [--all] [--dry-run]
	subCmd := "list"
	if len(os.Args) > 2 {
		subCmd = os.Args[2]
	}
	switch subCmd {
	case "consolidate":
		cmdProjectsConsolidate(cfg)
	case "prune":
		cmdProjectsPrune(cfg)
	case "list", "":
		cmdProjectsList(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown projects subcommand: %s\n", subCmd)
		fmt.Fprintln(os.Stderr, "usage: omnia projects list")
		fmt.Fprintln(os.Stderr, "       omnia projects consolidate [--all] [--dry-run]")
		fmt.Fprintln(os.Stderr, "       omnia projects prune [--dry-run]")
		exitFunc(1)
	}
}

func cmdProjectsList(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	projects, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects found.")
		return
	}

	fmt.Printf("Projects (%d):\n", len(projects))
	for _, p := range projects {
		sessionWord := "sessions"
		if p.SessionCount == 1 {
			sessionWord = "session"
		}
		promptWord := "prompts"
		if p.PromptCount == 1 {
			promptWord = "prompt"
		}
		fmt.Printf("  %-30s %4d obs   %3d %-9s  %3d %s\n",
			p.Name,
			p.ObservationCount,
			p.SessionCount, sessionWord,
			p.PromptCount, promptWord,
		)
	}
}

// projectGroup represents a set of project names that should be merged.
type projectGroup struct {
	Names     []string
	Canonical string // suggested canonical (most observations)
}

// groupSimilarProjects groups projects by name similarity and shared directories.
// Uses a simple union-find approach.
func groupSimilarProjects(projects []store.ProjectStats) []projectGroup {
	n := len(projects)
	if n == 0 {
		return nil
	}

	// parent[i] holds the root of i's component
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}

	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(x, y int) {
		rx, ry := find(x), find(y)
		if rx != ry {
			parent[rx] = ry
		}
	}

	// Build name-only slice and index map for FindSimilar
	names := make([]string, n)
	nameToIndex := make(map[string]int, n)
	for i, p := range projects {
		names[i] = p.Name
		nameToIndex[p.Name] = i
	}

	// Group by name similarity
	for i := 0; i < n; i++ {
		similar := project.FindSimilar(projects[i].Name, names, 3)
		for _, sm := range similar {
			if j, ok := nameToIndex[sm.Name]; ok {
				union(i, j)
			}
		}
	}

	// Group by shared directory
	dirToProjects := make(map[string][]int)
	for i, p := range projects {
		for _, dir := range p.Directories {
			if dir != "" {
				dirToProjects[dir] = append(dirToProjects[dir], i)
			}
		}
	}
	for _, idxs := range dirToProjects {
		for k := 1; k < len(idxs); k++ {
			union(idxs[0], idxs[k])
		}
	}

	// Collect components
	components := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		components[root] = append(components[root], i)
	}

	// Build groups — skip singletons (no duplicates)
	var groups []projectGroup
	for _, idxs := range components {
		if len(idxs) < 2 {
			continue
		}
		// Suggest the one with most observations as canonical
		bestIdx := idxs[0]
		for _, idx := range idxs[1:] {
			if projects[idx].ObservationCount > projects[bestIdx].ObservationCount {
				bestIdx = idx
			}
		}
		grpNames := make([]string, len(idxs))
		for k, idx := range idxs {
			grpNames[k] = projects[idx].Name
		}
		sort.Strings(grpNames)
		groups = append(groups, projectGroup{
			Names:     grpNames,
			Canonical: projects[bestIdx].Name,
		})
	}
	// Sort groups by canonical name for deterministic output
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Canonical < groups[j].Canonical
	})
	return groups
}

func cmdProjectsConsolidate(cfg store.Config) {
	doAll := false
	dryRun := false
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--all":
			doAll = true
		case "--dry-run":
			dryRun = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	if !doAll {
		// Single-project mode: detect canonical project for cwd, find variants
		cwd, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		canonical := detectProject(cwd)

		allNames, err := s.ListProjectNames()
		if err != nil {
			fatal(err)
		}

		// Check if the detected canonical actually exists in the DB.
		canonicalExists := false
		for _, n := range allNames {
			if n == canonical {
				canonicalExists = true
				break
			}
		}
		if !canonicalExists {
			fmt.Printf("Note: %q has no existing memories. Merging will move memories into this new project name.\n", canonical)
		}

		// Find candidates by name similarity
		similar := project.FindSimilar(canonical, allNames, 3)

		// Also find candidates by shared directory (catches renames like sdd-agent-team → agent-teams-lite)
		allStats, _ := s.ListProjectsWithStats()
		statsMap := make(map[string]store.ProjectStats)
		var cwdDirs []string // directories for the canonical project
		for _, ps := range allStats {
			statsMap[ps.Name] = ps
			if ps.Name == canonical {
				cwdDirs = ps.Directories
			}
		}
		// If canonical has no stats yet, use cwd as its directory
		if len(cwdDirs) == 0 {
			cwdDirs = []string{cwd}
		}
		// Find projects sharing a directory with the canonical
		similarNames := make(map[string]bool)
		for _, sm := range similar {
			similarNames[sm.Name] = true
		}
		for _, ps := range allStats {
			if ps.Name == canonical || similarNames[ps.Name] {
				continue
			}
			for _, d := range ps.Directories {
				for _, cd := range cwdDirs {
					if d == cd {
						similar = append(similar, project.ProjectMatch{
							Name:      ps.Name,
							MatchType: "shared directory",
						})
						similarNames[ps.Name] = true
					}
				}
			}
		}

		if len(similar) == 0 {
			fmt.Printf("No similar project names found for %q. Nothing to consolidate.\n", canonical)
			return
		}

		fmt.Printf("Detected project: %q\n\n", canonical)
		fmt.Printf("Found similar project names:\n")
		for i, sm := range similar {
			obs := 0
			if ps, ok := statsMap[sm.Name]; ok {
				obs = ps.ObservationCount
			}
			fmt.Printf("  [%d] %-30s %3d obs  (%s)\n", i+1, sm.Name, obs, sm.MatchType)
		}

		if dryRun {
			fmt.Printf("\n[dry-run] Would merge %d project(s) into %q\n", len(similar), canonical)
			return
		}

		fmt.Printf("\nSelect which to merge into %q (comma-separated numbers, 'all', or 'none'): ", canonical)
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "none" || answer == "n" || answer == "" {
			fmt.Println("Cancelled.")
			return
		}

		var sources []string
		if answer == "all" || answer == "a" {
			for _, sm := range similar {
				sources = append(sources, sm.Name)
			}
		} else {
			// Parse comma-separated indices
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				idx := 0
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(similar) {
					fmt.Fprintf(os.Stderr, "Invalid selection: %q (expected 1-%d)\n", part, len(similar))
					return
				}
				sources = append(sources, similar[idx-1].Name)
			}
		}

		if len(sources) == 0 {
			fmt.Println("Nothing selected.")
			return
		}

		fmt.Printf("\nMerging %d project(s) into %q...\n", len(sources), canonical)
		result, err := s.MergeProjects(sources, canonical)
		if err != nil {
			fatal(err)
		}

		fmt.Printf("Done! Merged into %q:\n", result.Canonical)
		fmt.Printf("  Observations: %d\n", result.ObservationsUpdated)
		fmt.Printf("  Sessions:     %d\n", result.SessionsUpdated)
		fmt.Printf("  Prompts:      %d\n", result.PromptsUpdated)
		return
	}

	// --all mode: group all projects by similarity + shared directories
	projects, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	groups := groupSimilarProjects(projects)

	if len(groups) == 0 {
		fmt.Println("No similar project name groups found.")
		return
	}

	fmt.Printf("Found %d group(s) of similar project names:\n\n", len(groups))

	// Build stats map for obs counts
	projectStatsMap := make(map[string]store.ProjectStats)
	for _, p := range projects {
		projectStatsMap[p.Name] = p
	}

	for i, g := range groups {
		fmt.Printf("Group %d:\n", i+1)
		for j, name := range g.Names {
			obs := 0
			if ps, ok := projectStatsMap[name]; ok {
				obs = ps.ObservationCount
			}
			marker := "  "
			if name == g.Canonical {
				marker = "→ "
			}
			fmt.Printf("  %s[%d] %-30s %3d obs\n", marker, j+1, name, obs)
		}
		fmt.Printf("  Suggested canonical: %q (→)\n", g.Canonical)

		if dryRun {
			fmt.Printf("  [dry-run] Would merge into %q\n\n", g.Canonical)
			continue
		}

		fmt.Printf("\n  Options:\n")
		fmt.Printf("    all     — merge everything into %q\n", g.Canonical)
		fmt.Printf("    1,3,... — merge only selected numbers into %q\n", g.Canonical)
		fmt.Printf("    rename  — choose a different canonical name\n")
		fmt.Printf("    skip    — don't touch this group\n")
		fmt.Printf("  Choice: ")
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))

		canonical := g.Canonical

		if answer == "skip" || answer == "s" || answer == "n" || answer == "" {
			fmt.Println("  Skipped.")
			fmt.Println()
			continue
		}

		if answer == "rename" || answer == "r" {
			fmt.Printf("  Enter canonical name: ")
			scanInputLine(&canonical)
			canonical = strings.TrimSpace(canonical)
			if canonical == "" {
				fmt.Println("  Empty input, skipping.")
				fmt.Println()
				continue
			}
			answer = "all" // after rename, merge everything into the new name
		}

		// Determine which sources to merge
		var sources []string
		if answer == "all" || answer == "a" || answer == "y" || answer == "yes" {
			for _, name := range g.Names {
				if name != canonical {
					sources = append(sources, name)
				}
			}
		} else {
			// Parse comma-separated indices
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				idx := 0
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(g.Names) {
					fmt.Fprintf(os.Stderr, "  Invalid selection: %q (expected 1-%d)\n", part, len(g.Names))
					fmt.Println()
					continue
				}
				selected := g.Names[idx-1]
				if selected != canonical {
					sources = append(sources, selected)
				}
			}
		}
		if len(sources) == 0 {
			fmt.Println("  Nothing to merge.")
			fmt.Println()
			continue
		}

		result, err := s.MergeProjects(sources, canonical)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error merging: %v\n", err)
			fmt.Println()
			continue
		}
		fmt.Printf("  Merged: %d obs, %d sessions, %d prompts\n\n",
			result.ObservationsUpdated, result.SessionsUpdated, result.PromptsUpdated)
	}
}

func cmdProjectsPrune(cfg store.Config) {
	dryRun := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--dry-run" {
			dryRun = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	allStats, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	// Find projects with 0 observations
	var candidates []store.ProjectStats
	for _, ps := range allStats {
		if ps.ObservationCount == 0 {
			candidates = append(candidates, ps)
		}
	}

	if len(candidates) == 0 {
		fmt.Println("No empty projects to prune.")
		return
	}

	fmt.Printf("Found %d project(s) with 0 observations:\n\n", len(candidates))
	for i, ps := range candidates {
		fmt.Printf("  [%d] %-30s %3d sessions  %3d prompts\n", i+1, ps.Name, ps.SessionCount, ps.PromptCount)
	}

	if dryRun {
		fmt.Printf("\n[dry-run] Would prune %d project(s)\n", len(candidates))
		return
	}

	fmt.Printf("\nSelect which to prune (comma-separated numbers, 'all', or 'none'): ")
	var answer string
	scanInputLine(&answer)
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer == "none" || answer == "n" || answer == "" {
		fmt.Println("Cancelled.")
		return
	}

	var selected []store.ProjectStats
	if answer == "all" || answer == "a" {
		selected = candidates
	} else {
		for _, part := range strings.Split(answer, ",") {
			part = strings.TrimSpace(part)
			idx := 0
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(candidates) {
				fmt.Fprintf(os.Stderr, "Invalid selection: %q (expected 1-%d)\n", part, len(candidates))
				return
			}
			selected = append(selected, candidates[idx-1])
		}
	}

	if len(selected) == 0 {
		fmt.Println("Nothing selected.")
		return
	}

	totalSessions := int64(0)
	totalPrompts := int64(0)
	for _, ps := range selected {
		result, err := s.PruneProject(ps.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error pruning %q: %v\n", ps.Name, err)
			continue
		}
		totalSessions += result.SessionsDeleted
		totalPrompts += result.PromptsDeleted
	}

	fmt.Printf("\nPruned %d project(s): %d sessions, %d prompts removed.\n", len(selected), totalSessions, totalPrompts)
}

func cmdSetup() {
	agents := setupSupportedAgents()

	// If agent name given directly: omnia setup opencode
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
		result, err := setupInstallAgent(os.Args[2])
		if err != nil {
			fatal(err)
		}
		fmt.Printf("✓ Installed %s plugin (%d files)\n", result.Agent, result.Files)
		fmt.Printf("  → %s\n", result.Destination)
		printPostInstall(result)
		return
	}

	// Interactive selection
	fmt.Println("omnia setup — Install agent plugin")
	fmt.Println()
	fmt.Println("Which agent do you want to set up?")
	fmt.Println()

	for i, a := range agents {
		fmt.Printf("  [%d] %s\n", i+1, a.Description)
		fmt.Printf("      Install to: %s\n\n", a.InstallDir)
	}

	fmt.Print("Enter choice (1-", len(agents), "): ")
	var input string
	scanInputLine(&input)

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(agents) {
		fmt.Fprintln(os.Stderr, "Invalid choice.")
		exitFunc(1)
	}

	selected := agents[choice-1]
	fmt.Printf("\nInstalling %s plugin...\n", selected.Name)

	result, err := setupInstallAgent(selected.Name)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("✓ Installed %s plugin (%d files)\n", result.Agent, result.Files)
	fmt.Printf("  → %s\n", result.Destination)
	printPostInstall(result)
}

func printPostInstall(result *setup.Result) {
	switch result.Agent {
	case "opencode":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart OpenCode — plugin + MCP server are ready")
		fmt.Println("  2. The plugin auto-starts the Engram HTTP server when needed")
		if result.TUIPluginEnabled {
			fmt.Println("\nAlso enabled: opencode-subagent-statusline in tui.json — sub-agent activity in the sidebar/footer.")
		}
	case "pi":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Pi so packages and MCP config are reloaded")
		fmt.Println("  2. Verify with: pi list")
	case "claude-code":
		// Offer to add engram tools to the permissions allowlist
		fmt.Print("\nAdd engram tools to ~/.claude/settings.json allowlist?\n")
		fmt.Print("This prevents Claude Code from asking permission on every tool call.\n")
		fmt.Print("Add to allowlist? (y/N): ")
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "y" || answer == "yes" {
			if err := setupAddClaudeCodeAllowlist(); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not update allowlist: %v\n", err)
				fmt.Fprintln(os.Stderr, "  You can add them manually to permissions.allow in ~/.claude/settings.json")
			} else {
				fmt.Println("  ✓ Engram tools added to allowlist")
			}
		} else {
			fmt.Println("  Skipped. You can add them later to permissions.allow in ~/.claude/settings.json")
		}

		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Claude Code — the plugin is active immediately")
		fmt.Println("  2. Verify with: claude plugin list")
		fmt.Println("  3. MCP config written to ~/.claude/mcp/engram.json using absolute binary path")
		fmt.Println("     (survives plugin auto-updates; re-run 'omnia setup claude-code' if you move the binary)")
	case "gemini-cli":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Gemini CLI so MCP config is reloaded")
		fmt.Println("  2. Verify ~/.gemini/settings.json includes mcpServers.engram")
		fmt.Println("  3. Verify ~/.gemini/system.md + ~/.gemini/.env exist for compaction recovery")
	case "codex":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Codex so MCP config is reloaded")
		fmt.Println("  2. Verify ~/.codex/config.toml has [mcp_servers.engram]")
		fmt.Println("  3. Verify model_instructions_file + experimental_compact_prompt_file are set")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Printf(`omnia v%s — Persistent memory for AI coding agents

Usage:
  omnia <command> [arguments]

Commands:
  serve [port]       Start HTTP API server (default: 7437)
  mcp [--tools=PROFILE] [--project NAME]
                     Start MCP server (stdio transport, for any AI agent)
                       Profiles: agent (15 tools), admin (4 tools), all (default, 19)
                       Combine: --tools=agent,admin or pick individual tools
                       Example: omnia mcp --tools=agent
                       --project NAME  Set process-level default project (overrides cwd detection).
                                       Also accepted as OMNIA_PROJECT=NAME env var.
  tui                Launch interactive terminal UI
  search <query>     Search memories [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]
  save <title> <msg> Save a memory  [--type TYPE] [--project PROJECT] [--scope SCOPE]
  delete <obs_id>    Delete an observation [--hard] (soft-delete by default; --hard removes permanently)
  delete session <id>
                     Delete a session by ID (session must have no observations)
  delete prompt <id>
                     Delete a prompt by ID (permanent)
  delete project <name> [--hard]
                     Cascade-delete a project: soft-deletes observations (or hard if --hard),
                     removes prompts; with --hard also removes sessions
  timeline <obs_id>  Show chronological context around an observation [--before N] [--after N]
  conflicts <sub>   Inspect and manage memory conflict relations
                       list     [--project P]  [--status S]  [--since RFC3339]  [--limit N]
                       show     <relation_id>
                       stats    [--project P]
                       scan     [--project P]  [--since RFC3339]  [--dry-run]  [--apply]  [--max-insert N]
                                [--semantic]  [--concurrency N]  [--timeout-per-call SECONDS]
                                [--max-semantic N]  [--yes]
                       deferred [--status S]  [--limit N]  [--inspect SYNC_ID]  [--replay]
  doctor             Run read-only operational diagnostics [--json] [--project P] [--check CODE]
  context [project]  Show recent context from previous sessions
  stats              Show memory system statistics
  export [file]      Export all memories to JSON (default: omnia-export.json)
  import <file>      Import memories from a JSON export file
  projects list      List all projects with observation, session, and prompt counts
  projects consolidate [--all] [--dry-run]
                     Merge similar project names into one canonical name
                       --all      Scan ALL projects for similar name groups
                       --dry-run  Preview what would be merged (no changes)
  setup [agent]      Install/setup agent integration (opencode, pi, claude-code, gemini-cli, codex)
  sync               Export new memories as compressed chunk to .engram/
                         --import   Import new chunks from .engram/ into local DB
                         --status   Show sync status
                         --project  Filter export to a specific project
                         --all      Export ALL projects (ignore directory-based filter)
		                 --cloud    Run sync against configured cloud endpoint (requires explicit --project)
	  cloud <subcommand> Cloud integration commands (opt-in)
	                        status     Show cloud config status
	                        enroll     Enroll a project for cloud sync
	                        config     Set cloud server URL
	                        serve      Run cloud backend + dashboard
  obsidian-export    Export memories to an Obsidian-compatible markdown vault
                       --vault         Path to Obsidian vault root (required)
                       --project       Filter export to a single project (optional)
                       --limit         Cap exported observations at N (optional)
                       --since         Export only observations after this date, e.g. 2026-01-01 (optional)
                       --force         Ignore incremental state, full re-export (optional)
                       --graph-config  Graph layout mode: preserve|force|skip (default: preserve)
                       --watch         Enable auto-sync mode (runs on interval until Ctrl+C)
                       --interval      Sync interval for --watch mode (default: 10m, minimum: 1m)
  dashboard          Start the unified local web dashboard [--port 7800] [--data-dir DIR]
  embed              Reconcile the optional semantic-search embeddings store [--force]
  collect            Run external-source collectors (GitHub, Discord, ...) into the local daemon
                       collect status   Show collector daemon health and source cursors
  migrate            Migrate a legacy ~/.engram data directory to ~/.omnia (safe, copy-only)
                       [--from DIR] [--to DIR]

  version            Print version
  help               Show this help

Environment:
  (Every OMNIA_* variable also accepts its legacy ENGRAM_* name as a fallback,
   so existing setups keep working.)
  OMNIA_DATA_DIR     Override data directory (default: ~/.omnia)
  OMNIA_PORT         Override HTTP server port (default: 7437)
  OMNIA_PROJECT      Process-level default project override.
                     For "omnia serve": fallback for GET /sync/status with no project param.
                     For "omnia mcp": sets DefaultProject, overriding cwd detection for all tools.
  OMNIA_HTTP_TOKEN   Optional Bearer auth for local HTTP server (omnia serve).
                     When set, the following routes require Authorization: Bearer <token>:
                       DELETE /sessions/{id}, DELETE /observations/{id}, DELETE /prompts/{id},
                       GET /export, POST /import, POST /projects/migrate
                     Comparison is constant-time. Token is read per-request (no restart needed).
                     When unset, all routes are open (zero-config default).
  OMNIA_TIMEZONE     Timezone for timestamp display in TUI and cloud dashboard.
                     Accepts any IANA zone name (e.g. America/New_York, Europe/Berlin).
                     Falls back to system local time when unset or invalid.
  OMNIA_AGENT_CLI    LLM runner for conflicts scan --semantic (claude or opencode)
  OMNIA_CLOUD_AUTOSYNC
                     Set to 1 to enable background autosync; also requires
                     OMNIA_CLOUD_TOKEN and OMNIA_CLOUD_SERVER
  OMNIA_CLOUD_SERVER
                     Cloud server URL used by autosync and omnia sync --cloud
  OMNIA_DATABASE_URL
                     Postgres DSN for omnia cloud serve
  OMNIA_CLOUD_HOST   Bind host for omnia cloud serve (default: 127.0.0.1)
  OMNIA_CLOUD_MAX_PUSH_BYTES
                     Max cloud push payload bytes (default: 8388608)
  OMNIA_CLOUD_TOKEN  Bearer token required in authenticated cloud serve mode
  OMNIA_CLOUD_INSECURE_NO_AUTH
                     Set to 1 ONLY for local insecure cloud serve mode (no auth)
                     Cannot be combined with OMNIA_CLOUD_TOKEN
                     Cannot be combined with OMNIA_CLOUD_ADMIN
  OMNIA_CLOUD_ALLOWED_PROJECTS
                     Comma-separated project allowlist enforced by cloud server.
                     Required for cloud serve in BOTH token auth and insecure no-auth mode.
                     Use * to allow all projects (dev/internal deploys).
  OMNIA_JWT_SECRET   Required in authenticated cloud serve mode (OMNIA_CLOUD_TOKEN set);
                     must be explicitly set to a non-default value
  OMNIA_CLOUD_ADMIN  Optional admin-only dashboard token in authenticated mode
                     Ignored/rejected in insecure mode (OMNIA_CLOUD_INSECURE_NO_AUTH=1)

MCP Configuration (add to your agent's config):
  {
    "mcp": {
      "omnia": {
        "type": "stdio",
        "command": "omnia",
        "args": ["mcp", "--tools=agent"]
      }
    }
  }
`, version)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "omnia: %s\n", err)
	exitFunc(1)
}

// resolveHomeFallback tries platform-specific environment variables to find
// a home directory when os.UserHomeDir() fails. This commonly happens on
// Windows when omnia is launched as an MCP subprocess without full env
// propagation.
func resolveHomeFallback() string {
	// Windows: try common env vars that might be set even when
	// %USERPROFILE% is missing.
	for _, env := range []string{"USERPROFILE", "HOME", "LOCALAPPDATA"} {
		if v := os.Getenv(env); v != "" {
			if env == "LOCALAPPDATA" {
				// LOCALAPPDATA is C:\Users\<user>\AppData\Local — go up two levels.
				parent := filepath.Dir(filepath.Dir(v))
				if parent != "." && parent != v {
					return parent
				}
			}
			return v
		}
	}

	// Unix: $HOME should always work, but try passwd-style fallback.
	if v := os.Getenv("HOME"); v != "" {
		return v
	}

	return ""
}

// migrateOrphanedDB checks for engram databases that ended up in wrong
// locations (e.g. drive root on Windows when UserHomeDir failed silently)
// and moves them to the correct location if the correct location has no DB.
func migrateOrphanedDB(correctDir string) {
	correctDB := filepath.Join(correctDir, datadir.DBFilename)

	// If a database already exists at the correct location (omnia.db, or a legacy
	// engram.db kept in place for compatibility), nothing to migrate.
	if _, err := os.Stat(datadir.DBPath(correctDir)); err == nil {
		return
	}

	// Known wrong locations: relative ".engram" resolved from common roots.
	// On Windows this typically ends up at C:\.engram or D:\.engram.
	candidates := []string{
		filepath.Join(string(filepath.Separator), ".engram", "engram.db"),
	}

	// On Windows, check all drive letter roots.
	if filepath.Separator == '\\' {
		for _, drive := range "CDEFGHIJ" {
			candidates = append(candidates,
				filepath.Join(string(drive)+":\\", ".engram", "engram.db"),
			)
		}
	}

	for _, candidate := range candidates {
		if candidate == correctDB {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		// Found an orphaned DB — migrate it.
		log.Printf("[omnia] found orphaned database at %s, migrating to %s", candidate, correctDB)

		if err := os.MkdirAll(correctDir, 0755); err != nil {
			log.Printf("[omnia] migration failed (create dir): %v", err)
			return
		}

		// Move DB and WAL/SHM files if they exist.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			src := candidate + suffix
			dst := correctDB + suffix
			if _, statErr := os.Stat(src); statErr != nil {
				continue
			}
			if renameErr := os.Rename(src, dst); renameErr != nil {
				log.Printf("[omnia] migration failed (move %s): %v", filepath.Base(src), renameErr)
				return
			}
		}

		// Clean up empty orphaned directory.
		orphanDir := filepath.Dir(candidate)
		entries, _ := os.ReadDir(orphanDir)
		if len(entries) == 0 {
			os.Remove(orphanDir)
		}

		log.Printf("[omnia] migration complete — memories recovered")
		return
	}
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
