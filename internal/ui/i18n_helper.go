package ui

import (
	"context"

	"github.com/velion/omnia/internal/ui/i18n"
)

// T is a templ-friendly wrapper around i18n.T: it reads the caller's
// language straight from ctx (populated by i18n.Middleware for every
// dashboard request) so templates and handlers can call ui.T(ctx, key)
// instead of the more verbose i18n.T(i18n.LangFrom(ctx), key).
func T(ctx context.Context, key string) string {
	return i18n.T(i18n.LangFrom(ctx), key)
}

// Tf is the interpolated-copy counterpart to T — a templ-friendly wrapper
// around i18n.Tf for catalog entries that embed a dynamic value (e.g. a
// count or a formatted name). Added in Slice 2 alongside the pages that
// need it (browse's "%d results", project detail's "%d recent", etc.).
func Tf(ctx context.Context, key string, a ...any) string {
	return i18n.Tf(i18n.LangFrom(ctx), key, a...)
}
