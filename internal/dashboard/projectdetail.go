package dashboard

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/velion/omnia/internal/ui"
)

// maxProjectDetailMemories caps the project-detail memory grid at the newest
// N observations (Command Center v2, Slice 4c — mockup view ⑥). The full,
// unbounded list stays one click away via the "View all in Browse" link
// (BrowseURL), so this page stays a fast landing/summary view rather than a
// second copy of Browse.
const maxProjectDetailMemories = 12

// ProjectDetailData bundles everything the project-detail landing page needs
// to render: project identity, aggregate stats, and a capped newest-first
// slice of the project's own memories for the shared card grid.
//
// Explicitly NOT included (out of scope for this slice, see task boundaries):
//   - "who has access" — that's cloud-RBAC-only and already lives on the
//     Slice 4b Admin Projects page (internal/cloud/cloudserver). Plumbing
//     cloud RBAC into the shared internal/dashboard package would break the
//     local (no-accounts) dashboard that also mounts this same Server.
//   - sub-project linking/rollup (Slice 5).
type ProjectDetailData struct {
	// Name is the canonicalized project name (lowercase+trim, alias-
	// resolved), matching the display convention Overview/Browse already
	// use for ProjectStats.Name and the ?project= param. Empty when Found
	// is false.
	Name string
	// Found is false only when the path segment resolved to an empty name
	// (missing/blank/undecodable project) — the page still renders a
	// friendly empty state instead of erroring or issuing a store query
	// with an empty search term. A syntactically valid but unknown project
	// name is still Found=true with zero-value stats; that case is already
	// a graceful empty state via ui.MemoryFeed's own "No results" block, so
	// it needs no special handling here.
	Found bool
	// Stats reuses the SAME per-project aggregation handleOverview already
	// computes (computeProjectStats) — Total is the header's memory count.
	Stats ProjectStats
	// SourceCount is the distinct-source count for the header stat (see
	// distinctSourceCount).
	SourceCount int
	// LastActivity is Stats.LatestUpdateAt formatted via ui.RelativeTime,
	// "" when the project has no observations.
	LastActivity string
	// Memories is capped to maxProjectDetailMemories, newest-first. Both
	// store paths already return newest-first (browseFromDB's underlying
	// engramdb query is `ORDER BY updated_at DESC`; loadProjectObs sorts
	// explicitly), so no additional sort is needed here.
	Memories []ObsView
	// BrowseURL is the "View all in Browse" link, scoped to this project.
	BrowseURL string
}

// handleProjectDetail renders the project-detail landing page (Command
// Center v2, Slice 4c — "entering a project" stops meaning
// /browse?project=X and gets a real home page: identity, stats, and its own
// recent memories). The {name} path segment is URL-encoded by every caller
// that links here (projectDetailURL) — decode it defensively as well, since
// this route can also be reached directly via a bookmarked/typed URL.
func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.PathValue("name")
	name, err := url.PathUnescape(raw)
	if err != nil {
		// Malformed escape sequence — fall back to the raw segment rather
		// than failing the request; buildProjectDetailData degrades
		// gracefully if this still matches nothing.
		name = raw
	}

	data := s.buildProjectDetailData(ctx, name)
	if rErr := projectDetailPage(data).Render(ctx, w); rErr != nil {
		s.logger.Error("render project detail", "err", rErr)
	}
}

// buildProjectDetailData assembles ProjectDetailData for a (possibly empty)
// project name. It REUSES the same store queries Browse and Overview
// already use — browseFromDB (DB path, exactly Browse's project-scoped
// query) and loadProjectObs (FTS/curated fallback, the same helper
// handleOverview's non-DB path and Browse's non-DB path both already call)
// — no new store queries are introduced for this slice.
func (s *Server) buildProjectDetailData(ctx context.Context, rawName string) ProjectDetailData {
	name := strings.TrimSpace(rawName)
	data := ProjectDetailData{Found: name != ""}
	if !data.Found {
		// No project identifier at all — don't issue a store query with an
		// empty search term (Engram's /search requires non-empty q); just
		// render the friendly empty state.
		return data
	}

	// Canonicalize like every other project identifier in this package
	// (Overview's ProjectStats.Name, Browse's ?project= param) so an
	// aliased or differently-cased raw name — e.g. a memory card's raw
	// project chip, which is NOT canonicalized — still resolves to the
	// same project page as the canonical Overview/Browse links.
	canonicalize := canonicalizerFunc(s.cfg.ProjectAliases)
	name = canonicalize(name)
	data.Name = name
	data.BrowseURL = buildBrowseURL(BrowseParams{Project: name})

	var views []ObsView
	if s.db != nil {
		if v, _, ok := s.browseFromDB(ctx, BrowseParams{Project: name}); ok {
			views = v
		}
	}
	if views == nil {
		v, err := loadProjectObs(ctx, s.records, name, 300)
		if err != nil {
			s.logger.Warn("project detail load failed", "project", name, "err", err)
		} else {
			views = v
		}
	}

	data.Stats = computeProjectStats(name, views)
	data.SourceCount = distinctSourceCount(data.Stats)
	if t, ok := parseEngramTimestamp(data.Stats.LatestUpdateAt); ok {
		data.LastActivity = ui.RelativeTime(t)
	}

	if len(views) > maxProjectDetailMemories {
		views = views[:maxProjectDetailMemories]
	}
	data.Memories = views
	return data
}

// distinctSourceCount counts the distinct ingestion "source" buckets for a
// project's header stat: one per non-zero entry in Stats.BySource (github /
// discord / jira / whatsapp — parsed from the omnia-meta block), PLUS one
// more if the project has any curated (non-ingested, no omnia-meta)
// observations. Curated observations have no Meta.Source to bucket by, so
// they're counted as a single implicit "manual" source — matching both the
// mockup's "Fuentes" breakdown (GitHub / Manual / Claude Code) and
// ui.MemoryCard's own "Manual" fallback label for non-ingested cards.
func distinctSourceCount(stats ProjectStats) int {
	n := 0
	for _, count := range stats.BySource {
		if count > 0 {
			n++
		}
	}
	if stats.Curated > 0 {
		n++
	}
	return n
}

// parseEngramTimestamp parses an Engram timestamp string ("2006-01-02
// 15:04:05", stored UTC without a zone suffix) into a time.Time. This
// mirrors the parsing already done ad hoc in isFresh (data.go) and formatAge
// (engram.go); kept as its own small helper rather than a third inline copy
// since this is the one call site that needs an actual time.Time (to hand to
// ui.RelativeTime, per the task's explicit instruction) instead of a
// pre-formatted string.
func parseEngramTimestamp(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// projectDetailURL returns the URL-escaped landing-page link for a project
// name (Command Center v2, Slice 4c). Empty name returns "" so callers (the
// Overview project row, Browse's active-project filter chip) can skip
// rendering a link entirely instead of linking to a broken /project/ path.
func projectDetailURL(name string) string {
	if name == "" {
		return ""
	}
	return "/project/" + url.PathEscape(name)
}
