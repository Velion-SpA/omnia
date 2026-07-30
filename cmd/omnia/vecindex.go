package main

import (
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
)

// embedStoreOptions returns the shared embed.Option set every direct
// embeddings-store opener in this package (and internal/dashboard, via its
// own Config fields) must pass through to embed.OpenStore. Originally
// vecIndexStoreOptions (design capability 7, "Production composition and PR
// ownership": "PR 2B owns production propagation of one shared OpenStore
// option derived from Config.VecIndex.Enabled. Every direct production
// opener must pass it ... Callers never invoke driver.Open or vec1.Register
// themselves."), extended by v0.4 memory-at-rest-security (design ADR-1/
// ADR-2/ADR-3) to also thread encCfg — an encrypted embeddings.db (after
// `omnia security encrypt`) can only be reopened by a production caller
// that passes embed.WithEncryption; without this, every normal
// `omnia embed`/auto-embed/recall/purge run would try to open the now-
// encrypted file via plain modernc and fail.
//
// vecIndexEnabled=false, encCfg zero value reproduces embed.OpenStore's
// pre-v0.4 behavior exactly (both embed.WithVecIndex(false) and
// embed.WithEncryption(false, ...) are documented no-ops), so every caller
// of this helper is byte-for-byte unaffected when neither capability is
// enabled.
func embedStoreOptions(vecIndexEnabled bool, encCfg config.EncryptionConfig) []embed.Option {
	return []embed.Option{
		embed.WithVecIndex(vecIndexEnabled),
		embed.WithEncryption(encCfg.Enabled, encCfg.KeychainService, encCfg.AllowPlaintextFallback),
	}
}
