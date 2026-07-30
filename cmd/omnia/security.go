package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/datadir"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/keychain"
	"github.com/velion/omnia/internal/store"
)

// securityKeychain is the subset of internal/keychain.Client's API
// cmdSecurity depends on. Declared as an interface so tests can inject a
// fake without spawning a real keychain CLI (mirrors internal/store's and
// internal/embed's own keychain.Resolver seam).
type securityKeychain interface {
	GetOrCreateHexKey(ctx context.Context, service, account string) (hexKey string, created bool, err error)
	Get(ctx context.Context, service, account string) (string, error)
	Set(ctx context.Context, service, account, value string) error
}

// newSecurityKeychainClient constructs the real keychain client. Injectable
// so tests never shell out to a real `security`/`secret-tool` process.
var newSecurityKeychainClient = func() securityKeychain { return keychain.New() }

const securityKeyAccount = "db-key-v1"

// cmdSecurity dispatches `omnia security <encrypt|decrypt|rotate-key>`
// (design capability 4 CLI, spec REQ-435), mirroring the existing
// nested-subcommand-namespace convention (cloud/embed/migrate/eval/cartridge).
func cmdSecurity(cfg store.Config) {
	if len(os.Args) < 3 {
		printSecurityUsage()
		return
	}
	switch os.Args[2] {
	case "encrypt":
		cmdSecurityEncrypt(cfg, os.Args[3:])
	case "decrypt":
		cmdSecurityDecrypt(cfg, os.Args[3:])
	case "rotate-key":
		cmdSecurityRotateKey(cfg, os.Args[3:])
	case "--help", "-h", "help":
		printSecurityUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown security subcommand: %s\n", os.Args[2])
		printSecurityUsage()
		exitFunc(1)
	}
}

func printSecurityUsage() {
	fmt.Println("usage: omnia security <encrypt|decrypt|rotate-key> [--config PATH]")
	fmt.Println()
	fmt.Println("Threat model: at-rest encryption protects a stopped process's on-disk files")
	fmt.Println("(disk theft, lost laptop). It does NOT protect a live-process memory dump or")
	fmt.Println("an attacker who already holds the unlocked OS keychain.")
}

// securityConfigFlag parses --config PATH from args, defaulting to
// config.DefaultPath() — mirrors cartridgeFlags' own minimal flag parsing.
func securityConfigFlag(args []string) string {
	path := config.DefaultPath()
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			path = args[i+1]
		}
	}
	return path
}

// securityDataDir resolves the effective data directory this capability
// operates in, using the SAME resolution every other command in this
// package uses.
func securityDataDir(cfg store.Config) string {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = datadir.Resolve("")
	}
	return dataDir
}

// securityPaths resolves the omnia.db and embeddings.db paths this
// capability migrates, using the SAME data-dir/embeddings-path resolution
// every other command in this package uses.
func securityPaths(cfg store.Config, appCfg config.Config) (omniaDBPath, embDBPath string) {
	dataDir := securityDataDir(cfg)
	omniaDBPath = datadir.DBPath(dataDir)
	embDBPath = config.ResolveEmbeddingsDBPath(appCfg.Embeddings.DBPath, dataDir)
	return
}

// writeRotateRecoveryFile persists a freshly rotated encryption key to a
// 0600 file in dataDir for the ONE genuine failure mode where the operator
// needs the raw key value: `rotate-key` has already re-encrypted BOTH
// omnia.db and embeddings.db with newKey, but the keychain Set call itself
// failed, so the keychain still holds the STALE old key — every subsequent
// open will fail until newKey is stored there manually. Mirrors the
// migration code's own `<file>.bak-<timestamp>` timestamp convention
// (internal/store/migrate_encryption.go). The raw key is deliberately
// NEVER printed to stderr (only this file's path is): stderr is captured
// far more readily — via shell redirection, terminal scrollback, CI log
// capture, or a pasted support ticket — than a single 0600 file the
// operator must deliberately open.
func writeRotateRecoveryFile(dataDir, service, account, newKey string) (string, error) {
	name := fmt.Sprintf("security-rotate-recovery-%s.txt", time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(dataDir, name)
	body := fmt.Sprintf(
		"omnia security rotate-key: both omnia.db and embeddings.db were re-encrypted with a NEW\n"+
			"key, but updating the OS keychain failed. The keychain still holds the OLD key, so\n"+
			"neither database is currently openable through the normal path.\n\n"+
			"Recovery: store the key below in the '%s' service / '%s' account keychain entry, then\n"+
			"restart omnia.\n\n"+
			"macOS:\n"+
			"  security add-generic-password -U -s %s -a %s -w '<key below>'\n\n"+
			"Linux (libsecret):\n"+
			"  secret-tool store --label='%s %s' service %s account %s\n"+
			"  (then paste the key at the prompt)\n\n"+
			"key: %s\n",
		service, account, service, account, service, account, service, account, newKey,
	)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func securityKeychainService(appCfg config.Config) string {
	if appCfg.Encryption.KeychainService != "" {
		return appCfg.Encryption.KeychainService
	}
	return "omnia"
}

// reportEncryptionResult prints a one-line summary for one migrated file.
func reportEncryptionResult(label string, alreadyDone bool, rowsBefore, rowsAfter int, backupPath string) {
	if alreadyDone {
		fmt.Printf("%s: already in the target state, nothing to do\n", label)
		return
	}
	if backupPath == "" {
		fmt.Printf("%s: nothing to migrate (no existing file)\n", label)
		return
	}
	fmt.Printf("%s: migrated %d rows (verified %d after); original backed up to %s\n", label, rowsBefore, rowsAfter, backupPath)
}

// cmdSecurityEncrypt implements `omnia security encrypt` (spec REQ-431/432,
// design capability 4 migration). Gated on encryption.enabled=true (unlike
// decrypt/rotate-key below): encrypting while the flag is off would leave a
// file the normal open path can't read back (it only selects the encrypted
// driver when the flag is on), so requiring the flag here is a correctness
// guard, not just style.
func cmdSecurityEncrypt(cfg store.Config, args []string) {
	path := securityConfigFlag(args)
	appCfg, err := config.Load(path)
	if err != nil || !appCfg.Encryption.Enabled {
		fmt.Println("capability disabled (set encryption.enabled: true in config.yaml to opt in)")
		return
	}

	ctx := context.Background()
	service := securityKeychainService(*appCfg)
	hexKey, _, kerr := newSecurityKeychainClient().GetOrCreateHexKey(ctx, service, securityKeyAccount)
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security encrypt: keychain unavailable (%v); refusing to encrypt without a key\n", kerr)
		exitFunc(1)
		return
	}

	omniaDBPath, embDBPath := securityPaths(cfg, *appCfg)

	storeResult, serr := store.MigrateToEncrypted(ctx, omniaDBPath, hexKey)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "omnia security encrypt: omnia.db: %v\n", serr)
		exitFunc(1)
		return
	}
	reportEncryptionResult("omnia.db", storeResult.AlreadyEncrypted, storeResult.RowsBefore, storeResult.RowsAfter, storeResult.BackupPath)

	embResult, eerr := embed.MigrateToEncrypted(ctx, embDBPath, hexKey, appCfg.VecIndex.Enabled)
	if eerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security encrypt: embeddings.db: %v\n", eerr)
		exitFunc(1)
		return
	}
	reportEncryptionResult("embeddings.db", embResult.AlreadyEncrypted, embResult.RowsBefore, embResult.RowsAfter, embResult.BackupPath)
}

// cmdSecurityDecrypt implements `omnia security decrypt` (spec REQ-435:
// reversible in either direction, never locks the user out). Deliberately
// NOT gated on encryption.enabled: REQ-435's own scenario is "set
// encryption.enabled back to false, THEN the store is transparently
// decrypted" — gating decrypt on the SAME flag a user has often already
// flipped off would make the command refuse to run exactly when it's
// needed. Config-load failure still degrades gracefully (never a bare
// fatal), matching every other command's convention.
func cmdSecurityDecrypt(cfg store.Config, args []string) {
	path := securityConfigFlag(args)
	appCfg, err := config.Load(path)
	if err != nil {
		fmt.Println("capability disabled (no readable config.yaml)")
		return
	}

	ctx := context.Background()
	service := securityKeychainService(*appCfg)
	hexKey, kerr := newSecurityKeychainClient().Get(ctx, service, securityKeyAccount)
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security decrypt: no encryption key found in the keychain (%v); cannot decrypt without it\n", kerr)
		exitFunc(1)
		return
	}

	omniaDBPath, embDBPath := securityPaths(cfg, *appCfg)

	storeResult, serr := store.MigrateToPlaintext(ctx, omniaDBPath, hexKey)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "omnia security decrypt: omnia.db: %v\n", serr)
		exitFunc(1)
		return
	}
	reportEncryptionResult("omnia.db", storeResult.AlreadyPlaintext, storeResult.RowsBefore, storeResult.RowsAfter, storeResult.BackupPath)

	embResult, eerr := embed.MigrateToPlaintext(ctx, embDBPath, hexKey, appCfg.VecIndex.Enabled)
	if eerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security decrypt: embeddings.db: %v\n", eerr)
		exitFunc(1)
		return
	}
	reportEncryptionResult("embeddings.db", embResult.AlreadyPlaintext, embResult.RowsBefore, embResult.RowsAfter, embResult.BackupPath)
}

// cmdSecurityRotateKey implements `omnia security rotate-key`: generates a
// new 32-byte key and re-encrypts both files with it BEFORE ever touching
// the keychain entry. If re-encryption of EITHER file fails, the keychain
// is left completely untouched (still holding the OLD key) and both
// on-disk files remain readable with it — never a state where the
// keychain holds a key that doesn't match what's on disk.
func cmdSecurityRotateKey(cfg store.Config, args []string) {
	path := securityConfigFlag(args)
	appCfg, err := config.Load(path)
	if err != nil || !appCfg.Encryption.Enabled {
		fmt.Println("capability disabled (set encryption.enabled: true in config.yaml to opt in)")
		return
	}

	ctx := context.Background()
	service := securityKeychainService(*appCfg)
	client := newSecurityKeychainClient()
	oldKey, kerr := client.Get(ctx, service, securityKeyAccount)
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security rotate-key: no existing encryption key found in the keychain (%v); nothing to rotate\n", kerr)
		exitFunc(1)
		return
	}
	newKeyBytes, gerr := keychain.GenerateKey()
	if gerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security rotate-key: generate new key: %v\n", gerr)
		exitFunc(1)
		return
	}
	newKey := hex.EncodeToString(newKeyBytes)

	omniaDBPath, embDBPath := securityPaths(cfg, *appCfg)

	storeResult, serr := store.RotateKey(ctx, omniaDBPath, oldKey, newKey)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "omnia security rotate-key: omnia.db: %v (keychain NOT changed; old key still active)\n", serr)
		exitFunc(1)
		return
	}
	embResult, eerr := embed.RotateKey(ctx, embDBPath, oldKey, newKey, appCfg.VecIndex.Enabled)
	if eerr != nil {
		fmt.Fprintf(os.Stderr, "omnia security rotate-key: embeddings.db: %v\n"+
			"omnia.db was ALREADY rotated to the new key, but embeddings.db was not — the keychain\n"+
			"has NOT been updated. omnia.db is now unreadable with the OLD key. Restore omnia.db\n"+
			"from its .bak-* backup and retry, or manually complete the rotation before restarting.\n", eerr)
		exitFunc(1)
		return
	}
	if serr := client.Set(ctx, service, securityKeyAccount, newKey); serr != nil {
		// Both on-disk files are now encrypted with newKey, but the
		// keychain still holds the stale oldKey — the operator MUST
		// recover newKey to regain access. Never print it to stderr
		// (shell redirection, terminal scrollback, CI log capture, and
		// pasted support tickets all make stderr far more likely to leak
		// than a single 0600 recovery file the operator must deliberately
		// open).
		dataDir := securityDataDir(cfg)
		recoveryPath, werr := writeRotateRecoveryFile(dataDir, service, securityKeyAccount, newKey)
		if werr != nil {
			fmt.Fprintf(os.Stderr, "omnia security rotate-key: both files rotated, but updating the keychain failed (%v),\n"+
				"AND writing a recovery file also failed (%v). The new key cannot be recovered once this\n"+
				"process exits; investigate and retry immediately.\n", serr, werr)
			exitFunc(1)
			return
		}
		fmt.Fprintf(os.Stderr, "omnia security rotate-key: both files rotated, but updating the keychain failed (%v).\n"+
			"The new key was written to %s (mode 0600) — follow the recovery instructions in that\n"+
			"file to store it in the '%s'/'%s' keychain entry manually, or every subsequent open\n"+
			"will fail until it is set.\n", serr, recoveryPath, service, securityKeyAccount)
		exitFunc(1)
		return
	}

	reportEncryptionResult("omnia.db", false, storeResult.RowsBefore, storeResult.RowsAfter, storeResult.BackupPath)
	reportEncryptionResult("embeddings.db", false, embResult.RowsBefore, embResult.RowsAfter, embResult.BackupPath)
}
