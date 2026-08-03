package engramdb

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/ncruces/go-sqlite3"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/keychain"
)

// This file gives the read-only reader the same at-rest encryption seam
// internal/store/encryption.go (omnia.db) and internal/embed/encryption.go
// (embeddings.db) already implement for the WRITE paths (v0.4
// `memory-at-rest-security`, spec REQ-430 through REQ-434).
//
// It exists because the capability shipped with the read side unaddressed
// (#228): every caller of Open — `omnia embed`'s reconcile reader and the
// dashboard's structural reader — went through a plain modernc `sql.Open`
// that knows nothing about encryption, so against an encrypted store they
// all died with the opaque "file is not a database (26)".
//
// The keychain-or-fail DECISION itself lives in internal/keychain.Resolve,
// shared verbatim with internal/store and internal/embed. Those two may
// never import each other (existing architecture guardrail), and this
// package must stay a leaf reader that neither of them depends on — so the
// thin wrapper below is deliberately a third local application of the same
// shared decision, not a fourth copy of its logic.

// encryptionKeyAccount is the fixed keychain account name for the single key
// protecting omnia.db and embeddings.db alike (design: "Service=omnia,
// account=db-key-v1"). It MUST match internal/store's and internal/embed's
// constant of the same name: the reader opens the very file internal/store
// wrote.
const encryptionKeyAccount = "db-key-v1"

// newKeychainClient constructs the real keychain client. Injectable (mirrors
// the identical seam in internal/store and internal/embed) so tests never
// shell out to a real `security`/`secret-tool` process.
var newKeychainClient = func() keychain.Resolver { return keychain.New() }

// auditAppend is injectable so tests can capture the degradation audit entry
// without touching the real on-disk audit log.
var auditAppend = audit.Append

// openOptions carries Open's optional behaviour. The zero value reproduces
// the pre-#228 read-only modernc path exactly.
type openOptions struct {
	encryptionEnabled                bool
	encryptionKeychainService        string
	encryptionAllowPlaintextFallback bool
}

// Option configures Open.
type Option func(*openOptions)

// WithEncryption opts the reader into at-rest encryption, mirroring
// embed.WithEncryption's signature and semantics so a caller can pass the
// SAME config.Encryption values to both without translating.
//
// enabled=false (the default) reproduces Open's pre-#228 behaviour exactly:
// the plain modernc read-only path, no keychain consultation of any kind.
func WithEncryption(enabled bool, keychainService string, allowPlaintextFallback bool) Option {
	return func(o *openOptions) {
		o.encryptionEnabled = enabled
		o.encryptionKeychainService = keychainService
		o.encryptionAllowPlaintextFallback = allowPlaintextFallback
	}
}

// resolveReaderEncryption applies the shared keychain.Resolve decision.
//
//   - encryption disabled → (active=false, err=nil): open exactly as before.
//   - key resolves → (hexKey, active=true, err=nil): open via adiantum.
//   - resolution fails, allowPlaintextFallback=true → warns, appends exactly
//     one audit entry, returns (active=false, err=nil).
//   - resolution fails, allowPlaintextFallback=false (default posture) →
//     (err != nil): the caller MUST refuse to open (REQ-434 fail-closed).
func resolveReaderEncryption(o openOptions, dbPath string) (hexKey string, active bool, err error) {
	if !o.encryptionEnabled {
		return "", false, nil
	}
	service := o.encryptionKeychainService
	if service == "" {
		service = "omnia"
	}
	decision, rerr := keychain.Resolve(context.Background(), newKeychainClient(), service, encryptionKeyAccount, o.encryptionAllowPlaintextFallback)
	if rerr != nil {
		return "", false, rerr
	}
	if decision.Degraded {
		log.Printf("[engramdb/encryption] WARNING: keychain unavailable (%v); encryption.allow_plaintext_fallback=true — reading %s UNENCRYPTED", decision.Cause, dbPath)
		auditAppend(audit.Entry{
			Ts:     audit.Now(),
			Actor:  "omnia",
			Action: audit.ActionEncryptionFallback,
			Result: "degraded",
			Note:   fmt.Sprintf("keychain unavailable, read unencrypted (allow_plaintext_fallback=true): %v", decision.Cause),
		})
		return "", false, nil
	}
	return decision.HexKey, true, nil
}

// openEncryptedReader opens dbPath read-only through the pure-Go ncruces
// driver with the adiantum encrypting VFS.
//
// Two deliberate differences from internal/store's and internal/embed's write
// paths, both load-bearing:
//
//  1. `mode=ro` is carried in the URI (an open FLAG, not a PRAGMA) so this
//     path keeps the read-only guarantee Open has always given its callers.
//     `omnia embed` reads while the daemon writes; nothing here may mutate
//     the store.
//  2. busy_timeout is issued in the init callback rather than as a URI
//     `_pragma=`, because URI pragmas are applied BEFORE any init callback
//     runs — which would set it ahead of the encryption key and violate the
//     ncruces driver's documented "encryption keys, busy timeout and locking
//     mode, in that order" requirement (see internal/embed/encryption.go's
//     openEncryptedEmbedDB for the empirical confirmation). journal_mode is
//     NOT set at all: it is a write operation and the plaintext read-only
//     path never set it either.
func openEncryptedReader(dbPath, hexKey string) (*sql.DB, error) {
	dsn := "file:" + dbPath + "?vfs=adiantum&mode=ro"
	init := func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		return c.Exec("PRAGMA busy_timeout=5000")
	}
	db, err := ncrucesdriver.Open(dsn, init)
	if err != nil {
		return nil, fmt.Errorf("engramdb: open encrypted %s: %w", dbPath, err)
	}
	return db, nil
}

// isPlaintextSQLiteFile reports whether path exists and still carries the
// unencrypted "SQLite format 3" header. A local copy of internal/store's
// helper of the same name: this package must not import internal/store, and
// without the check an unmigrated store fails with the same opaque
// "file is not a database" error #228 exists to eliminate.
func isPlaintextSQLiteFile(path string) (exists bool, plaintext bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	header := make([]byte, 16)
	n, rerr := f.Read(header)
	if rerr != nil && n == 0 {
		return true, false, nil // empty/unreadable file — treat as not-plaintext
	}
	return true, n >= 15 && string(header[:15]) == "SQLite format 3", nil
}
