package dashboard

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/ui/i18n"
)

// TestLayoutPropsForContext_TranslatesNavLabels_Spanish pins the default:
// with no lang on ctx, every nav item's Label must be the Spanish copy
// (keyed by NavItem.ID via the "nav.<id>" catalog entries), not the raw
// English literal BaseNavItems() sets.
func TestLayoutPropsForContext_TranslatesNavLabels_Spanish(t *testing.T) {
	props := layoutPropsForContext(context.Background(), "overview", "Overview")

	want := map[string]string{
		"overview": "Resumen",
		"browse":   "Explorar",
		"graph":    "Grafo",
		"sync":     "Sync",
		"activity": "Actividad",
	}
	got := map[string]string{}
	for _, item := range props.Nav {
		got[item.ID] = item.Label
	}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("nav item %q Label = %q, want %q", id, got[id], label)
		}
	}
}

// TestLayoutPropsForContext_TranslatesNavLabels_English checks the same nav
// items translate to English when the ctx carries LangEN — including the
// operator-only Admin entry, appended via WithAdminNav.
func TestLayoutPropsForContext_TranslatesNavLabels_English(t *testing.T) {
	ctx := i18n.WithLang(context.Background(), i18n.LangEN)
	ctx = WithAdminNav(ctx)
	props := layoutPropsForContext(ctx, "overview", "Overview")

	got := map[string]string{}
	for _, item := range props.Nav {
		got[item.ID] = item.Label
	}
	if got["overview"] != "Overview" {
		t.Errorf("overview Label = %q, want %q", got["overview"], "Overview")
	}
	if got["admin"] != "Admin" {
		t.Errorf("admin Label = %q, want %q", got["admin"], "Admin")
	}
}

// TestLayoutPropsForContext_TranslatesBrandSubAndStatus checks the wordmark
// subtitle and status chip text follow ctx language too.
func TestLayoutPropsForContext_TranslatesBrandSubAndStatus(t *testing.T) {
	esProps := layoutPropsForContext(context.Background(), "overview", "Overview")
	if esProps.BrandSub != "Conocimiento Unificado" {
		t.Errorf("es BrandSub = %q, want %q", esProps.BrandSub, "Conocimiento Unificado")
	}
	if esProps.StatusText != "En línea" {
		t.Errorf("es StatusText = %q, want %q", esProps.StatusText, "En línea")
	}

	enCtx := i18n.WithLang(context.Background(), i18n.LangEN)
	enProps := layoutPropsForContext(enCtx, "overview", "Overview")
	if enProps.BrandSub != "Unified Knowledge" {
		t.Errorf("en BrandSub = %q, want %q", enProps.BrandSub, "Unified Knowledge")
	}
	if enProps.StatusText != "Online" {
		t.Errorf("en StatusText = %q, want %q", enProps.StatusText, "Online")
	}
}
