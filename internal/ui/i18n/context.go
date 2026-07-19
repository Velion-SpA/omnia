package i18n

import "context"

// ctxKey is the unexported context key type for values this package stashes
// on the request context. Distinct values of the same type (below) keep the
// lang and path entries from colliding.
type ctxKey struct{ name string }

var (
	langCtxKey = ctxKey{"lang"}
	pathCtxKey = ctxKey{"path"}
)

// WithLang returns a context carrying the resolved display language.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, langCtxKey, l)
}

// LangFrom returns the display language carried on ctx, or DefaultLang
// (Spanish) when none was set — e.g. a background context, or a request
// that never passed through Middleware.
func LangFrom(ctx context.Context) Lang {
	if l, ok := ctx.Value(langCtxKey).(Lang); ok {
		return l
	}
	return DefaultLang
}

// WithPath returns a context carrying the current request path (+ query),
// used by the shared shell's lang toggle to build its /lang/{lang}?next=
// redirect target so switching languages returns the caller to the same
// page instead of always bouncing to "/".
func WithPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pathCtxKey, path)
}

// PathFrom returns the current request path carried on ctx, defaulting to
// "/" when none was set.
func PathFrom(ctx context.Context) string {
	if p, ok := ctx.Value(pathCtxKey).(string); ok && p != "" {
		return p
	}
	return "/"
}
