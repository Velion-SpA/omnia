// Package projectname is the single, dependency-free source of truth for
// canonical project-name normalization.
//
// Before this package existed, project-name normalization was reimplemented
// independently in internal/store (store.NormalizeProject), internal/config
// (normalizeProject) and internal/project (normalize) — and the copies
// disagreed: store.NormalizeProject collapsed repeated "-"/"_" separators
// (e.g. "my--project" -> "my-project"), while the config and project copies
// did not. That divergence meant the same raw name could normalize
// differently depending on the code path (config load vs MCP write vs cloud
// sync), which is why internal/mcp grew a whole collision-detection
// subsystem to patch the symptom (see normalizedProjectCollisionError).
//
// This package fixes the root cause: it exports the one canonical
// normalization rule, and internal/store, internal/config, and
// internal/project all delegate to it, so they can never diverge again.
//
// This package MUST stay leaf-level: it imports nothing from the rest of
// the project, only the standard library, so every other package is free
// to depend on it without risking an import cycle.
package projectname

import "strings"

// Normalize applies the canonical project-name normalization rule:
// lowercase + trim surrounding whitespace + collapse consecutive hyphens
// ("--" -> "-") and consecutive underscores ("__" -> "_").
//
// Empty or whitespace-only input normalizes to "".
func Normalize(name string) string {
	if name == "" {
		return ""
	}
	n := strings.TrimSpace(strings.ToLower(name))
	// Collapse multiple consecutive hyphens.
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	// Collapse multiple consecutive underscores.
	for strings.Contains(n, "__") {
		n = strings.ReplaceAll(n, "__", "_")
	}
	return n
}

// NormalizeOrUnknown behaves like Normalize, except empty or
// whitespace-only input returns "unknown" instead of "". Callers that must
// never surface an empty project name (e.g. project detection fallbacks)
// should use this variant.
func NormalizeOrUnknown(name string) string {
	n := Normalize(name)
	if n == "" {
		return "unknown"
	}
	return n
}
