package store

import (
	"fmt"
	"math"
)

// salience is a SEPARATE, optional signal from a machine writer (Umbral)
// expressing "this memory mattered more/less than its type alone suggests"
// (e.g. high prediction error). It is deliberately NOT importance: importance
// stays a pure, type-derived heuristic (config.DefaultImportanceWeight) that
// this slice never touches or makes writable. Machine-written salience must
// never override human-curated importance — it lives alongside it and, by
// default, does not even influence ranking (config.RankingWeights.Salience
// defaults to 0; see mcp.SalienceScore/RankScore) PROVIDED the stored value
// is a normal float — enforced below by normalizeSalience rejecting NaN, not
// merely bounding it: 0.0 * NaN is NaN, not 0, so a zero weight alone would
// not neutralize a poisoned value. mcp.SalienceScore/clampUnit are a second,
// independent defensive layer, not the primary guarantee.

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
//
// NaN needs its own explicit check: IEEE-754 comparisons against NaN are
// always false, so `NaN < salienceMin`/`NaN > salienceMax` both evaluate to
// false and a naive range check lets it through as "in range". Today that
// would only be saved by sqlite3_bind_double rebinding NaN to NULL — an
// accident of a lower layer, not a real guarantee. ±Inf are already caught
// by the range comparisons below, so only NaN needs a dedicated check.
func normalizeSalience(raw *float64) (*float64, error) {
	if raw == nil {
		return nil, nil
	}
	if math.IsNaN(*raw) {
		return nil, fmt.Errorf("invalid salience NaN: must be a finite number between %v and %v", salienceMin, salienceMax)
	}
	if *raw < salienceMin || *raw > salienceMax {
		return nil, fmt.Errorf("invalid salience %v: must be between %v and %v", *raw, salienceMin, salienceMax)
	}
	v := *raw
	return &v, nil
}
