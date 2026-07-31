package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	engrammcp "github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/store"
	_ "modernc.org/sqlite"
)

func seedDoctorSession(t *testing.T, cfg store.Config, id, project, directory string) {
	t.Helper()
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(id, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func newDoctorGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir git repo: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	run("remote", "add", "origin", "git@github.com:user/"+name+".git")
	return dir
}

func seedDoctorPendingMutation(t *testing.T, cfg store.Config, project, entity, entityKey, op, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "omnia.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, store.DefaultSyncTargetKey, entity, entityKey, op, payload, store.SyncSourceLocal, project); err != nil {
		t.Fatalf("insert sync mutation: %v", err)
	}
}

func seedDoctorRepairRows(t *testing.T, cfg store.Config, id, project, directory string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(id, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: id, Type: "bugfix", Title: "repair", Content: "content", Project: project, Scope: "project"}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(store.AddPromptParams{SessionID: id, Content: "prompt", Project: project}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
}

func TestCmdDoctorRepairValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing mode", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch"}, want: "exactly one of --plan, --dry-run, or --apply is required"},
		{name: "multiple modes", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--plan", "--apply"}, want: "exactly one of --plan, --dry-run, or --apply is required"},
		{name: "missing project", args: []string{"engram", "doctor", "repair", "--check", "session_project_directory_mismatch", "--plan"}, want: "--project is required"},
		{name: "unsupported check", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "sync_mutation_required_fields", "--plan"}, want: "unsupported repair check"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			oldExit := exitFunc
			exited := false
			exitFunc = func(code int) { exited = code != 0 }
			t.Cleanup(func() { exitFunc = oldExit })
			withArgs(t, tc.args...)
			_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if !exited || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exited=%v stderr=%q want %q", exited, stderr, tc.want)
			}
		})
	}
}

func TestCmdDoctorRepairPlanDryRunApplyJSON(t *testing.T) {
	cfg := testConfig(t)
	repo := newDoctorGitRepo(t, "engram")
	seedDoctorRepairRows(t, cfg, "repair-s1", "sias-app", repo)

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--plan")
	planOut, planErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if planErr != "" {
		t.Fatalf("plan stderr=%q", planErr)
	}
	plan := decodeRepairPlan(t, planOut)
	if plan["status"] != "planned" || plan["mode"] != "plan" || len(plan["actions"].([]any)) != 1 {
		t.Fatalf("plan=%v", plan)
	}
	counts := plan["counts"].(map[string]any)
	if counts["sessions_planned"] != float64(1) || counts["observations_planned"] != float64(1) || counts["prompts_planned"] != float64(1) {
		t.Fatalf("plan counts=%v", counts)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--dry-run")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["status"] != "dry_run" || dry["mode"] != "dry_run" {
		t.Fatalf("dry=%v", dry)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["status"] != "applied" || applied["backup_path"] == "" {
		t.Fatalf("applied=%v", applied)
	}
	appliedCounts := applied["counts"].(map[string]any)
	if appliedCounts["sessions_applied"] != float64(1) || appliedCounts["observations_applied"] != float64(1) || appliedCounts["prompts_applied"] != float64(1) {
		t.Fatalf("applied counts=%v", appliedCounts)
	}
	if _, err := os.Stat(applied["backup_path"].(string)); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "engram")
}

// TestCmdDoctorAcceptsConfigEqualsForm is finding #1's regression test:
// cmdDoctor's arg-parsing switch matched only the exact string "--config"
// (never "--config=PATH"), even though main()'s globalConfigPath — which
// already resolves --config for cfg BEFORE doctor's own parser ever runs —
// supports both forms. Live-reproduced: `omnia doctor --config /path/alt.yaml
// --check X` worked, `omnia doctor --config=/path/alt.yaml --check X` failed
// with `error: unknown doctor argument "--config=/path/alt.yaml"`.
func TestCmdDoctorAcceptsConfigEqualsForm(t *testing.T) {
	cfg := testConfig(t)
	oldExit := exitFunc
	exited := false
	exitFunc = func(code int) { exited = code != 0 }
	t.Cleanup(func() { exitFunc = oldExit })

	altConfig := filepath.Join(t.TempDir(), "alt.yaml")
	withArgs(t, "engram", "doctor", "--config="+altConfig, "--check", "manual_session_name_project_mismatch")
	_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if exited || strings.Contains(stderr, "unknown doctor argument") {
		t.Fatalf("expected --config=PATH to be accepted like --config PATH, got exited=%v stderr=%q", exited, stderr)
	}
}

// TestCmdDoctorRepairAcceptsConfigEqualsForm is finding #1's regression test
// for `omnia doctor repair`, the sibling arg-parsing loop with the identical
// bug (only the exact string "--config" was recognized).
func TestCmdDoctorRepairAcceptsConfigEqualsForm(t *testing.T) {
	cfg := testConfig(t)
	repo := newDoctorGitRepo(t, "engram")
	seedDoctorRepairRows(t, cfg, "repair-s1", "sias-app", repo)

	oldExit := exitFunc
	exited := false
	exitFunc = func(code int) { exited = code != 0 }
	t.Cleanup(func() { exitFunc = oldExit })

	altConfig := filepath.Join(t.TempDir(), "alt.yaml")
	withArgs(t, "engram", "doctor", "repair", "--config="+altConfig, "--project", "sias-app", "--check", "session_project_directory_mismatch", "--plan")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if exited || strings.Contains(stderr, "unknown doctor repair argument") {
		t.Fatalf("expected --config=PATH to be accepted like --config PATH, exited=%v stdout=%q stderr=%q", exited, out, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	plan := decodeRepairPlan(t, out)
	if plan["status"] != "planned" {
		t.Fatalf("plan=%v", plan)
	}
}

func decodeRepairPlan(t *testing.T, out string) map[string]any {
	t.Helper()
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("repair json invalid: %v\n%s", err, out)
	}
	return plan
}

func assertDoctorRepairProject(t *testing.T, cfg store.Config, sessionID, wantProject string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "omnia.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, query := range []string{`SELECT project FROM sessions WHERE id = ?`, `SELECT project FROM observations WHERE session_id = ?`, `SELECT project FROM user_prompts WHERE session_id = ?`} {
		var got string
		if err := db.QueryRow(query, sessionID).Scan(&got); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if got != wantProject {
			t.Fatalf("project=%q want %q for query %q", got, wantProject, query)
		}
	}
}

func TestCmdDoctorJSONSingleCheckAndProjectScope(t *testing.T) {
	cfg := testConfig(t)
	otherRepo := newDoctorGitRepo(t, "other")
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", otherRepo)
	seedDoctorSession(t, cfg, "manual-save-other", "other", otherRepo)
	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "session_project_directory_mismatch")

	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, stdout)
	}
	if report["status"] != "warning" || report["project"] != "engram" {
		t.Fatalf("report=%v", report)
	}
	checks := report["checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["check_id"] != "session_project_directory_mismatch" {
		t.Fatalf("checks=%v", checks)
	}
}

func TestCmdDoctorTextOutput(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")
	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "manual_session_name_project_mismatch")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "Omnia Doctor: ok") || !strings.Contains(stdout, "manual_session_name_project_mismatch") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestCmdDoctorInvalidCheckFailsLoudly(t *testing.T) {
	cfg := testConfig(t)
	oldExit := exitFunc
	exited := false
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })
	withArgs(t, "engram", "doctor", "--check", "not_real")
	_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if !exited || !strings.Contains(stderr, "invalid diagnostic check") {
		t.Fatalf("exited=%v stderr=%q", exited, stderr)
	}
}

func TestCmdDoctorJSONMatchesMemDoctorEnvelope(t *testing.T) {
	cfg := testConfig(t)
	otherRepo := newDoctorGitRepo(t, "other")
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", otherRepo)

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "session_project_directory_mismatch")
	cliStdout, cliStderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if cliStderr != "" {
		t.Fatalf("cli stderr=%q", cliStderr)
	}
	var cliEnvelope map[string]any
	if err := json.Unmarshal([]byte(cliStdout), &cliEnvelope); err != nil {
		t.Fatalf("cli json invalid: %v\n%s", err, cliStdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	mcpRes, err := engrammcp.DoctorToolHandler(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
		"check":   "session_project_directory_mismatch",
	}}})
	if err != nil {
		t.Fatalf("mem_doctor handler: %v", err)
	}
	mcpText, ok := mcppkg.AsTextContent(mcpRes.Content[0])
	if !ok {
		t.Fatal("expected text content from mem_doctor")
	}
	var mcpEnvelope map[string]any
	if err := json.Unmarshal([]byte(mcpText.Text), &mcpEnvelope); err != nil {
		t.Fatalf("mcp json invalid: %v\n%s", err, mcpText.Text)
	}
	if !reflect.DeepEqual(cliEnvelope, mcpEnvelope) {
		t.Fatalf("CLI and MCP doctor envelopes differ\nCLI=%v\nMCP=%v", cliEnvelope, mcpEnvelope)
	}
}

func TestCmdDoctorSyncMutationRequiredFieldsBlockedEnvelope(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntityObservation, "obs-missing", store.SyncOpUpsert, `{"sync_id":"obs-missing"}`)

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, stdout)
	}
	if report["status"] != "blocked" {
		t.Fatalf("expected blocked report, got %v", report)
	}
	checks := report["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("expected one check, got %v", checks)
	}
	check := checks[0].(map[string]any)
	if check["check_id"] != "sync_mutation_required_fields" || check["result"] != "blocked" || check["severity"] != "blocking" {
		t.Fatalf("unexpected check envelope: %v", check)
	}
	findings := check["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %v", findings)
	}
	finding := findings[0].(map[string]any)
	if finding["reason_code"] != "sync_mutation_payload_missing_required_fields" || finding["requires_confirmation"] != true {
		t.Fatalf("unexpected finding: %v", finding)
	}
	evidence := finding["evidence"].(map[string]any)
	if evidence["entity"] != store.SyncEntityObservation || evidence["entity_key"] != "obs-missing" {
		t.Fatalf("unexpected evidence: %v", evidence)
	}
}

// withStoreNewIgnoringEncryption overrides the package's storeNew injection
// seam (established by context_budget_wiring_test.go/main_extra_test.go) so
// every call opens a real, ordinary UNENCRYPTED store regardless of
// cfg.EncryptionEnabled.
//
// The two REQ-433 threat-model tests below need cfg.EncryptionEnabled=true
// so cmdDoctor's text/JSON output includes the threat-model statement (a
// value doctor.go reads directly off cfg, independent of how the store was
// actually opened — see writeDoctorJSON/renderDoctorText), but they must
// NEVER exercise the real encrypted-store code path to get there: that path
// resolves a key via the OS keychain (macOS `security` / Linux
// `secret-tool`), which is unavailable on CI runners (this is exactly the
// CI failure this seam fixes) and — even where a real keychain CLI exists,
// e.g. locally on macOS — a test must never depend on or mutate a real
// user's OS keychain in the first place.
//
// internal/store already has its own equivalent seam for this
// (newKeychainClient, overridden via encryption_test.go's fakeKeychain/
// withFakeKeychain) but it is unexported and unreachable from this package.
// Stripping the encryption fields before delegating to the real storeNew
// achieves the identical isolation goal — store.New/openEngramDB's own
// `if !cfg.EncryptionEnabled` guard (internal/store/encryption.go) means the
// keychain is never consulted at all once these fields are cleared.
func withStoreNewIgnoringEncryption(t *testing.T) {
	t.Helper()
	oldStoreNew := storeNew
	storeNew = func(cfg store.Config) (*store.Store, error) {
		cfg.EncryptionEnabled = false
		cfg.EncryptionKeychainService = ""
		cfg.EncryptionAllowPlaintextFallback = false
		return oldStoreNew(cfg)
	}
	t.Cleanup(func() { storeNew = oldStoreNew })
}

// TestCmdDoctor_EncryptionEnabled_StatesThreatModel (v0.4
// memory-at-rest-security, spec REQ-433): when encryption is active, the
// stated threat model — protects at-rest/lost-device while the process is
// stopped; does NOT protect a live-process memory dump or an attacker
// holding the unlocked keychain — MUST be discoverable in `omnia doctor`'s
// output (text and JSON).
//
// This test forces PATH empty so it is a real, permanent regression test
// for the CI failure that motivated withStoreNewIgnoringEncryption above
// (GitHub Actions Linux runners have neither `secret-tool` nor a usable
// keychain): if this test ever again touched the real encrypted-store code
// path, it would fail deterministically here — on any host, not just CI —
// instead of silently passing locally via a real macOS `security` CLI and
// only failing on Linux CI.
func TestCmdDoctor_EncryptionEnabled_StatesThreatModel(t *testing.T) {
	t.Setenv("PATH", "")
	withStoreNewIgnoringEncryption(t)
	cfg := testConfig(t)
	cfg.EncryptionEnabled = true
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "manual_session_name_project_mismatch")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "disk theft") && !strings.Contains(stdout, "lost laptop") {
		t.Fatalf("expected the at-rest/lost-device threat model text, got stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "does NOT protect") {
		t.Fatalf("expected the negative-scope statement (live-process/unlocked-keychain), got stdout=%q", stdout)
	}
}

// TestCmdDoctor_EncryptionDisabled_NoThreatModelText is the flag-off
// regression pin: the threat model banner must never appear (or otherwise
// change doctor's output) when encryption is disabled — byte-for-byte the
// pre-v0.4 output.
func TestCmdDoctor_EncryptionDisabled_NoThreatModelText(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "manual_session_name_project_mismatch")
	stdout, _ := captureOutput(t, func() { cmdDoctor(cfg) })
	if strings.Contains(stdout, "disk theft") || strings.Contains(stdout, "unlocked keychain") {
		t.Fatalf("threat model text must not appear when encryption is disabled, got stdout=%q", stdout)
	}
}

// TestCmdDoctor_EncryptionEnabled_JSONIncludesThreatModel confirms the
// same statement is present in --json mode too (REQ-433: "or its linked
// documentation" — but the JSON envelope is the natural machine-readable
// surface, so this pins it directly there as well).
//
// Also forces PATH empty (see TestCmdDoctor_EncryptionEnabled_StatesThreatModel
// above) — a real, permanent regression test proving this never depends on a
// real OS keychain CLI, matching real CI conditions on every run.
func TestCmdDoctor_EncryptionEnabled_JSONIncludesThreatModel(t *testing.T) {
	t.Setenv("PATH", "")
	withStoreNewIgnoringEncryption(t)
	cfg := testConfig(t)
	cfg.EncryptionEnabled = true
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "manual_session_name_project_mismatch")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	threatModel, ok := parsed["encryption_threat_model"].(string)
	if !ok || threatModel == "" {
		t.Fatalf("expected a non-empty encryption_threat_model field, got %#v", parsed["encryption_threat_model"])
	}
}

// TestCmdDoctor_FullScanPrintsProgressToStderr is finding #4's regression
// test: a full `omnia doctor` scan (no --check — every registered check
// runs) must print SOME progress to stderr as it goes, so a real user (or
// an agent watching command output) can tell the process is alive during a
// long real-data scan instead of seeing zero output for 2+ minutes. Progress
// goes to stderr specifically so it never interferes with --json's stdout
// envelope (checked separately below).
func TestCmdDoctor_FullScanPrintsProgressToStderr(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--project", "engram")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr == "" {
		t.Fatalf("expected non-empty progress on stderr for a full scan, got none; stdout=%q", stdout)
	}
	for _, code := range []string{"session_project_directory_mismatch", "sqlite_lock_contention"} {
		if !strings.Contains(stderr, code) {
			t.Errorf("expected stderr progress to mention check %q, got stderr=%q", code, stderr)
		}
	}
}

// TestCmdDoctor_FullScanJSONProgressStaysOffStdout confirms --json's stdout
// envelope stays valid, parseable JSON even though the full scan now prints
// progress lines — progress must go to stderr only, never interleaved into
// stdout.
func TestCmdDoctor_FullScanJSONProgressStaysOffStdout(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--json", "--project", "engram")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr == "" {
		t.Fatal("expected non-empty progress on stderr for a full --json scan too")
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor --json stdout must stay valid JSON even with progress on stderr: %v\n%s", err, stdout)
	}
}

// TestCmdDoctor_SingleCheckStaysStderrEmpty pins the existing, pre-fix
// behavior for a --check-scoped run (RunOne): progress is scoped to full
// scans (RunAll) only, matching every pre-existing --check-scoped test in
// this file that already asserts stderr == "".
func TestCmdDoctor_SingleCheckStaysStderrEmpty(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "session_project_directory_mismatch")
	_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("expected stderr empty for a --check-scoped run, got %q", stderr)
	}
}
