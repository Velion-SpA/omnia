package i18n

import (
	"net/http"
	"net/url"
	"strings"
)

// SwitchHandler serves GET /lang/{lang}: it sets the lang cookie to the
// requested (normalized) language and 303-redirects back to a sanitized
// same-origin target. It is intentionally PUBLIC (no auth) on both the
// local dashboard mux and the cloud dashboard's public-path allowlist, so
// the toggle also works from the (unauthenticated) cloud login page.
//
// Redirect target resolution, in order:
//  1. ?next= — accepted ONLY as a same-origin absolute path (must start
//     with "/", not "//", no scheme/host).
//  2. Referer header — accepted ONLY when its host matches the request's
//     host (same-origin).
//  3. "/" — the final fallback.
//
// Both rejection paths exist to close an open-redirect: neither an external
// next= nor an external Referer is ever trusted.
func SwitchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := ParseLang(r.PathValue("lang"))
		SetLangCookie(w, lang)

		next := sanitizeNextPath(r.URL.Query().Get("next"))
		if next == "" {
			next = sameOriginRefererPath(r)
		}
		if next == "" {
			next = "/"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// sanitizeNextPath returns raw unchanged when it is a safe same-origin
// absolute path, or "" when it is anything else (empty, protocol-relative
// "//host/...", or carries a scheme/host/userinfo of its own).
func sanitizeNextPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// A backslash is normalized to "/" by browsers, so "/\evil.com" would be
	// read as the protocol-relative "//evil.com" and escape the origin. Control
	// characters (incl. CR/LF) are never valid in a redirect path either.
	if strings.ContainsRune(raw, '\\') {
		return ""
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return ""
		}
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.IsAbs() || u.Host != "" || u.Scheme != "" || u.User != nil {
		return ""
	}
	return raw
}

// sameOriginRefererPath extracts a redirect path from the Referer header
// when — and only when — it points back at this same request's host.
// Returns "" for a missing, malformed, or cross-origin Referer.
func sameOriginRefererPath(r *http.Request) string {
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}
