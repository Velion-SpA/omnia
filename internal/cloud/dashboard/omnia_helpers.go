package dashboard

import (
	"fmt"
	"time"
)

// typeColor returns the Calypso-palette hex foreground for an observation type chip.
func typeColor(t string) string {
	switch t {
	case "decision":
		return "#22d3ee"
	case "bugfix":
		return "#f87171"
	case "architecture":
		return "#a78bfa"
	case "discovery":
		return "#34d399"
	case "pattern":
		return "#fb923c"
	case "config":
		return "#fbbf24"
	case "learning":
		return "#38bdf8"
	default:
		return "#94a3b8"
	}
}

// typeColorBg returns a translucent rgba background string for an observation type chip.
func typeColorBg(t string) string {
	switch t {
	case "decision":
		return "rgba(34,211,238,0.10)"
	case "bugfix":
		return "rgba(248,113,113,0.10)"
	case "architecture":
		return "rgba(167,139,250,0.10)"
	case "discovery":
		return "rgba(52,211,153,0.10)"
	case "pattern":
		return "rgba(251,146,60,0.10)"
	case "config":
		return "rgba(251,191,36,0.10)"
	case "learning":
		return "rgba(56,189,248,0.10)"
	default:
		return "rgba(148,163,184,0.08)"
	}
}

// isFresh reports whether t is within the last 24 hours.
func isFresh(t time.Time) bool {
	return time.Since(t) < 24*time.Hour
}

// relativeTime formats t as a human-readable relative duration (e.g. "3h ago", "just now").
func relativeTime(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "yesterday"
	}
	if days < 30 {
		return fmt.Sprintf("%dd ago", days)
	}
	return t.Format("Jan 2, 2006")
}
