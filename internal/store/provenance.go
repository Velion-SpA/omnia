package store

import "strings"

// Trust-tag constants for the memory-provenance foundation
// (omnia-provenance-foundation). These are ATTRIBUTION, not authentication:
// recording where a memory came from, never a judgment about whether its
// content is safe or correct. classifyTrust never blocks a save.
const (
	TrustTagUser       = "user"
	TrustTagAgent      = "agent"
	TrustTagIngestTool = "ingest:tool"
	TrustTagIngestWeb  = "ingest:web"
	TrustTagIngestDoc  = "ingest:doc"
	TrustTagUnverified = "unverified"
)

// classifyTrust maps a write-time `source` argument to its trust class. It is
// a pure function (design obs #1601): called from AddObservation before any
// persistence, not at retrieval time — retrieval-only filtering was proven
// insufficient (UW "Bad Memory" research, see design.md). Any source outside
// the recognized taxonomy — including empty/absent — degrades to
// "unverified" rather than rejecting the save: this tag is ATTRIBUTION, not
// authentication, so an unrecognized source is recorded honestly instead of
// blocking the write.
func classifyTrust(source string) string {
	switch strings.TrimSpace(source) {
	case TrustTagUser:
		return TrustTagUser
	case TrustTagAgent:
		return TrustTagAgent
	case TrustTagIngestTool:
		return TrustTagIngestTool
	case TrustTagIngestWeb:
		return TrustTagIngestWeb
	case TrustTagIngestDoc:
		return TrustTagIngestDoc
	default:
		return TrustTagUnverified
	}
}
