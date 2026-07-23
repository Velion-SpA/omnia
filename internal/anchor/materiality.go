package anchor

import "strings"

// DefaultMaterialityThreshold is the spec's conservative default (REQ-003):
// any non-whitespace change within the range is material. Callers that want
// a stricter policy (ignore small edits) can pass a higher threshold — see
// design's "Open Questions" (empirical tuning deferred).
const DefaultMaterialityThreshold = 0.0

// IsMaterialChange reports whether a range's content changed materially
// between two captures. It is pure (no git dependency, no I/O) so it is
// fully unit-testable in isolation from Probe.
//
// oldHash/newHash are the two captures' RangeHash values — an equality check
// is the fast path (identical hash always means "not material", regardless
// of threshold). oldContent/newContent are the RAW range text from each
// capture, used only as a fallback when the hashes differ: both are
// whitespace-normalized (normalizeRangeContent) before comparing, so
// reindentation, trailing whitespace, and blank-line insertion/removal never
// count as material on their own (REQ-003: "any NON-WHITESPACE change ...
// is material"). threshold is the minimum fraction of changed non-whitespace
// lines required to call it material; pass DefaultMaterialityThreshold (0.0)
// for the spec's conservative default (any single non-whitespace-changed
// line is material).
func IsMaterialChange(oldHash, newHash, oldContent, newContent string, threshold float64) bool {
	if oldHash == newHash {
		return false
	}

	oldNorm := normalizeRangeContent(oldContent)
	newNorm := normalizeRangeContent(newContent)
	if oldNorm == newNorm {
		// Hashes differ but the normalized text is identical — the only
		// difference was whitespace, which REQ-003 explicitly excludes.
		return false
	}

	changed, total := diffNonWhitespaceLines(oldNorm, newNorm)
	if total == 0 {
		return false
	}
	ratio := float64(changed) / float64(total)
	return ratio >= threshold
}

// normalizeRangeContent strips whitespace-only variance from a code range:
// each line is trimmed and blank lines are dropped entirely, so
// reindentation or blank-line insertion never registers as a content change.
func normalizeRangeContent(raw string) string {
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "\n")
}

// diffNonWhitespaceLines does a cheap positional (non-LCS) comparison of two
// already-normalized line sequences, returning the number of differing
// lines and the total line count compared (the longer side's length).
func diffNonWhitespaceLines(oldNorm, newNorm string) (changed, total int) {
	oldLines := splitNonEmpty(oldNorm)
	newLines := splitNonEmpty(newNorm)

	total = len(oldLines)
	if len(newLines) > total {
		total = len(newLines)
	}

	for i := 0; i < total; i++ {
		var o, n string
		if i < len(oldLines) {
			o = oldLines[i]
		}
		if i < len(newLines) {
			n = newLines[i]
		}
		if o != n {
			changed++
		}
	}
	return changed, total
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
