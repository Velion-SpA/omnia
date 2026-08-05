package store

import "fmt"

// salience is a SEPARATE, optional signal from a machine writer (Umbral)
// expressing "this memory mattered more/less than its type alone suggests"
// (e.g. high prediction error). It is deliberately NOT importance: importance
// stays a pure, type-derived heuristic (config.DefaultImportanceWeight) that
// this slice never touches or makes writable. Machine-written salience must
// never override human-curated importance — it lives alongside it and, by
// default, does not even influence ranking (config.RankingWeights.Salience
// defaults to 0; see mcp.SalienceScore/RankScore).

// salienceMin/salienceMax bound accepted values. [0,1] mirrors every other
// normalized ranking signal in this codebase (see ranker.Features' own doc,
// "All values are in [0,1]"), so an operator-configured weights.salience
// composes predictably against the existing weighted sum.
const (
	salienceMin = 0.0
	salienceMax = 1.0
)

// normalizeSalience validates a raw salience value from an AddObservation
// caller. nil means "not provided" and passes straight through — unlike
// Outcome/Source's empty-string sentinel, a real nil *float64 is used here
// because 0.0 is itself a meaningful salience score, so only nil can mean
// "absent" without conflating it with a real value.
func normalizeSalience(raw *float64) (*float64, error) {
	if raw == nil {
		return nil, nil
	}
	if *raw < salienceMin || *raw > salienceMax {
		return nil, fmt.Errorf("invalid salience %v: must be between %v and %v", *raw, salienceMin, salienceMax)
	}
	v := *raw
	return &v, nil
}
