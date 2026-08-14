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
// heading OUTSIDE any fenced code block, or fallback (typically the
// filename or project name — see docTitleFallback in repodoc.go) if the
// document has none.
//
// This shares findHeadings' fence-tracking scan with ChunkMarkdown/the
// section splitter rather than re-scanning lines with its own, weaker
// "#"-prefix check. That second path used to be the one place in this
// package that was NOT fence-aware: a shell comment inside the first
// ```sh fence in a README (e.g. "# Homebrew (macOS / Linux)") was picked up
// as the document title, and because the title is prefixed into every
// chunk's indexed body ("Document: {title}"), it silently retitled and
// relabeled every chunk in the file toward installation semantics — a
// document with no real H1 (an HTML banner instead, a very common README
// convention) always fell through to whatever code-fenced "#" line came
// first. findHeadings already ignores fenced content when finding section
// headings; DocTitle now reuses exactly that scan, so a heading-looking line
// inside a fence can never become the title any more than it can become a
// section boundary.
func DocTitle(content, fallback string) string {
	lines := strings.Split(content, "\n")
	for _, h := range findHeadings(lines) {
		if h.level == 1 {
			return h.text
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

// DefaultMaxHeadingDepth is the default ceiling FoldSectionsByDepth enforces
// (bugfix, this change: repodoc's default allowlist + unlimited depth
// measured 56 files / 937 chunks against this repo, ~66% of its ~479 delta
// memories — see allowlist.go's DefaultAllowlist doc comment for the full
// number). An identity question ("what IS this project") is answered by a
// document's top-level structure — an H1 title and its H2 sections — never
// by the sub-sub-section of a requirement several headings deep. Capping at
// H1/H2 keeps repodoc's corpus scoped to the identity-level content P1
// exists to serve (repodoc.go's package doc comment), while leaving
// everything deeper still SEARCHABLE, not deleted — see
// FoldSectionsByDepth's doc comment for why folding, not discarding, was
// chosen.
const DefaultMaxHeadingDepth = 2

// FoldSectionsByDepth collapses every Section deeper than maxDepth into its
// nearest ancestor Section that is within maxDepth, instead of dropping it.
//
// # Why fold instead of discard
//
// A naive depth cutoff ("only emit Sections with Level <= maxDepth") would
// silently delete every H3+ subsection's text. That is a strictly worse
// failure than not having a depth limit at all: a retrieved H2 chunk that
// promises a subsection ("see Non-goals below") would no longer contain it,
// and the missing text is not visible as missing — it just silently isn't
// there. Every rune ChunkMarkdown ever produced for a document is preserved
// somewhere in FoldSectionsByDepth's output; only its owning chunk/TopicKey
// changes for headings deeper than maxDepth.
//
// # Algorithm
//
// Sections arrive in ChunkMarkdown's document order. Walking left to right,
// the most recently emitted Section with Level <= maxDepth is the current
// "keeper" — the nearest ancestor chunk. Each subsequent Section deeper than
// maxDepth has its own heading re-rendered as literal ATX markdown (e.g.
// "### Non-goals") and appended to the keeper's Body, and the keeper's
// LineEnd is extended to cover the folded Section's line range, so the
// keeper's git staleness anchor (anchor.go) still spans exactly the text it
// now contains. The Level-0 preamble section, when present, is always its
// own keeper candidate (0 <= any maxDepth >= 1 this function actually acts
// on).
//
// maxDepth <= 0 means "no limit": sections are returned unchanged. This is
// the config override path the plan asks for on the allowlist
// (allowlist.go's DefaultAllowlist doc comment) mirrored for depth.
//
// # Edge case: no eligible ancestor yet
//
// A Section deeper than maxDepth that precedes any Section with Level <=
// maxDepth (e.g. a document that opens directly with "### Detail" and no
// preamble, H1, or H2 before it) has nothing to fold into. It is kept as
// its own standalone Section rather than dropped, and — because it is
// itself over-depth — it is NOT treated as a valid fold target for any
// Section that follows it either; only a true Level <= maxDepth Section can
// become a keeper. This favors "never silently discard" over strict depth
// enforcement in a corner case that should not occur in this package's
// actual scope (README/VISION/ARCHITECTURE/CONTRIBUTING/docs/adr all open
// with an H1 in practice).
func FoldSectionsByDepth(sections []Section, maxDepth int) []Section {
	if maxDepth <= 0 {
		return sections
	}

	out := make([]Section, 0, len(sections))
	keeper := -1 // index into out of the nearest ancestor within maxDepth; -1 = none yet

	for _, sec := range sections {
		if sec.Level <= maxDepth {
			out = append(out, sec)
			keeper = len(out) - 1
			continue
		}
		if keeper == -1 {
			out = append(out, sec) // no ancestor chunk to fold into — keep standalone
			continue
		}

		heading := sec.HeadingPath[len(sec.HeadingPath)-1]
		out[keeper].Body = strings.TrimRight(out[keeper].Body, "\n") + "\n\n" +
			strings.Repeat("#", sec.Level) + " " + heading + "\n\n" + strings.TrimRight(sec.Body, "\n")
		out[keeper].LineEnd = sec.LineEnd
	}

	return out
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
