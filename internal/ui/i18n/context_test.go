package i18n

import (
	"context"
	"testing"
)

// TestLangFrom_DefaultWhenAbsent guards the Spanish-default requirement at
// the context layer: a context with no lang value set must resolve to
// DefaultLang (Spanish), not a zero value or panic.
func TestLangFrom_DefaultWhenAbsent(t *testing.T) {
	if got := LangFrom(context.Background()); got != DefaultLang {
		t.Errorf("LangFrom(bare ctx) = %q, want %q", got, DefaultLang)
	}
}

// TestWithLang_RoundTrip checks the ctx round trip for both languages.
func TestWithLang_RoundTrip(t *testing.T) {
	ctx := WithLang(context.Background(), LangEN)
	if got := LangFrom(ctx); got != LangEN {
		t.Errorf("LangFrom(WithLang(en)) = %q, want %q", got, LangEN)
	}

	ctx = WithLang(context.Background(), LangES)
	if got := LangFrom(ctx); got != LangES {
		t.Errorf("LangFrom(WithLang(es)) = %q, want %q", got, LangES)
	}
}

// TestPathFrom_DefaultWhenAbsent guards the toggle link helper: a context
// with no path stashed defaults to "/" rather than an empty string (which
// would produce a broken /lang/es?next= link).
func TestPathFrom_DefaultWhenAbsent(t *testing.T) {
	if got := PathFrom(context.Background()); got != "/" {
		t.Errorf("PathFrom(bare ctx) = %q, want %q", got, "/")
	}
}

// TestWithPath_RoundTrip checks the ctx round trip for the current-path
// value used to build the lang-toggle's next= redirect target.
func TestWithPath_RoundTrip(t *testing.T) {
	ctx := WithPath(context.Background(), "/browse?project=omnia")
	if got := PathFrom(ctx); got != "/browse?project=omnia" {
		t.Errorf("PathFrom(WithPath(...)) = %q, want %q", got, "/browse?project=omnia")
	}
}
