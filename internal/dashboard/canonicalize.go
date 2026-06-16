package dashboard

import "strings"

// caseFoldKey returns the lowercase-trimmed canonical key for a project name.
// This is the normalization rule for case-only deduplication.
func caseFoldKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// canonicalizeProject resolves a raw project name to its canonical form.
// Resolution order:
//  1. Explicit alias from aliasMap (exact match on raw name)
//  2. Explicit alias from aliasMap (match on lowercase+trim of raw name)
//  3. Lowercase + trim (case-fold)
func canonicalizeProject(name string, aliasMap map[string]string) string {
	if aliasMap != nil {
		if canonical, ok := aliasMap[name]; ok {
			return canonical
		}
		// Also try the case-folded key so variant cases match the same alias
		// without requiring duplicate entries in the map.
		// Example: alias map has "01.- velion"→"velion"; raw name "01.- Velion"
		// misses the exact lookup but hits on caseFoldKey → "01.- velion" → "velion".
		if canonical, ok := aliasMap[caseFoldKey(name)]; ok {
			return canonical
		}
	}
	return caseFoldKey(name)
}

// rawProjectsForCanonical returns the subset of rawNames (actual DB project names)
// whose canonical form equals the given canonical after applying aliasMap and case-folding.
// Used to expand an alias target back to raw DB names for SQL IN queries so that
// aliased canonicals (e.g. "velion") fetch ALL their raw variants
// ("01.- velion", "01.- Velion", "velion", …) instead of only case-fold matches.
func rawProjectsForCanonical(canonical string, rawNames []string, aliasMap map[string]string) []string {
	var out []string
	for _, name := range rawNames {
		if canonicalizeProject(name, aliasMap) == canonical {
			out = append(out, name)
		}
	}
	return out
}

// canonicalizerFunc returns a canonicalize func(string) string bound to aliasMap.
// Pass this to engramdb.DB.ProjectsCanonical.
func canonicalizerFunc(aliasMap map[string]string) func(string) string {
	return func(name string) string {
		return canonicalizeProject(name, aliasMap)
	}
}

// hiddenSet builds a set of canonical names to hide. Input is the raw hidden list
// from config; each entry is itself canonicalized before being added to the set.
func hiddenSet(hidden []string, aliasMap map[string]string) map[string]struct{} {
	set := make(map[string]struct{}, len(hidden))
	for _, name := range hidden {
		set[canonicalizeProject(name, aliasMap)] = struct{}{}
	}
	return set
}

// filterHidden removes canonical names that appear in the hidden set.
// Input and output are sorted slices of canonical names.
func filterHidden(names []string, hidden map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := hidden[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}
