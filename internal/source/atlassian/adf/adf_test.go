package adf_test

import (
	"encoding/json"
	"testing"

	"github.com/velion/omnia/internal/source/atlassian/adf"
)

func TestToMarkdown(t *testing.T) {
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
			name: "invalid json returns empty string",
			in:   "not json",
			want: "",
		},
		{
			name: "plain paragraph",
			in:   `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"hello world"}]}]}`,
			want: "hello world",
		},
		{
			name: "strong mark",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]}]}]}`,
			want: "**bold**",
		},
		{
			name: "em mark",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"italic","marks":[{"type":"em"}]}]}]}`,
			want: "*italic*",
		},
		{
			name: "code mark",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"code","marks":[{"type":"code"}]}]}]}`,
			want: "`code`",
		},
		{
			name: "link mark",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}]}]}`,
			want: "[click](https://example.com)",
		},
		{
			name: "combined strong and link marks",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"strong"},{"type":"link","attrs":{"href":"https://example.com"}}]}]}]}`,
			want: "[**click**](https://example.com)",
		},
		{
			name: "heading level 2",
			in:   `{"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]}]}`,
			want: "## Title",
		},
		{
			name: "bullet list",
			in:   `{"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}]}`,
			want: "- one\n- two",
		},
		{
			name: "ordered list",
			in:   `{"type":"doc","content":[{"type":"orderedList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}]}]}`,
			want: "1. first\n2. second",
		},
		{
			name: "code block with language",
			in:   `{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)"}]}]}`,
			want: "```go\nfmt.Println(1)\n```",
		},
		{
			name: "code block without language",
			in:   `{"type":"doc","content":[{"type":"codeBlock","content":[{"type":"text","text":"raw"}]}]}`,
			want: "```\nraw\n```",
		},
		{
			name: "blockquote",
			in:   `{"type":"doc","content":[{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"quoted text"}]}]}]}`,
			want: "> quoted text",
		},
		{
			name: "hard break within paragraph",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"line one"},{"type":"hardBreak"},{"type":"text","text":"line two"}]}]}`,
			want: "line one\nline two",
		},
		{
			name: "rule between paragraphs",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"above"}]},{"type":"rule"},{"type":"paragraph","content":[{"type":"text","text":"below"}]}]}`,
			want: "above\n\n---\n\nbelow",
		},
		{
			name: "table basics with header row",
			in:   `{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]},{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"1"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"2"}]}]}]}]}]}`,
			want: "| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			name: "unknown node type degrades into content text",
			in:   `{"type":"doc","content":[{"type":"panel","content":[{"type":"paragraph","content":[{"type":"text","text":"warning message"}]}]}]}`,
			want: "warning message",
		},
		{
			name: "deeply nested unknown node still surfaces text",
			in:   `{"type":"doc","content":[{"type":"mediaGroup","content":[{"type":"mediaSingle","content":[{"type":"paragraph","content":[{"type":"text","text":"caption text"}]}]}]}]}`,
			want: "caption text",
		},
		{
			name: "unknown mark drops wrapper but keeps text",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"plain","marks":[{"type":"underline"}]}]}]}`,
			want: "plain",
		},
		{
			name: "multiple paragraphs joined by blank line",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]},{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}`,
			want: "first\n\nsecond",
		},
		{
			name: "javascript scheme link is rejected, text only",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}`,
			want: "click",
		},
		{
			name: "data scheme link is rejected, text only",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"data:text/html,evil"}}]}]}]}`,
			want: "click",
		},
		{
			name: "relative link with no scheme is still rendered as a link",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"local page","marks":[{"type":"link","attrs":{"href":"/wiki/page"}}]}]}]}`,
			want: "[local page](/wiki/page)",
		},
		{
			name: "link href containing a closing paren stays valid markdown",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"wiki page","marks":[{"type":"link","attrs":{"href":"https://example.com/wiki_(disambiguation)"}}]}]}]}`,
			want: "[wiki page](<https://example.com/wiki_(disambiguation)>)",
		},
		{
			name: "code mark with an embedded backtick uses a double-backtick fence",
			in:   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a` + "`" + `b","marks":[{"type":"code"}]}]}]}`,
			want: "``a`b``",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adf.ToMarkdown(json.RawMessage(tt.in))
			if got != tt.want {
				t.Errorf("ToMarkdown() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToMarkdown_NilInput(t *testing.T) {
	if got := adf.ToMarkdown(nil); got != "" {
		t.Errorf("ToMarkdown(nil) = %q, want empty string", got)
	}
}
