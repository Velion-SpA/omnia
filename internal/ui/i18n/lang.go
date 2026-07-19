// Package i18n provides the Omnia dashboard's internationalization layer:
// a Lang type, a bilingual message catalog, request/cookie/context plumbing
// to resolve the caller's language, and the /lang/{lang} switch handler that
// persists a language preference. The dashboard defaults to Spanish; English
// is available via a header toggle and persists across reloads through a
// cookie (see cookie.go).
package i18n

import "strings"

// Lang identifies a supported dashboard display language.
type Lang string

const (
	// LangES is Spanish — the dashboard's default language.
	LangES Lang = "es"
	// LangEN is English.
	LangEN Lang = "en"
)

// DefaultLang is used whenever no valid language preference is known (no
// cookie, an invalid cookie value, or an invalid /lang/{lang} path value).
const DefaultLang = LangES

// ParseLang normalizes s (lowercases, trims whitespace, strips a region
// suffix like "-CL" or "_US") and resolves it to a supported Lang. Anything
// that isn't recognized as "es" or "en" after normalization falls back to
// DefaultLang, so a garbage or missing value never surfaces as an error —
// the dashboard just renders in Spanish.
func ParseLang(s string) Lang {
	s = strings.ToLower(strings.TrimSpace(s))
	if idx := strings.IndexAny(s, "-_"); idx >= 0 {
		s = s[:idx]
	}
	switch Lang(s) {
	case LangES:
		return LangES
	case LangEN:
		return LangEN
	default:
		return DefaultLang
	}
}
