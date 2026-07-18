package ui

import (
	"fmt"
	"time"
)

// isFresh reports whether t is within the last 24 hours.
func isFresh(t time.Time) bool { return time.Since(t) < 24*time.Hour }

// relativeTime formats t as a human-readable relative duration.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	switch {
	case days == 1:
		return "yesterday"
	case days < 30:
		return fmt.Sprintf("%dd ago", days)
	}
	return t.Format("Jan 2, 2006")
}
