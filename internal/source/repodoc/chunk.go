package repodoc

import (
	"fmt"
	"regexp"
	"strings"
)

// headingRE matches an ATX heading line: 1-6 "#" at line start, a required
// space (CommonMark), then the heading text. Optional closed-ATX trailing
// "#"s (e.g. "## Title ##") are stripped separately in the caller.
var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// fenceRE matches a fenced code block delimiter line (``` or ~~~, any
// length ≥ 3), used to ignore "#" characters that appear inside code
// blocks — a shell comment or a Python "# comment" line must never be
// mistaken for a markdown heading.
var fenceRE = regexp.MustCompile("^(```+|~~~+)")

// Section is one heading-scoped slice of a markdown document, produced by
// ChunkMarkdown. Sections partition a document's lines exactly once: each
// heading's Body is the text between it and the next heading of ANY level
// (or EOF) — it does NOT include nested subsections, since those become
// their own Sections. This is "chunking by markdown heading" (plan P1).
type Section struct {
	// HeadingPath is the ancestor chain, root-to-leaf, ending with this
	// section's own heading text (e.g. ["Architecture", "Data flow"]).
	// Empty for the preamble section that precedes a document's first
	// heading, if any.
	HeadingPath []string
	// Slug is a GitHub-Flavored-Markdown-style anchor slug for the leaf
	// heading, de-duplicated within the document (a repeated heading text
	// gets a "-1", "-2", ... suffix on its 2nd, 3rd, ... occurrence,
	// mirroring GFM's own anchor-slugger behavior) so it is safe to use as
	// the unique half of a topic_key.
	Slug string
	// Level is the leaf heading's ATX level (1-6); 0 for the preamble
	// section.
	Level int
	// Body is the section's own text, heading line excluded.
	Body string
	// LineStart/LineEnd are the 1-based, inclusive line range in the
	// SOURCE file spanned by this section, heading line included. This is
	// exactly the shape internal/anchor.Probe.Capture's (start, end)
	// arguments want (see anchor.go), so a Section maps onto a git-blame
	// staleness anchor with no further translation.
	LineStart int
	LineEnd   int
}

// DocTitle returns the document's title: the text of its first level-1 ATX
// heading, or fallback (typically the filename) if the document has none.
func DocTitle(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		if m := headingRE.FindStringSubmatch(line); m != nil && len(m[1]) == 1 {
			return strings.TrimSpace(strings.TrimRight(m[2], "#"))
		}
	}
	return fallback
}

// ChunkMarkdown splits content into heading-scoped Sections.
//
// Setext headings ("Title\n=====") are intentionally NOT recognized: every
// document class this package targets (README/VISION/ARCHITECTURE/
// CONTRIBUTING, docs/**, adr/**, openspec/specs/**) is written with ATX
// ("# Title") headings throughout this repo, and adding setext support
// would double the parser's surface for no observed benefit. Revisit if
// that assumption stops holding for a project this package is pointed at.
func ChunkMarkdown(content string) []Section {
	lines := strings.Split(content, "\n")
	headings := findHeadings(lines)

	var sections []Section
	slugCount := map[string]int{}

	appendSection := func(path []string, level, start, end int) {
		bodyLines := lines[start-1 : end]
		if level > 0 && len(bodyLines) > 0 {
			bodyLines = bodyLines[1:] // drop the heading line itself
		}
		body := strings.Trim(strings.Join(bodyLines, "\n"), "\n")
		if level == 0 && strings.TrimSpace(body) == "" {
			return // no meaningful preamble — skip an empty leading section
		}
		leaf := "introduction"
		if len(path) > 0 {
			leaf = path[len(path)-1]
		}
		slug := slugify(leaf)
		slugCount[slug]++
		if n := slugCount[slug]; n > 1 {
			slug = fmt.Sprintf("%s-%d", slug, n-1)
		}
		sections = append(sections, Section{
			HeadingPath: append([]string(nil), path...),
			Slug:        slug,
			Level:       level,
			Body:        body,
			LineStart:   start,
			LineEnd:     end,
		})
	}

	if len(headings) == 0 {
		appendSection(nil, 0, 1, len(lines))
		return sections
	}

	if headings[0].line > 1 {
		appendSection(nil, 0, 1, headings[0].line-1)
	}

	var stack []rawHeading // ancestors with level < current heading's level
	for i, h := range headings {
		end := len(lines)
		if i+1 < len(headings) {
			end = headings[i+1].line - 1
		}
		for len(stack) > 0 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		path := make([]string, 0, len(stack)+1)
		for _, s := range stack {
			path = append(path, s.text)
		}
		path = append(path, h.text)
		appendSection(path, h.level, h.line, end)
		stack = append(stack, h)
	}
	return sections
}

// rawHeading is a single parsed ATX heading occurrence.
type rawHeading struct {
	level int
	text  string
	line  int // 1-based
}

// findHeadings scans lines for ATX headings, skipping anything inside a
// fenced code block (``` or ~~~) so a heading-looking comment inside a code
// sample is never mistaken for document structure.
func findHeadings(lines []string) []rawHeading {
	var headings []rawHeading
	inFence := false
	var fenceChar byte
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if len(trimmed) > 0 && trimmed[0] == fenceChar && fenceRE.MatchString(trimmed) {
				inFence = false
			}
			continue
		}
		if fenceRE.MatchString(trimmed) {
			inFence = true
			fenceChar = trimmed[0]
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			headings = append(headings, rawHeading{
				level: len(m[1]),
				text:  strings.TrimSpace(strings.TrimRight(m[2], "#")),
				line:  i + 1,
			})
		}
	}
	return headings
}

// slugify produces a GitHub-Flavored-Markdown-style anchor slug: lowercase,
// spaces/underscores collapsed to single hyphens, all other punctuation
// dropped.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// Drop punctuation/unicode entirely, matching GFM's slugger.
		}
	}
	return strings.TrimRight(b.String(), "-")
}
