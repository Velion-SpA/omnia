// Package storage converts Confluence storage-format HTML (the raw XHTML
// Confluence pages/comments are stored as, including custom macro elements
// like <ac:structured-macro>) into plain text/markdown.
//
// ToText is a pure function: no I/O, no globals. It walks the token stream
// via golang.org/x/net/html's Tokenizer. Elements without an explicit
// markdown mapping (including Confluence's ac:*/ri:* macro namespace tags)
// pass their children straight through as running text — only the unknown
// wrapper tag is dropped, never its content. This mirrors the ADF
// converter's unknown-node degradation philosophy.
package storage

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// frame tracks one open element on the conversion stack.
type frame struct {
	tag string
	// href holds the resolved link target when tag == "a".
	href string
	// sb accumulates inline (running) text for this element.
	sb strings.Builder
	// children accumulates finished block-level output produced by this
	// element's descendants (list items, table rows/cells, paragraphs
	// inside a blockquote, etc).
	children []string
	// orderedIdx tracks the running item number for an <ol> parent.
	orderedIdx int
	// headerDone marks that a <table> has already emitted its header
	// separator row.
	headerDone bool
}

var horizontalWS = regexp.MustCompile(`[ \t]+`)

// ToText converts Confluence storage-format HTML into plain text/markdown.
func ToText(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	z := html.NewTokenizer(strings.NewReader(raw))
	c := &converter{stack: []*frame{{tag: ""}}}

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(c.flush())
		case html.TextToken:
			c.writeText(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			var href string
			if hasAttr && tag == "a" {
				href = readHref(z)
			}
			switch tag {
			case "br":
				c.writeText("\n")
			case "hr":
				c.appendBlock("---")
			default:
				c.startTag(tag, href)
				if tt == html.SelfClosingTagToken {
					c.endTag(tag)
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			c.endTag(string(name))
		}
	}
}

type converter struct {
	stack []*frame
}

func (c *converter) top() *frame {
	return c.stack[len(c.stack)-1]
}

func (c *converter) writeText(s string) {
	c.top().sb.WriteString(s)
}

func (c *converter) appendBlock(s string) {
	c.top().children = append(c.top().children, s)
}

func (c *converter) startTag(tag, href string) {
	c.stack = append(c.stack, &frame{tag: tag, href: href})
}

// endTag closes the nearest open frame matching tag, collapsing (merging up)
// any unmatched intermediate frames first. Stray end tags with no matching
// open frame are ignored.
func (c *converter) endTag(tag string) {
	idx := -1
	for i := len(c.stack) - 1; i >= 1; i-- {
		if c.stack[i].tag == tag {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	for len(c.stack)-1 >= idx {
		c.popOne()
	}
}

// flush closes any still-open frames (malformed/unclosed HTML) and returns
// the final joined text.
func (c *converter) flush() string {
	for len(c.stack) > 1 {
		c.popOne()
	}
	root := c.stack[0]
	blocks := root.children
	if trailing := collapseSpace(root.sb.String()); trailing != "" {
		blocks = append(blocks, trailing)
	}
	return strings.Join(blocks, "\n\n")
}

// popOne pops the top frame and merges its rendered result into its parent,
// per the tag-specific rules below. Unknown tags always merge their
// accumulated text/children straight into the parent's running text —
// that's the "pass through children" degradation.
func (c *converter) popOne() {
	f := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	parent := c.top()

	switch f.tag {
	case "p":
		if text := collapseSpace(f.sb.String()); text != "" {
			parent.children = append(parent.children, text)
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(f.tag[1:])
		text := collapseSpace(f.sb.String())
		parent.children = append(parent.children, strings.Repeat("#", level)+" "+text)
	case "strong", "b":
		parent.sb.WriteString("**" + collapseSpace(f.sb.String()) + "**")
	case "em", "i":
		parent.sb.WriteString("*" + collapseSpace(f.sb.String()) + "*")
	case "code":
		parent.sb.WriteString(renderCode(collapseSpace(f.sb.String())))
	case "a":
		text := collapseSpace(f.sb.String())
		parent.sb.WriteString(renderLink(text, f.href))
	case "script", "style":
		// Script/style content must NEVER leak into converted text: the
		// x/net/html tokenizer treats everything between <script>/<style>
		// and its matching end tag as raw text (not markup), so it lands in
		// f.sb just like any other element's text — but unlike the default
		// "unknown tag passes through" rule, this one wrapper's entire
		// subtree is deliberately dropped (XSS/noise risk), not merged up.
	case "li":
		text := collapseSpace(f.sb.String())
		if len(f.children) > 0 {
			nested := strings.Join(f.children, "\n")
			if text != "" {
				text = text + "\n" + nested
			} else {
				text = nested
			}
		}
		marker := "- "
		if parent.tag == "ol" {
			parent.orderedIdx++
			marker = strconv.Itoa(parent.orderedIdx) + ". "
		}
		parent.children = append(parent.children, marker+text)
	case "ul", "ol":
		parent.children = append(parent.children, strings.Join(f.children, "\n"))
	case "blockquote":
		var content string
		if len(f.children) > 0 {
			content = strings.Join(f.children, "\n\n")
		} else {
			content = collapseSpace(f.sb.String())
		}
		parent.children = append(parent.children, quoteLines(content))
	case "td", "th":
		parent.children = append(parent.children, collapseSpace(f.sb.String()))
	case "tr":
		row := "| " + strings.Join(f.children, " | ") + " |"
		parent.children = append(parent.children, row)
		if !parent.headerDone {
			sep := make([]string, len(f.children))
			for i := range sep {
				sep[i] = "---"
			}
			parent.children = append(parent.children, "| "+strings.Join(sep, " | ")+" |")
			parent.headerDone = true
		}
	case "table":
		parent.children = append(parent.children, strings.Join(f.children, "\n"))
	default:
		// Unknown tag (div, span, or a Confluence macro like
		// ac:structured-macro / ac:rich-text-body / ri:page): pass its
		// accumulated inline text and any block children straight through
		// to the parent's running text, dropping only this wrapper tag.
		merged := f.sb.String()
		for _, b := range f.children {
			if merged != "" && !strings.HasSuffix(merged, " ") {
				merged += " "
			}
			merged += b
		}
		parent.sb.WriteString(merged)
	}
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

// collapseSpace collapses runs of horizontal whitespace (spaces/tabs) into a
// single space and trims the result, while leaving explicit "\n" characters
// (from <br/>) intact.
func collapseSpace(s string) string {
	return strings.TrimSpace(horizontalWS.ReplaceAllString(s, " "))
}

func readHref(z *html.Tokenizer) string {
	href := ""
	for {
		key, val, more := z.TagAttr()
		if string(key) == "href" {
			href = string(val)
		}
		if !more {
			break
		}
	}
	return href
}

// renderLink renders a markdown link if href passes sanitizeHref, otherwise
// returns text unchanged (no link syntax at all) — a rejected scheme
// (javascript:, data:, etc.) must never survive into the converted markdown
// as a clickable link. When the (already-allowlisted) href contains a
// closing paren, the angle-bracket destination form is used so the paren
// can't be misread as the end of the markdown link syntax.
//
// Duplicated from internal/source/atlassian/adf (not shared): the ADF and
// storage converters are deliberately kept separate packages (different
// parse models — JSON tree vs HTML tokens), this is the one small piece of
// markdown-output safety logic they both need.
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
// scheme (javascript:, data:, vbscript:, file:, etc.) is rejected so
// malicious Confluence storage-format HTML can never produce a clickable
// script-executing link in the converted markdown.
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
