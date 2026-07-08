package github

// chunk_test.go — white-box tests for formatIssue chunking behavior (C3/S4b).
// These tests live in package github (not github_test) so they can access unexported
// types (issue, comment) and the formatIssue function directly.

import (
	"strings"
	"testing"

	"github.com/Velion-SpA/omnia/internal/meta"
)

// TestFormatIssueSingleChunk verifies that a short issue produces exactly one item
// with no -partN suffix in the topic_key.
func TestFormatIssueSingleChunk(t *testing.T) {
	iss := issue{
		Number:  5,
		Title:   "Short issue",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/5",
		Body:    "Short body.",
		User:    struct{ Login string `json:"login"` }{Login: "alice"},
	}

	items := formatIssue(iss, nil, "org", "repo", "myproject")
	if len(items) != 1 {
		t.Fatalf("expected 1 item for short content, got %d", len(items))
	}
	if strings.Contains(items[0].TopicKey, "-part") {
		t.Errorf("short issue topic_key %q should not have -partN suffix", items[0].TopicKey)
	}
}

// TestFormatIssueLargeBodyChunks verifies that content exceeding maxChunkRunes (45 000)
// is split into multiple items, each with a unique -partN topic_key and the correct
// context header on continuation chunks. Topic keys must not exceed 120 chars (S4b).
func TestFormatIssueLargeBodyChunks(t *testing.T) {
	// Build comments large enough to exceed 45 000 runes total when combined with the body.
	// Each comment body is at maxCommentBodyLen (2000). We fabricate 25 comments;
	// formatIssue caps at maxComments (10) so we'll have exactly 10 × 2000 = 20 000 rune
	// comment block plus a 5 000-rune body and ~2 000 runes of formatting = ~27 000. Still
	// under 45 000. To guarantee we cross the boundary, we instead supply a comment list
	// with bodies that are 4 500 runes each; formatIssue truncates each to 2 000, but we
	// can't exceed the limit that way.
	//
	// The only reliable approach with the current truncation constraints is to fabricate
	// enough small comments that sum past 45 000. Since maxComments=10 limits us, we
	// instead build a large number of participant usernames and a moderately large body.
	// The real test path: use a body close to 5000 and pad via the source URL and label list.
	//
	// Alternatively, we create comments up to the cap and also a body of 5000, then verify
	// that when the content is JUST at a boundary it chunks. To force multiple chunks we
	// bypass the comment-count limit by calling formatIssue with exactly enough comments
	// so the total text exceeds 45 000 runes. maxCommentBodyLen truncation caps each to 2000,
	// but we can add many distinct short comments — however maxComments = 10 is a constant
	// in this package, meaning fetchComments fetches at most 10. formatIssue itself does
	// not cap the slice it receives, so we can pass more than 10 in the test.

	bigBody := strings.Repeat("b", 5000) // at truncation cap

	// 23 comments × 2000-char body = 46 000 rune comment block → total > 45 000.
	var comments []comment
	for i := 0; i < 23; i++ {
		comments = append(comments, comment{
			Body: strings.Repeat("c", 2000),
			User: struct {
				Login string `json:"login"`
			}{Login: "user"},
		})
	}

	iss := issue{
		Number:  99,
		Title:   "Large issue",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/99",
		Body:    bigBody,
		User:    struct{ Login string `json:"login"` }{Login: "author"},
	}

	items := formatIssue(iss, comments, "org", "repo", "myproject")
	if len(items) < 2 {
		t.Fatalf("expected >= 2 chunks for large content, got %d item(s)", len(items))
	}

	seen := make(map[string]bool)
	for i, item := range items {
		// Each topic_key must be unique.
		if seen[item.TopicKey] {
			t.Errorf("item %d: duplicate topic_key %q", i, item.TopicKey)
		}
		seen[item.TopicKey] = true

		// All items except the first must carry a -partN suffix.
		if i > 0 && !strings.Contains(item.TopicKey, "-part") {
			t.Errorf("item %d: continuation topic_key %q missing -partN suffix", i, item.TopicKey)
		}
		// S4b: total key length ≤ 120.
		if len([]rune(item.TopicKey)) > 120 {
			t.Errorf("item %d: topic_key length %d exceeds 120", i, len([]rune(item.TopicKey)))
		}
	}

	// First item must NOT have a context header.
	if strings.HasPrefix(items[0].Content, "<!--") {
		t.Error("first item should not start with context header")
	}
	// Continuation items must start with a context header (S4a).
	for i := 1; i < len(items); i++ {
		if !strings.HasPrefix(items[i].Content, "<!--") {
			t.Errorf("item %d (continuation) missing context header; starts: %.40s", i, items[i].Content)
		}
	}
}

// TestChunkedIssueHasMetaBlockInEachChunk verifies that every chunk produced by
// formatIssue contains a parseable omnia-meta block with the correct fields (Task 9).
func TestChunkedIssueHasMetaBlockInEachChunk(t *testing.T) {
	bigBody := strings.Repeat("b", 5000) // at truncation cap

	// 23 comments × 2000-char body exceeds a single 45k-rune chunk.
	var comments []comment
	for i := 0; i < 23; i++ {
		comments = append(comments, comment{
			Body: strings.Repeat("c", 2000),
			User: struct {
				Login string `json:"login"`
			}{Login: "reviewer"},
		})
	}

	iss := issue{
		Number:  42,
		Title:   "Large PR for meta test",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/42",
		Body:    bigBody,
		User:    struct{ Login string `json:"login"` }{Login: "author"},
	}

	items := formatIssue(iss, comments, "org", "repo", "myproject")
	if len(items) < 2 {
		t.Fatalf("expected >= 2 chunks for large content, got %d item(s)", len(items))
	}

	for i, item := range items {
		m, ok := meta.Parse(item.Content)
		if !ok {
			t.Errorf("chunk %d: meta.Parse returned false — no omnia-meta block found", i+1)
			continue
		}

		// Chunk-specific assertions.
		if len(items) > 1 {
			wantCurrent := i + 1
			wantTotal := len(items)
			if m.ChunkCurrent != wantCurrent {
				t.Errorf("chunk %d: ChunkCurrent = %d, want %d", i+1, m.ChunkCurrent, wantCurrent)
			}
			if m.ChunkTotal != wantTotal {
				t.Errorf("chunk %d: ChunkTotal = %d, want %d", i+1, m.ChunkTotal, wantTotal)
			}
		}

		// Common field assertions.
		if m.Kind != "issue" {
			t.Errorf("chunk %d: Kind = %q, want %q", i+1, m.Kind, "issue")
		}
		if m.Layer != "ingested" {
			t.Errorf("chunk %d: Layer = %q, want %q", i+1, m.Layer, "ingested")
		}
		if m.Source != "github" {
			t.Errorf("chunk %d: Source = %q, want %q", i+1, m.Source, "github")
		}
		if m.Project != "myproject" {
			t.Errorf("chunk %d: Project = %q, want %q", i+1, m.Project, "myproject")
		}
	}
}
