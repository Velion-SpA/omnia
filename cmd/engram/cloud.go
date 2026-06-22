package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/cloud"
	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudserver"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/cloud/constants"
	"github.com/velion/omnia/internal/cloud/remote"
	"github.com/velion/omnia/internal/store"
	engramsync "github.com/velion/omnia/internal/sync"
)

type cloudServerRuntime interface {
	Start() error
}

type defaultCloudRuntime struct {
	server *cloudserver.CloudServer
	store  *cloudstore.CloudStore
}

func (r *defaultCloudRuntime) Start() error {
	defer r.store.Close()
	return r.server.Start()
}

var newCloudRuntime = func(cfg cloud.Config) (cloudServerRuntime, error) {
	cs, err := cloudstore.New(cfg)
	if err != nil {
		return nil, err
	}
	allowedProjects := normalizeAllowedProjects(cfg.AllowedProjects)
	if err := backfillAllowedProjectMutationChunks(context.Background(), cs, allowedProjects); err != nil {
		_ = cs.Close()
		return nil, err
	}
	projectAuth := auth.NewProjectScopeAuthorizer(allowedProjects)
	token := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN"))
	cs.SetDashboardAllowedProjects(allowedProjects)
	insecureNoAuth := token == "" && envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	var authenticator cloudserver.Authenticator
	if !insecureNoAuth {
		authSvc, err := auth.NewService(cs, cfg.JWTSecret)
		if err != nil {
			_ = cs.Close()
			return nil, err
		}
		authSvc.SetBearerToken(token)
		authSvc.SetAllowedProjects(allowedProjects)
		authSvc.SetDashboardSessionTokens([]string{cfg.AdminToken})
		authenticator = authSvc
	}
	return &defaultCloudRuntime{
		server: cloudserver.New(
			cs,
			authenticator,
			cfg.Port,
			cloudserver.WithHost(cfg.BindHost),
			cloudserver.WithProjectAuthorizer(projectAuth),
			cloudserver.WithDashboardAdminToken(cfg.AdminToken),
			cloudserver.WithMaxPushBodyBytes(cfg.MaxPushBodyBytes),
		),
		store: cs,
	}, nil
}

func backfillAllowedProjectMutationChunks(ctx context.Context, cs *cloudstore.CloudStore, projects []string) error {
	for _, project := range projects {
		report, err := cs.BackfillMutationChunks(ctx, project, true)
		if err != nil {
			return fmt.Errorf("cloud repair materialize-mutations for project %q: %w", project, err)
		}
		if report.CandidateMutations > 0 || report.ChunksInserted > 0 {
			fmt.Fprintf(os.Stderr,
				"engram cloud repair materialize-mutations: project=%s candidates=%d already_materialized=%d chunks_planned=%d chunks_inserted=%d\n",
				report.Project, report.CandidateMutations, report.AlreadyMaterialized, report.ChunksPlanned, report.ChunksInserted,
			)
		}
	}
	return nil
}

var runUpgradeBootstrap = func(s *store.Store, project string, cc *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
	transport, err := remote.NewRemoteTransport(cc.ServerURL, cc.Token, project)
	if err != nil {
		return nil, err
	}
	return engramsync.BootstrapProject(s, transport, engramsync.UpgradeBootstrapOptions{Project: project, CreatedBy: "engram-cloud-upgrade"})
}

type cloudConfig struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

// cloudEntry holds one named cloud's connection details.
type cloudEntry struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	Username  string `json:"username,omitempty"`
}

// cloudConfigV2 is the v2 cloud.json shape.
// Backward-compat: if the file contains "server_url"/"token" at the top level (v1),
// loadCloudConfigV2 migrates it to clouds["cloud"] / default="cloud" so all
// existing target keys ("cloud", "cloud:<project>") remain valid.
type cloudConfigV2 struct {
	Clouds  map[string]cloudEntry `json:"clouds"`
	Default string                `json:"default,omitempty"`
}

// defaultCloudEntry returns the single/default cloud entry.
// Returns nil if no clouds are configured or the default is ambiguous.
func (c *cloudConfigV2) defaultCloudEntry() *cloudEntry {
	if c == nil || len(c.Clouds) == 0 {
		return nil
	}
	alias := c.Default
	if alias == "" {
		// If only one cloud, use it.
		if len(c.Clouds) == 1 {
			for _, e := range c.Clouds {
				cp := e
				return &cp
			}
		}
		return nil
	}
	e, ok := c.Clouds[alias]
	if !ok {
		return nil
	}
	return &e
}

func (c *cloudConfigV2) getCloud(alias string) (*cloudEntry, bool) {
	if c == nil {
		return nil, false
	}
	e, ok := c.Clouds[alias]
	if !ok {
		return nil, false
	}
	return &e, true
}

func (c *cloudConfigV2) listClouds() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Clouds))
	for alias := range c.Clouds {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

func cmdCloud(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud <subcommand> [options]")
		fmt.Fprintln(os.Stderr, "supported subcommands: status, enroll, config, serve, upgrade, repair, signup, login, refresh, add, list, remove, default")
		exitFunc(1)
	}
	if os.Args[2] == "--help" || os.Args[2] == "-h" || os.Args[2] == "help" {
		fmt.Println("usage: engram cloud <subcommand> [options]")
		fmt.Println("supported subcommands: status, enroll, config, serve, upgrade, repair, signup, login, refresh, add, list, remove, default")
		return
	}

	switch os.Args[2] {
	case "status":
		cmdCloudStatus(cfg)
	case "enroll":
		cmdCloudEnroll(cfg)
	case "config":
		cmdCloudConfig(cfg)
	case "serve":
		cmdCloudServe()
	case "upgrade":
		cmdCloudUpgrade(cfg)
	case "repair":
		cmdCloudRepair()
	case "signup":
		cmdCloudSignup(cfg)
	case "login":
		cmdCloudLogin(cfg)
	case "refresh":
		cmdCloudRefresh(cfg)
	case "add":
		cmdCloudAdd(cfg)
	case "list":
		cmdCloudList(cfg)
	case "remove":
		cmdCloudRemove(cfg)
	case "default":
		cmdCloudDefault(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "supported subcommands: status, enroll, config, serve, upgrade, repair, signup, login, refresh, add, list, remove, default")
		exitFunc(1)
	}
}

func cmdCloudRepair() {
	if len(os.Args) < 4 || os.Args[3] == "--help" || os.Args[3] == "-h" || os.Args[3] == "help" {
		fmt.Println("usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Println("repairs existing cloud_mutations into compatible cloud_chunks without deleting remote data")
		return
	}
	command := strings.TrimSpace(strings.ToLower(os.Args[3]))
	if command != "materialize-mutations" {
		fmt.Fprintf(os.Stderr, "unknown cloud repair command: %s\n", command)
		fmt.Fprintln(os.Stderr, "supported cloud repair commands: materialize-mutations")
		exitFunc(1)
		return
	}
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	dryRun := hasCloudUpgradeFlag(os.Args[4:], "--dry-run")
	apply := hasCloudUpgradeFlag(os.Args[4:], "--apply")
	if dryRun == apply {
		fmt.Fprintln(os.Stderr, "usage: engram cloud repair materialize-mutations --project <name> (--dry-run|--apply)")
		fmt.Fprintln(os.Stderr, "error: exactly one of --dry-run or --apply is required")
		exitFunc(1)
		return
	}

	cs, err := cloudstore.New(cloud.ConfigFromEnv())
	if err != nil {
		fatal(err)
		return
	}
	defer cs.Close()
	report, err := cs.BackfillMutationChunks(context.Background(), project, apply)
	if err != nil {
		fatal(err)
		return
	}
	encoded, err := jsonMarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(encoded))
}

func cmdCloudUpgrade(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade <doctor|repair|bootstrap|status|rollback> --project <name>")
		exitFunc(1)
		return
	}
	command := strings.TrimSpace(strings.ToLower(os.Args[3]))
	if command == "--help" || command == "-h" || command == "help" {
		fmt.Println("engram cloud upgrade")
		fmt.Println("workflow: doctor -> repair -> bootstrap -> status/rollback")
		fmt.Println("cloud is opt-in replication/shared access; local SQLite remains source of truth")
		fmt.Println("usage: engram cloud upgrade <doctor|repair|bootstrap|status|rollback> --project <name>")
		return
	}
	switch command {
	case "doctor":
		cmdCloudUpgradeDoctor(cfg)
	case "repair":
		cmdCloudUpgradeRepair(cfg)
	case "bootstrap":
		cmdCloudUpgradeBootstrap(cfg)
	case "status":
		cmdCloudUpgradeStatus(cfg)
	case "rollback":
		cmdCloudUpgradeRollback(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud upgrade command: %s\n", command)
		fmt.Fprintln(os.Stderr, "supported cloud upgrade commands: doctor, repair, bootstrap, status, rollback")
		exitFunc(1)
	}
}

func cmdCloudUpgradeDoctor(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade doctor --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	cloudConfigured := false
	if cc, cfgErr := resolveCloudRuntimeConfig(cfg); cfgErr == nil {
		if cc != nil {
			if validated, err := validateCloudServerURL(cc.ServerURL); err == nil && strings.TrimSpace(validated) != "" {
				cloudConfigured = true
			}
		}
	}
	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor enrollment check: %w", err))
		return
	}
	policyDenied, err := cloudUpgradePolicyDenied(s, project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor policy check: %w", err))
		return
	}

	report, err := engramsync.DiagnoseCloudUpgrade(engramsync.UpgradeDiagnosisInput{
		Project:         project,
		CloudConfigured: cloudConfigured,
		ProjectEnrolled: enrolled,
		PolicyDenied:    policyDenied,
	})
	if err != nil {
		fatal(err)
		return
	}

	legacyReport, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade doctor legacy mutation diagnosis: %w", err))
		return
	}
	if legacyReport.BlockedCount > 0 {
		first := legacyReport.Findings[0]
		report = engramsync.UpgradeDiagnosisReport{
			Status:  engramsync.UpgradeStatusBlocked,
			Class:   engramsync.UpgradeReasonClassBlocked,
			Code:    store.UpgradeReasonBlockedLegacyMutationManual,
			Message: fmt.Sprintf("manual-action-required: %s (seq=%d entity=%s op=%s)", first.Message, first.Seq, first.Entity, first.Op),
		}
	} else if legacyReport.RepairableCount > 0 {
		report = engramsync.UpgradeDiagnosisReport{
			Status:  engramsync.UpgradeStatusBlocked,
			Class:   engramsync.UpgradeReasonClassRepairable,
			Code:    store.UpgradeReasonRepairableLegacyMutationPayload,
			Message: fmt.Sprintf("project %q has %d repairable legacy mutation payload issue(s); run `engram cloud upgrade repair --project %s --apply`", project, legacyReport.RepairableCount, project),
		}
	}

	stage := store.UpgradeStageDoctorBlocked
	if report.Status == engramsync.UpgradeStatusReady {
		stage = store.UpgradeStageDoctorReady
	}
	_ = s.SaveCloudUpgradeState(store.CloudUpgradeState{
		Project:          project,
		Stage:            stage,
		RepairClass:      report.Class,
		LastErrorCode:    report.Code,
		LastErrorMessage: report.Message,
	})

	fmt.Printf("project: %s\n", project)
	fmt.Printf("status: %s\n", report.Status)
	fmt.Printf("class: %s\n", report.Class)
	fmt.Printf("reason_code: %s\n", report.Code)
	fmt.Printf("message: %s\n", report.Message)
}

func cloudUpgradePolicyDenied(s *store.Store, project string) (bool, error) {
	targets := []string{cloudTargetKeyForProject(project)}
	if cloudTargetKeyForProject(project) != constants.TargetKeyCloud {
		targets = append(targets, constants.TargetKeyCloud)
	}
	for _, targetKey := range targets {
		state, err := s.GetSyncState(targetKey)
		if err != nil {
			return false, err
		}
		if state == nil {
			continue
		}
		if strings.TrimSpace(derefString(state.ReasonCode)) == constants.ReasonPolicyForbidden {
			return true, nil
		}
	}
	return false, nil
}

func parseCloudUpgradeProjectArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) != "--project" {
			continue
		}
		if i+1 >= len(args) {
			return ""
		}
		project, _ := store.NormalizeProject(args[i+1])
		return strings.TrimSpace(project)
	}
	return ""
}

func cmdCloudUpgradeRepair(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade repair --project <name> [--dry-run|--apply]")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	apply := hasCloudUpgradeFlag(os.Args[4:], "--apply")
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	report, err := s.RepairCloudUpgrade(project, apply)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("class: %s\n", report.Class)
	fmt.Printf("reason_code: %s\n", report.ReasonCode)
	fmt.Printf("message: %s\n", report.Message)
	fmt.Printf("applied: %t\n", report.Applied)
}

func cmdCloudUpgradeBootstrap(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade bootstrap --project <name> [--resume]")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fatal(err)
		return
	}
	if cc == nil || strings.TrimSpace(cc.ServerURL) == "" {
		fatal(fmt.Errorf("cloud upgrade bootstrap requires configured cloud server"))
		return
	}
	validatedURL, err := validateCloudServerURL(cc.ServerURL)
	if err != nil {
		fatal(fmt.Errorf("invalid cloud runtime server URL: %w", err))
		return
	}
	cc.ServerURL = validatedURL
	if err := captureUpgradeSnapshotBeforeBootstrap(s, cfg, project); err != nil {
		fatal(err)
		return
	}
	legacyReport, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		fatal(fmt.Errorf("cloud upgrade bootstrap legacy mutation diagnosis: %w", err))
		return
	}
	if legacyReport.BlockedCount > 0 {
		first := legacyReport.Findings[0]
		fatal(fmt.Errorf("legacy mutation payloads require manual action before bootstrap (seq=%d entity=%s op=%s): %s", first.Seq, first.Entity, first.Op, first.Message))
		return
	}
	if legacyReport.RepairableCount > 0 {
		fatal(fmt.Errorf("legacy mutation payloads require repair before bootstrap: run `engram cloud upgrade repair --project %s --apply`", project))
		return
	}

	result, err := runUpgradeBootstrap(s, project, cc)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", result.Stage)
	fmt.Printf("resumed: %t\n", result.Resumed)
	fmt.Printf("noop: %t\n", result.NoOp)
}

func captureUpgradeSnapshotBeforeBootstrap(s *store.Store, cfg store.Config, project string) error {
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return fmt.Errorf("load cloud upgrade state before bootstrap snapshot: %w", err)
	}
	if state != nil {
		snapshot := state.Snapshot
		if snapshot.CloudConfigPresent || strings.TrimSpace(snapshot.CloudConfigJSON) != "" || snapshot.ProjectEnrolled {
			return nil
		}
	}

	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		return fmt.Errorf("load project enrollment before bootstrap snapshot: %w", err)
	}

	var snapshot store.CloudUpgradeSnapshot
	configBytes, err := os.ReadFile(cloudConfigPath(cfg))
	if err == nil {
		snapshot.CloudConfigPresent = true
		snapshot.CloudConfigJSON = string(configBytes)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read cloud config for bootstrap snapshot: %w", err)
	}
	snapshot.ProjectEnrolled = enrolled

	next := store.CloudUpgradeState{Project: project, Stage: store.UpgradeStagePlanned, RepairClass: store.UpgradeRepairClassNone, Snapshot: snapshot}
	if state != nil {
		next = *state
		next.Snapshot = snapshot
	}
	if err := s.SaveCloudUpgradeState(next); err != nil {
		return fmt.Errorf("persist pre-bootstrap rollback snapshot: %w", err)
	}
	return nil
}

func cmdCloudUpgradeStatus(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade status --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		fatal(err)
		return
	}
	if state == nil {
		fmt.Printf("project: %s\n", project)
		fmt.Printf("stage: %s\n", store.UpgradeStagePlanned)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", state.Stage)
	fmt.Printf("class: %s\n", state.RepairClass)
	fmt.Printf("reason_code: %s\n", strings.TrimSpace(state.LastErrorCode))
	fmt.Printf("reason_message: %s\n", strings.TrimSpace(state.LastErrorMessage))
}

func cmdCloudUpgradeRollback(cfg store.Config) {
	project := parseCloudUpgradeProjectArg(os.Args[4:])
	if project == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud upgrade rollback --project <name>")
		fmt.Fprintln(os.Stderr, "error: --project is required")
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		fatal(err)
		return
	}
	if state == nil {
		fatal(fmt.Errorf("rollback requires existing upgrade checkpoint state"))
		return
	}
	canRollback, err := s.CanRollbackCloudUpgrade(project)
	if err != nil {
		fatal(err)
		return
	}
	if !canRollback {
		fmt.Fprintln(os.Stderr, "rollback is unavailable post-bootstrap; use explicit disconnect/unenroll flows")
		exitFunc(1)
		return
	}
	if state.Snapshot.CloudConfigPresent {
		if err := os.WriteFile(cloudConfigPath(cfg), []byte(state.Snapshot.CloudConfigJSON), 0o644); err != nil {
			fatal(err)
			return
		}
	} else {
		_ = os.Remove(cloudConfigPath(cfg))
	}
	rolledBack, err := engramsync.RollbackProject(s, engramsync.UpgradeRollbackOptions{Project: project})
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("project: %s\n", project)
	fmt.Printf("stage: %s\n", rolledBack.Stage)
}

func hasCloudUpgradeFlag(args []string, flag string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == flag {
			return true
		}
	}
	return false
}

func cmdCloudStatus(cfg store.Config) {
	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: unable to read cloud runtime config: %v\n", err)
		exitFunc(1)
		return
	}
	if cc == nil || cc.ServerURL == "" {
		fmt.Println("Cloud status: not configured")
		return
	}
	validatedURL, err := validateCloudServerURL(cc.ServerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid cloud runtime server URL: %v\n", err)
		exitFunc(1)
		return
	}
	cc.ServerURL = validatedURL
	token := strings.TrimSpace(cc.Token)
	insecureNoAuth := envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	fmt.Printf("Cloud status: configured (target=%s)\n", constants.TargetKeyCloud)
	fmt.Printf("Server: %s\n", cc.ServerURL)
	if token == "" {
		if insecureNoAuth {
			fmt.Println("Auth status: ready (insecure local-dev mode: ENGRAM_CLOUD_INSECURE_NO_AUTH=1)")
			fmt.Println("Sync readiness: ready for explicit --project sync (project must be enrolled)")
			fmt.Println("Warning: bearer auth is disabled in insecure mode; do not use in production")
			printCloudStatusDaemonProbe()
			printCloudStatusSyncDiagnostic(cfg)
			return
		}
		fmt.Println("Auth status: token not configured (client token is optional at preflight)")
		fmt.Println("Sync readiness: ready to attempt explicit --project sync (project must be enrolled)")
		fmt.Println("Hint: if the remote server enforces bearer auth, set ENGRAM_CLOUD_TOKEN")
		printCloudStatusDaemonProbe()
		printCloudStatusSyncDiagnostic(cfg)
		return
	}
	fmt.Println("Auth status: ready (token provided via runtime cloud config)")
	fmt.Println("Sync readiness: ready for explicit --project sync (project must be enrolled)")
	printCloudStatusDaemonProbe()
	printCloudStatusSyncDiagnostic(cfg)
}

func printCloudStatusSyncDiagnostic(cfg store.Config) {
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err != nil {
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fmt.Printf("Sync diagnostic: unavailable (%v)\n", err)
		return
	}
	defer s.Close()
	state, err := s.GetSyncState(constants.TargetKeyCloud)
	if err != nil || state == nil {
		return
	}
	code := strings.TrimSpace(derefString(state.ReasonCode))
	message := strings.TrimSpace(derefString(state.ReasonMessage))
	if code == "" && message == "" {
		return
	}
	fmt.Printf("Sync diagnostic: %s\n", state.Lifecycle)
	if code != "" {
		fmt.Printf("reason_code: %s\n", code)
	}
	if message != "" {
		fmt.Printf("reason_message: %s\n", message)
	}
}

// cmdCloudEnroll enrolls a project for cloud sync. Enrollment is project-scoped in
// the local store (sync_enrolled_projects has no per-cloud dimension), so it gates
// replication to EVERY configured cloud. The optional --cloud-name <alias> makes
// the command cloud-aware: it validates that the named cloud exists in cloud.json
// (failing fast otherwise) and reports which cloud the project is intended for. It
// does not create per-cloud enrollment state, because none exists.
func cmdCloudEnroll(cfg store.Config) {
	args := os.Args[3:]
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "--help", "-h", "help":
			fmt.Println("usage: engram cloud enroll <project> [--cloud-name <alias>]")
			fmt.Println("Enroll a local-first project for explicit cloud replication.")
			fmt.Println("Enrollment is project-scoped and gates sync to every configured cloud;")
			fmt.Println("--cloud-name selects (and validates) the intended cloud for confirmation.")
			return
		}
	}

	projectName := ""
	cloudName := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cloud-name":
			if i+1 < len(args) {
				cloudName = strings.TrimSpace(args[i+1])
				i++
			}
		default:
			if projectName == "" && !strings.HasPrefix(args[i], "-") {
				projectName = strings.TrimSpace(args[i])
			}
		}
	}

	if projectName == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud enroll <project> [--cloud-name <alias>]")
		exitFunc(1)
		return
	}

	if cloudName != "" {
		v2, err := loadCloudConfigV2(cfg)
		if err != nil {
			fatal(err)
			return
		}
		if v2 == nil {
			fmt.Fprintf(os.Stderr, "error: cloud %q not found; run `engram cloud add %s --server <url>` first\n", cloudName, cloudName)
			exitFunc(1)
			return
		}
		if _, ok := v2.getCloud(cloudName); !ok {
			fmt.Fprintf(os.Stderr, "error: cloud %q not found; run `engram cloud add %s --server <url>` first\n", cloudName, cloudName)
			exitFunc(1)
			return
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if err := s.EnrollProject(projectName); err != nil {
		fatal(err)
		return
	}

	if cloudName != "" {
		fmt.Printf("✓ Project %q enrolled for cloud sync (cloud %q)\n", projectName, cloudName)
		return
	}
	fmt.Printf("✓ Project %q enrolled for cloud sync\n", projectName)
}

func cmdCloudConfig(cfg store.Config) {
	fs := flag.NewFlagSet("engram cloud config", flag.ContinueOnError)
	cloudAlias := fs.String("cloud", "", "cloud alias (default: default cloud)")
	server := fs.String("server", "", "cloud server URL")
	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, "usage: engram cloud config --server <url> [--cloud <alias>]")
		exitFunc(1)
		return
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud config --server <url> [--cloud <alias>]")
		exitFunc(1)
		return
	}
	validatedURL, err := validateCloudServerURL(*server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid server URL: %v\n", err)
		exitFunc(1)
		return
	}
	alias := strings.TrimSpace(*cloudAlias)
	if err := saveCloudConfigV2Entry(cfg, alias, validatedURL, "", ""); err != nil {
		fatal(err)
		return
	}
	if alias != "" {
		fmt.Printf("✓ Cloud %q server set to %s\n", alias, validatedURL)
	} else {
		fmt.Printf("✓ Cloud server set to %s\n", validatedURL)
	}
}

func cmdCloudAdd(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud add <alias> --server <url>")
		exitFunc(1)
		return
	}
	alias := strings.TrimSpace(os.Args[3])
	if alias == "" {
		fmt.Fprintln(os.Stderr, "error: alias is required")
		exitFunc(1)
		return
	}
	fs := flag.NewFlagSet("engram cloud add", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL")
	if err := fs.Parse(os.Args[4:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud add <alias> --server <url>")
		fmt.Fprintln(os.Stderr, "error: --server is required")
		exitFunc(1)
		return
	}
	validatedURL, err := validateCloudServerURL(*server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid server URL: %v\n", err)
		exitFunc(1)
		return
	}
	if err := saveCloudConfigV2Entry(cfg, alias, validatedURL, "", ""); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("✓ Cloud %q configured (server=%s)\n", alias, validatedURL)
}

func cmdCloudList(cfg store.Config) {
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		fatal(err)
		return
	}
	if v2 == nil || len(v2.Clouds) == 0 {
		fmt.Println("No clouds configured.")
		return
	}
	for _, alias := range v2.listClouds() {
		entry := v2.Clouds[alias]
		marker := ""
		if alias == v2.Default || (v2.Default == "" && len(v2.Clouds) == 1) {
			marker = " (default)"
		}
		loginStatus := "not logged in"
		if strings.TrimSpace(entry.Token) != "" {
			loginStatus = "logged in"
			if entry.Username != "" {
				loginStatus = fmt.Sprintf("logged in as %s", entry.Username)
			}
		}
		fmt.Printf("  %s%s: %s [%s]\n", alias, marker, entry.ServerURL, loginStatus)
	}
}

func cmdCloudRemove(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud remove <alias>")
		exitFunc(1)
		return
	}
	alias := strings.TrimSpace(os.Args[3])
	if alias == "" {
		fmt.Fprintln(os.Stderr, "error: alias is required")
		exitFunc(1)
		return
	}
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		fatal(err)
		return
	}
	if v2 == nil || v2.Clouds == nil {
		fmt.Fprintf(os.Stderr, "cloud %q not found\n", alias)
		exitFunc(1)
		return
	}
	if _, ok := v2.Clouds[alias]; !ok {
		fmt.Fprintf(os.Stderr, "cloud %q not found\n", alias)
		exitFunc(1)
		return
	}
	delete(v2.Clouds, alias)
	if v2.Default == alias {
		v2.Default = ""
	}
	if err := writeCloudConfigV2(cfg, v2); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("✓ Cloud %q removed\n", alias)
}

func cmdCloudDefault(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram cloud default <alias>")
		exitFunc(1)
		return
	}
	alias := strings.TrimSpace(os.Args[3])
	if alias == "" {
		fmt.Fprintln(os.Stderr, "error: alias is required")
		exitFunc(1)
		return
	}
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		fatal(err)
		return
	}
	if v2 == nil || v2.Clouds == nil {
		fmt.Fprintf(os.Stderr, "cloud %q not found\n", alias)
		exitFunc(1)
		return
	}
	if _, ok := v2.Clouds[alias]; !ok {
		fmt.Fprintf(os.Stderr, "cloud %q not found\n", alias)
		exitFunc(1)
		return
	}
	v2.Default = alias
	if err := writeCloudConfigV2(cfg, v2); err != nil {
		fatal(err)
		return
	}
	fmt.Printf("✓ Default cloud set to %q\n", alias)
}

func validateCloudServerURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.TrimSpace(parsed.RawQuery) != "" {
		return "", fmt.Errorf("query is not allowed")
	}
	if strings.TrimSpace(parsed.Fragment) != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func cmdCloudServe() {
	runtimeCfg := cloud.ConfigFromEnv()
	if err := validateCloudServeAuthConfig(); err != nil {
		fatal(err)
		return
	}
	runtime, err := newCloudRuntime(runtimeCfg)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("Starting Engram cloud server on port %d\n", runtimeCfg.Port)
	if err := runtime.Start(); err != nil {
		fatal(err)
	}
}

func validateCloudServeAuthConfig() error {
	token := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN"))
	adminToken := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_ADMIN"))
	insecureNoAuth := envBool("ENGRAM_CLOUD_INSECURE_NO_AUTH")
	allowlist := normalizeAllowedProjects(cloud.ConfigFromEnv().AllowedProjects)
	jwtSecretEnv := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET"))
	if insecureNoAuth && token != "" {
		return fmt.Errorf("conflicting cloud auth configuration: ENGRAM_CLOUD_INSECURE_NO_AUTH=1 cannot be used together with ENGRAM_CLOUD_TOKEN")
	}
	if token != "" && len(allowlist) > 0 {
		if jwtSecretEnv == "" {
			return fmt.Errorf("authenticated cloud serve requires explicit ENGRAM_JWT_SECRET (non-default); refusing implicit default secret")
		}
		if cloud.IsDefaultJWTSecret(jwtSecretEnv) {
			return fmt.Errorf("authenticated cloud serve requires a non-default ENGRAM_JWT_SECRET; refusing development default")
		}
		return nil
	}
	if insecureNoAuth {
		if len(allowlist) == 0 {
			return fmt.Errorf("cloud project allowlist is required even in insecure mode: set ENGRAM_CLOUD_ALLOWED_PROJECTS to one or more project names")
		}
		if adminToken != "" {
			return fmt.Errorf("ENGRAM_CLOUD_ADMIN is not supported when ENGRAM_CLOUD_INSECURE_NO_AUTH=1; remove ENGRAM_CLOUD_ADMIN or enable authenticated mode")
		}
		fmt.Fprintln(os.Stderr, "warning: ENGRAM_CLOUD_INSECURE_NO_AUTH=1 disables cloud API authentication; do not use in production")
		return nil
	}
	if token == "" {
		return fmt.Errorf("cloud auth token is required: set ENGRAM_CLOUD_TOKEN (or ENGRAM_CLOUD_INSECURE_NO_AUTH=1 for local insecure development)")
	}
	return fmt.Errorf("cloud project allowlist is required: set ENGRAM_CLOUD_ALLOWED_PROJECTS to one or more project names")
}

func normalizeAllowedProjects(projects []string) []string {
	normalized := make([]string, 0, len(projects))
	seen := make(map[string]struct{})
	for _, project := range projects {
		name, _ := store.NormalizeProject(project)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func cloudConfigPath(cfg store.Config) string {
	return filepath.Join(cfg.DataDir, "cloud.json")
}

// loadCloudConfig returns the default cloud entry as the legacy *cloudConfig type.
// All existing call sites remain unchanged. Internally reads v2 format and migrates v1.
func loadCloudConfig(cfg store.Config) (*cloudConfig, error) {
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		return nil, err
	}
	if v2 == nil {
		return nil, nil
	}
	entry := v2.defaultCloudEntry()
	if entry == nil {
		return nil, nil
	}
	return &cloudConfig{ServerURL: entry.ServerURL, Token: entry.Token}, nil
}

// loadCloudConfigV2 reads cloud.json and returns the v2 representation.
// If the file does not exist, returns nil, nil.
// If the file contains v1 format (server_url/token at top level), it is migrated
// to v2 in-memory (alias "cloud", default "cloud") without writing to disk.
func loadCloudConfigV2(cfg store.Config) (*cloudConfigV2, error) {
	path := cloudConfigPath(cfg)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Shape detection: probe for the "clouds" key.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("cloud config parse error: %w", err)
	}
	if _, hasV2 := probe["clouds"]; hasV2 {
		var v2 cloudConfigV2
		if err := json.Unmarshal(b, &v2); err != nil {
			return nil, fmt.Errorf("cloud config v2 parse error: %w", err)
		}
		return &v2, nil
	}
	// V1 migration: top-level server_url/token → clouds["cloud"].
	var v1 struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(b, &v1); err != nil {
		return nil, fmt.Errorf("cloud config v1 parse error: %w", err)
	}
	return &cloudConfigV2{
		Clouds:  map[string]cloudEntry{"cloud": {ServerURL: v1.ServerURL, Token: v1.Token}},
		Default: "cloud",
	}, nil
}

// saveCloudConfig updates the default cloud entry (ServerURL and/or Token) while
// preserving all other cloud entries. Existing callers remain unchanged.
func saveCloudConfig(cfg store.Config, cc *cloudConfig) error {
	return saveCloudConfigV2Entry(cfg, "", cc.ServerURL, cc.Token, "")
}

// saveCloudConfigV2Entry merges serverURL, token, and username into the named alias entry.
// Empty strings are not written (only non-empty values update the stored entry).
// If alias is "", the current default alias is used (falling back to "cloud").
func saveCloudConfigV2Entry(cfg store.Config, alias, serverURL, token, username string) error {
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		return err
	}
	if v2 == nil {
		v2 = &cloudConfigV2{Clouds: map[string]cloudEntry{}, Default: "cloud"}
	}
	if v2.Clouds == nil {
		v2.Clouds = map[string]cloudEntry{}
	}
	if alias == "" {
		alias = v2.Default
		if alias == "" {
			alias = "cloud"
		}
	}
	existing := v2.Clouds[alias]
	if serverURL != "" {
		existing.ServerURL = serverURL
	}
	if token != "" {
		existing.Token = token
	}
	if username != "" {
		existing.Username = username
	}
	v2.Clouds[alias] = existing
	return writeCloudConfigV2(cfg, v2)
}

// writeCloudConfigV2 marshals and writes a v2 config to disk.
func writeCloudConfigV2(cfg store.Config, v2 *cloudConfigV2) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v2, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cloudConfigPath(cfg), b, 0o644)
}
