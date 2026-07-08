package dashboard

import "log/slog"

// GroupIndex is a validated index of project_groups.
type GroupIndex struct {
	parents map[string][]string // parent canonical → []child canonicals
	childOf map[string]string   // child canonical → parent canonical
}

// newGroupIndex validates and builds GroupIndex from raw config.
// Invalid entries (self-referential, child-is-also-parent, child-has-multiple-parents)
// are skipped with a log warning — never crash.
func newGroupIndex(raw map[string][]string, logger *slog.Logger) *GroupIndex {
	g := &GroupIndex{
		parents: make(map[string][]string),
		childOf: make(map[string]string),
	}
	if len(raw) == 0 {
		return g
	}
	parentKeys := make(map[string]struct{}, len(raw))
	for parent := range raw {
		parentKeys[parent] = struct{}{}
	}
	for parent, children := range raw {
		for _, child := range children {
			if child == parent {
				if logger != nil {
					logger.Warn("project_groups: ignoring self-referential entry", "parent", parent, "child", child)
				}
				continue
			}
			if _, isParent := parentKeys[child]; isParent {
				if logger != nil {
					logger.Warn("project_groups: child is also a parent, ignoring", "parent", parent, "child", child)
				}
				continue
			}
			if existingParent, alreadyChild := g.childOf[child]; alreadyChild {
				if logger != nil {
					logger.Warn("project_groups: child already has a parent, ignoring",
						"child", child, "existing_parent", existingParent, "rejected_parent", parent)
				}
				continue
			}
			g.parents[parent] = append(g.parents[parent], child)
			g.childOf[child] = parent
		}
	}
	return g
}

// IsParent reports whether canonical is a group parent.
func (g *GroupIndex) IsParent(canonical string) bool {
	if g == nil {
		return false
	}
	_, ok := g.parents[canonical]
	return ok
}

// IsChild reports whether canonical is a child in any group.
func (g *GroupIndex) IsChild(canonical string) bool {
	if g == nil {
		return false
	}
	_, ok := g.childOf[canonical]
	return ok
}

// Children returns the validated child canonicals for a parent.
func (g *GroupIndex) Children(parent string) []string {
	if g == nil {
		return nil
	}
	return g.parents[parent]
}

// ParentOf returns the parent canonical for a child, or "" if not a child.
func (g *GroupIndex) ParentOf(child string) string {
	if g == nil {
		return ""
	}
	return g.childOf[child]
}

// groupRawNames returns all raw DB project names for the whole group:
// parent's raw names PLUS each child's raw names.
func (g *GroupIndex) groupRawNames(parent string, rawAll []string, aliases map[string]string) []string {
	var out []string
	out = append(out, rawProjectsForCanonical(parent, rawAll, aliases)...)
	for _, child := range g.Children(parent) {
		out = append(out, rawProjectsForCanonical(child, rawAll, aliases)...)
	}
	return out
}

// coreRawNames returns only the parent's raw DB project names (excluding children).
func (g *GroupIndex) coreRawNames(parent string, rawAll []string, aliases map[string]string) []string {
	return rawProjectsForCanonical(parent, rawAll, aliases)
}

// filterGroupChildren removes canonical names that are group children from the list.
func filterGroupChildren(names []string, g *GroupIndex) []string {
	if g == nil {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !g.IsChild(n) {
			out = append(out, n)
		}
	}
	return out
}

// GroupNav holds the sub-project navigation state for a group browse view.
// Nil when the current project is not a group parent.
type GroupNav struct {
	Parent    string        // canonical name of the parent
	ActiveSub string        // "" = all, "core" = parent-only, else child canonical
	Items     []GroupNavItem
}

// GroupNavItem is one entry in the sub-project nav row.
type GroupNavItem struct {
	Sub      string // "" = all, "core", or child canonical
	Label    string // display text
	URL      string
	IsActive bool
}
