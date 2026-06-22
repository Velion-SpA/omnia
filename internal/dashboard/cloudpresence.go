package dashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/engramdb"
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

// dashboardClouds is the read-only view of cloud.json the overview needs: the set
// of currently-configured cloud aliases and which one is the default.
type dashboardClouds struct {
	aliases      map[string]struct{}
	defaultAlias string
}

// cloudsByProject derives, per canonical project, the display names of the clouds
// it is loaded on. The second return value reports whether cloud placement is
// resolvable on this surface at all: it is false on the cloud dashboard, when the
// structural backend cannot enumerate sync targets, or when cloud.json is absent
// or unreadable — in which case the Projects panel shows no cloud pills.
//
// Mapping mechanism (all read-only):
//   - Each connected cloud alias A in cloud.json owns per-project target keys of
//     the form "A:<normalized-project>" (see cmd/omnia cloudnFor).
//   - The local store records a sync target key for a project once it has actually
//     replicated with that cloud (sync_chunks / healthy sync_state).
//   - We split each recorded target key into (alias, project): a known alias maps
//     to itself; the legacy "cloud" prefix maps to the configured default cloud.
//   - The project segment is canonicalized so it lines up with the overview rows.
func (s *Server) cloudsByProject(ctx context.Context, canonicalize func(string) string) (map[string][]string, bool) {
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

	sets := map[string]map[string]struct{}{}
	for tk := range targetKeys {
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
		if sets[canon] == nil {
			sets[canon] = map[string]struct{}{}
		}
		sets[canon][display] = struct{}{}
	}

	out := make(map[string][]string, len(sets))
	for proj, set := range sets {
		out[proj] = sortedKeys(set)
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

// sortedKeys returns the keys of set sorted ascending.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeCloudNames unions two cloud-name slices into a sorted, deduplicated slice.
func mergeCloudNames(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	set := make(map[string]struct{}, len(a)+len(b))
	for _, n := range a {
		set[n] = struct{}{}
	}
	for _, n := range b {
		set[n] = struct{}{}
	}
	return sortedKeys(set)
}
