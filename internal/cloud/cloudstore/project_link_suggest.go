package cloudstore

import "strings"

// SuggestProjectParents proposes parent links for projects that LOOK like
// sub-projects by name (e.g. "workly-marketing" under "workly"). It is a
// PURE function — no DB, no side effects — purely advisory: nothing here
// ever links a project on its own. The caller (the Admin Projects page)
// renders each suggestion as a one-click confirm/dismiss banner that calls
// SetProjectParent only when the operator confirms.
//
// For every project P in projects that is:
//  1. not already linked (P is not a key in existingLinks), and
//  2. not itself already a parent (P is not a value in existingLinks —
//     mirrors SetProjectParent's "child cannot already be a parent" rule)
//
// find the LONGEST other project Q in projects such that P equals Q followed
// by a separator ('-', '_', or ' ') and then more text (strings.HasPrefix),
// where Q is itself a valid parent candidate (Q is not a key in
// existingLinks — mirrors SetProjectParent's "parent cannot already be a
// child" rule). Matching is case-insensitive, mirroring the lower+trim
// canonicalize convention used elsewhere (internal/dashboard/canonicalize.go).
//
// A project with no valid candidate is simply omitted from the result — the
// returned map never contains an empty-string suggestion.
func SuggestProjectParents(projects []string, existingLinks map[string]string) map[string]string {
	out := make(map[string]string)
	if len(projects) == 0 {
		return out
	}

	childSet := make(map[string]struct{}, len(existingLinks))
	parentSet := make(map[string]struct{}, len(existingLinks))
	for child, parent := range existingLinks {
		childSet[suggestFold(child)] = struct{}{}
		parentSet[suggestFold(parent)] = struct{}{}
	}

	for _, p := range projects {
		pFold := suggestFold(p)
		if _, linked := childSet[pFold]; linked {
			continue
		}
		if _, isParent := parentSet[pFold]; isParent {
			continue
		}

		var bestQ string
		bestLen := -1
		for _, q := range projects {
			qFold := suggestFold(q)
			if qFold == pFold {
				continue // never suggest a project as its own parent
			}
			if _, isChild := childSet[qFold]; isChild {
				continue // Q must not already be a sub-project itself
			}
			if !hasSeparatorPrefix(pFold, qFold) {
				continue
			}
			if len(qFold) > bestLen {
				bestLen = len(qFold)
				bestQ = q
			}
		}
		if bestQ != "" {
			out[p] = bestQ
		}
	}
	return out
}

// suggestFold is the case-insensitive matching key: lowercase + trim,
// mirroring internal/dashboard/canonicalize.go's caseFoldKey convention.
func suggestFold(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// hasSeparatorPrefix reports whether pFold equals qFold followed by exactly
// one of '-', '_', ' ' and then at least one more character.
func hasSeparatorPrefix(pFold, qFold string) bool {
	if qFold == "" || !strings.HasPrefix(pFold, qFold) {
		return false
	}
	rest := pFold[len(qFold):]
	if len(rest) < 2 {
		return false // no separator, or separator with nothing after it
	}
	switch rest[0] {
	case '-', '_', ' ':
		return true
	default:
		return false
	}
}
