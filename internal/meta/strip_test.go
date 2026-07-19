package meta_test

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/meta"
)

func TestStrip_RemovesValidBlock(t *testing.T) {
	body := "# Pull Request: add auth\n\nThis PR introduces JWT auth."
	content := body + "\n\n" + meta.Render(meta.Meta{
		SchemaVersion: 1,
		Source:        "github",
		Kind:          "pull_request",
		Layer:         "ingested",
		Project:       "p1",
	})

	got := meta.Strip(content)
	if strings.Contains(got, "omnia-meta") || strings.Contains(got, "schema_version") {
		t.Errorf("Strip left meta block behind:\n%q", got)
	}
	if !strings.Contains(got, "JWT auth") {
		t.Errorf("Strip removed human body:\n%q", got)
	}
	if got != body {
		t.Errorf("Strip: got %q, want %q", got, body)
	}
}

func TestStrip_LeavesCuratedContentUntouched(t *testing.T) {
	// A human note with no valid block must be returned unchanged.
	content := "Just a human note.\n\nNo metadata here."
	if got := meta.Strip(content); got != content {
		t.Errorf("Strip modified curated content: got %q", got)
	}
}

func TestStrip_IgnoresInvalidQuotedBlock(t *testing.T) {
	// A note that merely quotes the fence but lacks mandatory fields is NOT a
	// valid block, so Strip must leave it intact (same contract as Parse).
	content := "Look at this format:\n```omnia-meta\nfoo: bar\n```\nEnd of note."
	if got := meta.Strip(content); got != content {
		t.Errorf("Strip touched an invalid quoted block: got %q", got)
	}
}
