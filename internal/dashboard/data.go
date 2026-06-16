package dashboard

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/engramdb"
	"github.com/velion/omnia/internal/meta"
)

// ObsView is a single observation enriched with parsed meta for UI display.
type ObsView struct {
	Obs     Observation
	Meta    meta.Meta
	HasMeta bool   // false → curated (no valid omnia-meta block)
	Age     string // human-readable updated_at age
}

// SubProjectStat holds a child project's display name and total count.
type SubProjectStat struct {
	Name  string
	Count int
}

// ProjectStats holds per-project counts for the overview page.
type ProjectStats struct {
	Name       string
	Total      int
	Ingested   int // observations WITH a valid omnia-meta block
	Curated    int // observations WITHOUT a valid omnia-meta block
	BySource   map[string]int
	ByType     map[string]int
	ByKind     map[string]int
	// IsGroup is true when this is a group parent card with sub-projects.
	IsGroup        bool
	SubProjects    []SubProjectStat // per-child counts (only when IsGroup=true)
	CoreCount      int              // parent's own observations count (only when IsGroup=true)
	LatestUpdateAt string
}

// enrichObs parses the omnia-meta block from an observation and returns an ObsView.
func enrichObs(obs Observation) ObsView {
	m, ok := meta.Parse(obs.Content)
	return ObsView{
		Obs:     obs,
		Meta:    m,
		HasMeta: ok,
		Age:     formatAge(obs.UpdatedAt),
	}
}

// TypeCount pairs a type name with its observation count.
type TypeCount struct {
	Name  string
	Count int
}

// sortedTypeCounts returns a slice of TypeCount sorted by count descending,
// then by name ascending for ties. Used by the overview card and browse filter.
func sortedTypeCounts(m map[string]int) []TypeCount {
	result := make([]TypeCount, 0, len(m))
	for name, count := range m {
		result = append(result, TypeCount{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// distinctTypes returns a sorted slice of non-empty, deduplicated observation
// types from the provided views. Used to populate the browse Category dropdown.
func distinctTypes(views []ObsView) []string {
	seen := map[string]struct{}{}
	for _, v := range views {
		if v.Obs.Type != "" {
			seen[v.Obs.Type] = struct{}{}
		}
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// browseTermForProject returns a broad search term that reliably retrieves
// omnia-ingested observations for a given project. We use "schema_version: 1"
// because every ingested observation contains this line in its omnia-meta block.
// Curated observations won't have this term, so they'll be missed in "ingested only"
// mode — callers that want all observations should use a separate broad search.
//
// API LIMITATION NOTE: Engram's GET /search requires a non-empty q parameter and
// is FTS-based (no "list all" endpoint exists). For a project-wide browse we use
// the best available broad terms. This means:
//   - Ingested observations: search "schema_version: 1" (present in every omnia-meta block)
//   - Curated observations: search the project name itself (may miss some, may over-fetch)
//   - Combined view: run both searches and deduplicate by ID
//
// A future Omnia index will provide O(1) listing by project without this FTS workaround.
const ingestedTerm = "schema_version: 1"

// loadProjectObs fetches all available observations for a project.
// It uses two searches (ingested term + project name) and deduplicates.
func loadProjectObs(ctx context.Context, client *engramClient, project string, limit int) ([]ObsView, error) {
	// Search 1: ingested observations (have omnia-meta block).
	ingested, err := client.Search(ctx, ingestedTerm, project, limit)
	if err != nil {
		return nil, err
	}

	// Search 2: broader search for curated observations using project name.
	curated, err := client.Search(ctx, project, project, limit)
	if err != nil {
		// Non-fatal: fall back to ingested only.
		curated = nil
	}

	// Deduplicate by ID.
	seen := make(map[int]struct{})
	var all []Observation
	for _, o := range ingested {
		if _, ok := seen[o.ID]; !ok {
			seen[o.ID] = struct{}{}
			all = append(all, o)
		}
	}
	for _, o := range curated {
		if _, ok := seen[o.ID]; !ok {
			seen[o.ID] = struct{}{}
			all = append(all, o)
		}
	}

	// Sort by updated_at descending (most recent first).
	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt > all[j].UpdatedAt
	})

	views := make([]ObsView, len(all))
	for i, o := range all {
		views[i] = enrichObs(o)
	}
	return views, nil
}

// computeProjectStats derives per-project counts from a slice of ObsView.
func computeProjectStats(project string, views []ObsView) ProjectStats {
	stats := ProjectStats{
		Name:     project,
		Total:    len(views),
		BySource: map[string]int{},
		ByType:   map[string]int{},
		ByKind:   map[string]int{},
	}
	for _, v := range views {
		if v.HasMeta {
			stats.Ingested++
			stats.BySource[v.Meta.Source]++
			stats.ByKind[v.Meta.Kind]++
		} else {
			stats.Curated++
		}
		if v.Obs.Type != "" {
			stats.ByType[v.Obs.Type]++
		}
		if v.Obs.UpdatedAt > stats.LatestUpdateAt {
			stats.LatestUpdateAt = v.Obs.UpdatedAt
		}
	}
	return stats
}

// dedupeViews removes duplicate ObsView entries by observation ID.
func dedupeViews(views []ObsView) []ObsView {
	seen := make(map[int]struct{}, len(views))
	out := make([]ObsView, 0, len(views))
	for _, v := range views {
		if _, ok := seen[v.Obs.ID]; !ok {
			seen[v.Obs.ID] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// computeGroupProjectStats builds a ProjectStats for a group parent by splitting
// the combined view set into core (parent-only) and per-child buckets.
// The base totals (Total, Ingested, Curated, BySource, ByType, ByKind) reflect
// the entire group; IsGroup, CoreCount, and SubProjects hold the split.
func computeGroupProjectStats(parent string, views []ObsView, g *GroupIndex, aliases map[string]string) ProjectStats {
	canonicalize := canonicalizerFunc(aliases)

	children := g.Children(parent)
	childCounts := make(map[string]int, len(children))
	for _, c := range children {
		childCounts[c] = 0
	}

	var coreCount int
	for _, v := range views {
		c := canonicalize(v.Obs.Project)
		if c == parent {
			coreCount++
		} else if _, isChild := childCounts[c]; isChild {
			childCounts[c]++
		}
	}

	subProjects := make([]SubProjectStat, 0, len(children))
	for _, child := range children {
		subProjects = append(subProjects, SubProjectStat{
			Name:  child,
			Count: childCounts[child],
		})
	}

	base := computeProjectStats(parent, views)
	base.IsGroup = true
	base.SubProjects = subProjects
	base.CoreCount = coreCount
	return base
}

// obsFromDB converts an engramdb.Observation into the dashboard's Observation
// type. Fields unavailable in the SQLite schema (SyncID, SessionID,
// RevisionCount, DuplicateCount, LastSeenAt, Rank) are left at zero values.
func obsFromDB(o engramdb.Observation) Observation {
	return Observation{
		ID:        o.ID,
		Type:      o.Type,
		Title:     o.Title,
		Content:   o.Content,
		Project:   o.Project,
		Scope:     o.Scope,
		TopicKey:  o.TopicKey,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

// mergeProjectNames merges two slices of project names into a sorted,
// deduplicated result. Empty or blank entries are dropped.
func mergeProjectNames(a, b []string) []string {
	seen := make(map[string]struct{})
	for _, n := range a {
		if n = strings.TrimSpace(n); n != "" {
			seen[n] = struct{}{}
		}
	}
	for _, n := range b {
		if n = strings.TrimSpace(n); n != "" {
			seen[n] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for n := range seen {
		result = append(result, n)
	}
	sort.Strings(result)
	return result
}

// knownProjectsCanonical returns the result of knownProjects with each name
// run through canonicalize (alias lookup + case-fold). Duplicates produced by
// canonicalization are removed; result is sorted.
func knownProjectsCanonical(status SyncStatus, cfg Config) []string {
	raw := knownProjects(status, cfg)
	canonicalize := canonicalizerFunc(cfg.ProjectAliases)
	seen := make(map[string]struct{}, len(raw))
	for _, n := range raw {
		seen[canonicalize(n)] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for n := range seen {
		result = append(result, n)
	}
	sort.Strings(result)
	return result
}

// FeedItem is a recent observation for the Live Feed panel.
type FeedItem struct {
	ID      int
	Title   string
	Type    string
	Project string // canonical name
	Age     string
}

// SourceStat holds aggregated counts for one ingestion source.
type SourceStat struct {
	Name    string
	Sub     string
	IconKey string // "github" | "discord" | "claude"
	Count   int
}

// OverviewData bundles all data needed by the overview/home page.
type OverviewData struct {
	Projects       []ProjectStats
	TotalMemories  int
	TotalProjects  int
	LastSync       string   // human-readable age, e.g. "2 min ago"
	LastSyncSource string   // e.g. "github"
	ByType         []TypeCount
	LiveFeed       []FeedItem
	Sources        []SourceStat
	EngUp          bool
}

// isFresh returns true when the updated_at timestamp is within 30 days.
func isFresh(updatedAt string) bool {
	if updatedAt == "" {
		return false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", updatedAt, time.UTC)
	if err != nil {
		return false
	}
	return time.Since(t) <= 30*24*time.Hour
}

// typeColor returns a CSS hex color for a given observation type label.
func typeColor(t string) string {
	switch t {
	case "architecture":
		return "#22d3ee"
	case "decision":
		return "#38bdf8"
	case "bugfix":
		return "#60a5fa"
	case "discovery":
		return "#818cf8"
	case "config":
		return "#a78bfa"
	case "pattern":
		return "#c084fc"
	default:
		return "#8a9ab5"
	}
}

// knownProjects returns the deduplicated, sorted set of Engram project names
// that the dashboard should display. The set is the UNION of:
//   - the hard-coded "omnia" default
//   - cfg.Projects (explicit list from config yaml or --projects flag)
//   - all routing targets from cfg.Routes (values of the routes map)
//
// Empty strings are dropped. The result is always sorted alphabetically.
// status is kept as a parameter for future use (e.g. cursor-derived projects).
func knownProjects(status SyncStatus, cfg Config) []string {
	seen := map[string]struct{}{"omnia": {}}

	// From explicit projects list.
	for _, p := range cfg.Projects {
		p = strings.TrimSpace(p)
		if p != "" {
			seen[p] = struct{}{}
		}
	}

	// From routing targets.
	for _, target := range cfg.Routes {
		target = strings.TrimSpace(target)
		if target != "" {
			seen[target] = struct{}{}
		}
	}

	// status.Cursors gives us source:repo keys, but without config we can't map
	// those to project names — so we leave them for a future improvement.
	_ = status

	var projects []string
	for p := range seen {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	return projects
}
