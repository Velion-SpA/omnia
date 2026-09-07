package repodoc

import "testing"

func TestRenderParseAnchorBlock_RoundTrip(t *testing.T) {
	a := AnchorInfo{
		RepoRoot:    "/home/user/repo",
		File:        "docs/architecture.md",
		Symbol:      "Architecture > Data flow",
		LineStart:   12,
		LineEnd:     40,
		BlameSHA:    "abc1234def5678901234567890123456789abcd",
		BlameAt:     "2026-08-14T10:00:00Z",
		ContentHash: "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
	}

	block := renderAnchorBlock(a)
	got, ok := ParseAnchorBlock("Some chunk content.\n\n" + block)
	if !ok {
		t.Fatalf("ParseAnchorBlock returned ok=false for a freshly rendered block:\n%s", block)
	}
	if got != a {
		t.Errorf("round-trip mismatch:\n got  = %+v\n want = %+v", got, a)
	}
}

func TestParseAnchorBlock_MissingMandatoryFieldsRejected(t *testing.T) {
	// content_hash omitted — must not parse as a valid anchor.
	block := anchorFence + "\n" +
		"file: docs/x.md\n" +
		"line_start: 1\n" +
		"line_end: 5\n" +
		anchorFenceClose + "\n"
	if _, ok := ParseAnchorBlock(block); ok {
		t.Error("expected ok=false when content_hash is missing")
	}
}

func TestParseAnchorBlock_NoBlockPresent(t *testing.T) {
	if _, ok := ParseAnchorBlock("just some plain text, no fenced block at all"); ok {
		t.Error("expected ok=false when no repodoc-anchor block is present")
	}
}

func TestParseAnchorBlock_LastFenceWins(t *testing.T) {
	// A doc that happens to quote the fence marker in prose must not
	// confuse the parser — only the LAST occurrence counts (mirrors
	// internal/meta.Parse's same defensive contract).
	fake := anchorFence + "\nfile: not-real.md\nline_start: 1\nline_end: 1\ncontent_hash: deadbeef\n" + anchorFenceClose + "\n"
	real := AnchorInfo{
		File:        "real.md",
		LineStart:   3,
		LineEnd:     9,
		ContentHash: "cafef00d",
	}
	content := "A doc that quotes a repodoc-anchor block as an example:\n\n" + fake + "\n\n" + renderAnchorBlock(real)

	got, ok := ParseAnchorBlock(content)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.File != "real.md" {
		t.Errorf("File = %q, want %q (last block should win)", got.File, "real.md")
	}
}

func TestRenderAnchorBlock_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	a := AnchorInfo{
		File:        "docs/x.md",
		LineStart:   1,
		LineEnd:     2,
		ContentHash: "abc123",
		// Symbol/BlameSHA/BlameAt left empty (git repo with no history, or capture partial failure).
	}
	block := renderAnchorBlock(a)
	got, ok := ParseAnchorBlock(block)
	if !ok {
		t.Fatal("expected ok=true even without optional fields")
	}
	if got.BlameSHA != "" || got.BlameAt != "" || got.Symbol != "" {
		t.Errorf("expected empty optional fields to stay empty, got %+v", got)
	}
}
