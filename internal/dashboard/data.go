package dashboard

import (
	"context"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/meta"
)

// ObsView is a single observation enriched with parsed meta for UI display.
type ObsView struct {
	Obs     Observation
	Meta    meta.Meta
	HasMeta bool   // false → curated (no valid omnia-meta block)
	Age     string // human-readable updated_at age
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
	}
	return stats
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
