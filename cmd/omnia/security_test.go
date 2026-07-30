package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/keychain"
	"github.com/velion/omnia/internal/store"
)

// fakeSecurityKeychain lets tests script Get/Set/GetOrCreateHexKey
// responses for cmdSecurity's three subcommands without shelling out to a
// real keychain — mirrors internal/store's/internal/embed's own fake
// keychain test doubles (all three share the same method shapes).
type fakeSecurityKeychain struct {
	hexKey string
	getErr error
	setErr error
	sets   []string // values passed to Set, in order
}

func (f *fakeSecurityKeychain) GetOrCreateHexKey(ctx context.Context, service, account string) (string, bool, error) {
	if f.getErr != nil {
		return "", false, f.getErr
	}
	return f.hexKey, false, nil
}

func (f *fakeSecurityKeychain) Get(ctx context.Context, service, account string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.hexKey, nil
}

func (f *fakeSecurityKeychain) Set(ctx context.Context, service, account, value string) error {
	f.sets = append(f.sets, value)
	if f.setErr != nil {
		return f.setErr
	}
	f.hexKey = value
	return nil
}

func withFakeSecurityKeychain(t *testing.T, f *fakeSecurityKeychain) {
	t.Helper()
	orig := newSecurityKeychainClient
	newSecurityKeychainClient = func() securityKeychain { return f }
	t.Cleanup(func() { newSecurityKeychainClient = orig })
}

func writeSecurityConfig(t *testing.T, dir string, yaml string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const securityTestHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// ─── cmdSecurityEncrypt ───

func TestCmdSecurityEncrypt_DisabledConfig_PrintsDisabledMessageNoFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: false}\n")
	cfg := testConfig(t)

	withArgs(t, "omnia", "security", "encrypt", "--config", cfgPath)
	stdout, _ := captureOutput(t, func() { cmdSecurity(cfg) })
	if !strings.Contains(stdout, "disabled") {
		t.Fatalf("expected a disabled message, got: %q", stdout)
	}
}

func TestCmdSecurityEncrypt_MissingConfigFile_DegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	withArgs(t, "omnia", "security", "encrypt", "--config", filepath.Join(dir, "does-not-exist.yaml"))
	stdout, _ := captureOutput(t, func() { cmdSecurity(cfg) })
	if !strings.Contains(stdout, "disabled") {
		t.Fatalf("a missing config.yaml must degrade to 'disabled', not fatal; got: %q", stdout)
	}
}

func TestCmdSecurityEncrypt_EncryptsBothStores(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: true}\nembeddings: {enabled: true}\n")
	cfg := testConfig(t)
	cfg.DataDir = dir

	// Seed a plaintext omnia.db with one observation.
	seedCfg := cfg
	seedCfg.DedupeWindow = 0
	s, err := store.New(seedCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("sess", "engram", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess", Type: "manual", Title: "t", Content: "c", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	s.Close()

	// Seed a plaintext embeddings.db with one row.
	embStore, err := embed.OpenStore(filepath.Join(dir, "embeddings.db"))
	if err != nil {
		t.Fatalf("embed.OpenStore: %v", err)
	}
	if err := embStore.Upsert(context.Background(), embed.Row{
		SyncID: "a", ObsID: 1, ContentHash: "h", Model: "m", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	embStore.Close()

	withFakeSecurityKeychain(t, &fakeSecurityKeychain{hexKey: securityTestHexKey})
	withArgs(t, "omnia", "security", "encrypt", "--config", cfgPath)
	stdout, stderr := captureOutput(t, func() { cmdSecurity(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "omnia.db") || !strings.Contains(stdout, "embeddings.db") {
		t.Fatalf("expected a report mentioning both files, got: %q", stdout)
	}

	rawStore, err := os.ReadFile(filepath.Join(dir, "omnia.db"))
	if err != nil {
		t.Fatalf("ReadFile omnia.db: %v", err)
	}
	if strings.HasPrefix(string(rawStore), "SQLite format 3") {
		t.Error("omnia.db must be encrypted after `omnia security encrypt`")
	}
	rawEmb, err := os.ReadFile(filepath.Join(dir, "embeddings.db"))
	if err != nil {
		t.Fatalf("ReadFile embeddings.db: %v", err)
	}
	if strings.HasPrefix(string(rawEmb), "SQLite format 3") {
		t.Error("embeddings.db must be encrypted after `omnia security encrypt`")
	}
}

// ─── cmdSecurityDecrypt ───

func TestCmdSecurityDecrypt_ReversesEncryption(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: true}\nembeddings: {enabled: true}\n")
	cfg := testConfig(t)
	cfg.DataDir = dir

	seedCfg := cfg
	seedCfg.DedupeWindow = 0
	s, err := store.New(seedCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("sess", "engram", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess", Type: "manual", Title: "t", Content: "c", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	s.Close()

	withFakeSecurityKeychain(t, &fakeSecurityKeychain{hexKey: securityTestHexKey})
	withArgs(t, "omnia", "security", "encrypt", "--config", cfgPath)
	captureOutput(t, func() { cmdSecurity(cfg) })

	withArgs(t, "omnia", "security", "decrypt", "--config", cfgPath)
	stdout, stderr := captureOutput(t, func() { cmdSecurity(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "omnia.db") {
		t.Fatalf("expected a report mentioning omnia.db, got: %q", stdout)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "omnia.db"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Error("omnia.db must be plaintext after `omnia security decrypt`")
	}

	// Readable by a plain store.New (no encryption config at all) — never
	// locks the user out (REQ-435).
	reopenCfg := cfg
	reopenCfg.DedupeWindow = 0
	reopened, err := store.New(reopenCfg)
	if err != nil {
		t.Fatalf("reopen after decrypt: %v", err)
	}
	defer reopened.Close()
	obs, err := reopened.AllObservations("engram", "project", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("len(obs) = %d, want 1", len(obs))
	}
}

func TestCmdSecurityDecrypt_NoKeyInKeychain_FailsLoudlyNotFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: false}\n")
	cfg := testConfig(t)
	cfg.DataDir = dir

	// Seed a plaintext omnia.db so isPlaintextSQLiteFile has something to
	// inspect (decrypt on a missing file is a silent no-op, not this path).
	seedCfg := cfg
	s, err := store.New(seedCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()

	oldExit := exitFunc
	var exitCode int
	exitFunc = func(code int) { exitCode = code; panic("exit") }
	t.Cleanup(func() { exitFunc = oldExit })

	withFakeSecurityKeychain(t, &fakeSecurityKeychain{getErr: errors.New("keychain: item not found")})
	withArgs(t, "omnia", "security", "decrypt", "--config", cfgPath)
	_, stderr := captureOutput(t, func() {
		defer func() { recover() }()
		cmdSecurity(cfg)
	})
	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr, "key") {
		t.Fatalf("expected an error mentioning the missing key, got stderr=%q", stderr)
	}
}

// ─── cmdSecurityRotateKey ───

func TestCmdSecurityRotateKey_RotatesAndUpdatesKeychainOnlyAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: true}\n")
	cfg := testConfig(t)
	cfg.DataDir = dir

	seedCfg := cfg
	seedCfg.DedupeWindow = 0
	s, err := store.New(seedCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("sess", "engram", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess", Type: "manual", Title: "t", Content: "c", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	s.Close()

	oldExit := exitFunc
	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	fk := &fakeSecurityKeychain{hexKey: securityTestHexKey}
	withFakeSecurityKeychain(t, fk)
	withArgs(t, "omnia", "security", "encrypt", "--config", cfgPath)
	captureOutput(t, func() { cmdSecurity(cfg) })
	if exited {
		t.Fatalf("encrypt step unexpectedly called exitFunc(%d)", exitCode)
	}

	withArgs(t, "omnia", "security", "rotate-key", "--config", cfgPath)
	stdout, stderr := captureOutput(t, func() { cmdSecurity(cfg) })
	if exited {
		t.Fatalf("rotate-key unexpectedly called exitFunc(%d); stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "omnia.db") {
		t.Fatalf("expected a report mentioning omnia.db, got: %q", stdout)
	}
	if len(fk.sets) != 1 {
		t.Fatalf("expected exactly 1 keychain Set call (the new key), got %d", len(fk.sets))
	}
	if fk.sets[0] == securityTestHexKey {
		t.Error("rotate-key must store a DIFFERENT key, not re-store the old one")
	}

	// Confirm the rotated file is readable with the NEW key (fk.sets[0])
	// and NOT with the old one — a direct, low-level check (rather than
	// going through store.New, which would hit internal/store's OWN
	// package-private keychain seam, not this test's fake) that the
	// rotation actually took effect on disk.
	newKey := fk.sets[0]
	n, err := countObservationsWithKey(t, cfg.DataDir+"/omnia.db", newKey)
	if err != nil {
		t.Fatalf("read rotated file with the NEW key: %v", err)
	}
	if n != 1 {
		t.Fatalf("count with new key = %d, want 1", n)
	}
	if _, err := countObservationsWithKey(t, cfg.DataDir+"/omnia.db", securityTestHexKey); err == nil {
		t.Fatal("expected the OLD key to no longer open the rotated file")
	}
}

// TestCmdSecurityRotateKey_KeychainSetFails_WritesRecoveryFileNotStderr is a
// RED test for a real security bug: when the keychain Set call fails AFTER
// both files have already been re-encrypted with the new key, the CLI used
// to print the raw 64-char hex key directly to stderr (capturable via shell
// redirection, terminal scrollback, CI log capture, or a pasted support
// ticket). The fix: write the recovery key to a 0600 file in the data dir
// and print only that file's PATH (plus recovery instructions) to stderr —
// never the raw key value.
func TestCmdSecurityRotateKey_KeychainSetFails_WritesRecoveryFileNotStderr(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeSecurityConfig(t, dir, "encryption: {enabled: true}\n")
	cfg := testConfig(t)
	cfg.DataDir = dir

	seedCfg := cfg
	seedCfg.DedupeWindow = 0
	s, err := store.New(seedCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("sess", "engram", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess", Type: "manual", Title: "t", Content: "c", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	s.Close()

	oldExit := exitFunc
	var exitCode int
	exitFunc = func(code int) { exitCode = code }
	t.Cleanup(func() { exitFunc = oldExit })

	fk := &fakeSecurityKeychain{hexKey: securityTestHexKey}
	withFakeSecurityKeychain(t, fk)
	withArgs(t, "omnia", "security", "encrypt", "--config", cfgPath)
	captureOutput(t, func() { cmdSecurity(cfg) })

	// Force the keychain Set call to fail AFTER both files have already
	// been rotated to a new key — the exact failure mode this test covers.
	fk.setErr = errors.New("keychain locked")
	withArgs(t, "omnia", "security", "rotate-key", "--config", cfgPath)
	_, stderr := captureOutput(t, func() { cmdSecurity(cfg) })

	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
	if len(fk.sets) == 0 {
		t.Fatalf("expected at least one attempted keychain Set call, got none")
	}
	newKey := fk.sets[len(fk.sets)-1]

	if strings.Contains(stderr, newKey) {
		t.Fatal("stderr must NEVER contain the raw rotated key")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var recoveryPath string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "security-rotate-recovery-") {
			recoveryPath = filepath.Join(dir, e.Name())
		}
	}
	if recoveryPath == "" {
		t.Fatalf("expected a security-rotate-recovery-*.txt file in %s, stderr=%q", dir, stderr)
	}
	if !strings.Contains(stderr, recoveryPath) {
		t.Fatalf("expected stderr to mention the recovery file path %q, got: %q", recoveryPath, stderr)
	}

	info, err := os.Stat(recoveryPath)
	if err != nil {
		t.Fatalf("stat recovery file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("recovery file perm = %o, want 0600", perm)
	}

	body, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatalf("read recovery file: %v", err)
	}
	if !strings.Contains(string(body), newKey) {
		t.Fatal("recovery file must contain the new key")
	}
}

// countObservationsWithKey opens dbPath via ncruces+adiantum+hexKey and
// counts rows in observations — a package-boundary-safe way for this CLI
// test to verify rotation without depending on internal/store's own
// private keychain seam.
func countObservationsWithKey(t *testing.T, dbPath, hexKey string) (int, error) {
	t.Helper()
	init := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		return fts5.Register(c)
	}
	db, err := ncrucesdriver.Open("file:"+dbPath+"?vfs=adiantum", init)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n)
	return n, err
}

var _ = keychain.ErrNotFound // keep import used if fakeSecurityKeychain composition changes
