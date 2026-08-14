package repodoc

import "testing"

// TestGitBlobHash_MatchesRealGit pins gitBlobHash's output against real
// `git hash-object` output captured for the same three fixtures (empty
// content, "hello\n", "# Title\n\nBody text.\n") — see hash.go's doc comment
// for why byte-for-byte agreement with git matters here. Ground truth was
// captured via `git hash-object <file>` in a scratch repo; if git's object
// hashing algorithm ever changed (it has not, in git's entire history) this
// test would need updating, but the values themselves are just SHA-1 of a
// well-known header+content shape, not derived from this package.
func TestGitBlobHash_MatchesRealGit(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{
			name:    "empty content",
			content: []byte{},
			want:    "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		},
		{
			name:    "hello with trailing newline",
			content: []byte("hello\n"),
			want:    "ce013625030ba8dba906f756967f9e9ca394464a",
		},
		{
			name:    "small markdown doc",
			content: []byte("# Title\n\nBody text.\n"),
			want:    "56beed11f23f4cba987f52637c4d5585c1fa8cf4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitBlobHash(tt.content)
			if got != tt.want {
				t.Errorf("gitBlobHash(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestGitBlobHash_Deterministic(t *testing.T) {
	a := gitBlobHash([]byte("some content\n"))
	b := gitBlobHash([]byte("some content\n"))
	if a != b {
		t.Errorf("gitBlobHash is not deterministic: %q != %q", a, b)
	}
}

func TestGitBlobHash_DiffersOnChange(t *testing.T) {
	a := gitBlobHash([]byte("version one\n"))
	b := gitBlobHash([]byte("version two\n"))
	if a == b {
		t.Errorf("gitBlobHash should differ for different content, both = %q", a)
	}
}
