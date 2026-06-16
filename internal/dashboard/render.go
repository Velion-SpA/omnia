package dashboard

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// mdRenderer is configured once. Security: html.WithUnsafe is intentionally
// omitted — goldmark escapes raw HTML by default, preventing stored XSS.
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM, // tables, strikethrough, linkify, task list
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
	),
)

// renderMarkdown converts Markdown to an HTML string safe for injection via templ.Raw.
// The input must already have the omnia-meta fenced block stripped.
// [[wikilinks]] are left as plain text — goldmark treats brackets as plain text,
// and no routing target exists yet for them.
// On render error, returns empty string and the error.
func renderMarkdown(content string) (string, error) {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(content), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderMarkdownSafe wraps renderMarkdown for use in templ templates.
// On error it returns the raw content so the page degrades gracefully.
func renderMarkdownSafe(content string) string {
	out, err := renderMarkdown(content)
	if err != nil {
		return content
	}
	return out
}
