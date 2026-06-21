package ui

// Card is the neutral memory-card model rendered by MemoryCard / MemoryFeed.
// Both dashboards build it from their own rows (local ObsView, cloud
// DashboardObservationRow); the card itself is identical everywhere.
type Card struct {
	Href      string
	Title     string // truncated by the card
	Snippet   string // already extracted by the caller
	Type      string
	SourceKey string // "github" | "discord" | "" — drives the source chip icon
	Tag       string // free meta tag (e.g. project) shown when SourceKey is empty
	Age       string
	Ingested  bool // true → ingested accent + layer badge, false → curated
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

// typeGlyphSVG returns the small inline glyph for an observation type.
func typeGlyphSVG(t string) string {
	switch t {
	case "architecture":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linejoin="round" aria-hidden="true"><polygon points="6,1 10.5,3.5 10.5,8.5 6,11 1.5,8.5 1.5,3.5"/></svg>`
	case "decision":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linejoin="round" aria-hidden="true"><polygon points="6,1.2 10.8,6 6,10.8 1.2,6"/></svg>`
	case "github-pr":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" aria-hidden="true"><circle cx="3" cy="3" r="1.3"/><circle cx="3" cy="9" r="1.3"/><circle cx="9" cy="3" r="1.3"/><line x1="3" y1="4.3" x2="3" y2="7.7"/><path d="M9 4.3 C9 6.5 5 6.5 5 8"/><line x1="5" y1="8" x2="3.1" y2="8"/></svg>`
	case "bugfix":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="7,1.5 4,6.5 6.5,6.5 5,10.5 8,5.5 5.5,5.5"/></svg>`
	case "discovery":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" aria-hidden="true"><path d="M1.5 6 C3 3 9 3 10.5 6 C9 9 3 9 1.5 6 Z"/><circle cx="6" cy="6" r="1.5"/></svg>`
	case "config":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" aria-hidden="true"><line x1="1.5" y1="3.5" x2="10.5" y2="3.5"/><line x1="1.5" y1="8.5" x2="10.5" y2="8.5"/><circle cx="4" cy="3.5" r="1.3" fill="var(--bg)"/><circle cx="8" cy="8.5" r="1.3" fill="var(--bg)"/></svg>`
	case "pattern":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true"><circle cx="3" cy="3" r="1"/><circle cx="6" cy="3" r="1"/><circle cx="9" cy="3" r="1"/><circle cx="3" cy="6" r="1"/><circle cx="6" cy="6" r="1"/><circle cx="9" cy="6" r="1"/><circle cx="3" cy="9" r="1"/><circle cx="6" cy="9" r="1"/><circle cx="9" cy="9" r="1"/></svg>`
	case "reference":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 1.5 H9.5 V10.5 L6 8.5 L2.5 10.5 Z"/></svg>`
	case "session_summary":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" aria-hidden="true"><rect x="1.5" y="1" width="9" height="10" rx="1"/><line x1="3.5" y1="4" x2="8.5" y2="4"/><line x1="3.5" y1="6" x2="8.5" y2="6"/><line x1="3.5" y1="8" x2="6.5" y2="8"/></svg>`
	case "passive":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" aria-hidden="true"><circle cx="6" cy="6" r="4.5"/><circle cx="6" cy="6" r="1.5" fill="currentColor" stroke="none"/></svg>`
	case "learning":
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" aria-hidden="true"><path d="M6 3 C4 2 2 2.5 1.5 3 L1.5 9.5 C2 9 4 8.5 6 9.5"/><path d="M6 3 C8 2 10 2.5 10.5 3 L10.5 9.5 C10 9 8 8.5 6 9.5"/><line x1="6" y1="3" x2="6" y2="9.5"/></svg>`
	default:
		return `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.1" aria-hidden="true"><circle cx="6" cy="6" r="4"/></svg>`
	}
}
