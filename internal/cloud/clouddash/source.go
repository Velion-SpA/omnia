package clouddash

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/dashboard"
	"github.com/velion/omnia/internal/engramdb"
)

// CloudStore is the slice of *cloudstore.CloudStore the dashboard adapter needs.
// Both methods return the FULL replicated set (no per-account filtering); the
// adapter applies the request scope on top, exactly as the previous cloud
// dashboard did via filterRowsByScope.
type CloudStore interface {
	ListProjects(query string) ([]cloudstore.DashboardProjectRow, error)
	ListRecentObservations(project, query string, limit int) ([]cloudstore.DashboardObservationRow, error)
}

// Source adapts the cloud replicated store to dashboard.DataSource. It is
// read-only (the cloud serves replicated chunk data; edits happen on the origin
// engram) and has no embeddings layer, so semantic search degrades to substring
// search and the graph page renders its unavailable state.
type Source struct {
	store CloudStore
}

// New builds a cloud DataSource over the given store.
func New(store CloudStore) *Source { return &Source{store: store} }

var _ dashboard.DataSource = (*Source)(nil)

// Health always reports healthy: the store is in-process, so there is no remote
// engine to probe. This drives the overview "engine up" pill.
func (s *Source) Health(context.Context) error { return nil }

func (s *Source) Records() dashboard.RecordReader { return cloudRecords{s} }

func (s *Source) Structural() (dashboard.StructuralReader, bool) { return cloudStructural{s}, true }

// Semantic is unavailable: the cloud has no embeddings store. Browse falls back to
// substring search and the graph page renders a clear unavailable state.
func (s *Source) Semantic() (dashboard.SemanticIndex, bool) { return nil, false }

// Mutations is unavailable: the cloud dashboard is a read-only view over
// replicated data. Edit/delete surface a clear "read-only" message.
func (s *Source) Mutations() (dashboard.MutationWriter, bool) { return nil, false }

func (s *Source) Close() error { return nil }

// scopedObservations returns every replicated observation visible to the
// request's scope, most-recent first (the store already orders them). It is the
// single choke point that enforces per-account isolation for record reads.
func (s *Source) scopedObservations(ctx context.Context) ([]cloudstore.DashboardObservationRow, error) {
	// limit 0 ⇒ no truncation: return the full visible set so callers can filter
	// and cap themselves. The store memoises its read model, so repeated calls
	// across a single page render are cheap.
	rows, err := s.store.ListRecentObservations("", "", 0)
	if err != nil {
		return nil, err
	}
	sc := scopeFrom(ctx)
	if sc.All {
		return rows, nil
	}
	out := make([]cloudstore.DashboardObservationRow, 0, len(rows))
	for _, r := range rows {
		if sc.CanView(r.Project) {
			out = append(out, r)
		}
	}
	return out, nil
}

// projects returns visible per-project counts. When canon is non-nil the project
// names are canonicalised and counts merged, mirroring engramdb.ProjectsCanonical.
func (s *Source) projects(ctx context.Context, canon func(string) string) ([]engramdb.ProjectCount, error) {
	rows, err := s.store.ListProjects("")
	if err != nil {
		return nil, err
	}
	sc := scopeFrom(ctx)
	agg := make(map[string]int, len(rows))
	for _, r := range rows {
		if !sc.CanView(r.Project) {
			continue
		}
		name := r.Project
		if canon != nil {
			name = canon(r.Project)
		}
		agg[name] += r.Observations
	}
	out := make([]engramdb.ProjectCount, 0, len(agg))
	for name, count := range agg {
		out = append(out, engramdb.ProjectCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ─── RecordReader ────────────────────────────────────────────────────────────

type cloudRecords struct{ s *Source }

// GetObservation resolves the synthetic int id back to a replicated row. Because
// it only scans the scoped set, a cross-account id resolves to "not found" — the
// dashboard renders a 404, never another account's memory.
func (c cloudRecords) GetObservation(ctx context.Context, id int) (*dashboard.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if syncIDToInt(r.SyncID) == id {
			obs := rowToDashboardObs(r)
			return &obs, nil
		}
	}
	return nil, fmt.Errorf("observation %d not found", id)
}

// Search is a substring fallback over the scoped rows (the cloud has no FTS or
// embeddings index). It matches title, content, project, topic key and type.
func (c cloudRecords) Search(ctx context.Context, query, project string, limit int) ([]dashboard.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	project = strings.TrimSpace(project)
	out := make([]dashboard.Observation, 0, len(rows))
	for _, r := range rows {
		if project != "" && !projectMatches(r.Project, project) {
			continue
		}
		if q != "" && !rowMatchesQuery(r, q) {
			continue
		}
		out = append(out, rowToDashboardObs(r))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ─── StructuralReader ────────────────────────────────────────────────────────

type cloudStructural struct{ s *Source }

func (c cloudStructural) List(ctx context.Context, f engramdb.Filter) ([]engramdb.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engramdb.Observation, 0, len(rows))
	for _, r := range rows {
		if filterMatches(r, f) {
			out = append(out, rowToEngramdbObs(r))
		}
	}
	// Apply offset/limit, mirroring engramdb.List (Limit defaults to 200 when 0).
	off := f.Offset
	if off < 0 {
		off = 0
	}
	if off >= len(out) {
		return []engramdb.Observation{}, nil
	}
	out = out[off:]
	lim := f.Limit
	if lim <= 0 {
		lim = 200
	}
	if len(out) > lim {
		out = out[:lim]
	}
	return out, nil
}

func (c cloudStructural) ListByIDs(ctx context.Context, ids []int) ([]engramdb.Observation, error) {
	want := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engramdb.Observation, 0, len(ids))
	for _, r := range rows {
		if _, ok := want[syncIDToInt(r.SyncID)]; ok {
			out = append(out, rowToEngramdbObs(r))
		}
	}
	return out, nil
}

func (c cloudStructural) Projects(ctx context.Context) ([]engramdb.ProjectCount, error) {
	return c.s.projects(ctx, nil)
}

func (c cloudStructural) ProjectsCanonical(ctx context.Context, canonicalize func(string) string) ([]engramdb.ProjectCount, error) {
	return c.s.projects(ctx, canonicalize)
}

func (c cloudStructural) Types(ctx context.Context) ([]engramdb.TypeCount, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, r := range rows {
		t := strings.TrimSpace(r.Type)
		if t != "" {
			counts[t]++
		}
	}
	out := make([]engramdb.TypeCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, engramdb.TypeCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ─── filtering + mapping helpers ─────────────────────────────────────────────

func filterMatches(r cloudstore.DashboardObservationRow, f engramdb.Filter) bool {
	if f.Type != "" && r.Type != f.Type {
		return false
	}
	switch {
	case len(f.RawProjects) > 0:
		matched := false
		for _, p := range f.RawProjects {
			if r.Project == p {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	case f.CanonicalProject != "":
		// engramdb matches LOWER(TRIM(project)) = LOWER(TRIM(?)).
		if !strings.EqualFold(strings.TrimSpace(r.Project), strings.TrimSpace(f.CanonicalProject)) {
			return false
		}
	case f.Project != "":
		if r.Project != f.Project {
			return false
		}
	}
	return true
}

func projectMatches(rowProject, want string) bool {
	return strings.EqualFold(strings.TrimSpace(rowProject), want)
}

func rowMatchesQuery(r cloudstore.DashboardObservationRow, q string) bool {
	return strings.Contains(strings.ToLower(r.Title), q) ||
		strings.Contains(strings.ToLower(r.Content), q) ||
		strings.Contains(strings.ToLower(r.Project), q) ||
		strings.Contains(strings.ToLower(r.TopicKey), q) ||
		strings.Contains(strings.ToLower(r.Type), q)
}

func rowToDashboardObs(r cloudstore.DashboardObservationRow) dashboard.Observation {
	return dashboard.Observation{
		ID:        syncIDToInt(r.SyncID),
		SyncID:    r.SyncID,
		SessionID: r.SessionID,
		Type:      r.Type,
		Title:     r.Title,
		Content:   r.Content,
		Project:   r.Project,
		TopicKey:  r.TopicKey,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.CreatedAt, // cloud rows expose only a creation timestamp
	}
}

func rowToEngramdbObs(r cloudstore.DashboardObservationRow) engramdb.Observation {
	return engramdb.Observation{
		ID:        syncIDToInt(r.SyncID),
		SyncID:    r.SyncID,
		Type:      r.Type,
		Title:     r.Title,
		Content:   r.Content,
		Project:   r.Project,
		TopicKey:  r.TopicKey,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.CreatedAt,
	}
}

// syncIDToInt derives a stable, positive 31-bit integer id from a row's sync id.
// The local dashboard keys /detail/{id} on engram's integer ids; cloud rows have
// only a composite (project/session/sync) identity. Hashing the sync id gives a
// deterministic id that round-trips through /detail/{id} and survives restarts
// without any persisted map. 31 bits keeps the value positive and arch-portable
// (fits int on 32-bit builds); collisions are astronomically unlikely at
// dashboard scale and at worst point two memories at the same detail link.
func syncIDToInt(syncID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(syncID))
	v := int(h.Sum64() & 0x7FFFFFFF)
	if v == 0 {
		v = 1
	}
	return v
}
