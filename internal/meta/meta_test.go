package meta

import (
	"strings"
	"testing"
	"time"
)

func TestRoundTripFull(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 23, 38, 21, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 13, 1, 31, 48, 0, time.UTC)
	ingestedAt := time.Date(2026, 6, 13, 2, 0, 0, 0, time.UTC)

	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "github",
		Kind:          "pull_request",
		Layer:         "ingested",
		Project:       "saluvita",
		Repo:          "arratiabenjamin/saluvita",
		SourceID:      "5",
		Status:        "closed",
		Author:        "arratiabenjamin",
		Participants:  []string{"arratiabenjamin", "RoberCornejo"},
		URL:           "https://github.com/arratiabenjamin/saluvita/pull/5",
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		IngestedAt:    ingestedAt,
		ChunkCurrent:  2,
		ChunkTotal:    3,
	}

	rendered := Render(m)
	// Embed the block in some content.
	content := "## Body\n\nSome content here.\n\n" + rendered

	got, ok := Parse(content)
	if !ok {
		t.Fatal("Parse returned false for valid content")
	}

	assertEqual(t, "SchemaVersion", m.SchemaVersion, got.SchemaVersion)
	assertEqual(t, "Source", m.Source, got.Source)
	assertEqual(t, "Kind", m.Kind, got.Kind)
	assertEqual(t, "Layer", m.Layer, got.Layer)
	assertEqual(t, "Project", m.Project, got.Project)
	assertEqual(t, "Repo", m.Repo, got.Repo)
	assertEqual(t, "SourceID", m.SourceID, got.SourceID)
	assertEqual(t, "Status", m.Status, got.Status)
	assertEqual(t, "Author", m.Author, got.Author)
	assertStringSliceEqual(t, "Participants", m.Participants, got.Participants)
	assertEqual(t, "URL", m.URL, got.URL)
	assertTimeEqual(t, "CreatedAt", m.CreatedAt, got.CreatedAt)
	assertTimeEqual(t, "UpdatedAt", m.UpdatedAt, got.UpdatedAt)
	assertTimeEqual(t, "IngestedAt", m.IngestedAt, got.IngestedAt)
	assertEqual(t, "ChunkCurrent", m.ChunkCurrent, got.ChunkCurrent)
	assertEqual(t, "ChunkTotal", m.ChunkTotal, got.ChunkTotal)
}

func TestRoundTripMinimal(t *testing.T) {
	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "discord",
		Kind:          "message_digest",
		Layer:         "ingested",
		Project:       "omnia",
	}

	rendered := Render(m)
	got, ok := Parse(rendered)
	if !ok {
		t.Fatal("Parse returned false for minimal content")
	}

	assertEqual(t, "SchemaVersion", m.SchemaVersion, got.SchemaVersion)
	assertEqual(t, "Source", m.Source, got.Source)
	assertEqual(t, "Kind", m.Kind, got.Kind)
	assertEqual(t, "Layer", m.Layer, got.Layer)
	assertEqual(t, "Project", m.Project, got.Project)
	assertEqual(t, "Repo", "", got.Repo)
	assertEqual(t, "SourceID", "", got.SourceID)
	assertEqual(t, "ChunkCurrent", 0, got.ChunkCurrent)
	assertEqual(t, "ChunkTotal", 0, got.ChunkTotal)
	if len(got.Participants) != 0 {
		t.Errorf("Participants = %v, want empty", got.Participants)
	}
}

func TestRoundTripZeroTimes(t *testing.T) {
	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "github",
		Kind:          "issue",
		Layer:         "ingested",
		Project:       "myproject",
		// Zero times — should not be emitted.
	}

	rendered := Render(m)

	// Zero times must not appear in rendered output.
	if strings.Contains(rendered, "created_at") {
		t.Error("rendered block contains created_at for zero time")
	}
	if strings.Contains(rendered, "updated_at") {
		t.Error("rendered block contains updated_at for zero time")
	}
	if strings.Contains(rendered, "ingested_at") {
		t.Error("rendered block contains ingested_at for zero time")
	}

	got, ok := Parse(rendered)
	if !ok {
		t.Fatal("Parse returned false for zero-time content")
	}

	if !got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want zero", got.CreatedAt)
	}
	if !got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt = %v, want zero", got.UpdatedAt)
	}
	if !got.IngestedAt.IsZero() {
		t.Errorf("IngestedAt = %v, want zero", got.IngestedAt)
	}
}

func TestParseNoBlock(t *testing.T) {
	content := "## Body\n\nThis is some regular content.\n\nNo meta block here."
	_, ok := Parse(content)
	if ok {
		t.Error("Parse returned true for content with no omnia-meta block")
	}
}

func TestParseMalformedBlock(t *testing.T) {
	// Fence opens but never closes.
	content := "## Body\n\n```omnia-meta\nschema_version: 1\nsource: github\n"
	_, ok := Parse(content)
	if ok {
		t.Error("Parse returned true for malformed block (no closing fence)")
	}
}

func TestRoundTripSingleParticipant(t *testing.T) {
	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "github",
		Kind:          "issue",
		Layer:         "ingested",
		Project:       "myproject",
		Participants:  []string{"alice"},
	}

	rendered := Render(m)
	got, ok := Parse(rendered)
	if !ok {
		t.Fatal("Parse returned false")
	}

	assertStringSliceEqual(t, "Participants", m.Participants, got.Participants)
}

func TestRoundTripChunkField(t *testing.T) {
	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "github",
		Kind:          "issue",
		Layer:         "ingested",
		Project:       "myproject",
		ChunkCurrent:  1,
		ChunkTotal:    5,
	}

	rendered := Render(m)
	if !strings.Contains(rendered, "chunk: 1/5") {
		t.Errorf("rendered block missing chunk: 1/5; got:\n%s", rendered)
	}

	got, ok := Parse(rendered)
	if !ok {
		t.Fatal("Parse returned false")
	}

	assertEqual(t, "ChunkCurrent", 1, got.ChunkCurrent)
	assertEqual(t, "ChunkTotal", 5, got.ChunkTotal)
}

func TestChunkNotEmittedWhenZero(t *testing.T) {
	m := Meta{
		SchemaVersion: SchemaVersion,
		Source:        "github",
		Kind:          "issue",
		Layer:         "ingested",
		Project:       "myproject",
		ChunkCurrent:  0,
		ChunkTotal:    0,
	}

	rendered := Render(m)
	if strings.Contains(rendered, "chunk:") {
		t.Errorf("rendered block should not contain chunk field when ChunkCurrent==0; got:\n%s", rendered)
	}
}

// --- helpers ---

func assertEqual[T comparable](t *testing.T, field string, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", field, want, got)
	}
}

func assertTimeEqual(t *testing.T, field string, want, got time.Time) {
	t.Helper()
	// Compare truncated to second to avoid sub-second precision differences.
	if !want.UTC().Truncate(time.Second).Equal(got.UTC().Truncate(time.Second)) {
		t.Errorf("%s: want %v, got %v", field, want, got)
	}
}

func assertStringSliceEqual(t *testing.T, field string, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: len want %d, got %d (%v vs %v)", field, len(want), len(got), want, got)
		return
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("%s[%d]: want %q, got %q", field, i, want[i], got[i])
		}
	}
}
