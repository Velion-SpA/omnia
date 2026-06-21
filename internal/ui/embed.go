// Package ui is the shared Omnia design system — one CSS, one shell layout, one
// set of components used by BOTH the local dashboard (internal/dashboard) and the
// cloud dashboard (internal/cloud/dashboard). Changing the look here changes it
// everywhere; the two dashboards can no longer drift apart.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var StaticFS embed.FS

// StaticHandler serves the embedded /static assets (omnia.css, pico.min.css,
// htmx.min.js) stripped of the given route prefix (e.g. "/static" locally or
// "/dashboard/static" in the cloud).
func StaticHandler(prefix string) http.Handler {
	sub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		panic("ui: embedded static FS missing: " + err.Error())
	}
	return http.StripPrefix(prefix, http.FileServer(http.FS(sub)))
}
