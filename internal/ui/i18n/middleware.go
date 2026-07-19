package i18n

import "net/http"

// Middleware annotates every request's context with the caller's resolved
// language (from the lang cookie, defaulting to Spanish) and current path
// (used by the shared shell's lang toggle to build its next= redirect
// target). It is the single wrap point both the local dashboard mux
// (internal/dashboard/handlers.go) and the cloud dashboard mount
// (internal/cloud/cloudserver/dashboard_mount.go, via the same
// dashboard.Server.Handler()) share, so both surfaces carry lang identically.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithLang(r.Context(), LangFromRequest(r))
		ctx = WithPath(ctx, r.URL.RequestURI())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
