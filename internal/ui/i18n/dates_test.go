package i18n

import (
	"testing"
	"time"
)

func TestFormatDate(t *testing.T) {
	ts := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)

	if got, want := FormatDate(ts, LangEN), "Jan 2, 2026"; got != want {
		t.Errorf("FormatDate(en) = %q, want %q", got, want)
	}
	if got, want := FormatDate(ts, LangES), "2 ene 2026"; got != want {
		t.Errorf("FormatDate(es) = %q, want %q", got, want)
	}
}
