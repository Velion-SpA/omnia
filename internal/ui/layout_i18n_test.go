package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/ui/i18n"
)

func renderLayout(t *testing.T, ctx context.Context, props LayoutProps) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Layout(props).Render(ctx, &buf); err != nil {
		t.Fatalf("Layout render failed: %v", err)
	}
	return buf.String()
}

func baseLayoutProps() LayoutProps {
	return LayoutProps{
		Title:      "Overview",
		BrandTitle: "Omnia",
		BrandSub:   "Unified Knowledge",
		BrandHref:  "/",
		Nav:        []NavItem{{Href: "/", Label: "Overview", ID: "overview"}},
		Active:     "overview",
		StatusText: "Online",
		AssetBase:  "/static",
	}
}

// TestLayout_HTMLLangAttribute_DefaultsSpanish pins the default: with no
// lang set on ctx, <html lang="..."> must render "es".
func TestLayout_HTMLLangAttribute_DefaultsSpanish(t *testing.T) {
	html := renderLayout(t, context.Background(), baseLayoutProps())
	if !strings.Contains(html, `<html lang="es"`) {
		t.Errorf("expected <html lang=\"es\"> by default, got:\n%s", html)
	}
}

// TestLayout_HTMLLangAttribute_ReflectsEnglishCtx checks the attribute
// follows ctx when English is set.
func TestLayout_HTMLLangAttribute_ReflectsEnglishCtx(t *testing.T) {
	ctx := i18n.WithLang(context.Background(), i18n.LangEN)
	html := renderLayout(t, ctx, baseLayoutProps())
	if !strings.Contains(html, `<html lang="en"`) {
		t.Errorf("expected <html lang=\"en\"> with English ctx, got:\n%s", html)
	}
}

// TestLayout_LangToggle_ActiveLangIsPlainText_OtherIsLink guards the header
// toggle contract: the CURRENT language renders as non-clickable text, the
// OTHER language renders as a link to /lang/<other>?next=<path>.
func TestLayout_LangToggle_ActiveLangIsPlainText_OtherIsLink(t *testing.T) {
	ctx := i18n.WithLang(context.Background(), i18n.LangES)
	ctx = i18n.WithPath(ctx, "/browse?project=omnia")
	html := renderLayout(t, ctx, baseLayoutProps())

	if !strings.Contains(html, `<span class="lang-current" aria-current="true">ES</span>`) {
		t.Errorf("expected ES to render as the active (plain-text) toggle entry, got:\n%s", html)
	}
	wantHref := `href="/lang/en?next=%2Fbrowse%3Fproject%3Domnia"`
	if !strings.Contains(html, wantHref) {
		t.Errorf("expected EN toggle link %s, got:\n%s", wantHref, html)
	}
}

// TestLayout_LangToggle_SwapsWhenEnglishActive is the mirror case.
func TestLayout_LangToggle_SwapsWhenEnglishActive(t *testing.T) {
	ctx := i18n.WithLang(context.Background(), i18n.LangEN)
	ctx = i18n.WithPath(ctx, "/")
	html := renderLayout(t, ctx, baseLayoutProps())

	if !strings.Contains(html, `<span class="lang-current" aria-current="true">EN</span>`) {
		t.Errorf("expected EN to render as the active (plain-text) toggle entry, got:\n%s", html)
	}
	if !strings.Contains(html, `href="/lang/es?next=%2F"`) {
		t.Errorf("expected ES toggle link with next=/, got:\n%s", html)
	}
}

// TestLayout_LogoutButton_Translated checks the Logout button text follows
// ctx language when a signed-in user is present.
func TestLayout_LogoutButton_Translated(t *testing.T) {
	props := baseLayoutProps()
	props.User = "alice"
	props.LogoutURL = "/logout"

	esHTML := renderLayout(t, i18n.WithLang(context.Background(), i18n.LangES), props)
	if !strings.Contains(esHTML, "Salir") {
		t.Errorf("expected Spanish 'Salir' logout label, got:\n%s", esHTML)
	}

	enHTML := renderLayout(t, i18n.WithLang(context.Background(), i18n.LangEN), props)
	if !strings.Contains(enHTML, "Logout") {
		t.Errorf("expected English 'Logout' logout label, got:\n%s", enHTML)
	}
}
