package main

import (
	"strings"
	"testing"

	engramsync "github.com/velion/omnia/internal/sync"

	"github.com/velion/omnia/internal/store"
)

// stubCloudSyncHooks overrides the sync hooks so cloud pushes succeed without a
// real server, and restores them on cleanup. syncExport returns a non-empty
// result; syncStatus reports a settled remote (0 pending) so the outcome resolves
// to a healthy sync_state row.
func stubCloudSyncHooks(t *testing.T) {
	t.Helper()
	oldExport := syncExport
	oldStatus := syncStatus
	oldImport := syncImport
	t.Cleanup(func() {
		syncExport = oldExport
		syncStatus = oldStatus
		syncImport = oldImport
	})
	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-multicloud", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) { return 1, 1, 0, nil }
	syncImport = func(*engramsync.Syncer) (*engramsync.ImportResult, error) {
		return &engramsync.ImportResult{}, nil
	}
}

func assertSyncedTargetKey(t *testing.T, cfg store.Config, targetKey string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify %s): %v", targetKey, err)
	}
	defer s.Close()
	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state %q: %v", targetKey, err)
	}
	if state == nil {
		t.Fatalf("expected a sync_state row for target key %q, got nil", targetKey)
	}
	if state.Lifecycle != store.SyncLifecycleHealthy && state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("expected target key %q to be synced (healthy/pending), got lifecycle %q", targetKey, state.Lifecycle)
	}
}

func assertNotSyncedTargetKey(t *testing.T, cfg store.Config, targetKey string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify-absent %s): %v", targetKey, err)
	}
	defer s.Close()
	// GetSyncState ensures (creates) an idle row on read; a synced target would be
	// healthy/pending instead. So "idle/empty" proves the key was never written by sync.
	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state %q: %v", targetKey, err)
	}
	if state != nil && (state.Lifecycle == store.SyncLifecycleHealthy || state.Lifecycle == store.SyncLifecyclePending) {
		t.Fatalf("expected target key %q to be untouched by sync, got lifecycle %q", targetKey, state.Lifecycle)
	}
}

// TestCmdSyncNamedCloudRecordsAliasTargetKey proves that `omnia sync --cloud
// --cloud-name work --project proj-a` records its sync state under the
// alias-prefixed target key "work:proj-a" and NOT under the legacy "cloud:proj-a".
func TestCmdSyncNamedCloudRecordsAliasTargetKey(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	stubCloudSyncHooks(t)

	cfg := testConfig(t)
	// Two configured clouds; sync only to "work" via --cloud-name.
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.test", "work-token", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "personal", "https://personal.example.test", "personal-token", ""); err != nil {
		t.Fatalf("add personal cloud: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "sync", "--cloud", "--cloud-name", "work", "--project", "proj-a")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("named-cloud sync should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud sync complete for project \"proj-a\".") {
		t.Fatalf("expected cloud sync success messaging, got %q", stdout)
	}

	assertSyncedTargetKey(t, cfg, "work:proj-a")
	assertNotSyncedTargetKey(t, cfg, "personal:proj-a")
	assertNotSyncedTargetKey(t, cfg, cloudTargetKeyForProject("proj-a")) // legacy "cloud:proj-a"
}

// TestCmdSyncDefaultCloudStillUsesLegacyTargetKey proves the default path (no
// --cloud-name, env-only config) still records under the legacy "cloud:<project>"
// key, preserving backward compatibility and the dashboard's "cloud:" mapping.
func TestCmdSyncDefaultCloudStillUsesLegacyTargetKey(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	stubCloudSyncHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("default cloud sync should succeed, panic=%v stderr=%q", recovered, stderr)
	}

	assertSyncedTargetKey(t, cfg, cloudTargetKeyForProject("proj-a")) // "cloud:proj-a"
}

// TestCmdSyncMultiCloudSyncsEveryConfiguredCloud proves that `omnia sync --cloud`
// without --cloud-name replicates the project to every configured cloud, each
// under its own alias-prefixed key.
func TestCmdSyncMultiCloudSyncsEveryConfiguredCloud(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	stubCloudSyncHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.test", "work-token", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "personal", "https://personal.example.test", "personal-token", ""); err != nil {
		t.Fatalf("add personal cloud: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("multi-cloud sync should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, `== cloud "work" ==`) || !strings.Contains(stdout, `== cloud "personal" ==`) {
		t.Fatalf("expected per-cloud headers in multi-cloud output, got %q", stdout)
	}

	assertSyncedTargetKey(t, cfg, "work:proj-a")
	assertSyncedTargetKey(t, cfg, "personal:proj-a")
}

// TestResolveCloudRuntimeConfigForAliasEnvTokenFootgun proves that an explicitly
// named cloud is NOT clobbered by ENGRAM_CLOUD_TOKEN, while the default/legacy
// path still honours it (issue: Benja's shell exports ENGRAM_CLOUD_TOKEN).
func TestResolveCloudRuntimeConfigForAliasEnvTokenFootgun(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.test", "work-token", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")

	// Explicit alias (applyEnvOverrides=false): per-alias token wins.
	ccNamed, err := resolveCloudRuntimeConfigForAlias(cfg, "work", false)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfigForAlias(work,false): %v", err)
	}
	if ccNamed.Token != "work-token" {
		t.Fatalf("named cloud token must NOT be clobbered by env; got %q want %q", ccNamed.Token, "work-token")
	}

	// Same alias with env overrides allowed: env token wins (legacy behavior).
	ccEnv, err := resolveCloudRuntimeConfigForAlias(cfg, "work", true)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfigForAlias(work,true): %v", err)
	}
	if ccEnv.Token != "env-token" {
		t.Fatalf("env override should win when allowed; got %q want %q", ccEnv.Token, "env-token")
	}

	// preflight for the named cloud must also carry the per-alias token, not env.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	ccPre, err := preflightCloudSyncForAlias(s, cfg, "proj-a", "work", false, false)
	if err != nil {
		t.Fatalf("preflightCloudSyncForAlias: %v", err)
	}
	if ccPre.Token != "work-token" {
		t.Fatalf("preflight for named cloud must use per-alias token; got %q want %q", ccPre.Token, "work-token")
	}
}

// TestCmdCloudEnrollCloudNameValidatesAlias proves `omnia cloud enroll <project>
// --cloud-name <alias>` validates the alias and enrolls the project; an unknown
// alias fails fast.
func TestCmdCloudEnrollCloudNameValidatesAlias(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.test", "", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}

	// Unknown alias → non-zero exit, alias named in error.
	withArgs(t, "engram", "cloud", "enroll", "proj-a", "--cloud-name", "nope")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudEnroll(cfg) })
	if code, ok := recovered.(exitCode); !ok || code == 0 {
		t.Fatalf("expected non-zero exit for unknown alias, got %v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "nope") {
		t.Fatalf("expected unknown alias name in error, got %q", stderr)
	}

	// Known alias → enrolls and reports the cloud.
	withArgs(t, "engram", "cloud", "enroll", "proj-a", "--cloud-name", "work")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudEnroll(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("enroll with known alias should not exit; stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "enrolled for cloud sync") || !strings.Contains(stdout, "work") {
		t.Fatalf("expected cloud-aware enroll confirmation, got %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()
	enrolled, err := s.IsProjectEnrolled("proj-a")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("expected proj-a to be enrolled after cloud-aware enroll")
	}
}
