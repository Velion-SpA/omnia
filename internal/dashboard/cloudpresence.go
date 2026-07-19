package dashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/engramdb"
	"github.com/velion/omnia/internal/ui/i18n"
)

// defaultCloudTargetPrefix mirrors store.DefaultSyncTargetKey ("cloud"). Legacy
// and single-cloud sync records this prefix for every per-project target
// ("cloud:<project>"), so a "cloud:"-prefixed target maps to whichever cloud is
// currently the default in cloud.json.
const defaultCloudTargetPrefix = "cloud"

// cloudTargetKeyReader is the read-only capability the local structural backend
// (*engramdb.DB) provides to enumerate cloud sync targets. The cloud dashboard's
// structural reader does NOT implement it, so a type assertion there fails and
// the overview renders without cloud pills — which is correct, because on the
// cloud surface every project lives on that one cloud by definition.
type cloudTargetKeyReader interface {
	CloudTargetKeys(ctx context.Context) (map[string]struct{}, error)
}

// syncStateReader is the read-only capability the local structural backend
// (*engramdb.DB) provides to enumerate per-target sync health (lifecycle,
// reason code/message, last error). Mirrors cloudTargetKeyReader: the cloud
// dashboard's structural reader does NOT implement it either, so a type
// assertion there fails and both the overview pills and the /sync page degrade
// to the pre-OBL-12 behavior (healthy-only pills, no health table) — correct,
// because on the cloud surface there is no local sync_state to read.
type syncStateReader interface {
	SyncStates(ctx context.Context) ([]engramdb.SyncTargetState, error)
}

// CloudPillStatus classifies a cloud placement pill for rendering: healthy
// (green), pending/running (amber), or degraded/blocked (red). Every sync_state
// lifecycle other than "healthy" and "degraded" (idle, pending, running, or an
// unrecognized future value) is treated as "pending" — a conservative "not yet
// confirmed healthy" signal — EXCEPT that a non-empty reason_code always forces
// "degraded", since a reason code only appears on a blocked/failed attempt.
type CloudPillStatus string

const (
	CloudPillHealthy  CloudPillStatus = "healthy"
	CloudPillPending  CloudPillStatus = "pending"
	CloudPillDegraded CloudPillStatus = "degraded"
)

// CloudPlacement is one cloud badge for a project: the display alias plus
// enough health context (status + reason code) to render green/amber/red
// instead of letting a degraded/blocked target with zero chunks silently
// disappear from the overview (it would otherwise look identical to a project
// that was never configured for that cloud at all).
type CloudPlacement struct {
	Name       string // display cloud alias, e.g. "work"
	Status     CloudPillStatus
	ReasonCode string // "" when none recorded
}

// classifySyncLifecycle maps a raw sync_state lifecycle + reason_code to a
// pill status. Checked in this order: reason_code always wins (it is only ever
// set alongside a blocked/failed attempt, see store.MarkSyncBlocked/Failure);
// otherwise "healthy"/"degraded" map directly; anything else (idle, pending,
// running, or an unrecognized future lifecycle) is "pending".
func classifySyncLifecycle(lifecycle, reasonCode string) CloudPillStatus {
	if strings.TrimSpace(reasonCode) != "" {
		return CloudPillDegraded
	}
	switch strings.TrimSpace(lifecycle) {
	case "healthy":
		return CloudPillHealthy
	case "degraded":
		return CloudPillDegraded
	default:
		return CloudPillPending
	}
}

// cloudPlacementSeverity ranks a pill status so mergeCloudPlacements (group
// parent + children) keeps the WORST status for a given cloud name — an
// operator must see that some part of the group is unhealthy, not the best case.
func cloudPlacementSeverity(status CloudPillStatus) int {
	switch status {
	case CloudPillDegraded:
		return 2
	case CloudPillPending:
		return 1
	default:
		return 0
	}
}

// dashboardClouds is the read-only view of cloud.json the overview needs: the set
// of currently-configured cloud aliases and which one is the default.
type dashboardClouds struct {
	aliases      map[string]struct{}
	defaultAlias string
}

// cloudsByProject derives, per canonical project, the clouds it is loaded on
// PLUS each one's current health (CloudPlacement). The second return value
// reports whether cloud placement is resolvable on this surface at all: it is
// false on the cloud dashboard, when the structural backend cannot enumerate
// cloud sync targets at all, or when cloud.json is absent or unreadable — in
// which case the Projects panel shows no cloud pills.
//
// Mapping mechanism (all read-only):
//   - Each connected cloud alias A in cloud.json owns per-project target keys of
//     the form "A:<normalized-project>" (see cmd/omnia cloudnFor).
//   - CloudTargetKeys decides WHICH targets exist when sync_state detail is
//     unavailable (fallback for a structural reader that implements only
//     CloudTargetKeys, e.g. in tests) — every key it returns renders healthy,
//     preserving the original pre-OBL-12 behavior exactly.
//   - When the backend ALSO implements syncStateReader (the real *engramdb.DB
//     always does), SyncStates supplies the authoritative current lifecycle +
//     reason_code for every target it knows about — including one CloudTargetKeys
//     excludes on purpose (a degraded target with zero chunks) — and that
//     classification wins, so a currently-unhealthy target never reads as a
//     stale green pill.
//   - We split each recorded target key into (alias, project): a known alias maps
//     to itself; the legacy "cloud" prefix maps to the configured default cloud.
//   - The project segment is canonicalized so it lines up with the overview rows.
func (s *Server) cloudsByProject(ctx context.Context, canonicalize func(string) string) (map[string][]CloudPlacement, bool) {
	reader, ok := s.db.(cloudTargetKeyReader)
	if !ok {
		return nil, false
	}
	clouds := loadDashboardClouds(s.cfg.EngramDataDir)
	if len(clouds.aliases) == 0 {
		return nil, false
	}
	targetKeys, err := reader.CloudTargetKeys(ctx)
	if err != nil {
		return nil, false
	}

	// sync_state gives per-target lifecycle/reason detail (including degraded
	// targets with zero chunks, which CloudTargetKeys excludes by design). When
	// unavailable, targetKeys alone still decides which targets exist and every
	// one of them renders healthy — unchanged from pre-OBL-12 behavior.
	states := map[string]engramdb.SyncTargetState{}
	if sr, ok := s.db.(syncStateReader); ok {
		if rows, err := sr.SyncStates(ctx); err == nil {
			for _, row := range rows {
				states[row.TargetKey] = row
			}
		}
	}

	allKeys := make(map[string]struct{}, len(targetKeys)+len(states))
	for tk := range targetKeys {
		allKeys[tk] = struct{}{}
	}
	for tk := range states {
		allKeys[tk] = struct{}{}
	}

	sets := map[string]map[string]CloudPlacement{}
	for tk := range allKeys {
		alias, project, ok := splitCloudTargetKey(tk)
		if !ok {
			continue
		}
		display, ok := clouds.displayName(alias)
		if !ok {
			continue // alias for a cloud that is no longer configured
		}
		canon := canonicalize(project)
		if canon == "" {
			continue
		}

		placement := CloudPlacement{Name: display, Status: CloudPillHealthy}
		if st, known := states[tk]; known {
			placement.Status = classifySyncLifecycle(st.Lifecycle, st.ReasonCode)
			placement.ReasonCode = st.ReasonCode
		}

		if sets[canon] == nil {
			sets[canon] = map[string]CloudPlacement{}
		}
		if existing, has := sets[canon][display]; has && cloudPlacementSeverity(existing.Status) >= cloudPlacementSeverity(placement.Status) {
			continue // keep the worse-or-equal status already recorded for this alias
		}
		sets[canon][display] = placement
	}

	out := make(map[string][]CloudPlacement, len(sets))
	for proj, set := range sets {
		list := make([]CloudPlacement, 0, len(set))
		for _, p := range set {
			list = append(list, p)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		out[proj] = list
	}
	return out, true
}

// displayName resolves a recorded target-key alias to the cloud name to show.
// A configured alias maps to itself; the legacy "cloud" prefix maps to the
// configured default cloud (falling back to "cloud" itself when it is configured).
func (c dashboardClouds) displayName(alias string) (string, bool) {
	if _, known := c.aliases[alias]; known {
		return alias, true
	}
	if alias == defaultCloudTargetPrefix && c.defaultAlias != "" {
		return c.defaultAlias, true
	}
	return "", false
}

// loadDashboardClouds reads cloud.json from the Engram data dir (read-only) and
// returns the configured aliases + default. It mirrors loadCloudConfigV2: a v2
// file exposes its "clouds" map and "default"; a v1 file (top-level
// server_url/token) is treated as the single alias "cloud". Any read/parse
// failure yields an empty result so the overview degrades to no cloud pills.
func loadDashboardClouds(dataDir string) dashboardClouds {
	dir := engramdb.ResolveDataDir(dataDir)
	raw, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		return dashboardClouds{}
	}

	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return dashboardClouds{}
	}

	aliases := map[string]struct{}{}
	defaultAlias := ""
	if _, hasV2 := probe["clouds"]; hasV2 {
		var v2 struct {
			Clouds  map[string]json.RawMessage `json:"clouds"`
			Default string                     `json:"default"`
		}
		if json.Unmarshal(raw, &v2) != nil {
			return dashboardClouds{}
		}
		for alias := range v2.Clouds {
			if alias = strings.TrimSpace(alias); alias != "" {
				aliases[alias] = struct{}{}
			}
		}
		defaultAlias = strings.TrimSpace(v2.Default)
	} else {
		// v1 migration: a single unnamed cloud is alias "cloud".
		aliases[defaultCloudTargetPrefix] = struct{}{}
		defaultAlias = defaultCloudTargetPrefix
	}

	// A single configured cloud is implicitly the default (mirrors defaultCloudEntry).
	if defaultAlias == "" && len(aliases) == 1 {
		for alias := range aliases {
			defaultAlias = alias
		}
	}
	// Only honour a default that actually exists.
	if defaultAlias != "" {
		if _, ok := aliases[defaultAlias]; !ok {
			defaultAlias = ""
		}
	}

	return dashboardClouds{aliases: aliases, defaultAlias: defaultAlias}
}

// splitCloudTargetKey splits "alias:project" into its parts. Bare keys with no
// project segment (e.g. "cloud", "local") return ok=false and are ignored.
func splitCloudTargetKey(targetKey string) (alias, project string, ok bool) {
	parts := strings.SplitN(targetKey, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	alias = strings.TrimSpace(parts[0])
	project = strings.TrimSpace(parts[1])
	if alias == "" || project == "" {
		return "", "", false
	}
	return alias, project, true
}

// mergeCloudPlacements unions two cloud-placement slices (a group parent's own
// clouds and a child's) into a sorted, deduplicated-by-name slice. When both
// sides carry a placement for the same cloud name, the WORSE status wins (see
// cloudPlacementSeverity) — a group parent must surface a child's degraded
// target, not hide it behind the parent's own healthy one.
func mergeCloudPlacements(a, b []CloudPlacement) []CloudPlacement {
	if len(b) == 0 {
		return a
	}
	set := make(map[string]CloudPlacement, len(a)+len(b))
	for _, p := range a {
		set[p.Name] = p
	}
	for _, p := range b {
		if existing, ok := set[p.Name]; !ok || cloudPlacementSeverity(p.Status) > cloudPlacementSeverity(existing.Status) {
			set[p.Name] = p
		}
	}
	out := make([]CloudPlacement, 0, len(set))
	for _, p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// syncTargetViews returns every recorded cloud sync target as a display row
// for the /sync page's health table, resolving each target key's alias to a
// cloud.json display name (falling back to the raw alias when cloud.json
// doesn't recognize it, e.g. a removed cloud) so a target is never dropped
// silently. Returns nil when the structural backend cannot read sync_state
// (e.g. the cloud dashboard's backend, which intentionally doesn't implement
// it) or when there is nothing recorded yet.
func (s *Server) syncTargetViews(ctx context.Context) []SyncTargetView {
	lang := i18n.LangFrom(ctx)
	reader, ok := s.db.(syncStateReader)
	if !ok {
		return nil
	}
	states, err := reader.SyncStates(ctx)
	if err != nil || len(states) == 0 {
		return nil
	}
	clouds := loadDashboardClouds(s.cfg.EngramDataDir)

	out := make([]SyncTargetView, 0, len(states))
	for _, st := range states {
		alias, project, ok := splitCloudTargetKey(st.TargetKey)
		if !ok {
			// Bare target key (no project segment) — show the raw key as the
			// cloud column rather than dropping the row.
			alias, project = st.TargetKey, ""
		}
		display, ok := clouds.displayName(alias)
		if !ok {
			display = alias // unconfigured/removed cloud — show the raw alias, not nothing
		}
		out = append(out, SyncTargetView{
			Cloud:         display,
			Project:       project,
			Lifecycle:     st.Lifecycle,
			ReasonCode:    st.ReasonCode,
			ReasonMessage: st.ReasonMessage,
			LastError:     st.LastError,
			Age:           formatAge(st.UpdatedAt, lang),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cloud != out[j].Cloud {
			return out[i].Cloud < out[j].Cloud
		}
		return out[i].Project < out[j].Project
	})
	return out
}
