package dashboard

import (
	"context"
	"sort"

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

// knownProjects returns the set of known Engram projects that have Omnia data.
// Since Engram has no "list projects" endpoint, we derive the list from the
// state.json cursors (each cursor's project = the routing target) plus a default
// "omnia" project. Callers may augment this with config if available.
func knownProjects(status SyncStatus) []string {
	seen := map[string]struct{}{"omnia": {}}
	for _, c := range status.Cursors {
		// The cursor key tells us source:repo but NOT the project name.
		// Without config, we can't recover the project from the cursor alone.
		// We include "omnia" as the canonical fallback and note this limitation.
		_ = c
	}
	var projects []string
	for p := range seen {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	return projects
}
