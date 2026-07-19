package ui

// Card is the neutral memory-card model rendered by MemoryCard / MemoryFeed.
// Both dashboards build it from their own rows (local ObsView, cloud
// DashboardObservationRow); the card itself is identical everywhere.
type Card struct {
	Href      string
	Title     string // truncated by the card
	Snippet   string // already extracted by the caller
	Type      string
	Project   string // footer project chip; empty hides the chip
	SourceKey string // "github" | "discord" | "" — drives the source chip icon
	Tag       string // free-form source label shown when SourceKey is empty (falls back to "Manual")
	Age       string
	Ingested  bool // true → subtle accent glow, false → curated (no glow)
}

// cardTypeAccentClass maps an observation type to its accent-color modifier
// class, shared by the card's left stripe (.card-accent-bar) and its type
// badge (.card-type-badge). Unknown/empty types get the neutral default.
func cardTypeAccentClass(t string) string {
	switch t {
	case "architecture":
		return "card-type-architecture"
	case "decision":
		return "card-type-decision"
	case "bugfix":
		return "card-type-bugfix"
	case "config":
		return "card-type-config"
	case "discovery":
		return "card-type-discovery"
	case "pattern":
		return "card-type-pattern"
	default:
		return "card-type-default"
	}
}

// truncateTitle trims long titles for the list view.
func truncateTitle(s string) string {
	const max = 80
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// typeGlyphSVG previously rendered a per-type inline glyph next to the card
// title. Removed in the Command Center v2 Explorar rework (Slice 4a) — the
// card now leads with a colored type badge instead (see cardTypeAccentClass),
// matching the mockup. Deleted rather than left dead per the design review's
// "delete dead code" direction.
