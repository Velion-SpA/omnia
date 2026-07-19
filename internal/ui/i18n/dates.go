package i18n

import (
	"fmt"
	"time"
)

// monthAbbrevEN/ES are used only by FormatDate's "old" fallback branch — the
// point where relative-time formatting (formatAge, RelativeTimeLang) gives up
// on "Xd ago" phrasing and falls back to a short absolute date. Kept as a
// simple index-by-month array (no locale library dependency) since this is
// the only place that needs a localized month name in the whole dashboard.
var monthAbbrevEN = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
var monthAbbrevES = [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

// FormatDate returns a short, locale-appropriate absolute date for t — used
// by relative-time formatters once an item is old enough that "Xd ago" stops
// being useful. English keeps the pre-Slice-2 "Jan 2, 2006" shape exactly
// (day-of-month without zero-padding, matching time.Time.Format's "2" verb);
// Spanish uses the equivalent "2 ene 2006" day-month-year order.
func FormatDate(t time.Time, lang Lang) string {
	day, year := t.Day(), t.Year()
	if lang == LangES {
		return fmt.Sprintf("%d %s %d", day, monthAbbrevES[t.Month()-1], year)
	}
	return fmt.Sprintf("%s %d, %d", monthAbbrevEN[t.Month()-1], day, year)
}
