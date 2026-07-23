package anchor

import "testing"

// ─── 1.9: materiality helper (pure, table-driven) ────────────────────────────

func TestIsMaterialChange(t *testing.T) {
	tests := []struct {
		name         string
		oldHash      string
		newHash      string
		oldContent   string
		newContent   string
		threshold    float64
		wantMaterial bool
	}{
		{
			name:         "identical hash short-circuits to not material regardless of content",
			oldHash:      "same",
			newHash:      "same",
			oldContent:   "func Foo() { return 1 }",
			newContent:   "func Foo() { return 2 }",
			threshold:    0,
			wantMaterial: false,
		},
		{
			name:         "different hash, real non-whitespace change, default threshold -> material",
			oldHash:      "h1",
			newHash:      "h2",
			oldContent:   "func Foo() {\n\treturn 1\n}",
			newContent:   "func Foo() {\n\treturn 2\n}",
			threshold:    0,
			wantMaterial: true,
		},
		{
			name:         "different hash but whitespace-only diff (reindent) -> not material",
			oldHash:      "h1",
			newHash:      "h2",
			oldContent:   "func Foo() {\n\treturn 1\n}",
			newContent:   "func Foo() {\n    return 1\n}",
			threshold:    0,
			wantMaterial: false,
		},
		{
			name:         "different hash but only blank-line insertion -> not material",
			oldHash:      "h1",
			newHash:      "h2",
			oldContent:   "line1\nline2",
			newContent:   "line1\n\nline2",
			threshold:    0,
			wantMaterial: false,
		},
		{
			name:         "different hash, single-line change out of many, threshold above ratio -> not material",
			oldHash:      "h1",
			newHash:      "h2",
			oldContent:   "a\nb\nc\nd\ne\nf\ng\nh\ni\nj",
			newContent:   "a\nb\nc\nd\ne\nf\ng\nh\ni\nX",
			threshold:    0.5,
			wantMaterial: false,
		},
		{
			name:         "different hash, majority lines changed, threshold met -> material",
			oldHash:      "h1",
			newHash:      "h2",
			oldContent:   "a\nb\nc\nd",
			newContent:   "w\nx\ny\nz",
			threshold:    0.5,
			wantMaterial: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsMaterialChange(tt.oldHash, tt.newHash, tt.oldContent, tt.newContent, tt.threshold)
			if got != tt.wantMaterial {
				t.Errorf("IsMaterialChange() = %v, want %v", got, tt.wantMaterial)
			}
		})
	}
}

func TestNormalizeRangeContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trims trailing whitespace", "foo   \nbar\t\n", "foo\nbar"},
		{"drops blank lines", "foo\n\n\nbar", "foo\nbar"},
		{"trims leading indentation", "    foo\n\tbar", "foo\nbar"},
		{"empty input stays empty", "", ""},
		{"whitespace-only input becomes empty", "   \n\t\n  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRangeContent(tt.in); got != tt.want {
				t.Errorf("normalizeRangeContent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
