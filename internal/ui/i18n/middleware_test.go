package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMiddleware_DefaultsToSpanishWithoutCookie pins the Spanish-default
// requirement all the way through the middleware.
func TestMiddleware_DefaultsToSpanishWithoutCookie(t *testing.T) {
	var gotLang Lang
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = LangFrom(r.Context())
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	Middleware(next).ServeHTTP(w, r)

	if gotLang != LangES {
		t.Errorf("Middleware without cookie set ctx lang = %q, want %q", gotLang, LangES)
	}
}

// TestMiddleware_ReadsLangFromCookie checks the cookie flows into ctx.
func TestMiddleware_ReadsLangFromCookie(t *testing.T) {
	var gotLang Lang
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = LangFrom(r.Context())
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "en"})
	w := httptest.NewRecorder()
	Middleware(next).ServeHTTP(w, r)

	if gotLang != LangEN {
		t.Errorf("Middleware with cookie=en set ctx lang = %q, want %q", gotLang, LangEN)
	}
}

// TestMiddleware_SetsPathFromRequest checks the current-path context value
// (used by the header lang toggle's next= link) is populated from the
// request's URI.
func TestMiddleware_SetsPathFromRequest(t *testing.T) {
	var gotPath string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = PathFrom(r.Context())
	})

	r := httptest.NewRequest(http.MethodGet, "/browse?project=omnia", nil)
	w := httptest.NewRecorder()
	Middleware(next).ServeHTTP(w, r)

	if gotPath != "/browse?project=omnia" {
		t.Errorf("Middleware ctx path = %q, want %q", gotPath, "/browse?project=omnia")
	}
}
