package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/cloud/constants"
)

// TestCloudConfigV2MigrationFromV1 verifies that a v1 cloud.json (server_url + token
// at the top level) is migrated in-memory to v2 shape with clouds["cloud"] and default="cloud".
func TestCloudConfigV2MigrationFromV1(t *testing.T) {
	cfg := testConfig(t)

	// Write a v1-format cloud.json directly.
	v1JSON := `{"server_url": "https://v1.example.test", "token": "v1-token"}`
	if err := os.WriteFile(cfg.DataDir+"/cloud.json", []byte(v1JSON), 0o644); err != nil {
		t.Fatalf("write v1 cloud.json: %v", err)
	}

	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	if v2 == nil {
		t.Fatal("expected non-nil v2 after v1 migration")
	}
	if v2.Default != "cloud" {
		t.Errorf("expected Default=%q, got %q", "cloud", v2.Default)
	}
	entry, ok := v2.getCloud("cloud")
	if !ok {
		t.Fatal("expected clouds[\"cloud\"] to exist after v1 migration")
	}
	if entry.ServerURL != "https://v1.example.test" {
		t.Errorf("expected ServerURL=%q, got %q", "https://v1.example.test", entry.ServerURL)
	}
	if entry.Token != "v1-token" {
		t.Errorf("expected Token=%q, got %q", "v1-token", entry.Token)
	}
}

// TestCloudConfigV2RoundTrip verifies that a v2 cloud.json with multiple entries
// round-trips correctly through load → write → load.
func TestCloudConfigV2RoundTrip(t *testing.T) {
	cfg := testConfig(t)

	// Add two cloud entries and verify they survive a reload.
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.com", "work-token", "workuser"); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "personal", "https://personal.example.com", "personal-token", "personaluser"); err != nil {
		t.Fatalf("add personal cloud: %v", err)
	}
	// Set "work" as default.
	v2first, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	v2first.Default = "work"
	if err := writeCloudConfigV2(cfg, v2first); err != nil {
		t.Fatalf("writeCloudConfigV2: %v", err)
	}

	// Reload and verify.
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("reload loadCloudConfigV2: %v", err)
	}
	if v2 == nil {
		t.Fatal("expected non-nil v2 after reload")
	}
	if v2.Default != "work" {
		t.Errorf("expected Default=%q, got %q", "work", v2.Default)
	}

	work, ok := v2.getCloud("work")
	if !ok {
		t.Fatal("clouds[\"work\"] missing after reload")
	}
	if work.ServerURL != "https://work.example.com" || work.Token != "work-token" || work.Username != "workuser" {
		t.Errorf("work cloud mismatch: %+v", work)
	}

	personal, ok := v2.getCloud("personal")
	if !ok {
		t.Fatal("clouds[\"personal\"] missing after reload")
	}
	if personal.ServerURL != "https://personal.example.com" || personal.Token != "personal-token" {
		t.Errorf("personal cloud mismatch: %+v", personal)
	}

	// defaultCloudEntry should resolve to "work".
	def := v2.defaultCloudEntry()
	if def == nil {
		t.Fatal("expected non-nil defaultCloudEntry")
	}
	if def.ServerURL != "https://work.example.com" {
		t.Errorf("expected defaultCloudEntry ServerURL=%q, got %q", "https://work.example.com", def.ServerURL)
	}
}

// TestCloudAddAndList verifies cmdCloudAdd creates an entry visible in cmdCloudList.
func TestCloudAddAndList(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	withArgs(t, "engram", "cloud", "add", "mycloud", "--server", "https://mycloud.example.com")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudAdd(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudAdd panicked; stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "mycloud") {
		t.Errorf("expected alias in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "https://mycloud.example.com") {
		t.Errorf("expected server URL in output, got %q", stdout)
	}

	// List should show the entry.
	withArgs(t, "engram", "cloud", "list")
	listOut, _, recovered := captureOutputAndRecover(t, func() { cmdCloudList(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudList panicked; output=%q", listOut)
	}
	if !strings.Contains(listOut, "mycloud") {
		t.Errorf("expected alias in list output, got %q", listOut)
	}
	if !strings.Contains(listOut, "https://mycloud.example.com") {
		t.Errorf("expected server URL in list output, got %q", listOut)
	}
}

// TestCloudRemove verifies that cmdCloudRemove deletes only the specified entry.
func TestCloudRemove(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	// Add two clouds.
	if err := saveCloudConfigV2Entry(cfg, "alpha", "https://alpha.example.com", "", ""); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "beta", "https://beta.example.com", "", ""); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	// Remove "alpha".
	withArgs(t, "engram", "cloud", "remove", "alpha")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudRemove(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudRemove panicked; stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("expected alias in remove output, got %q", stdout)
	}

	// Verify alpha is gone, beta is still there.
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	if _, ok := v2.getCloud("alpha"); ok {
		t.Error("expected alpha to be removed")
	}
	if _, ok := v2.getCloud("beta"); !ok {
		t.Error("expected beta to still exist")
	}
}

// TestCloudDefault verifies that cmdCloudDefault sets the v2 Default field.
func TestCloudDefault(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.com", "", ""); err != nil {
		t.Fatalf("add work: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "personal", "https://personal.example.com", "", ""); err != nil {
		t.Fatalf("add personal: %v", err)
	}

	withArgs(t, "engram", "cloud", "default", "personal")
	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudDefault(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudDefault panicked; stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "personal") {
		t.Errorf("expected alias in output, got %q", stdout)
	}

	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	if v2.Default != "personal" {
		t.Errorf("expected Default=%q, got %q", "personal", v2.Default)
	}
}

// TestCloudLoginWritesTokenToCorrectEntry verifies that --cloud <alias> routes the
// token to the correct cloud entry and does not disturb the default "cloud" entry.
func TestCloudLoginWritesTokenToCorrectEntry(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "work-jwt-token",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)

	// Add "work" cloud with the stub server URL.
	if err := saveCloudConfigV2Entry(cfg, "work", srv.URL, "", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	// Add a default "cloud" entry so we can verify it is NOT touched.
	if err := saveCloudConfigV2Entry(cfg, "cloud", "https://default.example.com", "existing-default-token", "defaultuser"); err != nil {
		t.Fatalf("add default cloud: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--cloud", "work",
		"--username", "workuser",
		"--password", "pw",
	)

	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudLogin(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudLogin panicked; stdout=%q", stdout)
	}

	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}

	// Token must go to "work" entry.
	work, ok := v2.getCloud("work")
	if !ok {
		t.Fatal("work cloud not found after login")
	}
	if work.Token != "work-jwt-token" {
		t.Errorf("expected work token %q, got %q", "work-jwt-token", work.Token)
	}

	// Default "cloud" entry token must be UNCHANGED.
	cloud, ok := v2.getCloud("cloud")
	if !ok {
		t.Fatal("default cloud not found after login to work")
	}
	if cloud.Token != "existing-default-token" {
		t.Errorf("expected default token to be unchanged %q, got %q", "existing-default-token", cloud.Token)
	}
}

// TestSingleCloudTargetKeyUnchanged is a golden test that verifies the v1-migration
// alias "cloud" produces the exact same target keys as the legacy cloudTargetKeyForProject.
func TestSingleCloudTargetKeyUnchanged(t *testing.T) {
	const legacyAlias = "cloud" // == constants.TargetKeyCloud

	tests := []struct {
		project string
	}{
		{project: "myproject"},
		{project: ""},
		{project: "  "},
		{project: "Foo-Bar"},
	}

	for _, tc := range tests {
		wantNew := cloudnFor(legacyAlias, tc.project)
		wantOld := cloudTargetKeyForProject(tc.project)
		if wantNew != wantOld {
			t.Errorf("cloudnFor(%q, %q)=%q but cloudTargetKeyForProject(%q)=%q — backward compat broken",
				legacyAlias, tc.project, wantNew, tc.project, wantOld)
		}
	}

	// Explicit golden values.
	if got := cloudnFor("cloud", ""); got != constants.TargetKeyCloud {
		t.Errorf("cloudnFor(\"cloud\", \"\") = %q, want %q", got, constants.TargetKeyCloud)
	}
	if got := cloudnFor("cloud", "myproject"); got != "cloud:myproject" {
		t.Errorf("cloudnFor(\"cloud\", \"myproject\") = %q, want %q", got, "cloud:myproject")
	}
}

// TestMultiCloudTargetKeyCarriesAlias verifies that non-default aliases produce
// their own target key namespace.
func TestMultiCloudTargetKeyCarriesAlias(t *testing.T) {
	got := cloudnFor("work", "myproject")
	if got != "work:myproject" {
		t.Errorf("cloudnFor(\"work\", \"myproject\") = %q, want %q", got, "work:myproject")
	}

	// Without project, alias is returned as-is.
	if got := cloudnFor("work", ""); got != "work" {
		t.Errorf("cloudnFor(\"work\", \"\") = %q, want %q", got, "work")
	}

	// Empty alias falls back to "cloud".
	if got := cloudnFor("", "myproject"); got != "cloud:myproject" {
		t.Errorf("cloudnFor(\"\", \"myproject\") = %q, want %q", got, "cloud:myproject")
	}
}

// TestLoadCloudConfigBackwardCompat verifies that after a v1→v2 migration,
// the old loadCloudConfig API still returns the correct ServerURL and Token.
func TestLoadCloudConfigBackwardCompat(t *testing.T) {
	cfg := testConfig(t)

	// Write a v1-format cloud.json.
	v1JSON := `{"server_url": "https://compat.example.test", "token": "compat-token"}`
	if err := os.WriteFile(cfg.DataDir+"/cloud.json", []byte(v1JSON), 0o644); err != nil {
		t.Fatalf("write v1 cloud.json: %v", err)
	}

	cc, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc == nil {
		t.Fatal("expected non-nil cloudConfig from v1 file")
	}
	if cc.ServerURL != "https://compat.example.test" {
		t.Errorf("expected ServerURL=%q, got %q", "https://compat.example.test", cc.ServerURL)
	}
	if cc.Token != "compat-token" {
		t.Errorf("expected Token=%q, got %q", "compat-token", cc.Token)
	}
}

// TestSaveCloudConfigPreservesOtherClouds verifies that calling the old saveCloudConfig
// (which updates the default entry) does not delete other cloud entries.
func TestSaveCloudConfigPreservesOtherClouds(t *testing.T) {
	cfg := testConfig(t)

	// Seed two non-default entries.
	if err := saveCloudConfigV2Entry(cfg, "work", "https://work.example.com", "work-token", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}
	if err := saveCloudConfigV2Entry(cfg, "personal", "https://personal.example.com", "personal-token", ""); err != nil {
		t.Fatalf("add personal cloud: %v", err)
	}

	// Call the old API — must only affect the default "cloud" entry.
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://default.example.com", Token: "default-token"}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}

	work, ok := v2.getCloud("work")
	if !ok || work.Token != "work-token" {
		t.Errorf("work cloud not preserved: ok=%v entry=%+v", ok, work)
	}
	personal, ok := v2.getCloud("personal")
	if !ok || personal.Token != "personal-token" {
		t.Errorf("personal cloud not preserved: ok=%v entry=%+v", ok, personal)
	}

	// The default "cloud" entry should be set.
	cloud, ok := v2.getCloud("cloud")
	if !ok {
		t.Fatal("default cloud entry not found after saveCloudConfig")
	}
	if cloud.ServerURL != "https://default.example.com" {
		t.Errorf("expected default ServerURL=%q, got %q", "https://default.example.com", cloud.ServerURL)
	}
	if cloud.Token != "default-token" {
		t.Errorf("expected default Token=%q, got %q", "default-token", cloud.Token)
	}
}

// TestCloudLoginRequestBodySentToCorrectAlias is an integration smoke-test
// that verifies the login body reaches the server associated with the alias.
func TestCloudLoginRequestBodySentToCorrectAlias(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "alias-token",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)

	// Add "staging" cloud pointing to the stub.
	if err := saveCloudConfigV2Entry(cfg, "staging", srv.URL, "", ""); err != nil {
		t.Fatalf("add staging cloud: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--cloud", "staging",
		"--username", "staginguser",
		"--password", "stagingpw",
	)

	_, _, recovered := captureOutputAndRecover(t, func() { cmdCloudLogin(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("unexpected exit during login to staging")
	}

	// Verify the body sent to the stub server.
	var sent map[string]string
	if err := json.Unmarshal(stub.loginBody, &sent); err != nil {
		t.Fatalf("parse login body: %v; raw=%q", err, stub.loginBody)
	}
	if sent["username"] != "staginguser" {
		t.Errorf("expected username=%q, got %q", "staginguser", sent["username"])
	}
	if sent["password"] != "stagingpw" {
		t.Errorf("expected password=%q, got %q", "stagingpw", sent["password"])
	}

	// Token must be saved to "staging" entry.
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfigV2: %v", err)
	}
	staging, ok := v2.getCloud("staging")
	if !ok {
		t.Fatal("staging cloud not found")
	}
	if staging.Token != "alias-token" {
		t.Errorf("expected staging token %q, got %q", "alias-token", staging.Token)
	}
}

// TestCloudLoginUnknownAliasExitsNonZero verifies that specifying a non-existent
// --cloud alias fails with a clear error.
func TestCloudLoginUnknownAliasExitsNonZero(t *testing.T) {
	cfg := testConfig(t)

	// Create a v2 config with one real entry, so v2 is non-nil.
	if err := saveCloudConfigV2Entry(cfg, "cloud", "https://default.example.com", "", ""); err != nil {
		t.Fatalf("seed cloud entry: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--cloud", "nonexistent",
		"--username", "u",
		"--password", "p",
	)

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudLogin(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit for unknown alias, got panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "nonexistent") {
		t.Errorf("expected alias name in error message, got: %q", stderr)
	}
}

// Ensure http package import is used.
var _ = http.StatusOK
