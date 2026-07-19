package ui

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/ui/i18n"
)

func TestRelativeTimeLang(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name string
		t    time.Time
		lang i18n.Lang
		want string
	}{
		{"just now (es)", now.Add(-5 * time.Second), i18n.LangES, "ahora mismo"},
		{"just now (en)", now.Add(-5 * time.Second), i18n.LangEN, "just now"},
		{"minutes ago (es)", now.Add(-5 * time.Minute), i18n.LangES, "hace 5m"},
		{"minutes ago (en)", now.Add(-5 * time.Minute), i18n.LangEN, "5m ago"},
		{"hours ago (es)", now.Add(-3 * time.Hour), i18n.LangES, "hace 3h"},
		{"hours ago (en)", now.Add(-3 * time.Hour), i18n.LangEN, "3h ago"},
		{"yesterday (es)", now.Add(-25 * time.Hour), i18n.LangES, "ayer"},
		{"yesterday (en)", now.Add(-25 * time.Hour), i18n.LangEN, "yesterday"},
		{"days ago (es)", now.Add(-5 * 24 * time.Hour), i18n.LangES, "hace 5d"},
		{"days ago (en)", now.Add(-5 * 24 * time.Hour), i18n.LangEN, "5d ago"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RelativeTimeLang(c.t, c.lang); got != c.want {
				t.Errorf("RelativeTimeLang() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRelativeTimeLang_OldFallsBackToAbsoluteDate(t *testing.T) {
	old := time.Now().Add(-45 * 24 * time.Hour)

	gotEN := RelativeTimeLang(old, i18n.LangEN)
	if gotEN == "just now" || gotEN == "unknown" {
		t.Errorf("RelativeTimeLang(en, old) = %q, expected an absolute date", gotEN)
	}
	gotES := RelativeTimeLang(old, i18n.LangES)
	if gotES == "ahora mismo" || gotES == "desconocido" {
		t.Errorf("RelativeTimeLang(es, old) = %q, expected an absolute date", gotES)
	}
}
