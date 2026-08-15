package mcp

// answer_test.go — table-driven tests for P3's assembly/confidence logic
// (answer.go): ExtractAnswerText's three recognized shapes plus its "drop,
// don't slice" default, AssembleAnswerContext's budget behavior, and
// ClassifyAnswerConfidence's none/low/high derivation.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/velion/omnia/internal/store"
)

func TestExtractAnswerText(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantOK      bool
		wantFull    string // only checked when non-empty
		wantPrimary string // only checked when non-empty
	}{
		{
			name:        "bold field What/Why — no secondary tier, Primary == Full",
			content:     "**What**: Fixed a crash in checkout.\n**Why**: A customer reported it.",
			wantOK:      true,
			wantFull:    "What: Fixed a crash in checkout.\nWhy: A customer reported it.",
			wantPrimary: "What: Fixed a crash in checkout.\nWhy: A customer reported it.",
		},
		{
			name:        "bold field with Where and Learned — Primary drops the secondary tier",
			content:     "**What**: Refactored auth.\n**Where**: internal/auth/middleware.go\n**Learned**: Must set httpOnly.",
			wantOK:      true,
			wantFull:    "What: Refactored auth.\nWhere: internal/auth/middleware.go\nLearned: Must set httpOnly.",
			wantPrimary: "What: Refactored auth.",
		},
		{
			name: "session_summary heading style — the exact confabulation shape",
			content: "## Goal\nImprove tests\n\n## Accomplished\n- Added coverage for the answer endpoint\n\n" +
				"## Next Steps\n- Calibrate the confidence threshold",
			wantOK:      true,
			wantPrimary: "Goal: Improve tests\nAccomplished: - Added coverage for the answer endpoint",
		},
		{
			name:        "repodoc chunk header (Document/Section), single paragraph — no internal break, Primary == Full",
			content:     "Document: Omnia README\nSection: Installation\n\nRun `brew install omnia` to get started.\n",
			wantOK:      true,
			wantFull:    "Document: Omnia README\nSection: Installation\n\nRun `brew install omnia` to get started.",
			wantPrimary: "Document: Omnia README\nSection: Installation\n\nRun `brew install omnia` to get started.",
		},
		{
			name: "repodoc chunk header, multiple paragraphs — Primary is header + lead paragraph only, never mid-sentence",
			content: "Document: omnia\nSection: (introduction)\n\n" +
				"Persistent memory for AI coding agents — local-first, one binary.\n\n" +
				"This second paragraph should NOT appear in Primary, only in Full.",
			wantOK: true,
			wantFull: "Document: omnia\nSection: (introduction)\n\n" +
				"Persistent memory for AI coding agents — local-first, one binary.\n\n" +
				"This second paragraph should NOT appear in Primary, only in Full.",
			wantPrimary: "Document: omnia\nSection: (introduction)\n\n" +
				"Persistent memory for AI coding agents — local-first, one binary.",
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
			name:        "bold field with empty body is skipped, remaining fields still extracted",
			content:     "**What**: \n**Why**: real reason here",
			wantOK:      true,
			wantFull:    "Why: real reason here",
			wantPrimary: "Why: real reason here",
		},
		{
			name:    "an arbitrary bold phrase that is NOT a recognized label is not treated as structure",
			content: "**Warning**: this is bold but not a recognized field name",
			wantOK:  false,
		},
		{
			name:        "only secondary-tier labels present — Primary falls back to Full rather than losing the chunk",
			content:     "**Where**: internal/auth/middleware.go\n**Learned**: Must set httpOnly.",
			wantOK:      true,
			wantFull:    "Where: internal/auth/middleware.go\nLearned: Must set httpOnly.",
			wantPrimary: "Where: internal/auth/middleware.go\nLearned: Must set httpOnly.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := store.Observation{Content: tt.content}
			got, ok := ExtractAnswerText(obs)
			if ok != tt.wantOK {
				t.Fatalf("ExtractAnswerText(%q) ok = %v, want %v (got %+v)", tt.content, ok, tt.wantOK, got)
			}
			if tt.wantFull != "" && got.Full != tt.wantFull {
				t.Errorf("ExtractAnswerText(%q).Full = %q, want %q", tt.content, got.Full, tt.wantFull)
			}
			if tt.wantPrimary != "" && got.Primary != tt.wantPrimary {
				t.Errorf("ExtractAnswerText(%q).Primary = %q, want %q", tt.content, got.Primary, tt.wantPrimary)
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
	for _, tier := range []struct {
		name string
		text string
	}{{"Full", got.Full}, {"Primary", got.Primary}} {
		if tier.text == "## Goal" || strings.TrimSpace(tier.text) == "## Goal" {
			t.Fatalf("ExtractAnswerText(%q).%s = %q — heading alone reached output with no body, the exact confabulation shape", content, tier.name, tier.text)
		}
		if !strings.Contains(tier.text, "Work on the conversational retrieval plan") {
			t.Errorf("ExtractAnswerText(%q).%s = %q, want it to contain the heading's body text", content, tier.name, tier.text)
		}
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

// TestAssembleAnswerContext_FallsBackToPrimaryTierWhenFullDoesNotFit is the
// direct regression test for the live-store bug: a chunk whose Full tier
// (What+Where+Learned) exceeds the budget must still contribute its smaller
// Primary tier (What alone) rather than being dropped outright.
func TestAssembleAnswerContext_FallsBackToPrimaryTierWhenFullDoesNotFit(t *testing.T) {
	results := []store.SearchResult{
		srWithContent(1, "obs-1", "bugfix",
			"**What**: "+strings.Repeat("a", 20)+"\n**Where**: "+strings.Repeat("b", 200)+"\n**Learned**: "+strings.Repeat("c", 200)),
	}
	// Full tier is ~430+ chars (too big); Primary tier ("What: aaa...a", ~26
	// chars) fits comfortably in 100.
	got := AssembleAnswerContext(results, 100)
	if len(got.Sources) != 1 || got.Sources[0].SyncID != "obs-1" {
		t.Fatalf("AssembleAnswerContext sources = %+v, want exactly [obs-1] via its Primary tier", got.Sources)
	}
	if strings.Contains(got.Context, "Where:") || strings.Contains(got.Context, "Learned:") {
		t.Errorf("AssembleAnswerContext.Context = %q, must contain only the Primary (What) tier when Full doesn't fit", got.Context)
	}
	if !strings.Contains(got.Context, "What:") {
		t.Errorf("AssembleAnswerContext.Context = %q, want it to contain the Primary tier", got.Context)
	}
}

// TestAssembleAnswerContext_SkipsOversizedChunkKeepsSmallerLaterOne is the
// direct regression test for the live-store bug report: when the
// TOP-RANKED chunk is too big for the budget even at its Primary tier, that
// chunk must be SKIPPED — not treated as a stop condition — so a smaller,
// lower-ranked chunk behind it still makes it into the context. The old
// token.TrimToBudget-based assembly returned a completely empty context in
// exactly this shape.
func TestAssembleAnswerContext_SkipsOversizedChunkKeepsSmallerLaterOne(t *testing.T) {
	results := []store.SearchResult{
		// Even its Primary (What alone) tier is bigger than the budget.
		srWithContent(1, "obs-huge", "bugfix", "**What**: "+strings.Repeat("x", 500)),
		// Small enough to fit entirely on its own.
		srWithContent(2, "obs-small", "preference", "**What**: short answer\n**Why**: because"),
	}
	got := AssembleAnswerContext(results, 100)
	if len(got.Sources) != 1 || got.Sources[0].SyncID != "obs-small" {
		t.Fatalf("AssembleAnswerContext sources = %+v, want exactly [obs-small] — the oversized obs-huge must be SKIPPED, not abort assembly", got.Sources)
	}
	if got.Context == "" {
		t.Fatalf("AssembleAnswerContext.Context is empty, want the smaller later chunk's text")
	}
}

// TestAssembleAnswerContext_RealisticBudgetLiveStoreRepro reproduces the
// exact failure the coordinator's live smoke test found: an ordinary
// mem_save bugfix memory (What+Why+Where+Learned, ~2.2KB joined) ranked
// first against the real 1400-char consumer budget. Before tiering, this
// returned zero sources and an empty context.
func TestAssembleAnswerContext_RealisticBudgetLiveStoreRepro(t *testing.T) {
	bugfix := "**What**: " + strings.Repeat("w", 500) +
		"\n**Why**: " + strings.Repeat("y", 500) +
		"\n**Where**: internal/mcp/answer.go" +
		"\n**Learned**: " + strings.Repeat("l", 900)
	results := []store.SearchResult{srWithContent(1, "obs-bugfix", "bugfix", bugfix)}
	got := AssembleAnswerContext(results, 1400)
	if len(got.Sources) == 0 || got.Context == "" {
		t.Fatalf("AssembleAnswerContext(maxChars=1400) = %+v, want a non-empty context via the Primary (What+Why) tier", got)
	}
}

// TestAssembleAnswerContext_DocChunkLeadParagraphFitsMoreChunksInBudget is
// the direct regression test for the measured live-store grounding bug
// (identity grounding via GET /answer measured 0.000 vs 0.625 over GET
// /search's raw top-4): a doc chunk ranked ahead that consumes most of the
// budget on its Full tier used to starve every doc chunk behind it, even
// when neither chunk was individually oversized. With docLeadParagraph
// tiering, the first chunk's Primary (header + lead paragraph) is small
// enough that the second chunk still fits.
func TestAssembleAnswerContext_DocChunkLeadParagraphFitsMoreChunksInBudget(t *testing.T) {
	firstHeader := "Document: Omnia — Architecture\nSection: Vision\n\n"
	firstLead := strings.Repeat("Omnia is a knowledge layer, not just an ingestor. ", 20) // no internal "\n\n"
	firstSecondParagraph := strings.Repeat("More unrelated architecture prose that should not be needed. ", 20)
	rankedFirstDoc := firstHeader + firstLead + "\n\n" + firstSecondParagraph

	secondDoc := "Document: omnia\nSection: (introduction)\n\n" +
		"Persistent memory for AI coding agents — local-first, one binary."

	results := []store.SearchResult{
		srWithContent(1, "obs-vision", "doc", rankedFirstDoc),
		srWithContent(2, "obs-intro", "doc", secondDoc),
	}
	// Budget computed FROM the fixtures, not a hand-picked magic number:
	// fits the FIRST doc's Primary (header+lead) plus the SECOND doc's
	// whole content, but NOT the first doc's Full tier (header+lead+second
	// paragraph) plus the second doc's content — forcing the exact
	// fallback path this test exists to cover.
	firstPrimarySize := utf8.RuneCountInString(strings.TrimSpace(firstHeader + firstLead))
	secondDocSize := utf8.RuneCountInString(secondDoc)
	budget := firstPrimarySize + secondDocSize + 10 // +10 slack, still well under Full+second
	if fullPlusSecond := utf8.RuneCountInString(rankedFirstDoc) + secondDocSize; budget >= fullPlusSecond {
		t.Fatalf("test fixture sizing is wrong: budget %d must be LESS than Full+second (%d) to exercise the fallback", budget, fullPlusSecond)
	}
	got := AssembleAnswerContext(results, budget)
	if len(got.Sources) != 2 {
		t.Fatalf("AssembleAnswerContext sources = %+v, want both doc chunks (lead-paragraph tiering should free enough budget for the second)", got.Sources)
	}
	if !strings.Contains(got.Context, "Persistent memory for AI coding agents") {
		t.Errorf("AssembleAnswerContext.Context = %q, want it to contain the second doc chunk's fact", got.Context)
	}
	if strings.Contains(got.Context, "second paragraph of unrelated architecture prose") {
		t.Errorf("AssembleAnswerContext.Context = %q, must NOT contain the first doc's second paragraph (Primary tier should have excluded it)", got.Context)
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

// TestClassifyAnswerConfidence covers the semantic-cosine-floor design
// (engram obs #2618) that replaced the post-fusion RRF two-floor design
// (engram obs #2585). See ClassifyAnswerConfidence's own doc comment for the
// measurement behind semanticFloor's default and for what off_topic can and
// cannot detect — these tests cover the FUNCTION's logic in isolation, not
// corpus-level pass/fail.
func TestClassifyAnswerConfidence(t *testing.T) {
	tests := []struct {
		name          string
		sig           AnswerConfidenceSignals
		semanticFloor float64
		want          AnswerConfidence
	}{
		{
			name:          "zero hits is none regardless of semantic score",
			sig:           AnswerConfidenceSignals{HitCount: 0, TopSemanticScore: 1, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceNone,
		},
		{
			name:          "top semantic score below floor is off_topic",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 0.30, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceOffTopic,
		},
		{
			name:          "top semantic score exactly at the floor is NOT off_topic (strictly-below only) — falls through to high",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 0.55, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
		{
			name:          "top semantic score above floor, sources assembled, nothing degraded: high",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 0.70, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
		{
			name:          "semanticFloor <= 0 disables the off_topic trigger even with a very low cosine",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 0.01, HasTopSemanticScore: true},
			semanticFloor: 0,
			want:          AnswerConfidenceHigh,
		},
		{
			name:          "nil cosine + otherwise-clean signals never produces off_topic, falls through to high",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 3, HasTopSemanticScore: false},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
		{
			name:          "nil cosine + SourcesAssembled 0 falls through to low, NOT off_topic",
			sig:           AnswerConfidenceSignals{HitCount: 3, SourcesAssembled: 0, HasTopSemanticScore: false},
			semanticFloor: 0.55,
			want:          AnswerConfidenceLow,
		},
		{
			name:          "hits exist but nothing survived extraction is low, never high",
			sig:           AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 0, TopSemanticScore: 1, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceLow,
		},
		{
			name:          "recall degraded is low even with a strong semantic score",
			sig:           AnswerConfidenceSignals{HitCount: 2, SourcesAssembled: 2, RecallDegraded: true, TopSemanticScore: 1, HasTopSemanticScore: true},
			semanticFloor: 0.55,
			want:          AnswerConfidenceLow,
		},
		{
			name: "FTS ladder step 2 WITHOUT fusion demotes high to low (soft signal, describes the actual path)",
			sig: AnswerConfidenceSignals{
				HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 1, HasTopSemanticScore: true,
				FTSRelaxed: true, FTSRelaxStep: 2, FusionRan: false,
			},
			semanticFloor: 0.55,
			want:          AnswerConfidenceLow,
		},
		{
			name: "FTS ladder step 2 WITH fusion does NOT demote — the diagnostic describes a different path than the one that produced these results",
			sig: AnswerConfidenceSignals{
				HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 1, HasTopSemanticScore: true,
				FTSRelaxed: true, FTSRelaxStep: 2, FusionRan: true,
			},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
		{
			name: "FTS ladder step 1 (stopwords only) never demotes, fusion or not",
			sig: AnswerConfidenceSignals{
				HitCount: 3, SourcesAssembled: 3, TopSemanticScore: 1, HasTopSemanticScore: true,
				FTSRelaxed: true, FTSRelaxStep: 1, FusionRan: false,
			},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
		{
			name:          "no semantic score available (sentinel-only match) with sources assembled: high, off_topic branch skipped entirely",
			sig:           AnswerConfidenceSignals{HitCount: 1, SourcesAssembled: 1, HasTopSemanticScore: false},
			semanticFloor: 0.55,
			want:          AnswerConfidenceHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyAnswerConfidence(tt.sig, tt.semanticFloor)
			if got != tt.want {
				t.Errorf("ClassifyAnswerConfidence(%+v, semanticFloor=%v) = %q, want %q", tt.sig, tt.semanticFloor, got, tt.want)
			}
		})
	}
}
