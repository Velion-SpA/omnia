package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/keychain"
)

// This file implements v0.4's `memory-at-rest-security` capability (design
// ADR-1/ADR-2/ADR-3, spec REQ-430 through REQ-434): the omnia.db half of the
// dual-SQLite-driver encryption seam that sqlite-vec-index's driver
// selection already established for internal/embed's embeddings.db
// (embed/vec1_connector.go). internal/store does not import internal/config
// (same convention every other Config field on this struct documents), but
// DOES import internal/keychain (a dependency-free leaf package — no import
// cycle risk). The keychain-or-fail DECISION itself lives in
// internal/keychain.Resolve (tasks.md 4.7 REFACTOR), shared verbatim with
// internal/embed's own encryption.go — internal/store and internal/embed
// must never import each other (existing architecture guardrail), so this
// is the one place the logic can live without either package acquiring a
// dependency on the other.

// encryptionKeyAccount is the fixed keychain account name for the single
// key protecting both omnia.db and embeddings.db (design: "Service=omnia,
// account=db-key-v1"; one shared key, not one per database — simpler
// key-management/rotation story).
const encryptionKeyAccount = "db-key-v1"

// newKeychainClient constructs the real keychain client. Injectable
// (mirrors openDB's own package-level var seam) so tests never shell out to
// a real `security`/`secret-tool` process.
var newKeychainClient = func() keychain.Resolver { return keychain.New() }

// auditAppend is injectable (mirrors internal/mcp's own auditAppend seam)
// so tests can capture the degradation audit entry without touching the
// real on-disk audit log.
var auditAppend = audit.Append

// ErrEncryptionKeyUnavailable re-exports keychain.ErrKeyUnavailable under a
// store-domain name for callers that only import internal/store (spec
// REQ-434's fail-closed contract: the store MUST NEVER silently open as
// plaintext or generate a throwaway key when this is returned).
var ErrEncryptionKeyUnavailable = keychain.ErrKeyUnavailable

// openEngramDB opens dbPath honoring cfg.EncryptionEnabled.
//
// Disabled (the zero value, spec REQ-430): returns openDB("sqlite", dbPath)
// exactly as before this capability existed — byte-for-byte the pre-v0.4
// modernc path, no keychain consultation of any kind.
//
// Enabled (spec REQ-431/432/434, design ADR-2/ADR-3): resolves (or
// generates, on first enable) a 32-byte key from the OS keychain via the
// shared keychain.Resolve decision and opens dbPath through the pure-Go
// ncruces driver's adiantum encrypting VFS. Keychain-unavailable
// degradation is explicit, never silent: AllowPlaintextFallback=false (the
// default) refuses to open (ErrEncryptionKeyUnavailable); =true logs a loud
// warning, appends exactly one ActionEncryptionFallback audit entry, and
// falls back to the plain modernc path — a conscious operator opt-in,
// never automatic.
func openEngramDB(cfg Config, dbPath string) (*sql.DB, error) {
	if !cfg.EncryptionEnabled {
		return openDB("sqlite", dbPath)
	}

	// A brand-new/nonexistent dbPath is fine (a fresh store is created
	// encrypted from scratch, below). But an EXISTING, detectably plaintext
	// file means the operator flipped encryption.enabled=true on an
	// already-populated plaintext store without migrating it first — the
	// natural first move, instead of running `omnia security encrypt`.
	// Without this check, ncruces' adiantum VFS fails with a generic
	// "file is not a database"-style error that gives no hint the fix is
	// to run the migration CLI (reuses migrate_encryption.go's own
	// isPlaintextSQLiteFile detection helper, previously unused here).
	if existed, plaintext, perr := isPlaintextSQLiteFile(dbPath); perr != nil {
		return nil, fmt.Errorf("engram: inspect %s: %w", dbPath, perr)
	} else if existed && plaintext {
		return nil, fmt.Errorf("engram: %s is a plaintext SQLite database but encryption.enabled=true — run `omnia security encrypt` first to migrate it", dbPath)
	}

	service := cfg.EncryptionKeychainService
	if service == "" {
		service = "omnia"
	}
	decision, err := keychain.Resolve(context.Background(), newKeychainClient(), service, encryptionKeyAccount, cfg.EncryptionAllowPlaintextFallback)
	if err != nil {
		return nil, err
	}
	if decision.Degraded {
		log.Printf("[store/encryption] WARNING: keychain unavailable (%v); encryption.allow_plaintext_fallback=true — opening %s UNENCRYPTED", decision.Cause, dbPath)
		auditAppend(audit.Entry{
			Ts:     audit.Now(),
			Actor:  "omnia",
			Action: audit.ActionEncryptionFallback,
			Result: "degraded",
			Note:   fmt.Sprintf("keychain unavailable, opened unencrypted (allow_plaintext_fallback=true): %v", decision.Cause),
		})
		return openDB("sqlite", dbPath)
	}

	return openAdiantumDB(dbPath, decision.HexKey)
}

// openAdiantumDB opens dbPath through the bundled, pure-Go ncruces/go-sqlite3
// driver with the adiantum encrypting VFS selected via the DSN's `vfs=`
// parameter and the key supplied through a per-connection PRAGMA (issued in
// the init callback, NOT the URI — the adiantum package's own docs warn a
// URI-embedded key is visible via vfs.Filename.URIParameters to any code
// that inspects the open filename; the PRAGMA form never appears there).
//
// fts5.Register is ALSO installed on every connection in the same init
// callback: internal/store's schema (observations_fts, procedures_fts,
// internal/store/procedures.go:401) depends on FTS5, which — unlike
// modernc.org/sqlite's build — is not compiled into the ncruces driver by
// default (empirically confirmed: opening without it fails with "no such
// module: fts5" on the very first FTS5-backed migration). This mirrors
// internal/embed's vec1.Register per-connection init pattern exactly
// (design ADR-4's "never assumed to be process-global").
func openAdiantumDB(dbPath, hexKey string) (*sql.DB, error) {
	dsn := "file:" + dbPath + "?vfs=adiantum"
	init := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		return fts5.Register(c)
	}
	db, err := ncrucesdriver.Open(dsn, init)
	if err != nil {
		return nil, fmt.Errorf("open encrypted database: %w", err)
	}
	return db, nil
}
