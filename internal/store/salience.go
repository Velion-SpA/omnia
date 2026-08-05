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
// is a normal float. This is enforced at write time by normalizeSalience
// below (which rejects NaN/out-of-range, not merely clamps) — a zero weight
// does not neutralize a poisoned value on its own: 0.0 * NaN is NaN, not 0,
// so an unrejected NaN would silently corrupt every weighted-sum RankScore
// call regardless of configured weight. mcp.SalienceScore/clampUnit are a
// second, independent defensive layer for values already in the store (e.g.
// written before this check existed), not the primary guarantee.

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
// always false, so `NaN < salienceMin` and `NaN > salienceMax` both evaluate
// to false and a naive range check lets NaN through as "in range". Today
// that would still not corrupt data — sqlite3_bind_double rebinds NaN to
// NULL in SQLite's C implementation — but a data-integrity guarantee must
// not rest on an accident of a lower layer: a caller writing salience: NaN
// must get a rejection, not a value that is silently discarded underneath
// it. ±Inf are already caught by the existing range comparisons below (Inf
// > salienceMax, -Inf < salienceMin), so only NaN needs a dedicated check.
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
