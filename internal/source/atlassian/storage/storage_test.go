package storage_test

import (
	"testing"

	"github.com/velion/omnia/internal/source/atlassian/storage"
)

func TestToText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty input returns empty string",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only input returns empty string",
			in:   "   \n\t  ",
			want: "",
		},
		{
			name: "plain paragraph",
			in:   "<p>hello world</p>",
			want: "hello world",
		},
		{
			name: "strong element",
			in:   "<p>this is <strong>bold</strong> text</p>",
			want: "this is **bold** text",
		},
		{
			name: "b element treated same as strong",
			in:   "<p><b>bold</b></p>",
			want: "**bold**",
		},
		{
			name: "em element",
			in:   "<p>this is <em>italic</em> text</p>",
			want: "this is *italic* text",
		},
		{
			name: "code element",
			in:   "<p>run <code>go test</code> now</p>",
			want: "run `go test` now",
		},
		{
			name: "link with href",
			in:   `<p><a href="https://example.com">click here</a></p>`,
			want: "[click here](https://example.com)",
		},
		{
			name: "heading level 2",
			in:   "<h2>Title</h2>",
			want: "## Title",
		},
		{
			name: "unordered list",
			in:   "<ul><li>one</li><li>two</li></ul>",
			want: "- one\n- two",
		},
		{
			name: "ordered list",
			in:   "<ol><li>first</li><li>second</li></ol>",
			want: "1. first\n2. second",
		},
		{
			name: "blockquote",
			in:   "<blockquote><p>quoted text</p></blockquote>",
			want: "> quoted text",
		},
		{
			name: "line break within paragraph",
			in:   "<p>line one<br/>line two</p>",
			want: "line one\nline two",
		},
		{
			name: "horizontal rule between paragraphs",
			in:   "<p>above</p><hr/><p>below</p>",
			want: "above\n\n---\n\nbelow",
		},
		{
			name: "table basics with header row",
			in:   "<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>",
			want: "| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			name: "unknown confluence macro tag passes through its children",
			in:   `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>note text</p></ac:rich-text-body></ac:structured-macro>`,
			want: "note text",
		},
		{
			name: "unknown span tag passes through text inline",
			in:   `<p>before <span class="highlight">middle</span> after</p>`,
			want: "before middle after",
		},
		{
			name: "html entities are decoded",
			in:   "<p>Tom &amp; Jerry</p>",
			want: "Tom & Jerry",
		},
		{
			name: "multiple paragraphs joined by blank line",
			in:   "<p>first</p><p>second</p>",
			want: "first\n\nsecond",
		},
		{
			name: "script tag content is dropped, not leaked",
			in:   "<p>before</p><script>alert(document.cookie)</script><p>after</p>",
			want: "before\n\nafter",
		},
		{
			name: "style tag content is dropped, not leaked",
			in:   "<p>before</p><style>body{color:red}</style><p>after</p>",
			want: "before\n\nafter",
		},
		{
			name: "javascript scheme link is rejected, text only",
			in:   `<p><a href="javascript:alert(1)">click</a></p>`,
			want: "click",
		},
		{
			name: "data scheme link is rejected, text only",
			in:   `<p><a href="data:text/html,evil">click</a></p>`,
			want: "click",
		},
		{
			name: "relative link with no scheme is still rendered as a link",
			in:   `<p><a href="/wiki/page">local page</a></p>`,
			want: "[local page](/wiki/page)",
		},
		{
			name: "link href containing a closing paren stays valid markdown",
			in:   `<p><a href="https://example.com/wiki_(disambiguation)">wiki page</a></p>`,
			want: "[wiki page](<https://example.com/wiki_(disambiguation)>)",
		},
		{
			name: "code element with an embedded backtick uses a double-backtick fence",
			in:   "<p><code>a`b</code></p>",
			want: "``a`b``",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := storage.ToText(tt.in)
			if got != tt.want {
				t.Errorf("ToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
