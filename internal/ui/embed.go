// Package ui is the shared Omnia design system — one CSS, one shell layout, one
// set of components used by the unified dashboard (internal/dashboard). The cloud
// mounts that same dashboard over a cloud data source, so the local and cloud
// surfaces render from a single design base and can no longer drift apart.
package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

//go:embed static
var StaticFS embed.FS

// AssetVersion is a short content hash of the mutable embedded assets
// (omnia.css, admin.js), computed once at init and appended as a "?v=" query to
// their URLs. Cloudflare caches /static aggressively (Cache-Control max-age 4h),
// so without a per-content version a CSS/JS change stays invisible behind a
// stale edge cache until the TTL expires. Hashing the content means the URL
// changes exactly when the asset does — fresh styles on every deploy, no manual
// cache purge.
var AssetVersion = computeAssetVersion()

func computeAssetVersion() string {
	h := sha256.New()
	for _, name := range []string{"static/omnia.css", "static/admin.js"} {
		b, err := StaticFS.ReadFile(name)
		if err != nil {
			continue
		}
		_, _ = h.Write([]byte(name))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// StaticHandler serves the embedded /static assets (omnia.css, pico.min.css,
// htmx.min.js) stripped of the given route prefix ("/static" on both the local
// and cloud dashboards, which share the unified route layout).
func StaticHandler(prefix string) http.Handler {
	sub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		panic("ui: embedded static FS missing: " + err.Error())
	}
	return http.StripPrefix(prefix, http.FileServer(http.FS(sub)))
}
