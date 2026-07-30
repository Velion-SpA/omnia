package embed

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/vec1"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/keychain"
)

// This file implements v0.4's `memory-at-rest-security` capability (design
// ADR-1/ADR-2/ADR-3, spec REQ-430 through REQ-434) for internal/embed's
// embeddings.db — the sibling half of the seam internal/store/encryption.go
// implements for omnia.db. The keychain-or-fail DECISION itself lives in
// internal/keychain.Resolve (tasks.md 4.7 REFACTOR), shared verbatim with
// internal/store: internal/store and internal/embed must never import each
// other (existing architecture guardrail — see worker_test.go), so this is
// the one place the logic can live without either package depending on the
// other.
//
// Ownership stays package-local, mirroring vec1_connector.go's own
// convention: core-store driver selection (internal/store) must not
// acquire a dependency on this file, and vice versa.

// encryptionKeyAccount is the fixed keychain account name for the single
// key protecting both omnia.db and embeddings.db (design: "Service=omnia,
// account=db-key-v1"; one shared key, not one per database).
const encryptionKeyAccount = "db-key-v1"

// newKeychainClient constructs the real keychain client. Injectable
// (mirrors vec1_connector.go's own option-seam convention) so tests never
// shell out to a real `security`/`secret-tool` process.
var newKeychainClient = func() keychain.Resolver { return keychain.New() }

// auditAppend is injectable so tests can capture the degradation audit
// entry without touching the real on-disk audit log.
var auditAppend = audit.Append

// WithEncryption opts a Store into the additive at-rest encryption
// capability (spec REQ-430 through REQ-434). enabled=false (the default,
// zero value) reproduces OpenStore's pre-v0.4 behavior exactly. When
// enabled, keychainService selects the OS keychain service name
// ("omnia" if empty) and allowPlaintextFallback controls the
// keychain-unavailable degradation decision (design ADR-3): false (the
// default posture) makes OpenStore refuse to open when the keychain cannot
// be reached; true logs a loud warning, appends exactly one
// ActionEncryptionFallback audit entry, and opens unencrypted instead.
func WithEncryption(enabled bool, keychainService string, allowPlaintextFallback bool) Option {
	return func(o *storeOptions) {
		o.encryptionEnabled = enabled
		o.encryptionKeychainService = keychainService
		o.encryptionAllowPlaintextFallback = allowPlaintextFallback
	}
}

// resolveEmbedEncryption applies the shared keychain.Resolve decision for
// OpenStore's encryption option.
//
//   - cfg.encryptionEnabled is false → (active=false, err=nil): encryption
//     was never requested, caller proceeds exactly as before this
//     capability existed.
//   - key resolves normally → (hexKey, active=true, err=nil): caller MUST
//     open via the adiantum VFS with hexKey.
//   - resolution fails and allowPlaintextFallback=true → logs a warning,
//     appends exactly one audit entry, returns (active=false, err=nil):
//     caller proceeds exactly like encryption was never requested (a
//     conscious, audited operator opt-in — never silent).
//   - resolution fails and allowPlaintextFallback=false (the default) →
//     (err != nil): caller MUST refuse to open (spec REQ-434 fail-closed).
func resolveEmbedEncryption(cfg storeOptions, dbPath string) (hexKey string, active bool, err error) {
	if !cfg.encryptionEnabled {
		return "", false, nil
	}
	service := cfg.encryptionKeychainService
	if service == "" {
		service = "omnia"
	}
	decision, rerr := keychain.Resolve(context.Background(), newKeychainClient(), service, encryptionKeyAccount, cfg.encryptionAllowPlaintextFallback)
	if rerr != nil {
		return "", false, rerr
	}
	if decision.Degraded {
		log.Printf("[embed/encryption] WARNING: keychain unavailable (%v); encryption.allow_plaintext_fallback=true — opening %s UNENCRYPTED", decision.Cause, dbPath)
		auditAppend(audit.Entry{
			Ts:     audit.Now(),
			Actor:  "omnia",
			Action: audit.ActionEncryptionFallback,
			Result: "degraded",
			Note:   fmt.Sprintf("keychain unavailable, opened unencrypted (allow_plaintext_fallback=true): %v", decision.Cause),
		})
		return "", false, nil
	}
	return decision.HexKey, true, nil
}

// openEncryptedEmbedDB opens path through the bundled, pure-Go
// ncruces/go-sqlite3 driver with the adiantum encrypting VFS selected via
// the DSN's `vfs=` parameter. Unlike OpenStore's plain modernc dsn (which
// sets busy_timeout/journal_mode via `_pragma=` URI parameters), this DSN
// carries ONLY `vfs=adiantum`: the ncruces driver's own docs specify that
// "encryption keys, busy timeout and locking mode should be the first
// PRAGMAs set, in that order" — URI `_pragma=` parameters are applied before
// any init callback runs, which would set busy_timeout/journal_mode BEFORE
// the encryption key, violating that required order (empirically confirmed:
// combining them produces "invalid _pragma: unable to open database file"
// against the still-undecrypted file). All three PRAGMAs are therefore
// issued explicitly, in the correct order, inside the init callback below.
//
// includeVec1 composes vec1.Register in the SAME per-connection init
// callback when the vec-index capability is ALSO enabled (design: "encryption
// may compose its VFS initialization and vec1.Register in the same
// internal/embed connection initializer, in documented order") — encryption
// key material is established first, then Vec1 registration, matching the
// order both capabilities' own designs document independently.
func openEncryptedEmbedDB(path, hexKey string, includeVec1 bool) (*sql.DB, error) {
	encDSN := "file:" + path + "?vfs=adiantum"
	init := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		if err := c.Exec("PRAGMA busy_timeout=5000"); err != nil {
			return err
		}
		if err := c.Exec("PRAGMA journal_mode=WAL"); err != nil {
			return err
		}
		if includeVec1 {
			return vec1.Register(c)
		}
		return nil
	}
	db, err := ncrucesdriver.Open(encDSN, init)
	if err != nil {
		return nil, fmt.Errorf("embed: open encrypted database: %w", err)
	}
	return db, nil
}
