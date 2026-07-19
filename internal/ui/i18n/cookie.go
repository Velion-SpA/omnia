package i18n

import "net/http"

// CookieName is the persistence mechanism for a caller's language choice —
// a plain, non-secret cookie (no HttpOnly needed) so the value is easy to
// inspect/debug and works identically on the local and cloud dashboards
// without any account/session system.
const CookieName = "lang"

// cookieMaxAgeSeconds is ~1 year, matching the locked design's persistence
// requirement (the switch should "stick" well beyond a browsing session).
const cookieMaxAgeSeconds = 365 * 24 * 60 * 60

// LangFromRequest resolves the caller's language from the lang cookie,
// falling back to DefaultLang (Spanish) when the cookie is absent or its
// value doesn't parse to a supported language.
func LangFromRequest(r *http.Request) Lang {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return DefaultLang
	}
	return ParseLang(c.Value)
}

// SetLangCookie persists l as the caller's language preference: Path=/ (so
// it applies dashboard-wide), SameSite=Lax (so it survives the /lang
// redirect and normal top-level navigation), ~1 year Max-Age.
func SetLangCookie(w http.ResponseWriter, l Lang) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    string(l),
		Path:     "/",
		MaxAge:   cookieMaxAgeSeconds,
		SameSite: http.SameSiteLaxMode,
	})
}
