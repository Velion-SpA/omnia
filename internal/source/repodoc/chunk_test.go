package repodoc

import (
	"strings"
	"testing"
)

const sampleDoc = `# Omnia

Omnia is a persistent memory system.

## Vision

We want memory that survives sessions.

### Non-goals

Not a general-purpose database.

## Architecture

Source -> Item -> Sink.

### Non-goals

This is a duplicate heading text to test slug de-duplication.
`

func TestChunkMarkdown_HeadingPathAndBody(t *testing.T) {
	sections := ChunkMarkdown(sampleDoc)

	want := []struct {
		path  []string
		slug  string
		level int
	}{
		{[]string{"Omnia"}, "omnia", 1},
		{[]string{"Omnia", "Vision"}, "vision", 2},
		{[]string{"Omnia", "Vision", "Non-goals"}, "non-goals", 3},
		{[]string{"Omnia", "Architecture"}, "architecture", 2},
		{[]string{"Omnia", "Architecture", "Non-goals"}, "non-goals-1", 3},
	}
	if len(sections) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(sections), len(want), sections)
	}
	for i, w := range want {
		got := sections[i]
		if strings.Join(got.HeadingPath, ">") != strings.Join(w.path, ">") {
			t.Errorf("section %d HeadingPath = %v, want %v", i, got.HeadingPath, w.path)
		}
		if got.Slug != w.slug {
			t.Errorf("section %d Slug = %q, want %q", i, got.Slug, w.slug)
		}
		if got.Level != w.level {
			t.Errorf("section %d Level = %d, want %d", i, got.Level, w.level)
		}
	}

	// The "Vision" section's Body must contain its own text but NOT the
	// nested "Non-goals" text — sections partition the document exactly
	// once, so nested content belongs only to its own section.
	visionBody := sections[1].Body
	if !strings.Contains(visionBody, "survives sessions") {
		t.Errorf("Vision body missing own text: %q", visionBody)
	}
	if strings.Contains(visionBody, "general-purpose database") {
		t.Errorf("Vision body leaked nested Non-goals content: %q", visionBody)
	}
}

func TestChunkMarkdown_LineRanges(t *testing.T) {
	sections := ChunkMarkdown(sampleDoc)
	lines := strings.Split(sampleDoc, "\n")

	for _, sec := range sections {
		if sec.LineStart < 1 || sec.LineEnd > len(lines) || sec.LineEnd < sec.LineStart {
			t.Errorf("section %v has invalid line range [%d,%d] for a %d-line doc",
				sec.HeadingPath, sec.LineStart, sec.LineEnd, len(lines))
		}
	}

	// Sections must not overlap and must be in ascending order (heading
	// chunking partitions the file's lines exactly once).
	for i := 1; i < len(sections); i++ {
		if sections[i].LineStart <= sections[i-1].LineStart {
			t.Errorf("section %d LineStart %d is not after section %d LineStart %d",
				i, sections[i].LineStart, i-1, sections[i-1].LineStart)
		}
	}
}

func TestChunkMarkdown_CodeFenceHashNotHeading(t *testing.T) {
	doc := "# Title\n\n```bash\n# this is a shell comment, not a heading\necho hi\n```\n\nAfter fence.\n"
	sections := ChunkMarkdown(doc)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1 (fenced '#' must not create a heading): %+v", len(sections), sections)
	}
	if !strings.Contains(sections[0].Body, "shell comment") {
		t.Errorf("expected fenced content preserved in body, got: %q", sections[0].Body)
	}
}

func TestChunkMarkdown_NoHeadings(t *testing.T) {
	doc := "Just some plain text.\nNo headings at all.\n"
	sections := ChunkMarkdown(doc)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
	if sections[0].Level != 0 {
		t.Errorf("Level = %d, want 0 (preamble/no-heading doc)", sections[0].Level)
	}
	if len(sections[0].HeadingPath) != 0 {
		t.Errorf("HeadingPath = %v, want empty", sections[0].HeadingPath)
	}
}

func TestChunkMarkdown_EmptyDoc(t *testing.T) {
	sections := ChunkMarkdown("")
	if len(sections) != 0 {
		t.Errorf("got %d sections for empty doc, want 0: %+v", len(sections), sections)
	}
}

func TestChunkMarkdown_PreambleBeforeFirstHeading(t *testing.T) {
	doc := "Some intro text before any heading.\n\n# First Heading\n\nHeading body.\n"
	sections := ChunkMarkdown(doc)
	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2 (preamble + heading): %+v", len(sections), sections)
	}
	if sections[0].Level != 0 || !strings.Contains(sections[0].Body, "intro text") {
		t.Errorf("preamble section wrong: %+v", sections[0])
	}
	if sections[1].Level != 1 {
		t.Errorf("second section Level = %d, want 1", sections[1].Level)
	}
}

// TestFoldSectionsByDepth_DefaultCollapsesH3IntoH2 uses sampleDoc's real
// H1/H2/H3 structure (the same fixture TestChunkMarkdown_HeadingPathAndBody
// uses) to verify the default depth (2) folds both H3 "Non-goals" sections
// into their parent H2, without losing any text and without merging the two
// unrelated H2 sections into each other.
func TestFoldSectionsByDepth_DefaultCollapsesH3IntoH2(t *testing.T) {
	sections := FoldSectionsByDepth(ChunkMarkdown(sampleDoc), DefaultMaxHeadingDepth)

	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3 (Omnia H1, Vision H2 w/ folded Non-goals, Architecture H2 w/ folded Non-goals): %+v", len(sections), sections)
	}
	if sections[0].Level != 1 || sections[1].Level != 2 || sections[2].Level != 2 {
		t.Fatalf("levels = [%d,%d,%d], want [1,2,2]", sections[0].Level, sections[1].Level, sections[2].Level)
	}

	vision := sections[1]
	if !strings.Contains(vision.Body, "survives sessions") {
		t.Errorf("Vision section lost its own body: %q", vision.Body)
	}
	if !strings.Contains(vision.Body, "### Non-goals") || !strings.Contains(vision.Body, "general-purpose database") {
		t.Errorf("Vision section did not fold in its child Non-goals section: %q", vision.Body)
	}

	arch := sections[2]
	if !strings.Contains(arch.Body, "Source -> Item -> Sink") {
		t.Errorf("Architecture section lost its own body: %q", arch.Body)
	}
	if !strings.Contains(arch.Body, "### Non-goals") || !strings.Contains(arch.Body, "duplicate heading text") {
		t.Errorf("Architecture section did not fold in its child Non-goals section: %q", arch.Body)
	}

	// The two folded "Non-goals" sections must land in DIFFERENT parents —
	// folding must not cross-contaminate sibling H2 branches.
	if strings.Contains(vision.Body, "duplicate heading text") {
		t.Error("Architecture's Non-goals content leaked into Vision's folded body")
	}
	if strings.Contains(arch.Body, "general-purpose database") {
		t.Error("Vision's Non-goals content leaked into Architecture's folded body")
	}

	// LineEnd must extend to cover the folded child's range so the git
	// staleness anchor (anchor.go) still spans exactly the retained text.
	unfolded := ChunkMarkdown(sampleDoc)
	visionChildEnd := unfolded[2].LineEnd // the standalone "Non-goals" under Vision
	if vision.LineEnd != visionChildEnd {
		t.Errorf("Vision LineEnd = %d, want extended to folded child's LineEnd %d", vision.LineEnd, visionChildEnd)
	}
}

// TestFoldSectionsByDepth_ZeroOrNegativeMeansUnlimited covers the config
// override path (allowlist.go's DefaultAllowlist doc comment describes the
// same "opt out via override" contract for the allowlist; this mirrors it
// for depth): maxDepth <= 0 must return sections completely unchanged.
func TestFoldSectionsByDepth_ZeroOrNegativeMeansUnlimited(t *testing.T) {
	unfolded := ChunkMarkdown(sampleDoc)
	for _, maxDepth := range []int{0, -1, -6} {
		got := FoldSectionsByDepth(ChunkMarkdown(sampleDoc), maxDepth)
		if len(got) != len(unfolded) {
			t.Fatalf("maxDepth=%d: got %d sections, want %d (unchanged)", maxDepth, len(got), len(unfolded))
		}
		for i := range got {
			if got[i].Level != unfolded[i].Level || got[i].Body != unfolded[i].Body {
				t.Errorf("maxDepth=%d: section %d changed, want unchanged: got %+v, want %+v", maxDepth, i, got[i], unfolded[i])
			}
		}
	}
}

// TestFoldSectionsByDepth_NoAncestorKeepsStandalone covers the corner case
// documented on FoldSectionsByDepth: a document whose very first heading is
// already deeper than maxDepth has no ancestor chunk to fold into, so it
// must be kept as its own Section (never silently discarded) rather than
// erroring or being dropped.
func TestFoldSectionsByDepth_NoAncestorKeepsStandalone(t *testing.T) {
	doc := "### Detail\n\nNo H1 or H2 precedes this heading at all.\n"
	sections := FoldSectionsByDepth(ChunkMarkdown(doc), DefaultMaxHeadingDepth)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1 (kept standalone, not dropped): %+v", len(sections), sections)
	}
	if sections[0].Level != 3 {
		t.Errorf("Level = %d, want 3 (unfolded — no eligible ancestor existed)", sections[0].Level)
	}
	if !strings.Contains(sections[0].Body, "No H1 or H2 precedes") {
		t.Errorf("standalone section lost its text: %q", sections[0].Body)
	}
}

func TestDocTitle_FirstH1(t *testing.T) {
	got := DocTitle(sampleDoc, "fallback")
	if got != "Omnia" {
		t.Errorf("DocTitle = %q, want %q", got, "Omnia")
	}
}

func TestDocTitle_FallsBackWhenNoH1(t *testing.T) {
	got := DocTitle("## Only an H2\n\nbody\n", "fallback-name")
	if got != "fallback-name" {
		t.Errorf("DocTitle = %q, want fallback %q", got, "fallback-name")
	}
}

// TestDocTitle_TableDriven is the regression suite for the bug this change
// fixes: DocTitle used to scan raw lines for a "#"-prefixed line without any
// fence tracking, so a shell comment inside a README's first ```sh fence
// (e.g. "# Homebrew (macOS / Linux)") was picked up as the document title —
// and because the title is prefixed into every chunk's indexed body
// ("Document: {title}"), it silently retitled every chunk toward whatever
// happened to sit in that fence. DocTitle now reuses findHeadings' fence-
// tracking scan (the same one ChunkMarkdown/the section splitter uses), so
// every case below must resolve to the fallback or to a REAL, unfenced H1 —
// never to fenced content.
func TestDocTitle_TableDriven(t *testing.T) {
	const fallback = "fallback-name"

	tests := map[string]struct {
		content string
		want    string
	}{
		"no H1 at all (HTML banner instead, README's real-world convention)": {
			content: "<div align=\"center\">\n  <img src=\"logo.png\">\n</div>\n\n## Features\n\nStuff.\n",
			want:    fallback,
		},
		"H1-looking line inside a ```sh fence": {
			content: "## Install\n\n```sh\n# Homebrew (macOS / Linux)\nbrew install example/tap/thing\n```\n",
			want:    fallback,
		},
		"H1-looking line inside a plain ``` fence": {
			content: "## Notes\n\n```\n# not a title, just a fenced comment\n```\n",
			want:    fallback,
		},
		"H1-looking line inside a ~~~ fence": {
			content: "## Notes\n\n~~~\n# not a title, tilde fence\n~~~\n",
			want:    fallback,
		},
		"real H1 after a fenced false heading": {
			content: "```sh\n# fenced, not the title\n```\n\n# Real Title\n\nBody.\n",
			want:    "Real Title",
		},
		"fence that is never closed swallows any '#' line to EOF": {
			content: "Intro.\n\n```sh\n# inside an unterminated fence\necho hi\n# also inside it\n",
			want:    fallback,
		},
		"indented (4-space) code block is not a fence and not a heading": {
			content: "Intro paragraph.\n\n    # this is an indented code block, not a heading\n    echo hi\n\nMore text.\n",
			want:    fallback,
		},
		"real H1 after an indented code block": {
			content: "    # indented, not a heading\n\n# Real Title\n\nBody.\n",
			want:    "Real Title",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := DocTitle(tc.content, fallback)
			if got != tc.want {
				t.Errorf("DocTitle(%q, %q) = %q, want %q", tc.content, fallback, got, tc.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Non-goals":               "non-goals",
		"What is Omnia?":          "what-is-omnia",
		"  Leading/trailing  ":    "leadingtrailing",
		"Über Café":               "ber-caf",
		"Already-slugged-text":    "already-slugged-text",
		"Multiple   Spaces  Here": "multiple-spaces-here",
	}
	for in, want := range tests {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
