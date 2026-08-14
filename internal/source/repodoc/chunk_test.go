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
