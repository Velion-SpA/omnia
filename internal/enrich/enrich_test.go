package enrich_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Velion-SpA/omnia/internal/enrich"
)

func TestNormalizeTopicKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "github/velion-api/issue-1", "github/velion-api/issue-1"},
		{"spaces", "hello world", "hello-world"},
		{"uppercase", "GitHub/Velion", "github/velion"},
		{"special chars", "foo@bar!baz", "foobarbaz"},
		{"long key", strings.Repeat("a", 130), strings.Repeat("a", 120)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrich.NormalizeTopicKey(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeTopicKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestChunkContent(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		chunkSize int
		wantLen   int
	}{
		{"fits in one", "hello", 10, 1},
		{"exact boundary", "abcde", 5, 1},
		{"needs two chunks", "abcdefghij", 5, 2},
		{"three chunks", "abcdefghijklmno", 5, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := enrich.ChunkContent(tt.content, tt.chunkSize)
			if len(chunks) != tt.wantLen {
				t.Errorf("ChunkContent len=%d, want %d", len(chunks), tt.wantLen)
			}
			// Verify reconstruction.
			var joined string
			for _, c := range chunks {
				joined += c
			}
			if joined != tt.content {
				t.Errorf("ChunkContent round-trip failed: got %q, want %q", joined, tt.content)
			}
		})
	}
}

// TestNormalizeTopicKeyMultibyteAccents verifies FIX-4: a string of multibyte
// (accented) characters longer than 120 runes is truncated safely. The result
// must be valid UTF-8 and at most 120 runes long.
func TestNormalizeTopicKeyMultibyteAccents(t *testing.T) {
	// "ñ" is a 2-byte UTF-8 rune — use 130 of them to exceed the 120-rune limit.
	input := strings.Repeat("ñ", 130)
	got := enrich.NormalizeTopicKey(input)

	// Result must be valid UTF-8.
	if !utf8.ValidString(got) {
		t.Error("NormalizeTopicKey produced invalid UTF-8 for multibyte input")
	}

	// Result must be at most 120 runes.
	runes := []rune(got)
	if len(runes) > 120 {
		t.Errorf("NormalizeTopicKey result is %d runes, want ≤ 120", len(runes))
	}

	// Result must not be empty (ñ is a letter, so all runes are kept).
	if len(runes) == 0 {
		t.Error("NormalizeTopicKey produced empty result for non-empty multibyte input")
	}
}

func TestExtractKeywords(t *testing.T) {
	kws := enrich.ExtractKeywords(
		[]string{"Go", "api", "Go"},
		[]string{"engram", "API"},
	)
	want := map[string]bool{"go": true, "api": true, "engram": true}
	if len(kws) != len(want) {
		t.Errorf("keywords len=%d, want %d: %v", len(kws), len(want), kws)
	}
	for _, k := range kws {
		if !want[k] {
			t.Errorf("unexpected keyword %q", k)
		}
	}
}
