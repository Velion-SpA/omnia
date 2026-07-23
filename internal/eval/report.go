package eval

// Segment is one row of a segmented eval report (spec EVAL-3): the raw
// hit/total counts and total token cost for a single capability or language
// slice, from which Accuracy and QualityPer1kTokens are derived — never
// stored as bare pre-computed scalars, so a report reader can always audit
// the ratio back to its inputs.
type Segment struct {
	Label       string
	Total       int
	Hits        int
	TotalTokens int
}

// Accuracy returns this segment's hit rate. A segment with zero cases
// reports 0 rather than dividing by zero.
func (s Segment) Accuracy() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(s.Total)
}

// QualityPer1kTokens returns this segment's quality-per-1k-tokens figure
// (spec EVAL-1's metric, applied per segment rather than only in aggregate).
func (s Segment) QualityPer1kTokens() float64 {
	return QualityPer1kTokens(s.Accuracy(), s.TotalTokens)
}

// CaseResult is one scored EvalCase outcome: Score()'s hit verdict plus the
// case's full total_tokens (spec EVAL-1 — query-embedding + retrieval +
// injected-context + judge tokens summed by the caller, which owns the
// non-judge token accounting Score() does not compute). BuildReport
// aggregates a batch of these into the spec EVAL-3 segmented view.
type CaseResult struct {
	Case        EvalCase
	Hit         bool
	TotalTokens int
}

// Report is the spec EVAL-3 segmented report: accuracy and
// quality-per-1k-tokens broken down per capability (all four buckets) AND
// separately per language (EN vs ES), plus (spec EVAL-7) a distinct
// retrieval-only recall@k section that BuildReport never populates and never
// derives from ByCapability/ByLanguage — callers attach it separately so the
// intrinsic (retrieval) and extrinsic (end-task) figures can never collapse
// into one merged number.
type Report struct {
	ByCapability map[Capability]Segment
	ByLanguage   map[Language]Segment
	// Retrieval is spec EVAL-7's retrieval-only recall@k section. Nil until a
	// caller explicitly attaches one (e.g. via RunRetrievalSection) — a run
	// with no retrieval section is distinguishable from one whose retrieval
	// figure happens to be zero.
	Retrieval *RetrievalSection
}

// allCapabilities is the closed set BuildReport always seeds into
// ByCapability, so every one of the four buckets is present in the report
// even when a run has zero cases for it. This matters because a *missing*
// key and a key with 0% accuracy mean different things: a reader must never
// have to guess whether a bucket was skipped or genuinely scored zero.
var allCapabilities = []Capability{
	CapabilityRecall,
	CapabilityCausal,
	CapabilityStateUpdate,
	CapabilityStateAbstraction,
}

// allLanguages is the closed set BuildReport always seeds into ByLanguage,
// for the same "missing vs. zero" reason as allCapabilities. Spec EVAL-3
// segments by EN-query vs. bilingual ES-query slices — the harness's own
// corpus (testdata/cases.json, real dogfooded material) already carries a
// per-case Language field, so this segmentation needs no separate dataset.
// internal/embed's existing bilingual set (ab_pairs.json) deliberately stays
// out of this report: its ABPair shape has no Capability or expected-fact
// field to segment by, and spec EVAL-7 (PR3) keeps its retrieval-only
// recall@k metric un-merged with this end-task accuracy view.
var allLanguages = []Language{
	LanguageEN,
	LanguageES,
}

// BuildReport aggregates a run's CaseResults into the spec EVAL-3 segmented
// view: one row per capability, one row per language, every bucket present.
func BuildReport(results []CaseResult) Report {
	byCap := make(map[Capability]Segment, len(allCapabilities))
	for _, cap := range allCapabilities {
		byCap[cap] = Segment{Label: string(cap)}
	}
	byLang := make(map[Language]Segment, len(allLanguages))
	for _, lang := range allLanguages {
		byLang[lang] = Segment{Label: string(lang)}
	}

	for _, r := range results {
		byCap[r.Case.Capability] = accumulate(byCap[r.Case.Capability], r)
		byLang[r.Case.Language] = accumulate(byLang[r.Case.Language], r)
	}

	return Report{ByCapability: byCap, ByLanguage: byLang}
}

// accumulate folds one CaseResult's hit/token outcome into seg. It never
// touches seg.Label — the caller (BuildReport) always seeds every segment's
// Label from the closed capability/language set before accumulating, so a
// result still lands in a correctly-labeled bucket even if its own
// Capability/Language string happens to differ in case or spacing from the
// canonical constant.
func accumulate(seg Segment, r CaseResult) Segment {
	seg.Total++
	if r.Hit {
		seg.Hits++
	}
	seg.TotalTokens += r.TotalTokens
	return seg
}
