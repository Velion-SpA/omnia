// Package intent classifies a conversational query into one of a small,
// fixed set of question KINDS (docs/conversational-retrieval-plan.md, "P2 —
// Query intent classification and routing"), so a caller can route each
// kind to a different ranking profile instead of forcing every question
// through one relevance-only ranked list.
//
// The motivating problem (plan P2): an identity question ("qué es Omnia" /
// "what is Omnia") wants a definition and does not care about recency; a
// status question ("cómo va Workly" / "how is Workly going") wants the
// latest state and recency is decisive; an open-items question wants claims
// that are still open. One ranking cannot serve all three, so the plan
// calls for per-intent ranking hints (see profile.go) driven by a cheap
// classifier (this file).
//
// Classification is LEXICAL + PATTERN matching only: no LLM, no network, no
// I/O. Budget is < 1 ms for a query up to ~200 characters (see
// bench_test.go) — the plan explicitly rejects any LLM round-trip inside
// retrieval for classification, since it is ~1% of the 100 ms retrieval
// budget, not the 10 ms the brief allowed.
//
// Bilingual ES/EN from day one: the real consumer speaks Spanish, and
// internal/recall/bilingual_test.go already treats cross-lingual recall as
// a first-class concern for this codebase. Every cue table below carries
// both languages, not English-plus-an-afterthought.
//
// Safety property (plan P2, explicit): "Unclassified -> today's behaviour,
// byte-for-byte. The classifier can only ever improve a query it
// recognises." Unknown is the safe default and the fallback for anything
// not confidently matched — a MISROUTED query is worse than an unrouted
// one, because an unrouted query still gets today's relevance-only
// ranking, while a misrouted one actively distorts it (e.g. zeroing
// recency for a status question). Classify never guesses past its
// confidence threshold; ProfileFor(Unknown) returns the zero-value
// RoutingProfile, i.e. no overrides at all.
//
// This package is a dependency-free leaf, deliberately mirroring
// internal/recall's own precedent (see recall.go's package doc): it
// imports only the standard library, so any caller — internal/mcp,
// internal/server, a future Hermes-side client — can depend on it without
// pulling in config/store/mcp coupling. internal/config.RankingConfig and
// internal/mcp/type_lens.go (ApplyTypeLens) are read-only reference points
// for profile.go's field shapes (see that file's doc comment); neither is
// imported here.
package intent

import "regexp"

// Intent is one of the seven question kinds the plan's routing table
// defines. It is a plain string type (not an int enum) so classifier
// output, eval fixtures, and any future JSON/log serialization stay
// human-readable without a String() method to keep in sync.
type Intent string

const (
	// Identity: "qué es" / "háblame sobre" / "what is" / "tell me about".
	// Wants a definition; does not care about recency.
	Identity Intent = "identity"
	// Status: "cómo va" / "cómo está" / "how is X going". Wants the latest
	// state; recency is decisive.
	Status Intent = "status"
	// Delta: "cómo arreglamos" / "how did we fix" / a raw error string.
	// Already served correctly by the existing signature/exact-match lane.
	Delta Intent = "delta"
	// OpenItems: "qué falta" / "qué queda" / "próximos pasos" / "what's
	// next". Wants claims that are still OPEN — belongs in P4's claim lane,
	// which does not exist yet.
	OpenItems Intent = "open_items"
	// Rationale: "por qué elegimos" / "why did we choose". Wants the
	// decision/architecture record, not the freshest memory.
	Rationale Intent = "rationale"
	// CrossProject: "en qué estoy bloqueado" / "esta semana" / "across
	// everything". Wants a view spanning multiple projects — belongs in
	// P5's multi-project mode, which does not exist yet.
	CrossProject Intent = "cross_project"
	// Unknown is the safe default: no cue matched confidently, or nothing
	// matched at all. Callers get today's behaviour, byte-for-byte.
	Unknown Intent = "unknown"
)

// DefaultConfidenceThreshold is the confidence a matched cue must clear for
// Classify to commit to that intent rather than falling back to Unknown.
// Every cue currently in signals (see below) is calibrated well above this
// bar (0.8-0.95); the one deliberately weaker signal — Delta's raw-error-
// string heuristic, 0.75 — sits below it on purpose, so
// ClassifyWithThreshold's threshold mechanism has a real case to exercise
// (see intent_test.go's threshold test) and callers who want stricter
// precision than the package default can raise the bar without a code
// change.
//
// 0.75 was chosen, not 0.8, because the raw-error-string heuristic (a
// composite of several independent, structurally distinctive patterns —
// stack-trace shapes, `panic:`, `FooError:` — see the errorPattern comment
// below) is still a strong, low-false-positive signal in practice; it is
// marked "weaker" only relative to an exact bilingual phrase match, not in
// an absolute sense.
const DefaultConfidenceThreshold = 0.6

// Classification is Classify's result: the routed intent, a confidence
// score in [0,1], and the human-readable cue label that produced it (empty
// for Unknown when nothing matched at all). Confidence is always the raw
// score of the best signal that matched — even when that score fell below
// the threshold and Intent was downgraded to Unknown — so a caller can
// apply its own, different threshold without re-running Classify (see
// ClassifyWithThreshold).
type Classification struct {
	Intent     Intent
	Confidence float64
	Cue        string
}

// signal is one lexical/pattern rule: if pattern matches the query, it
// votes for intent with the given confidence. cue is a short human-readable
// label (not the regex source) used in eval output and test failure
// messages.
type signal struct {
	pattern    *regexp.Regexp
	intent     Intent
	confidence float64
	cue        string
}

// signals is the classifier's entire rule table (design mirrors
// internal/mcp/type_lens.go's lensSignals: an ordered, hand-curated,
// eval-tunable rule list — "not a claim of correct classification," an
// accepted heuristic posture). Unlike lensSignals' first-match-wins scan,
// Classify below evaluates every rule and keeps the HIGHEST-confidence
// match (ties broken by table order) — this package is scoring a
// confidence, not just picking a category, so "first plausible rule" is
// the wrong tie-break when two rules of different specificity both match
// the same query (see Classify's doc comment for a worked example).
//
// Table order still matters for readability and for the tie-break, so
// rules are grouped by intent below, most distinctive first. Identity's
// two GENERIC phrase rules are deliberately given the LOWEST confidence
// (0.8) of any phrase rule: "qué es" / "what is" are the broadest, most
// collision-prone cues in the whole table (e.g. "what is the status of X"
// legitimately contains "what is" as a substring), so every other intent's
// more specific phrase must be able to outscore identity when both match.
// This is the concrete mechanism behind the plan's own worry that
// identity-class mis-routing was the original motivating bug (P1/P2) — the
// fix is not "match identity first," it is "match identity LAST among
// confident rules."
//
// Identity ALSO has four narrower capability/property sub-cues at 0.85
// (added after a blind cross-validation run against
// internal/eval/testdata/conversational_cases.json found identity
// precision at 0.400 — see each block's own comment below for the specific
// false positives/negatives that drove it). 0.85 keeps them BELOW every
// other intent's 0.9+ cues (so "cómo va Workly" still resolves to Status,
// not Identity, even though it also matches identity's "cómo <verbo>
// <Entidad>" shape) while keeping them ABOVE the two fully-generic 0.8
// rules, per this table's own specificity principle: a narrower cue must
// outscore a broader one of the same intent, not just of a different one.
//
// GOTCHA (same one internal/mcp/type_lens.go's lensSignals comment already
// flags for the `arregl` stem): Go's RE2 \b is an ASCII-only word
// boundary — it treats á/é/í/ó/ú/ñ as NON-word runes. A trailing \b placed
// immediately after an alternative that can end in one of those (e.g.
// "eligió", "está", "arregló") therefore checks a nonword-to-nonword
// transition against the following space/punctuation/end-of-string and
// NEVER matches — silently costing recall on exactly the conjugated forms
// this classifier most needs (verified empirically: it dropped
// "cómo está"/"se arregló"/"se eligió" from their respective intents
// during this package's own fixture testing). Wherever an alternative can
// end in an accented vowel, the boundary below is spelled out as
// `(?:[^\p{L}0-9_]|$)` instead of `\b` — RE2 does support Unicode
// character classes like \p{L} outside of \b, so this consumes one
// trailing "not a letter/digit/underscore" rune (or matches end-of-string)
// instead of relying on \b's ASCII blind spot. Leading boundaries are
// unaffected and stay plain \b: every cue in this table starts with a
// plain ASCII letter (qué/cómo/por/what/how/...), so \b's ASCII-only
// definition is correct there.
var signals = []signal{
	// --- rationale (0.95): "por qué elegimos" / "why did we choose" -------
	{
		pattern:    regexp.MustCompile(`(?i)\bpor ?qu[eé] (elegimos|decidimos|usamos|escogimos|preferimos)\b|\bpor ?qu[eé] se (eligi[oó]|decidi[oó]|escogi[oó])(?:[^\p{L}0-9_]|$)|\bcu[aá]l es la raz[oó]n\b|\brazon(es)? detr[aá]s de\b`),
		intent:     Rationale,
		confidence: 0.95,
		cue:        "por qué elegimos/decidimos/usamos",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bwhy did we (choose|decide|pick|go with)\b|\bwhy do(es)? we use\b|\bwhat('s| is) the reasoning\b|\brationale for\b|\breason(ing)? behind\b`),
		intent:     Rationale,
		confidence: 0.95,
		cue:        "why did we choose/decide",
	},

	// --- rationale (0.9), BROAD: bare "por qué"/"why" plus a past-tense
	// decision verb ------------------------------------------------------
	//
	// Blind-corpus finding (docs/conversational-retrieval-plan.md ship-gate
	// cross-validation): the two rules above are over-specific — they
	// require the exact subject "we"/"elegimos" family, but real rationale
	// questions are usually phrased about the PROJECT as subject ("why did
	// Omnia move to...", "¿por qué se aprobó...") with no "we"/"nosotros"
	// anywhere. Recall on the independent corpus was 0.125 (1/8) because of
	// this. The real signal, per the corpus's own confusion analysis, is
	// simpler than a verb whitelist: a bare "why"/"por qué" co-occurring
	// with a past-tense decision verb. For Spanish that is any regular
	// preterite 3rd-person verb, which reliably ends in an accented "ó"
	// (aprobó, unificó, pasó, eligió, ...); for English it is the "did" /
	// "was" / "were" past-tense auxiliary. Both are safe to broaden here
	// because, empirically, no non-rationale query in this table's own
	// bilingual test corpus contains "por qué" or "why did/was/were" at
	// all — see intent_test.go and fixtures_test.go, which stay green with
	// this addition — so widening costs nothing in precision while fixing
	// recall.
	{
		// NOTE: the trailing boundary after "qu[eé]" is the SAME gotcha the
		// signals doc comment above flags — "qué" ends in the accented
		// vowel é, so a plain trailing \b never matches here (checks a
		// nonword-to-nonword transition against the following space). Must
		// use `(?:[^\p{L}0-9_]|$)`, exactly like every other qu[eé]-ending
		// alternative in this table.
		pattern:    regexp.MustCompile(`(?i)\bpor ?qu[eé](?:[^\p{L}0-9_]|$).{0,80}\p{L}+ó(?:[^\p{L}0-9_]|$)`),
		intent:     Rationale,
		confidence: 0.9,
		cue:        "por qué + past-tense (-ó) verb, broad",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bwhy (did|was|were)\b`),
		intent:     Rationale,
		confidence: 0.9,
		cue:        "why did/was/were, broad",
	},

	// --- delta phrase (0.95): "cómo arreglamos" / "how did we fix" -------
	{
		pattern:    regexp.MustCompile(`(?i)\bc[oó]mo (arreglamos|solucionamos|resolvimos|se arregl[oó]|se resolvi[oó]|se solucion[oó])(?:[^\p{L}0-9_]|$)|\bqu[eé] hicimos para (arreglar|solucionar|resolver)\b`),
		intent:     Delta,
		confidence: 0.95,
		cue:        "cómo arreglamos/resolvimos",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bhow did we (fix|solve|resolve)\b|\bhow was (this |it |that )?(fixed|resolved|solved)\b|\bhow (was|were) .{0,40}(fixed|resolved|solved)\b`),
		intent:     Delta,
		confidence: 0.95,
		cue:        "how did we fix/solve/resolve",
	},

	// --- delta raw-error-string (0.75, deliberately below the 0.8 default
	// spread — see DefaultConfidenceThreshold's doc comment): a query that
	// IS (or quotes) an error, not a query ABOUT how one was fixed. Patterns
	// are structural (stack-trace/exception shapes), not phrase cues, so
	// they carry no language split — a stack trace looks the same in ES and
	// EN source.
	{
		pattern:    regexp.MustCompile(`(?i)\b\w+(Error|Exception)\b\s*:|\b\w+(Error|Exception)\b|\bpanic:\s|\btraceback \(most recent call last\)|\.(go|py|js|ts|rb|java):\d+|\bat \S+\(\S+:\d+\)|\bfatal error:`),
		intent:     Delta,
		confidence: 0.75,
		cue:        "raw error/stack-trace string",
	},

	// --- status (0.9): "cómo va" / "cómo está" / "how is X going" --------
	//
	// "cómo quedó" (ES) and "how did X go/turn out" (EN) were added after
	// the blind-corpus run: "¿cómo quedó el hardening de v0.4?" / "how did
	// the v0.4 hardening pass go" both returned Unknown — a past-tense
	// "how did it turn out" is as much a status question as "cómo va", it
	// just asks about something already finished rather than in flight.
	// "what's the state of X" (EN) was added because it was the concrete
	// dangerous false positive from cause #1 below: "what's the state of
	// project fragmentation in Omnia" was winning as Identity on the bare
	// "what's" cue, at 0.8, because no status rule covered "state of" (only
	// "status of"). This rule's 0.9 now outscores identity's 0.8.
	{
		pattern:    regexp.MustCompile(`(?i)\bc[oó]mo (va|est[aá]|anda|viene|qued[oó])(?:[^\p{L}0-9_]|$)|\ben qu[eé] estado (est[aá]|va|se encuentra)(?:[^\p{L}0-9_]|$)|\bestado de\b|\bqu[eé] estado tiene\b`),
		intent:     Status,
		confidence: 0.9,
		cue:        "cómo va/está/quedó",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bhow('s| is)\b.{0,40}\b(going|doing)\b|\bwhat('s| is) the status of\b|\bstatus of\b|\bhow did\b.{0,40}\b(go|turn out)\b|\bwhat('s| is) the state of\b`),
		intent:     Status,
		confidence: 0.9,
		cue:        "how is X going / status of / how did X go / state of",
	},

	// --- open_items (0.9): "qué falta" / "qué queda" / "what's next" -----
	{
		pattern:    regexp.MustCompile(`(?i)\bqu[eé] (falta|queda|sigue)\b|\bpr[oó]ximos pasos\b|\bqu[eé] queda pendiente\b`),
		intent:     OpenItems,
		confidence: 0.9,
		cue:        "qué falta/queda/sigue",
	},
	{
		// "what's pending" (bare, not just "still pending") was added after
		// the blind-corpus run: "what's pending for the v0.3.2 release" was
		// the second dangerous cause-#1 false positive, winning as Identity
		// on the bare "what's" cue because the old alternation only covered
		// "still pending", not the bare form.
		pattern:    regexp.MustCompile(`(?i)\bwhat('s| is) (left|next|remaining|(?:still )?pending)\b|\bwhat remains\b|\bnext steps\b|\boutstanding items\b|\bopen items\b`),
		intent:     OpenItems,
		confidence: 0.9,
		cue:        "what's next/left/remaining/pending",
	},

	// --- cross_project (0.9): "en qué estoy bloqueado" / "esta semana" /
	// "across everything" ---------------------------------------------
	//
	// The plan's literal ES cue list includes bare "esta semana" ("this
	// week"). That phrase alone is deliberately NOT a cue here: "cómo va
	// Workly esta semana" is a status question about ONE project that
	// happens to mention a timeframe, not a cross-project question — a bare
	// "esta semana" cue would misroute every such query. Per the plan's own
	// ship gate ("a misrouted query is worse than an unrouted one"), the
	// cue is narrowed to timeframe language that ALSO carries an explicit
	// cross-project signal ("todos los/mis proyectos", "everything").
	{
		// "entre X y Y" (between X and Y) and "qué/cuáles proyectos" (which
		// projects) were added after the blind-corpus run (cause #4: recall
		// was 0.25, 2/8). "¿qué me bloquea esta semana entre Omnia y
		// Workly?" and "¿qué proyectos quedaron mezclados...?" both named
		// TWO projects (or asked about "proyectos" plural) but matched no
		// existing cue at all. "entre \w+ y \w+" and "qué/cuáles proyectos"
		// are the generic, project-name-agnostic shapes for those two real
		// signals (plural "projects", and "between/entre X and Y" naming
		// two projects) called out in the corpus's own confusion notes.
		pattern:    regexp.MustCompile(`(?i)\ben qu[eé] (estoy|tengo|est[aá]) bloqueado\b|\ben todos (mis|los) proyectos\b|\bresumen semanal de todos\b|\btodos mis proyectos\b|\bentre \w+ y \w+\b|\bqu[eé] proyectos\b|\bcu[aá]les proyectos\b`),
		intent:     CrossProject,
		confidence: 0.9,
		cue:        "en qué estoy bloqueado / todos los proyectos / entre X y Y / qué proyectos",
	},
	{
		// "across X and Y" and "which projects" are the EN pairing of the
		// two additions above: "what's blocking me this week across Omnia
		// and Workly" was the third cause-#1 false positive (won as
		// Identity at 0.8 on the bare "what's" cue); "which projects got
		// tangled together..." was a cause-#4 recall miss.
		pattern:    regexp.MustCompile(`(?i)\bacross everything\b|\bacross all projects\b|\bwhat am i blocked on\b|\ball my projects\b|\beverything i'?m working on\b|\bacross \w+ and \w+\b|\bwhich projects\b`),
		intent:     CrossProject,
		confidence: 0.9,
		cue:        "across everything/all projects / across X and Y / which projects",
	},

	// --- identity (0.8, deliberately the lowest phrase-rule confidence —
	// see the signals doc comment above): "qué es" / "háblame sobre" /
	// "what is" / "tell me about" ----------------------------------------
	{
		pattern:    regexp.MustCompile(`(?i)\bqu[eé] (es|son)\b|\bqui[eé]n es\b|\bh[aá]blame (sobre|de)\b|\bcu[eé]ntame (sobre|de)\b|\bdefinici[oó]n de\b`),
		intent:     Identity,
		confidence: 0.8,
		cue:        "qué es / háblame sobre",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bwhat('s| is)\b|\bwho is\b|\btell me about\b|\bdescribe\b|\bdefine\b`),
		intent:     Identity,
		confidence: 0.8,
		cue:        "what is / tell me about",
	},

	// --- identity (0.85), SPECIFIC capability/property sub-cues ----------
	//
	// Blind-corpus finding (cause #2): real identity questions are rarely
	// the textbook "qué es X" / "what is X". The recurring real shape is a
	// capability or property question about a named entity — "how does X
	// work", "where does X keep Y", "does X have Y" — and every one of
	// these six corpus cases returned Unknown under the old table:
	// "¿cómo busca Omnia la información?", "how does Omnia's search work",
	// "¿dónde guarda Omnia sus datos?", "where does Omnia store its data",
	// "¿Omnia tiene una nube propia?", "does Omnia have a cloud offering".
	//
	// These four rules are deliberately MORE specific than the generic
	// "qué es"/"what is" pair above (0.85 > 0.8) — per the specificity
	// principle used throughout this table: a cue anchored to "<verb>
	// <ProperNoun>" or "<ProperNoun> tiene/have" is a stronger, narrower
	// signal than a bare interrogative, so it should win any tie against
	// the generic cue. They still sit BELOW every other intent's 0.9+
	// cues, so a query like "cómo va Workly" (status) or "qué proyectos..."
	// (cross_project) that happens to also match one of these shapes still
	// loses to the more specific same-confidence-tier intent rule.
	//
	// GOTCHA (found and fixed during this fix's own testing, worth
	// recording alongside the ASCII-\b gotcha above): the leading `(?i)`
	// on every pattern in this table makes the WHOLE pattern
	// case-insensitive, which silently folds `[A-Z]` into `[A-Za-z]` — so
	// a naive `[A-Z]` here would match ANY letter, not just a capital one,
	// defeating the entire "named entity" signal these four rules exist
	// for (verified empirically: it turned "cómo se solucionaron los
	// falsos negativos..." into a false-positive Identity match, because
	// "se solucionaron" satisfied "<word> <word>" once the capital check
	// was silently no-op'd). Fix: wrap just the capital-letter class in
	// `(?-i:...)`, which locally turns case-insensitivity back off for
	// that one character class while leaving the rest of the pattern
	// (cómo/dónde/does/tiene, all lowercase-authored) matched
	// case-insensitively as intended.
	{
		pattern:    regexp.MustCompile(`(?i)\bc[oó]mo \p{L}+ (?-i:[A-ZÁÉÍÓÚÑ])\p{L}*(?:[^\p{L}0-9_]|$)|\bd[oó]nde \p{L}+ (?-i:[A-ZÁÉÍÓÚÑ])\p{L}*(?:[^\p{L}0-9_]|$)`),
		intent:     Identity,
		confidence: 0.85,
		cue:        "cómo/dónde <verbo> <Entidad> (capacidad/propiedad)",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\b(?-i:[A-ZÁÉÍÓÚÑ])\p{L}* tiene (un|una|el|la|su)\b`),
		intent:     Identity,
		confidence: 0.85,
		cue:        "<Entidad> tiene (does X have)",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\b(how|where) does (?-i:[A-Z])\w+('s)?\b`),
		intent:     Identity,
		confidence: 0.85,
		cue:        "how/where does <Entity> (capability/property)",
	},
	{
		pattern:    regexp.MustCompile(`(?i)\bdoes (?-i:[A-Z])\w+ have\b`),
		intent:     Identity,
		confidence: 0.85,
		cue:        "does <Entity> have (capability/property)",
	},
}

// Classify routes query to the best-matching Intent using
// DefaultConfidenceThreshold. It is the entry point most callers want; use
// ClassifyWithThreshold directly only when a caller needs a different
// precision/recall trade-off than the package default (e.g. a consumer
// that would rather abstain more often in exchange for fewer misroutes).
func Classify(query string) Classification {
	return ClassifyWithThreshold(query, DefaultConfidenceThreshold)
}

// ClassifyWithThreshold scans every rule in signals and keeps the
// highest-confidence match (ties broken by table order — see signals' doc
// comment), then downgrades to Unknown if that best score is below
// threshold. Confidence in the returned Classification is always the raw
// best score found, even when Intent was downgraded to Unknown, so a
// caller can compare against ITS OWN threshold without re-scanning.
//
// Worked example of why "highest confidence" (not "first match") is the
// right combining rule: the query "qué es lo que falta para terminar P2"
// contains BOTH "qué es" (identity's cue) and "que falta" (open_items'
// cue, appearing later in "...lo que falta..."). A human reading this
// query hears an open-items question ("what's left"), not an identity
// question. Because open_items' rule (0.9) outscores identity's (0.8),
// Classify returns OpenItems regardless of which rule's regex happens to
// be earlier in the table — the confidence spread encodes the priority,
// not the table position.
//
// Evaluating every rule (rather than stopping at the first match) is still
// cheap enough to stay inside the < 1 ms budget: signals has ~13 entries,
// each a single compiled regexp.MatchString call over a query capped
// around 200 characters (see bench_test.go for the measured cost).
func ClassifyWithThreshold(query string, threshold float64) Classification {
	best := Classification{Intent: Unknown, Confidence: 0}
	for _, s := range signals {
		if s.confidence <= best.Confidence {
			// This rule can never beat the current best regardless of
			// whether it matches, so skip the regex evaluation entirely.
			continue
		}
		if s.pattern.MatchString(query) {
			best = Classification{Intent: s.intent, Confidence: s.confidence, Cue: s.cue}
		}
	}
	if best.Confidence < threshold {
		return Classification{Intent: Unknown, Confidence: best.Confidence, Cue: best.Cue}
	}
	return best
}
