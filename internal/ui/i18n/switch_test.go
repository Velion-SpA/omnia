package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newSwitchMux builds a minimal mux exposing the /lang/{lang} switch route,
// mirroring how both the local and cloud dashboards register it.
func newSwitchMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /lang/{lang}", SwitchHandler())
	return mux
}

func doSwitchRequest(t *testing.T, target string, referer string) *http.Response {
	t.Helper()
	mux := newSwitchMux()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = "omnia.example"
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Result()
}

// TestSwitchHandler_ValidLang_SetsCookieAndRedirectsToNext is the happy path:
// a valid lang sets the cookie and 303-redirects to a sanitized ?next=.
func TestSwitchHandler_ValidLang_SetsCookieAndRedirectsToNext(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en?next=/browse", "")

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (303 See Other)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/browse" {
		t.Errorf("Location = %q, want %q", loc, "/browse")
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != CookieName || cookies[0].Value != "en" {
		t.Fatalf("expected lang=en cookie, got %+v", cookies)
	}
}

// TestSwitchHandler_InvalidLang_FallsBackToSpanish checks an invalid path
// value normalizes to the Spanish default rather than erroring.
func TestSwitchHandler_InvalidLang_FallsBackToSpanish(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/klingon?next=/browse", "")

	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Value != "es" {
		t.Fatalf("expected lang=es cookie fallback, got %+v", cookies)
	}
}

// TestSwitchHandler_RejectsAbsoluteExternalNext is the open-redirect guard:
// an absolute external URL in ?next= must be rejected, falling back to "/".
func TestSwitchHandler_RejectsAbsoluteExternalNext(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en?next=https://evil.com", "")

	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q (external next rejected)", loc, "/")
	}
}

// TestSwitchHandler_RejectsProtocolRelativeNext covers the "//evil.com"
// bypass attempt (no scheme, but browsers treat it as an absolute URL).
func TestSwitchHandler_RejectsProtocolRelativeNext(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en?next=//evil.com", "")

	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q (protocol-relative next rejected)", loc, "/")
	}
}

// TestSwitchHandler_RejectsBackslashNext covers the "/\evil.com" bypass:
// browsers normalize the backslash to "/", turning it into the protocol-
// relative "//evil.com", so the path must be rejected.
func TestSwitchHandler_RejectsBackslashNext(t *testing.T) {
	// %5C is an encoded backslash; Query().Get decodes it to "/\evil.com".
	resp := doSwitchRequest(t, "/lang/en?next=/%5Cevil.com", "")

	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q (backslash-path bypass rejected)", loc, "/")
	}
}

// TestSwitchHandler_FallsBackToRefererWhenNextMissing checks the Referer
// fallback for same-origin requests (e.g. the toggle on the login page,
// which has no ?next= but does have a Referer).
func TestSwitchHandler_FallsBackToRefererWhenNextMissing(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en", "http://omnia.example/login")

	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want %q (same-origin referer fallback)", loc, "/login")
	}
}

// TestSwitchHandler_RejectsCrossOriginReferer checks a Referer pointing at a
// different host is not trusted either.
func TestSwitchHandler_RejectsCrossOriginReferer(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en", "https://evil.com/phish")

	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q (cross-origin referer rejected)", loc, "/")
	}
}

// TestSwitchHandler_NoNextNoReferer_FallsBackToRoot checks the final default
// when neither ?next= nor Referer is usable.
func TestSwitchHandler_NoNextNoReferer_FallsBackToRoot(t *testing.T) {
	resp := doSwitchRequest(t, "/lang/en", "")

	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}
