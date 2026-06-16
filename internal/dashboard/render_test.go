package dashboard

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		absent   []string
	}{
		{
			name:     "bold renders to strong tag",
			input:    "**bold text**",
			contains: []string{"<strong>bold text</strong>"},
			absent:   []string{"**"},
		},
		{
			name:     "heading renders to h2 tag",
			input:    "## Section",
			contains: []string{"<h2"},
			absent:   []string{"##"},
		},
		{
			name:     "unordered list renders to ul/li",
			input:    "- item one\n- item two",
			contains: []string{"<ul>", "<li>item one</li>", "<li>item two</li>"},
		},
		{
			name:     "inline code renders with code tag",
			input:    "use `foo()` here",
			contains: []string{"<code>foo()</code>"},
		},
		{
			name:     "code block renders as pre/code",
			input:    "```go\nfunc f() {}\n```",
			contains: []string{"<pre>", "<code"},
		},
		{
			name:  "raw HTML is escaped not passed through",
			input: "<script>alert('xss')</script>",
			// goldmark without WithUnsafe escapes raw HTML
			absent: []string{"<script>"},
		},
		{
			name:  "angle brackets in content are escaped",
			input: "value: <dangerous>",
			// Should not produce unescaped HTML tags
			absent: []string{"<dangerous>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderMarkdown(tt.input)
			if err != nil {
				t.Fatalf("renderMarkdown(%q) error: %v", tt.input, err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("renderMarkdown(%q)\ngot:  %s\nwant substring: %s", tt.input, got, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("renderMarkdown(%q)\ngot:  %s\nshould NOT contain: %s", tt.input, got, absent)
				}
			}
		})
	}
}

func TestRenderMarkdownSafe(t *testing.T) {
	// Should not panic on empty input
	_ = renderMarkdownSafe("")

	// Should not panic on valid markdown
	result := renderMarkdownSafe("**hello**")
	if !strings.Contains(result, "<strong>") {
		t.Errorf("renderMarkdownSafe expected <strong> tag, got: %s", result)
	}
}

func TestStripMetaBlockPreservesContent(t *testing.T) {
	// Ensure renderMarkdown never sees the omnia-meta block
	content := "**What**: decision made\n\n```omnia-meta\nkind: decision\n```"
	stripped := stripMetaBlock(content)
	rendered, err := renderMarkdown(stripped)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(rendered, "omnia-meta") {
		t.Errorf("rendered output should not contain omnia-meta block, got: %s", rendered)
	}
	if !strings.Contains(rendered, "<strong>What</strong>") {
		t.Errorf("expected bold 'What', got: %s", rendered)
	}
}
