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
//
// A second, related bug surfaced during the first live-store smoke test
// (post-calibration-attempt review): at the REAL consumer budget (1400
// chars), `GET /answer` came back with zero sources and an empty context
// even though five of the top six results extracted cleanly. Cause: joining
// every recognized label (What+Why+Where+Learned) made a single ordinary
// bugfix memory ~2.2KB — bigger than the whole budget — so the old
// "top-N-complete" trim kept zero chunks and stopped, never even trying the
// smaller results behind it. ExtractAnswerText now returns a TIERED result
// (see AnswerText) and AssembleAnswerContext tries the full tier, then the
// primary tier alone, then SKIPS (not aborts) a chunk that fits neither —
// see AssembleAnswerContext's own doc for why this deliberately does not
// reuse internal/token.TrimToBudget anymore.
//
// A third bug (2026-08-14): identity grounding via GET /answer measured
// 0.000 on the live store, while grounding over GET /search's raw top-4
// measured 0.625 on the SAME corpus — evidence was being retrieved and then
// lost during assembly. Traced to TWO separate assembly-level causes, both
// fixed:
//
//  1. docChunkHeaderRE had a latent greedy-regex bug ((?s) dot-matches-
//     newline plus a greedy `.+`): FindString on a multi-paragraph doc
//     chunk matched all the way to the LAST blank line in the chunk, not
//     the first one after the header. Harmless as long as the regex was
//     only used for a boolean MatchString check (the whole chunk was kept
//     regardless) — became a real bug once docLeadParagraph needed
//     FindString to isolate just the header. Fixed by restricting the
//     Document/Section lines to `[^\n]+` (see docChunkHeaderRE's own doc).
//  2. Doc chunks had NO Primary/Full split at all (Primary == Full, the
//     WHOLE chunk) — fixed via docLeadParagraph (header + lead paragraph
//     as Primary). But tiering alone was not enough: AssembleAnswerContext
//     originally preferred each chunk's Full tier whenever it fit, so ONE
//     moderately-sized higher-ranked chunk (a 1235-char doc section that
//     does not contain the queried fact) still fully consumed its own Full
//     tier's worth of budget, crowding out a lower-ranked, smaller,
//     ACTUALLY-relevant chunk. Fixed by switching AssembleAnswerContext to
//     a breadth-first two-pass strategy (see its own doc).
//
// Both fixes measurably increased the number of distinct sources GET
// /answer surfaces per identity query (roughly 1 -> 3-7 on the live
// store). Grounding STILL measures 0.000 after both fixes, and the
// remaining cause is OUT OF THIS FILE'S SCOPE: the one README chunk that
// literally contains all four identity corpus facts ("Features," a single
// undivided bullet list with no internal paragraph breaks — so its OWN
// Primary tier alone runs ~1400-1500 chars, close to the whole budget)
// either does not rank inside the retrieval candidate window at all for
// several query phrasings, or ranks around position 9 of 10-25 — behind
// enough other extraction-successful candidates that no assembly ordering
// strategy can be expected to reliably fit it. This is a retrieval/ranking
// problem (RankPipeline's scoring, type-lens, or intent-routing for
// identity-classified queries — none of which this file owns), not an
// assembly one; see the live diagnosis this doc comment summarizes for the
// full per-query evidence trail.
import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/velion/omnia/internal/store"
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
	// AnswerConfidenceNone means retrieval returned literally nothing —
	// HitCount == 0 (zero hits above the adaptive floor, or a genuinely
	// empty FTS5 result set). Consumer-facing framing: "No tengo nada sobre
	// eso."
	//
	// This is a NARROWER claim than the value used to make. The prior
	// design tied `none` to a false-premise/absence detector built on the
	// post-fusion RRF score (see ClassifyAnswerConfidence's history for why
	// that axis was measured to carry no usable magnitude, engram obs
	// #2585) — that overclaimed, and the wording this value used to justify
	// ("no tengo eso registrado", implying the store was checked and found
	// nothing ABOUT the topic) has been removed along with it. `none` now
	// says exactly one thing: retrieval found zero results. It says nothing
	// about whether the store has related-but-unretrieved content, and
	// nothing about whether the question's premise is false — see
	// AnswerConfidenceOffTopic for the (also limited) signal that now owns
	// the "found something, but not about this" case.
	AnswerConfidenceNone AnswerConfidence = "none"
	// AnswerConfidenceOffTopic means retrieval returned results, but the
	// top-ranked one's raw semantic cosine similarity is below the
	// configured floor (config.AnswerConfig.SemanticCosineFloor) — the
	// store has content, it just doesn't appear to be about this question.
	// Consumer-facing framing: "Eso está fuera de lo que sé."
	//
	// MEASURED (2026-08-14, engram obs #2618), conversational eval corpus,
	// live store: this reliably catches GENUINELY off-topic questions —
	// salary and unrelated-product-usage questions measured top-hit cosine
	// 0.2963-0.3826, cleanly below every identity ([0.6371, 0.7454]) and
	// status ([0.4362, 0.7443]) case in the corpus.
	//
	// It does NOT, and structurally CANNOT, catch a false premise phrased
	// in the store's own vocabulary. Concrete counter-example from the same
	// measurement: "is Omnia a GitLab command-line client" (false — Omnia
	// is not a GitLab CLI) scored top-hit cosine 0.691, ABOVE the lowest
	// legitimate identity question in the corpus (0.6371). Cosine measures
	// TOPICALITY — how much a question's embedding overlaps the embedding
	// of what got retrieved — not ANSWERHOOD. A false claim built entirely
	// out of the store's own vocabulary ("Omnia", "deploy", "Kubernetes")
	// embeds close to the store's real content about deployment precisely
	// BECAUSE it borrows that vocabulary; nothing about cosine similarity
	// distinguishes "this text is relevant to the claim" from "this text
	// confirms the claim." Detecting a false premise is an ENTAILMENT
	// problem (does the retrieved evidence support or contradict the
	// specific claim in the question), not a SIMILARITY problem — no
	// embedding-distance threshold, on this axis or any other, resolves
	// that. `off_topic` is a real, useful, narrower signal than the false-
	// premise detector the endpoint originally aimed for; it must not be
	// read as that detector.
	AnswerConfidenceOffTopic AnswerConfidence = "off_topic"
	// AnswerConfidenceLow means evidence exists but is weak, degraded, or
	// (this file's own added rule) had nothing safely quotable in it. It no
	// longer overlaps AnswerConfidenceOffTopic's case — a top result below
	// the semantic floor is classified off_topic, not low.
	AnswerConfidenceLow AnswerConfidence = "low"
	// AnswerConfidenceHigh means full-quality retrieval surfaced at least
	// one strongly-scored, structurally-extractable source. Unchanged.
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
//
// [^\n]+ (a SINGLE physical line per title/section, matching this file's
// own "single physical line" assumption elsewhere), NOT `(?s).+` — the
// previous version's dot-matches-newline greedy `.+\n\n` only ever mattered
// for the boolean MatchString check ExtractAnswerText used it for, where a
// greedy match past the intended header was harmless (the whole chunk was
// kept regardless). It stopped being harmless once docLeadParagraph started
// calling FindString to isolate JUST the header: on any chunk with a SECOND
// "\n\n" later in the body (i.e. more than one paragraph — the exact shape
// docLeadParagraph exists to split), the greedy `.+` matched all the way to
// the LAST "\n\n" in the whole chunk instead of the first one after
// "Section: …", silently swallowing the entire body into "header" and
// leaving docLeadParagraph nothing to find a paragraph break in. Caught by
// TestAssembleAnswerContext_DocChunkLeadParagraphFitsMoreChunksInBudget.
var docChunkHeaderRE = regexp.MustCompile(`^Document: [^\n]+\nSection: [^\n]+\n\n`)

// labeledSection is one `**Label**: body` or `## Label\nbody` span found by
// extractLabeledSections.
type labeledSection struct {
	label string
	body  string
}

// primaryAnswerLabels is the tiering rule behind AnswerText: which of
// boldOrHeadingLabelRE's fixed label vocabulary actually answers a
// question, versus which is provenance/gotcha material that is nice to
// include but never required.
//
//   - What, Why (mem_save): the fix itself and the motivation for it. Where
//     (a file path) and Learned (a gotcha) describe the fix, they don't
//     answer "what happened."
//   - Goal, Accomplished (mem_session_summary): what the session set out to
//     do and what it did. Instructions, Discoveries, Next Steps, and
//     Relevant Files are process/handoff notes for the NEXT session, not
//     answer material for a question asked now.
//
// This split exists because joining every label unconditionally made
// ordinary bugfix memories (What+Why+Where+Learned) run ~2.2KB — bigger
// than GET /answer's real 1400-char consumer budget on their own — which
// silently produced an EMPTY assembled context. See answer.go's package doc
// comment for the live-store repro.
var primaryAnswerLabels = map[string]bool{
	"What":         true,
	"Why":          true,
	"Goal":         true,
	"Accomplished": true,
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

// AnswerText is ExtractAnswerText's tiered result for one observation.
//
// AssembleAnswerContext uses this to fit a budget without ever slicing a
// chunk mid-sentence (the confabulation fix stays intact — both fields are
// always used WHOLE or not at all): it tries Full first, falls back to the
// smaller Primary when Full doesn't fit the remaining budget, and skips the
// chunk entirely only when neither tier fits.
type AnswerText struct {
	// Primary is the minimal answer-shaped tier: only labels in
	// primaryAnswerLabels (What/Why, or Goal/Accomplished for session
	// summaries). For a repodoc chunk (no labeled sections at all),
	// Primary equals Full — the whole chunk is already minimal by P1
	// construction. If a labeled observation has NO primary-tier label at
	// all (content used only Where/Learned-style fields, which mem_save's
	// format does not actually produce today since What is mandatory —
	// but nothing enforces that at read time), Primary falls back to Full
	// rather than silently discarding the chunk's only content.
	Primary string
	// Full is every recognized label joined, in original order — the
	// complete extraction, same shape ExtractAnswerText returned before
	// this file's tiering fix.
	Full string
}

// tieredTextFromSections builds an AnswerText from extractLabeledSections'
// output. See primaryAnswerLabels for the primary/secondary split.
func tieredTextFromSections(sections []labeledSection) AnswerText {
	fullParts := make([]string, 0, len(sections))
	primaryParts := make([]string, 0, len(sections))
	for _, sec := range sections {
		line := sec.label + ": " + sec.body
		fullParts = append(fullParts, line)
		if primaryAnswerLabels[sec.label] {
			primaryParts = append(primaryParts, line)
		}
	}
	full := strings.Join(fullParts, "\n")
	primary := strings.Join(primaryParts, "\n")
	if primary == "" {
		primary = full
	}
	return AnswerText{Primary: primary, Full: full}
}

// docLeadParagraph builds a doc chunk's Primary tier: the chunk's own
// "Document: …\nSection: …\n\n" header (header is the FindString match of
// docChunkHeaderRE, always ending in the blank line before the body) plus
// the body's first paragraph — the text up to the next blank line
// (`\n\n`), or the whole body if it has no internal paragraph break at all.
//
// MEASURED (2026-08-14): identity grounding via GET /answer measured 0.000
// on the live store while grounding over GET /search's raw top-4 measured
// 0.625 on the SAME corpus. Root cause traced directly (not inferred): doc
// chunks previously had NO Primary/Full split (Primary == Full, the WHOLE
// chunk) — unlike mem_save/session_summary content, which the What/Why
// tiering already shrinks. A repodoc chunk runs 900-2000+ chars including
// its own markdown/HTML boilerplate (badges, headers), so ONE ordinary-sized
// doc chunk (e.g. 1235 chars) that happens to rank first among doc-type
// candidates consumes nearly the WHOLE 1400-char budget by itself — leaving
// every SMALLER doc chunk behind it, including the one that actually
// carries the queried fact, without enough remaining budget to fit even
// though nothing here is individually oversized. Traced end to end for
// "¿dónde guarda Omnia sus datos?": rank-1 candidate (a 7468-char bugfix
// writeup) correctly skips both tiers per AssembleAnswerContext's
// skip-not-abort rule; rank-2 (the "Architecture > Vision" doc chunk, 1235
// chars — which does NOT contain the expected fact) fits whole and consumes
// 1235 of 1400; rank-3 (the "(introduction)" doc chunk, 1943 chars — which
// DOES contain it) has only ~165 chars of budget left and is skipped. A
// doc chunk's lead paragraph is the same kind of safe, complete, non-sliced
// unit the What/Why split already uses for mem_save content — never a
// mid-sentence cut, just a smaller complete unit — so more distinct doc
// chunks now fit the same budget.
func docLeadParagraph(content string) AnswerText {
	whole := strings.TrimSpace(content)
	header := docChunkHeaderRE.FindString(content)
	body := content[len(header):]
	primary := whole
	if idx := strings.Index(body, "\n\n"); idx >= 0 {
		primary = strings.TrimSpace(header + body[:idx])
	}
	return AnswerText{Primary: primary, Full: whole}
}

// ExtractAnswerText returns the answer-shaped text obs.Content actually
// carries, tiered, and whether any was found at all. It tries, in order:
//
//  1. Labeled sections (bold-field or heading-style, extractLabeledSections)
//     — the common case for delta/decision/architecture/discovery/pattern/
//     config/preference saves AND session summaries. Tiered via
//     tieredTextFromSections.
//  2. The repodoc chunk header (docChunkHeaderRE) — already answer-shaped
//     by construction (P1 chunking), tiered via docLeadParagraph (header +
//     lead paragraph as Primary, the whole chunk as Full) — see that
//     function's doc for the measured grounding-loss bug this fixes.
//
// Anything matching neither returns (AnswerText{}, false): a freeform note,
// a raw digest, or any other unstructured content is DROPPED by
// AssembleAnswerContext rather than character-sliced — this is the direct
// fix for the confabulation this file's own doc comment describes.
func ExtractAnswerText(obs store.Observation) (AnswerText, bool) {
	content := obs.Content

	if sections := extractLabeledSections(content); len(sections) > 0 {
		return tieredTextFromSections(sections), true
	}

	if docChunkHeaderRE.MatchString(content) {
		return docLeadParagraph(content), true
	}

	return AnswerText{}, false
}

// AnswerAssembly is AssembleAnswerContext's return value: the joined,
// budgeted answer text plus one AnswerSource per chunk it was built from, in
// the same order they appear in Context.
type AnswerAssembly struct {
	Context string
	Sources []AnswerSource
}

// answerChunk pairs one result's tiered extracted text with its citation.
type answerChunk struct {
	text   AnswerText
	source AnswerSource
}

// AssembleAnswerContext is GET /answer's whole assembly step (P3): for each
// already-ranked result (the caller runs RankPipeline first, exactly like
// GET /search — see cmd/omnia/recall.go's buildHTTPAnswerFunc), extract its
// tiered text via ExtractAnswerText, drop anything with none, then fit
// chunks into maxChars in TWO PASSES, both walking candidates IN RANK
// ORDER:
//
//  1. Breadth pass: place each chunk's smaller Primary tier (What/Why,
//     Goal/Accomplished, or a doc chunk's header+lead paragraph — see
//     AnswerText's own doc). A chunk whose Primary alone doesn't fit the
//     remaining budget is SKIPPED — not a stop condition — so a later,
//     smaller chunk still gets a chance (same divergence from internal/
//     token.TrimToBudget's "stop at first miss" contract as before: P0's
//     injection budget is a flat list of comparably-sized items where
//     preserving end-to-end rank order matters more than salvaging a later
//     item; /answer's chunks vary wildly in size, so one oversized
//     top-ranked chunk must not blank out every smaller result behind it).
//  2. Depth pass: with whatever budget is left over, upgrade already-placed
//     chunks from Primary to Full, IN RANK ORDER (the highest-ranked chunk
//     gets first claim on leftover budget) — same fit-or-skip-this-upgrade
//     rule, never a stop condition.
//
// Breadth-first (not "prefer each chunk's richest tier that fits, in rank
// order," this function's original shape) is a deliberate choice, not the
// simpler alternative: MEASURED on the live store, prefer-Full-first
// produced grounding=0.000 on the identity corpus (vs. 0.625 for GET
// /search's raw top-4) even after doc chunks gained Primary/Full tiering
// (docLeadParagraph) — because ONE moderately-sized higher-ranked chunk
// (e.g. a 1235-char doc section that happens to rank #2 but does not
// contain the queried fact) still fully consumes its Full tier's worth of
// budget under prefer-Full-first, crowding out a lower-ranked, smaller,
// ACTUALLY-relevant chunk (e.g. a README "Features" bullet list ranked
// #9) even though nothing here is individually oversized. Breadth-first
// maximizes the number of distinct citable sources that make it in before
// spending any surplus on completeness — the right default for an
// assembled-context endpoint whose whole point is to cite MULTIPLE related
// memories, not to quote one document verbatim. A chunk is still NEVER
// partially included in either tier in either pass — that invariant is
// what keeps this the confabulation fix, not a slicer with extra steps.
//
// maxChars <= 0 yields an empty AnswerAssembly — a caller must supply a
// positive budget; see buildHTTPAnswerFunc for where the P3 default (1400,
// Hermes' own budget) is applied when the request omits max_chars.
func AssembleAnswerContext(results []store.SearchResult, maxChars int) AnswerAssembly {
	if maxChars <= 0 {
		return AnswerAssembly{}
	}

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

	// placedIdx[i] indexes into chunks for the i-th placed item, in the
	// order they were placed (== rank order, since both passes walk
	// candidates in rank order and never reorder).
	placedIdx := make([]int, 0, len(chunks))
	placedText := make([]string, 0, len(chunks))
	used := 0

	// Pass 1 (breadth): Primary tier only, skip-not-abort.
	for i, c := range chunks {
		size := utf8.RuneCountInString(c.text.Primary)
		if used+size > maxChars {
			continue
		}
		placedIdx = append(placedIdx, i)
		placedText = append(placedText, c.text.Primary)
		used += size
	}
	if len(placedIdx) == 0 {
		return AnswerAssembly{}
	}

	// Pass 2 (depth): upgrade Primary -> Full with leftover budget, in the
	// same rank order pass 1 placed them in (== candidates' original rank
	// order, since pass 1 never reorders).
	for i, ci := range placedIdx {
		full := chunks[ci].text.Full
		if full == placedText[i] {
			continue // no secondary tier to upgrade to (Primary == Full)
		}
		fullSize := utf8.RuneCountInString(full)
		delta := fullSize - utf8.RuneCountInString(placedText[i])
		if used+delta > maxChars {
			continue
		}
		placedText[i] = full
		used += delta
	}

	sources := make([]AnswerSource, len(placedIdx))
	for i, ci := range placedIdx {
		sources[i] = chunks[ci].source
	}
	return AnswerAssembly{Context: strings.Join(placedText, answerContextSeparator), Sources: sources}
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
	// TopSemanticScore is the top surviving result's raw semantic cosine
	// similarity (recall.Result.SemanticScore, see cmd/omnia/recall.go's
	// wiring) — NOT the fused RRF score (that axis, engram obs #2585,
	// carries no usable magnitude). HasTopSemanticScore false means this
	// result had NO cosine at all (23.1% of results, measured — arrived via
	// the lexical leg alone, below AdaptiveFloor): that is a MISSING signal,
	// not a low one, and must never be coerced to 0 or treated as evidence
	// of off-topicality — see ClassifyAnswerConfidence's own doc for how
	// this file handles that case.
	TopSemanticScore    float64
	HasTopSemanticScore bool
	// FTSRelaxed/FTSRelaxStep mirror store.SearchDiag.Relaxed/Step for a
	// dedicated lexical-only diagnostic query (see cmd/omnia/recall.go's
	// answerFTSDiag). Read ONLY together with FusionRan below — see
	// ClassifyAnswerConfidence for why this signal cannot speak for a hybrid
	// retrieval on its own.
	FTSRelaxed   bool
	FTSRelaxStep int
	// FusionRan reports whether the results this signal set describes came
	// from hybrid lexical+semantic fusion (recall.Service) rather than the
	// lexical-only FTS5 path.
	//
	// It exists because FTSRelaxed/FTSRelaxStep are measured by a SEPARATE,
	// lexical-only probe. When fusion ran, that probe describes a different
	// query path than the one that actually produced the results: the
	// semantic leg can find the answer perfectly well while the lexical leg
	// needed relaxing, so lexical relaxation says nothing about whether
	// evidence was found. Measured: treating relaxation as an unconditional
	// `none` trigger produced false_refusal rates of 0.250 on identity and
	// 0.800 on status questions — 8 of 10 status cases and 2 of 8 identity
	// cases were reported as "no evidence" while carrying one to three real,
	// structurally-extracted sources.
	//
	// As of the two-floor design (ClassifyAnswerConfidence's own doc),
	// FusionRan no longer gates a `none` trigger — score floors own that job
	// now — but it still gates relaxation's demoted role as a soft `low`
	// signal, for the same reason: the diagnostic only describes the actual
	// retrieval path when fusion did NOT run.
	FusionRan bool
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
// signals the retrieval pipeline already computes — never guessed, never
// LLM-scored.
//
// THIS DESIGN (2026-08-14, engram obs #2618) replaces the prior post-fusion
// RRF-score two-floor design (engram obs #2585) with a floor over the top
// result's raw SEMANTIC COSINE similarity (recall.Result.SemanticScore).
// The RRF axis was measured to carry no usable magnitude at all — it is a
// rank-position combinator (1/(k+rank) per leg), not a similarity score, so
// it stays ~constant for any query where something ranks near the top of
// both legs (nearly every query, in a store this size and topically
// overlapping) — see git history for that measurement's full grid. Cosine
// DOES carry real magnitude, and was measured directly against the
// conversational eval corpus on the live store:
//
//	identity  min=0.6371 max=0.7454 (n=8)
//	status    min=0.4362 max=0.7443 (n=10)
//	absence (n=10), top-hit cosine per case — BIMODAL:
//	  genuinely off-topic (salary, unrelated product usage): 0.2963-0.3826
//	  false premise, in-vocabulary (GitLab CLI, K8s deploy):  0.5687-0.7115
//
// The off-topic cluster sits well below the identity/status range —
// SemanticCosineFloor (config.AnswerConfig, default 0.55) is chosen to
// separate it cleanly. The false-premise cluster does NOT separate from
// identity/status — it overlaps them, because cosine measures TOPICALITY
// (embedding overlap with the question) not ANSWERHOOD (does the retrieved
// text actually support or refute the specific claim). That is a structural
// limit of a similarity signal, not a threshold-tuning gap: no floor on
// this axis, and no floor on any single-vector-similarity axis, can catch a
// false claim built from the store's own vocabulary. See
// AnswerConfidenceOffTopic's own doc for the concrete GitLab counter-example
// and config.AnswerConfig.SemanticCosineFloor's doc for the full floor
// derivation.
//
//   - none: HitCount == 0 (zero hits above the adaptive floor). This is now
//     the ONLY meaning `none` carries — see AnswerConfidenceNone's own doc
//     for why the prior wording overclaimed.
//   - off_topic: HasTopSemanticScore && TopSemanticScore < semanticFloor
//     (strictly below — a score exactly AT the floor is not off_topic).
//     HasTopSemanticScore == false NEVER triggers this branch (see below).
//   - low: nothing survived structural extraction into a citable source
//     (this file's own added rule — see SourcesAssembled's doc: `high` must
//     never describe an empty Context), OR RecallDegraded is true, OR (soft
//     signal, see FusionRan's doc) the lexical relaxation ladder reached
//     step 2 on a retrieval that did NOT use fusion (i.e. the diagnostic
//     describes the actual path that produced these results).
//   - high: otherwise.
//
// semanticFloor <= 0 disables the off_topic trigger entirely — same "0
// means off" convention the removed fused-score floors used.
//
// Nil-cosine rule (deliberate, tested explicitly): when HasTopSemanticScore
// is false, the off_topic branch NEVER fires, regardless of semanticFloor —
// it falls straight through to the normal low/high logic below, exactly as
// if semanticFloor were disabled for this result. "Absence of evidence
// about topicality is not evidence of off-topicality." This matters beyond
// the single 23.1%-of-results case where one hit's cosine is missing: when
// hybrid recall is unavailable at all (no recall.Service configured, or a
// mid-query fallback to plain FTS5 — see recallOrFTSSearchWithRelevance),
// EVERY result in that response has no cosine, because cosine is a
// hybrid-recall byproduct with nothing to fall back to. Forcing a nil
// cosine to `low` would therefore permanently cap the ENTIRE non-hybrid
// operating mode at `low` — a much bigger behavior change than this task
// scopes (it would make `high` unreachable any time embeddings are down or
// unconfigured, independent of retrieval quality). Falling through instead
// keeps non-hybrid mode exactly as capable as it was before this signal
// existed: the off_topic axis just isn't evaluated for it.
func ClassifyAnswerConfidence(sig AnswerConfidenceSignals, semanticFloor float64) AnswerConfidence {
	if sig.HitCount == 0 {
		return AnswerConfidenceNone
	}
	if semanticFloor > 0 && sig.HasTopSemanticScore && sig.TopSemanticScore < semanticFloor {
		return AnswerConfidenceOffTopic
	}
	if sig.SourcesAssembled == 0 {
		return AnswerConfidenceLow
	}
	if sig.RecallDegraded {
		return AnswerConfidenceLow
	}
	// Relaxation kept as a CONTRIBUTING signal (never a hard none/low
	// trigger — that is what the off_topic/none branches above own now): it
	// can only demote high->low, and only when fusion did NOT run, i.e.
	// only when answerFTSDiag's lexical-only probe actually describes the
	// retrieval path that produced these results. Under fusion, the probe
	// measures a DIFFERENT path (see FusionRan's own doc) and is noise with
	// respect to these specific results — applying it even as a soft cap
	// there would reintroduce spurious `low` verdicts on genuinely strong
	// fused hits for no evidential reason.
	if sig.FTSRelaxed && sig.FTSRelaxStep >= 2 && !sig.FusionRan {
		return AnswerConfidenceLow
	}
	return AnswerConfidenceHigh
}
