package mcp

// answer_test.go — table-driven tests for P3's assembly/confidence logic
// (answer.go): ExtractAnswerText's three recognized shapes plus its "drop,
// don't slice" default, AssembleAnswerContext's budget behavior, and
// ClassifyAnswerConfidence's none/low/high derivation.

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func TestExtractAnswerText(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantOK  bool
		want    string // only checked when non-empty
	}{
		{
			name:    "bold field What/Why",
			content: "**What**: Fixed a crash in checkout.\n**Why**: A customer reported it.",
			wantOK:  true,
			want:    "What: Fixed a crash in checkout.\nWhy: A customer reported it.",
		},
		{
			name:    "bold field with Where and Learned",
			content: "**What**: Refactored auth.\n**Where**: internal/auth/middleware.go\n**Learned**: Must set httpOnly.",
			wantOK:  true,
		},
		{
			name: "session_summary heading style — the exact confabulation shape",
			content: "## Goal\nImprove tests\n\n## Accomplished\n- Added coverage for the answer endpoint\n\n" +
				"## Next Steps\n- Calibrate the confidence threshold",
			wantOK: true,
		},
		{
			name:    "repodoc chunk header (Document/Section)",
			content: "Document: Omnia README\nSection: Installation\n\nRun `brew install omnia` to get started.\n",
			wantOK:  true,
			want:    "Document: Omnia README\nSection: Installation\n\nRun `brew install omnia` to get started.",
		},
		{
			name:    "unstructured freeform content — must be DROPPED, not sliced",
			content: "just a plain note with no structure at all, written mid-sentence and never labelled",
			wantOK:  false,
		},
		{
			name:    "empty content",
			content: "",
			wantOK:  false,
		},
		{
			name:    "bold field with empty body is skipped, remaining fields still extracted",
			content: "**What**: \n**Why**: real reason here",
			wantOK:  true,
			want:    "Why: real reason here",
		},
		{
			name:    "an arbitrary bold phrase that is NOT a recognized label is not treated as structure",
			content: "**Warning**: this is bold but not a recognized field name",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := store.Observation{Content: tt.content}
			got, ok := ExtractAnswerText(obs)
			if ok != tt.wantOK {
				t.Fatalf("ExtractAnswerText(%q) ok = %v, want %v (got text %q)", tt.content, ok, tt.wantOK, got)
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("ExtractAnswerText(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestExtractAnswerText_MarkdownHeadingNeverReachesOutputAsBareText is the
// direct regression test for the plan's own motivating bug: a
// mem_session_summary's "## Goal" heading must never appear in the output as
// an isolated fragment the way a raw character slice would produce it — it
// must always be paired with its own body text under a "Label: body" shape.
func TestExtractAnswerText_MarkdownHeadingNeverReachesOutputAsBareText(t *testing.T) {
	content := "## Goal\nWork on the conversational retrieval plan for voice-agent consumers"
	obs := store.Observation{Content: content}
	got, ok := ExtractAnswerText(obs)
	if !ok {
		t.Fatalf("ExtractAnswerText(%q) ok = false, want true", content)
	}
	if got == "## Goal" || strings.TrimSpace(got) == "## Goal" {
		t.Fatalf("ExtractAnswerText(%q) = %q — heading alone reached output with no body, the exact confabulation shape", content, got)
	}
	if !strings.Contains(got, "Work on the conversational retrieval plan") {
		t.Errorf("ExtractAnswerText(%q) = %q, want it to contain the heading's body text", content, got)
	}
}

func srWithContent(id int64, syncID, typ, content string) store.SearchResult {
	return store.SearchResult{
		Observation: store.Observation{ID: id, SyncID: syncID, Type: typ, Title: "t" + syncID, Content: content},
	}
}

func TestAssembleAnswerContext_DropsUnstructuredChunksEntirely(t *testing.T) {
	results := []store.SearchResult{
		srWithContent(1, "obs-a", "bugfix", "**What**: Fixed the bug.\n**Why**: It crashed."),
		srWithContent(2, "obs-b", "manual", "unstructured freeform note with no fields at all"),
	}
	got := AssembleAnswerContext(results, 1000)
	if len(got.Sources) != 1 || got.Sources[0].SyncID != "obs-a" {
		t.Fatalf("AssembleAnswerContext sources = %+v, want exactly [obs-a] (obs-b has no extractable structure and must be dropped)", got.Sources)
	}
	if strings.Contains(got.Context, "unstructured freeform note") {
		t.Errorf("AssembleAnswerContext.Context = %q, must not contain unstructured content", got.Context)
	}
}

func TestAssembleAnswerContext_BudgetKeepsTopCompleteDropsRest(t *testing.T) {
	// Each extracted text is ~30 chars ("What: " + 24 'a's).
	longField := strings.Repeat("a", 24)
	results := []store.SearchResult{
		srWithContent(1, "obs-1", "bugfix", "**What**: "+longField),
		srWithContent(2, "obs-2", "bugfix", "**What**: "+longField),
		srWithContent(3, "obs-3", "bugfix", "**What**: "+longField),
	}
	// Budget fits exactly one chunk (30 chars) plus separator overhead would
	// exceed it for a second — TrimToBudget sizes PER-ITEM text only (not
	// including the join separator), so use a budget that clearly fits one
	// but not two.
	got := AssembleAnswerContext(results, 32)
	if len(got.Sources) != 1 {
		t.Fatalf("AssembleAnswerContext(budget=32) kept %d sources, want exactly 1 (top-ranked, complete)", len(got.Sources))
	}
	if got.Sources[0].SyncID != "obs-1" {
		t.Errorf("AssembleAnswerContext(budget=32) kept %q, want the FIRST (top-ranked) result obs-1", got.Sources[0].SyncID)
	}
}

func TestAssembleAnswerContext_ZeroBudgetYieldsEmpty(t *testing.T) {
	results := []store.SearchResult{srWithContent(1, "obs-1", "bugfix", "**What**: something")}
	got := AssembleAnswerContext(results, 0)
	if got.Context != "" || len(got.Sources) != 0 {
		t.Fatalf("AssembleAnswerContext(maxChars=0) = %+v, want empty (TrimToBudget's own budget<=0 contract)", got)
	}
}

func TestAssembleAnswerContext_SourcesCiteSyncIDNeverIntegerID(t *testing.T) {
	// Regression for the plan's explicit anti-goal: id 1918 named two
	// different observations across two replicas — AnswerSource must carry
	// ONLY SyncID, and AssembleAnswerContext must populate it from
	// Observation.SyncID, never from Observation.ID.
	results := []store.SearchResult{srWithContent(1918, "obs-9437f9849e481520", "bugfix", "**What**: something happened")}
	got := AssembleAnswerContext(results, 1000)
	if len(got.Sources) != 1 {
		t.Fatalf("AssembleAnswerContext sources = %+v, want exactly 1", got.Sources)
	}
	if got.Sources[0].SyncID != "obs-9437f9849e481520" {
		t.Errorf("AssembleAnswerContext source SyncID = %q, want the observation's real sync_id, not its integer id", got.Sources[0].SyncID)
	}
}

func TestClassifyAnswerConfidence(t *testing.T) {
	tests := []struct {
		name string
		sig  AnswerConfidenceSignals
		thr  float64
		want AnswerConfidence
	}{
		{
			name: "zero hits is none",
			sig:  AnswerConfidenceSignals{HitCount: 0},
			thr:  0.05,
			want: AnswerConfidenceNone,
		},
		{
			name: "FTS ladder reached step 2 is none, even with hits",
			sig:  AnswerConfidenceSignals{HitCount: 3, FTSRelaxed: true, FTSRelaxStep: 2, SourcesAssembled: 3, TopScore: 1, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceNone,
		},
		{
			name: "FTS ladder at step 1 (stopwords only) does not force none",
			sig:  AnswerConfidenceSignals{HitCount: 3, FTSRelaxed: true, FTSRelaxStep: 1, SourcesAssembled: 3, TopScore: 1, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceHigh,
		},
		{
			name: "hits exist but nothing survived extraction is low, never high",
			sig:  AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 0, TopScore: 1, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceLow,
		},
		{
			name: "recall degraded is low even with a strong score",
			sig:  AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 2, RecallDegraded: true, TopScore: 1, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceLow,
		},
		{
			name: "top score below threshold is low",
			sig:  AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 2, TopScore: 0.01, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceLow,
		},
		{
			name: "top score at or above threshold, not degraded, has sources: high",
			sig:  AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 2, TopScore: 0.05, HasTopScore: true},
			thr:  0.05,
			want: AnswerConfidenceHigh,
		},
		{
			name: "no top score available (sentinel-only match) with sources assembled: high",
			sig:  AnswerConfidenceSignals{HitCount: 1, SourcesAssembled: 1, HasTopScore: false},
			thr:  0.05,
			want: AnswerConfidenceHigh,
		},
		{
			name: "threshold <= 0 disables the score-based low trigger",
			sig:  AnswerConfidenceSignals{HitCount: 1, SourcesAssembled: 1, TopScore: 0, HasTopScore: true},
			thr:  0,
			want: AnswerConfidenceHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyAnswerConfidence(tt.sig, tt.thr)
			if got != tt.want {
				t.Errorf("ClassifyAnswerConfidence(%+v, %v) = %q, want %q", tt.sig, tt.thr, got, tt.want)
			}
		})
	}
}
