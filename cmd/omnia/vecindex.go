package main

import "github.com/velion/omnia/internal/embed"

// vecIndexStoreOptions returns the shared embed.Option set every direct
// embeddings-store opener in this package (and internal/dashboard, via its
// own Config field) must pass through to embed.OpenStore (design capability
// 7, "Production composition and PR ownership": "PR 2B owns production
// propagation of one shared OpenStore option derived from
// Config.VecIndex.Enabled. Every direct production opener must pass it ...
// Callers never invoke driver.Open or vec1.Register themselves.").
//
// enabled=false reproduces embed.OpenStore's pre-v0.4 behavior exactly
// (embed.WithVecIndex(false) is a documented no-op), so every caller of this
// helper is byte-for-byte unaffected when vector_index.enabled is absent or
// false.
func vecIndexStoreOptions(enabled bool) []embed.Option {
	return []embed.Option{embed.WithVecIndex(enabled)}
}
