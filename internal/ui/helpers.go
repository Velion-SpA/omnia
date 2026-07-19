package ui

import (
	"fmt"
	"time"

	"github.com/velion/omnia/internal/ui/i18n"
)

// isFresh reports whether t is within the last 24 hours.
func isFresh(t time.Time) bool { return time.Since(t) < 24*time.Hour }

// RelativeTime formats t as a human-readable relative duration (e.g. "5m
// ago", "yesterday", falling back to an absolute date after 30 days).
// Exported for cross-package reuse — Command Center v2, Slice 4b uses it for
// the Admin Projects card's "last activity" stat (cloudserver is a separate
// package from ui, so it needs a public name).
func RelativeTime(t time.Time) string {
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

// RelativeTimeLang is the locale-aware variant of RelativeTime, added in
// i18n Slice 2 for the shared-dashboard pages (project detail's
// "Last activity" stat). RelativeTime itself is left UNCHANGED (still
// English-only) because internal/cloud/cloudserver's admin pages — which
// call it directly — are Slice 3 scope; changing RelativeTime's signature
// would force edits there. Same thresholds as RelativeTime, only the wording
// is resolved through the i18n catalog.
func RelativeTimeLang(t time.Time, lang i18n.Lang) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T(lang, "age.justNow")
	case d < time.Hour:
		return i18n.Tf(lang, "age.minutesAgo", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.Tf(lang, "age.hoursAgo", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	switch {
	case days == 1:
		return i18n.T(lang, "age.yesterday")
	case days < 30:
		return i18n.Tf(lang, "age.daysAgo", days)
	}
	return i18n.FormatDate(t, lang)
}
