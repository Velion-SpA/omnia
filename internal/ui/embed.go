// Package ui is the shared Omnia design system — one CSS, one shell layout, one
// set of components used by the unified dashboard (internal/dashboard). The cloud
// mounts that same dashboard over a cloud data source, so the local and cloud
// surfaces render from a single design base and can no longer drift apart.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var StaticFS embed.FS

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
