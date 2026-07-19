package ui

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/ui/i18n"
)

// TestT_ReadsLangFromCtx guards the templ-friendly wrapper: it must resolve
// the same copy i18n.T(i18n.LangFrom(ctx), key) would, without callers having
// to spell out LangFrom themselves.
func TestT_ReadsLangFromCtx(t *testing.T) {
	esCtx := i18n.WithLang(context.Background(), i18n.LangES)
	if got := T(esCtx, "nav.overview"); got != "Resumen" {
		t.Errorf("T(es ctx, nav.overview) = %q, want %q", got, "Resumen")
	}

	enCtx := i18n.WithLang(context.Background(), i18n.LangEN)
	if got := T(enCtx, "nav.overview"); got != "Overview" {
		t.Errorf("T(en ctx, nav.overview) = %q, want %q", got, "Overview")
	}
}

// TestT_DefaultsToSpanishWithoutCtxLang pins the Spanish-default requirement
// through the helper: a bare context (no lang set) resolves to Spanish.
func TestT_DefaultsToSpanishWithoutCtxLang(t *testing.T) {
	if got := T(context.Background(), "nav.overview"); got != "Resumen" {
		t.Errorf("T(bare ctx, nav.overview) = %q, want Spanish default %q", got, "Resumen")
	}
}
