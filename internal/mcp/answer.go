package mcp

// answer.go — P3 (docs/conversational-retrieval-plan.md "Answer-shaped
// context endpoint"): the deterministic, server-side assembly and
// confidence-classification logic behind GET /answer.
//
// The problem this file exists to fix: Hermes' summarizeOmniaMemories pulls
// **What**/**Why** fields out of retrieved content and, for entries that
// have neither (session_summary rows, which use `## Goal` / `## Accomplished`
// headings instead), falls back to a RAW CHARACTER SLICE of the stored
// content. That is exactly how a markdown heading (`## Goal …`) reached an
// LLM prompt and produced a confabulated identity answer (the GitLab
// incident the plan opens with) — a heading fragment, sliced out of context,
// read as if it were prose.
//
// The fix is not a smarter slicer. It is the plan's explicit rule: "a chunk
// with no extractable structure is DROPPED, not truncated mid-sentence."
// ExtractAnswerText below recognizes every structured shape this store
// actually produces (mem_save's **Label**: fields, mem_session_summary's
// `## Heading` sections, and repodoc's `Document: … / Section: …` chunk
// header) and returns ok=false for anything else — AssembleAnswerContext
// then excludes that result entirely rather than guessing at a safe
// substring. This logic is meant to be the ONE place any Omnia consumer
// (starting with Hermes, via GET /answer) gets answer-shaped text from, so
// no other consumer ever has to reimplement (and re-break) it.
import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// AnswerSource cites the evidence one chunk of AssembleAnswerContext's
// output came from, using ONLY the sync ID (never Observation.ID).
//
// This is not a style preference. Verified during this plan's investigation:
// integer id 1918 names a session_summary in one replica and an unrelated
// cloud-sync investigation in another — replicas assign integer IDs
// independently, so an id-based citation is actively wrong across more than
// one Omnia instance. sync_id is generated once at write time and carried
// unchanged through every sync fan-out (design D-sync-id), so it is the only
// identifier this endpoint may cite.
type AnswerSource struct {
	SyncID    string
	Title     string
	Type      string
	UpdatedAt string
}

// AnswerConfidence is GET /answer's calibrated "do we actually have this"
// signal (P3's "Calibrated 'I don't have this'" section) — see
// ClassifyAnswerConfidence for how each value is derived.
type AnswerConfidence string

const (
	// AnswerConfidenceNone means the caller should treat this as "no
	// evidence found," not as a low-quality answer — Hermes' contract is to
	// say "no tengo eso registrado" on this value rather than let its LLM
	// fill the hole with a guess.
	AnswerConfidenceNone AnswerConfidence = "none"
	// AnswerConfidenceLow means evidence exists but is weak, degraded, or
	// (this file's own added rule) had nothing safely quotable in it.
	AnswerConfidenceLow AnswerConfidence = "low"
	// AnswerConfidenceHigh means full-quality retrieval surfaced at least
	// one strongly-scored, structurally-extractable source.
	AnswerConfidenceHigh AnswerConfidence = "high"
)

// answerContextSeparator joins each surviving chunk's extracted text in
// AssembleAnswerContext's output. Distinctive enough that it cannot
// plausibly occur inside real observation content and blur two adjacent
// chunks together — the same role internal/eval's own
// conversationalGroundingContextSeparator plays for the eval harness's
// grounding check (a deliberate, independently-maintained copy across that
// package boundary, not a shared import — see eval's own doc comment for
// why duplicating a 10-byte constant beats a production package depending
// on internal/eval).
const answerContextSeparator = "\n\n---\n\n"

// ─── Structured-field extraction ──────────────────────────────────────────

// boldOrHeadingLabelRE finds the start of every recognized "labeled
// section" marker in an observation's content: mem_save's bold-field
// convention (`**What**:`, `**Why**:`, `**Where**:`, `**Learned**:` — see
// internal/setup/setup.go's mem_save protocol text, the format every
// bugfix/decision/architecture/discovery/pattern/config/preference save
// uses) and mem_session_summary's markdown-heading convention (`## Goal`,
// `## Instructions`, `## Discoveries`, `## Accomplished`, `## Next Steps`,
// `## Relevant Files` — same source). Both are FIXED, closed vocabularies:
// this intentionally does NOT match an arbitrary `**Bold**:` phrase or an
// arbitrary `## Heading`, because either would risk carving a paraphrase or
// an unrelated document heading in half.
var boldOrHeadingLabelRE = regexp.MustCompile(
	`\*\*(?:What|Why|Where|Learned)\*\*:?` +
		`|(?m:^#{1,6}[ \t]+(?:Goal|Instructions|Discoveries|Accomplished|Next Steps|Relevant Files)[ \t]*$)`,
)

// docChunkHeaderRE matches repodoc's own structural marker — every P1
// (repodoc) chunk's content begins with exactly this two-line header
// (internal/source/repodoc/repodoc.go's buildItems: "Document: %s\nSection:
// %s\n\n") before the quoted section body. Its presence IS the "extractable
// structure" for a `doc`-type observation: the document title and heading
// path are already prefixed onto the chunk (plan P1: "so a chunk retrieved
// alone still says what it is about"), so the whole chunk is safe to quote
// as-is rather than needing field-by-field extraction.
var docChunkHeaderRE = regexp.MustCompile(`(?s)^Document: .+\nSection: .+\n\n`)

// labeledSection is one `**Label**: body` or `## Label\nbody` span found by
// extractLabeledSections.
type labeledSection struct {
	label string
	body  string
}

// extractLabeledSections splits content on every boldOrHeadingLabelRE match
// and returns one labeledSection per match whose body (the text up to the
// NEXT match, or end of content) is non-empty after trimming. Returns nil
// when content has no recognized label at all — the "no structure" case
// ExtractAnswerText treats as a drop signal.
func extractLabeledSections(content string) []labeledSection {
	locs := boldOrHeadingLabelRE.FindAllStringIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}

	sections := make([]labeledSection, 0, len(locs))
	for i, loc := range locs {
		label := strings.Trim(content[loc[0]:loc[1]], "*#: \t")
		bodyEnd := len(content)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		body := strings.TrimSpace(content[loc[1]:bodyEnd])
		if body == "" {
			continue
		}
		sections = append(sections, labeledSection{label: label, body: body})
	}
	return sections
}

// ExtractAnswerText returns the answer-shaped text obs.Content actually
// carries, and whether any was found at all. It tries, in order:
//
//  1. Labeled sections (bold-field or heading-style, extractLabeledSections)
//     — the common case for delta/decision/architecture/discovery/pattern/
//     config/preference saves AND session summaries.
//  2. The repodoc chunk header (docChunkHeaderRE) — the whole chunk is
//     already answer-shaped by construction (P1 chunking), so it is kept
//     whole rather than field-extracted.
//
// Anything matching neither returns ("", false): a freeform note, a raw
// digest, or any other unstructured content is DROPPED by
// AssembleAnswerContext rather than character-sliced — this is the direct
// fix for the confabulation this file's own doc comment describes.
func ExtractAnswerText(obs store.Observation) (string, bool) {
	content := obs.Content

	if sections := extractLabeledSections(content); len(sections) > 0 {
		parts := make([]string, 0, len(sections))
		for _, sec := range sections {
			parts = append(parts, sec.label+": "+sec.body)
		}
		return strings.Join(parts, "\n"), true
	}

	if docChunkHeaderRE.MatchString(content) {
		return strings.TrimSpace(content), true
	}

	return "", false
}

// AnswerAssembly is AssembleAnswerContext's return value: the joined,
// budgeted answer text plus one AnswerSource per chunk it was built from, in
// the same order they appear in Context.
type AnswerAssembly struct {
	Context string
	Sources []AnswerSource
}

// answerChunk pairs one result's extracted text with its citation, so
// token.TrimToBudget can size and trim by char count without re-deriving
// either from the other.
type answerChunk struct {
	text   string
	source AnswerSource
}

// AssembleAnswerContext is GET /answer's whole assembly step (P3): for each
// already-ranked result (the caller runs RankPipeline first, exactly like
// GET /search — see cmd/omnia/recall.go's buildHTTPAnswerFunc), extract its
// structured text via ExtractAnswerText, drop anything with none, then keep
// top-ranked extracted chunks whole, in order, until the next one would
// exceed maxChars — reusing internal/token.TrimToBudget's exact
// "top-N-complete, never partial" semantics ApplyTokenBudget (P0) already
// established, sized in characters (P3's own `max_chars` contract) rather
// than estimated tokens.
//
// maxChars <= 0 yields an empty AnswerAssembly (TrimToBudget's own
// documented behavior for budget <= 0) — a caller must supply a positive
// budget; see buildHTTPAnswerFunc for where the P3 default (1400, Hermes'
// own budget) is applied when the request omits max_chars.
func AssembleAnswerContext(results []store.SearchResult, maxChars int) AnswerAssembly {
	chunks := make([]answerChunk, 0, len(results))
	for _, r := range results {
		text, ok := ExtractAnswerText(r.Observation)
		if !ok {
			continue
		}
		chunks = append(chunks, answerChunk{
			text: text,
			source: AnswerSource{
				SyncID:    r.SyncID,
				Title:     r.Title,
				Type:      r.Type,
				UpdatedAt: r.UpdatedAt,
			},
		})
	}
	if len(chunks) == 0 {
		return AnswerAssembly{}
	}

	kept, _ := token.TrimToBudget(chunks, func(c answerChunk) int {
		return utf8.RuneCountInString(c.text)
	}, maxChars)

	if len(kept) == 0 {
		return AnswerAssembly{}
	}

	parts := make([]string, 0, len(kept))
	sources := make([]AnswerSource, 0, len(kept))
	for _, c := range kept {
		parts = append(parts, c.text)
		sources = append(sources, c.source)
	}
	return AnswerAssembly{Context: strings.Join(parts, answerContextSeparator), Sources: sources}
}

// ─── Confidence classification ────────────────────────────────────────────

// AnswerConfidenceSignals bundles every input ClassifyAnswerConfidence reads
// — all of them values the retrieval pipeline already computes for other
// purposes (P3's "derived, not guessed" requirement); this type adds no new
// measurement of its own, only a name for each existing signal at the point
// GET /answer needs to read it.
type AnswerConfidenceSignals struct {
	// HitCount is len(results) BEFORE assembly/extraction filtering —
	// "zero hits above the adaptive floor" (recall.Fuse's own
	// AdaptiveFloor, or a plain empty FTS5 result set) is the first `none`
	// trigger.
	HitCount int
	// TopScore is the top surviving result's un-normalized relevance —
	// RRF fusion Score for the hybrid path, negated FTS5 bm25 Rank for the
	// FTS5-only path (the SAME relevance map RankPipeline itself scores
	// against, see rank_pipeline.go) — read BEFORE RankResults'
	// recency/importance reweighting, matching "fused score" in the plan's
	// own wording. Meaningless when HasTopScore is false (the sentinel/
	// signature pre-emption lanes carry no relevance score).
	TopScore    float64
	HasTopScore bool
	// FTSRelaxed/FTSRelaxStep mirror store.SearchDiag.Relaxed/Step for a
	// dedicated lexical-only diagnostic query (see cmd/omnia/recall.go's
	// answerFTSDiag) — "the FTS relaxation ladder had to reach step 2
	// (OR-of-terms) to return anything" is the second `none` trigger,
	// independent of whether the hybrid/semantic leg found something.
	FTSRelaxed   bool
	FTSRelaxStep int
	// RecallDegraded mirrors mcp.RecallHealth.Degraded (the #226 envelope
	// GET /search already surfaces) — a `low` trigger per the plan.
	RecallDegraded bool
	// SourcesAssembled is len(AnswerAssembly.Sources) AFTER extraction and
	// budget trimming. This is NOT one of the plan's two literal `none`/
	// `low` triggers — it is this file's own added safety rule (see
	// ClassifyAnswerConfidence's doc) that `high` must never be returned
	// alongside an empty Context.
	SourcesAssembled int
}

// ClassifyAnswerConfidence derives GET /answer's confidence value from
// signals the retrieval pipeline already computes (P3's "Calibrated 'I
// don't have this'" section) — never guessed, never LLM-scored:
//
//   - none: HitCount == 0 (zero hits above the adaptive floor), OR the FTS
//     relaxation ladder had to reach step 2 to return anything at all.
//   - low: hits exist but nothing survived structural extraction into a
//     citable source (this file's own added rule — see SourcesAssembled's
//     doc: `high` must never describe an empty Context), OR
//     RecallDegraded is true, OR the top result's fused score is below
//     threshold.
//   - high: otherwise — a strongly-scored, non-degraded result set with at
//     least one citable, structurally-extracted source.
//
// threshold is the plan's "calibrated threshold" (P3: "must be calibrated
// against the eval corpus, not picked" — see config.AnswerConfig.
// ConfidenceThreshold's own doc for the KNOWN GAP: that calibration run has
// not actually been completed yet, and the shipped default is a reasoned
// placeholder, not a measured one). A zero/negative threshold disables the
// score-based `low`
// trigger entirely (every non-degraded, structurally-extracted hit reads as
// `high`) — useful for an operator who wants confidence driven purely by
// the two structural signals above.
func ClassifyAnswerConfidence(sig AnswerConfidenceSignals, threshold float64) AnswerConfidence {
	if sig.HitCount == 0 {
		return AnswerConfidenceNone
	}
	if sig.FTSRelaxed && sig.FTSRelaxStep >= 2 {
		return AnswerConfidenceNone
	}
	if sig.SourcesAssembled == 0 {
		return AnswerConfidenceLow
	}
	if sig.RecallDegraded {
		return AnswerConfidenceLow
	}
	if threshold > 0 && sig.HasTopScore && sig.TopScore < threshold {
		return AnswerConfidenceLow
	}
	return AnswerConfidenceHigh
}
