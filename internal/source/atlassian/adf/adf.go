// Package adf converts Atlassian Document Format (ADF) JSON — used by Jira
// issue/comment bodies — into markdown text.
//
// ToMarkdown is a pure function: no I/O, no globals. Unknown node types
// degrade gracefully by recursing into their content children and emitting
// their text, so a forward-compat ADF node (a new Jira feature we don't know
// about yet) never silently drops content — only the unknown wrapper (or an
// unrecognized mark) is dropped, never the subtree.
package adf

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// node mirrors the minimal ADF JSON node shape needed for markdown
// conversion. Unrecognized JSON fields are ignored by encoding/json.
type node struct {
	Type    string                     `json:"type"`
	Text    string                     `json:"text"`
	Content []node                     `json:"content"`
	Marks   []markNode                 `json:"marks"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
}

type markNode struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs"`
}

// ToMarkdown converts a raw ADF JSON document (or fragment) into markdown.
// Returns "" for empty input or invalid JSON.
func ToMarkdown(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var root node
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return ""
	}
	return strings.TrimSpace(renderNode(root))
}

// renderNode renders a single block-level node to markdown. The top-level
// "doc" type is intentionally NOT special-cased: it falls into the default
// (unknown-node) branch below, which recurses into content and joins the
// resulting blocks — exactly the behavior a doc-level container needs.
func renderNode(n node) string {
	switch n.Type {
	case "paragraph":
		return renderInline(n.Content)
	case "heading":
		level := getIntAttr(n.Attrs, "level", 1)
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		return strings.Repeat("#", level) + " " + renderInline(n.Content)
	case "bulletList":
		return renderList(n.Content, false)
	case "orderedList":
		return renderList(n.Content, true)
	case "codeBlock":
		lang := getStringAttr(n.Attrs, "language")
		code := renderInline(n.Content)
		return "```" + lang + "\n" + code + "\n```"
	case "blockquote":
		return quoteLines(renderBlocks(n.Content))
	case "rule":
		return "---"
	case "table":
		return renderTable(n.Content)
	case "text":
		return applyMarks(n.Text, n.Marks)
	case "hardBreak":
		return ""
	default:
		// Unknown node type: degrade by recursing into content children only.
		// This preserves text from forward-compat/unrecognized ADF nodes
		// (e.g. panel, mediaGroup) while dropping just the wrapper.
		return renderBlocks(n.Content)
	}
}

// renderBlocks renders a sequence of block-level nodes, joined by a blank
// line (standard markdown block separation).
func renderBlocks(nodes []node) string {
	var parts []string
	for _, n := range nodes {
		if b := renderNode(n); b != "" {
			parts = append(parts, b)
		}
	}
	return strings.Join(parts, "\n\n")
}

// renderInline renders a sequence of inline nodes (text, hardBreak, and any
// unknown inline node which degrades into its own inline content),
// concatenated with no extra separation.
func renderInline(nodes []node) string {
	var sb strings.Builder
	for _, n := range nodes {
		sb.WriteString(renderInlineNode(n))
	}
	return sb.String()
}

func renderInlineNode(n node) string {
	switch n.Type {
	case "text":
		return applyMarks(n.Text, n.Marks)
	case "hardBreak":
		return "\n"
	default:
		return renderInline(n.Content)
	}
}

// applyMarks wraps text with markdown syntax for recognized marks. Marks are
// applied innermost-to-outermost: code, strong, em, then link. Unrecognized
// mark types are silently ignored — the wrapper mark is dropped but the text
// itself is always preserved.
func applyMarks(text string, marks []markNode) string {
	var hasCode, hasStrong, hasEm, hasLink bool
	var href string
	for _, m := range marks {
		switch m.Type {
		case "code":
			hasCode = true
		case "strong":
			hasStrong = true
		case "em":
			hasEm = true
		case "link":
			if h := getStringAttr(m.Attrs, "href"); h != "" {
				hasLink = true
				href = h
			}
		}
	}
	if hasCode {
		text = renderCode(text)
	}
	if hasStrong {
		text = "**" + text + "**"
	}
	if hasEm {
		text = "*" + text + "*"
	}
	if hasLink {
		text = renderLink(text, href)
	}
	return text
}

// renderLink renders a markdown link if href passes sanitizeHref, otherwise
// returns text unchanged (no link syntax at all) — a rejected scheme
// (javascript:, data:, etc.) must never survive into the converted markdown
// as a clickable link. When the (already-allowlisted) href contains a
// closing paren, the angle-bracket destination form is used so the paren
// can't be misread as the end of the markdown link syntax.
func renderLink(text, href string) string {
	safeHref, ok := sanitizeHref(href)
	if !ok {
		return text
	}
	if strings.Contains(safeHref, ")") {
		return "[" + text + "](<" + safeHref + ">)"
	}
	return "[" + text + "](" + safeHref + ")"
}

// sanitizeHref allows relative URLs (no scheme — they can't invoke a
// script/data URI handler) and absolute http/https/mailto URLs. Any other
// scheme (javascript:, data:, vbscript:, file:, etc.) is rejected so a
// malicious ADF document can never produce a clickable script-executing
// link in the converted markdown.
func sanitizeHref(href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return href, true
	default:
		return "", false
	}
}

// renderCode wraps text in a markdown inline code span. If text itself
// contains a backtick, a single-backtick fence would be corrupted (it would
// close the span early), so a double-backtick fence is used instead — with
// padding spaces when text starts/ends with a backtick, per standard
// markdown code-span rules.
func renderCode(text string) string {
	if !strings.Contains(text, "`") {
		return "`" + text + "`"
	}
	inner := text
	if strings.HasPrefix(inner, "`") || strings.HasSuffix(inner, "`") {
		inner = " " + inner + " "
	}
	return "``" + inner + "``"
}

// renderList renders bulletList/orderedList content (a sequence of listItem
// nodes) as markdown list lines joined by a single newline.
func renderList(items []node, ordered bool) string {
	var lines []string
	idx := 0
	for _, item := range items {
		idx++
		marker := "-"
		if ordered {
			marker = strconv.Itoa(idx) + "."
		}
		lines = append(lines, renderListItem(item, marker))
	}
	return strings.Join(lines, "\n")
}

func renderListItem(item node, marker string) string {
	var textParts []string
	var nestedParts []string
	for _, child := range item.Content {
		switch child.Type {
		case "bulletList":
			nestedParts = append(nestedParts, renderList(child.Content, false))
		case "orderedList":
			nestedParts = append(nestedParts, renderList(child.Content, true))
		default:
			if b := renderNode(child); b != "" {
				textParts = append(textParts, b)
			}
		}
	}
	line := marker + " " + strings.Join(textParts, " ")
	if len(nestedParts) > 0 {
		line += "\n" + strings.Join(nestedParts, "\n")
	}
	return line
}

// renderTable renders "table basics": a header separator is inserted after
// the first row, regardless of whether cells are tableHeader or tableCell.
func renderTable(rows []node) string {
	var lines []string
	headerDone := false
	for _, row := range rows {
		if row.Type != "tableRow" {
			continue
		}
		var cells []string
		for _, cell := range row.Content {
			cells = append(cells, renderTableCell(cell))
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
		if !headerDone {
			sep := make([]string, len(cells))
			for i := range sep {
				sep[i] = "---"
			}
			lines = append(lines, "| "+strings.Join(sep, " | ")+" |")
			headerDone = true
		}
	}
	return strings.Join(lines, "\n")
}

func renderTableCell(cell node) string {
	return strings.TrimSpace(strings.ReplaceAll(renderBlocks(cell.Content), "\n", " "))
}

func quoteLines(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

func getStringAttr(attrs map[string]json.RawMessage, key string) string {
	raw, ok := attrs[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func getIntAttr(attrs map[string]json.RawMessage, key string, def int) int {
	raw, ok := attrs[key]
	if !ok {
		return def
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return def
	}
	return int(f)
}
