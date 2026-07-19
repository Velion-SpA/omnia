package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLangFromRequest_NoCookie_DefaultsSpanish pins the product requirement:
// a request with no lang cookie resolves to Spanish.
func TestLangFromRequest_NoCookie_DefaultsSpanish(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := LangFromRequest(r); got != DefaultLang {
		t.Errorf("LangFromRequest(no cookie) = %q, want %q", got, DefaultLang)
	}
}

// TestLangFromRequest_ValidCookie_EN checks the cookie carries an explicit
// English preference through.
func TestLangFromRequest_ValidCookie_EN(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "en"})
	if got := LangFromRequest(r); got != LangEN {
		t.Errorf("LangFromRequest(cookie=en) = %q, want %q", got, LangEN)
	}
}

// TestLangFromRequest_InvalidCookieValue_DefaultsSpanish checks an invalid
// or garbage cookie value falls back to the Spanish default rather than
// erroring.
func TestLangFromRequest_InvalidCookieValue_DefaultsSpanish(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "klingon"})
	if got := LangFromRequest(r); got != LangES {
		t.Errorf("LangFromRequest(cookie=klingon) = %q, want %q", got, LangES)
	}
}

// TestSetLangCookie_Attributes pins the exact persistence contract from the
// locked design: cookie name "lang", ~1 year Max-Age, Path=/, SameSite=Lax.
func TestSetLangCookie_Attributes(t *testing.T) {
	rec := httptest.NewRecorder()
	SetLangCookie(rec, LangEN)

	resp := rec.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly 1 cookie set, got %d", len(cookies))
	}
	c := cookies[0]

	if c.Name != CookieName {
		t.Errorf("cookie Name = %q, want %q", c.Name, CookieName)
	}
	if c.Value != "en" {
		t.Errorf("cookie Value = %q, want %q", c.Value, "en")
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want %q", c.Path, "/")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want SameSiteLaxMode", c.SameSite)
	}
	const oneYearSeconds = 365 * 24 * 60 * 60
	if c.MaxAge != oneYearSeconds {
		t.Errorf("cookie MaxAge = %d, want %d (~1 year)", c.MaxAge, oneYearSeconds)
	}
}
