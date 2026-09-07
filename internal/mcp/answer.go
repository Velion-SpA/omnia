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
// signals the retrieval pipeline already computes (P3's "Calibrated 'I
// don't have this'" section) — never guessed, never LLM-scored.
//
// SUPERSEDES the plan's original single-threshold design (relaxation as an
// unconditional `none` trigger). MEASURED (2026-08-14) over the live store,
// `omnia eval --profile conversational --target answer`, 3 runs:
//
//	config                                    | refusal id | refusal status | confidence absence
//	relaxation -> none unconditionally        |      0.250 |          0.800 |        0.100 (pass)
//	relaxation -> low when FusionRan          |      0.000 |          0.000 |        1.000 (fail)
//
// Neither passed both targets — relaxation was the ONLY signal separating
// the absence class, and it is too blunt in both directions. The natural
// next idea, "gate on top-score strength instead," was tested directly by
// pulling the top result's raw fusion score (GET /search?envelope=1&explain=1,
// the `score_breakdown[id].fusion` field — the SAME quantity as TopScore
// below) for all 28 identity/status/absence corpus cases against this live
// store. It does NOT separate the classes: identity range [0.0164, 0.0306],
// status range [0.0164, 0.0328], absence range [0.0268, 0.0313] — absence
// sits almost entirely INSIDE the identity/status range, and several real
// identity/status cases score BELOW every absence case (e.g.
// identity-tagline-es 0.0164 vs. absence-gitlab-hallucination-es 0.0268).
// No single floor, and no PAIR of floors on this axis, can cleanly split
// them — this is not a calibration gap, it is that RRF fusion score
// deliberately discards magnitude (it is a rank-position combinator, 1/(k+
// rank) per leg) so it stays ~constant for any query where SOMETHING ranks
// near the top of both legs, which nearly every query does in a store this
// size and topically overlapping. See config.AnswerConfig's own doc for the
// two-floor sweep this file's grid produced and the recommendation that
// follows from it — TL;DR: floorA/floorB below still exist because a future
// signal WITH real magnitude (e.g. raw semantic cosine similarity, not
// currently surfaced per-item by this store's explain path) could use this
// exact two-branch shape productively; they are NOT currently claimed to
// clear both P3 targets simultaneously.
//
//   - none: HitCount == 0 (zero hits above the adaptive floor), OR
//     HasTopScore && TopScore < floorA (noneFloor).
//   - low: HasTopScore && TopScore < floorB (lowFloor) and >= floorA, OR
//     nothing survived structural extraction into a citable source (this
//     file's own added rule — see SourcesAssembled's doc: `high` must never
//     describe an empty Context), OR RecallDegraded is true, OR (soft
//     signal, see FusionRan's doc) the lexical relaxation ladder reached
//     step 2 on a retrieval that did NOT use fusion (i.e. the diagnostic
//     describes the actual path that produced these results).
//   - high: otherwise.
//
// floorA <= 0 disables the none-by-score trigger; floorB <= 0 disables the
// low-by-score trigger — same "0 means off" convention the single-threshold
// version used, preserved for operators who want confidence driven purely
// by the structural signals.
func ClassifyAnswerConfidence(sig AnswerConfidenceSignals, floorA, floorB float64) AnswerConfidence {
	if sig.HitCount == 0 {
		return AnswerConfidenceNone
	}
	if sig.HasTopScore {
		if floorA > 0 && sig.TopScore < floorA {
			return AnswerConfidenceNone
		}
		if floorB > 0 && sig.TopScore < floorB {
			return AnswerConfidenceLow
		}
	}
	if sig.SourcesAssembled == 0 {
		return AnswerConfidenceLow
	}
	if sig.RecallDegraded {
		return AnswerConfidenceLow
	}
	// Relaxation kept as a CONTRIBUTING signal (never a hard none/low
	// trigger anymore — the score floors above own that job): it can only
	// demote high->low, and only when fusion did NOT run, i.e. only when
	// answerFTSDiag's lexical-only probe actually describes the retrieval
	// path that produced these results. Under fusion, the probe measures a
	// DIFFERENT path (see FusionRan's own doc) and is noise with respect to
	// these specific results — applying it even as a soft cap there would
	// reintroduce spurious `low` verdicts on genuinely strong fused hits for
	// no evidential reason. Verified for this live store: fusion runs for
	// essentially every corpus query (embeddings+recall+vector_index all
	// enabled, Ollama reachable), so this branch is a rare-path safety net
	// (embeddings outage, degraded fallback) rather than a live lever here.
	if sig.FTSRelaxed && sig.FTSRelaxStep >= 2 && !sig.FusionRan {
		return AnswerConfidenceLow
	}
	return AnswerConfidenceHigh
}
