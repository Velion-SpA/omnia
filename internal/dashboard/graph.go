package dashboard

import (
	"sort"
	"strconv"

	"github.com/velion/omnia/internal/embed"
)

// Graph tuning defaults. k is the per-node neighbor cap; min is the minimum
// cosine similarity (== dot, since vectors are unit-normalized) for an edge.
const (
	defaultGraphK   = 6
	defaultGraphMin = 0.60
)

// GraphView is the data passed to graphPage. When Available is false the page
// renders a clear "semantic graph unavailable" state instead of a canvas.
type GraphView struct {
	Available bool
	Projects  []string // dropdown options (canonical project names)
	Project   string   // selected project filter ("" = all)
	K         int      // per-node neighbor cap in effect
	Min       float64  // similarity threshold in effect
	Total     int      // total stored memories (before isolated-node pruning)
	Connected int      // nodes rendered (degree >= 1 within the current view)
	EdgeCount int      // edges rendered
	// Payload is the node/edge data the D3 view parses. It is rendered into the
	// page via templ.JSONScript, whose encoder HTML-escapes <, >, & — safe to
	// embed inside a <script type="application/json"> block.
	Payload graphPayloadJSON
}

// graphPayloadJSON is the wire shape embedded in the page and parsed by D3.
type graphPayloadJSON struct {
	Nodes []graphNodeJSON `json:"nodes"`
	Edges []graphEdgeJSON `json:"edges"`
}

type graphNodeJSON struct {
	ID      int    `json:"id"` // obs_id — D3 forceLink id and /detail/{id} target
	Title   string `json:"title"`
	Project string `json:"project"` // canonical
	Type    string `json:"type"`
	Degree  int    `json:"degree"`
}

type graphEdgeJSON struct {
	Source int     `json:"source"` // obs_id
	Target int     `json:"target"` // obs_id
	Weight float32 `json:"weight"` // cosine similarity
}

// buildGraphView turns the raw store graph into a render-ready view: it
// canonicalizes node projects, drops hidden projects, optionally scopes to a
// selected project (plus its group children), re-derives degree within the
// surviving edge set, and drops isolated nodes so the canvas shows a readable
// cluster of CONNECTED memories rather than a field of floating singletons.
func (s *Server) buildGraphView(nodes []embed.GraphNode, edges []embed.GraphEdge, project string, projects []string, k int, minScore float64, total int) GraphView {
	canon := canonicalizerFunc(s.cfg.ProjectAliases)
	hidden := hiddenSet(s.cfg.ProjectHidden, s.cfg.ProjectAliases)

	// Allowed canonical-project set when a project filter is active (a group
	// parent includes its children, matching browse/semanticSearch scoping).
	var allowed map[string]struct{}
	if project != "" {
		allowed = map[string]struct{}{canon(project): {}}
		if s.groups.IsParent(project) {
			for _, child := range s.groups.Children(project) {
				allowed[canon(child)] = struct{}{}
			}
		}
	}

	// Keep map: obs_id -> node (project canonicalized, degree reset to recount
	// against the surviving edge set).
	kept := make(map[int]embed.GraphNode, len(nodes))
	for _, n := range nodes {
		cp := canon(n.Project)
		if _, isHidden := hidden[cp]; isHidden {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[cp]; !ok {
				continue
			}
		}
		n.Project = cp
		n.Degree = 0
		kept[n.ObsID] = n
	}

	// Keep edges whose BOTH endpoints survived; recount degree as we go.
	keptEdges := make([]embed.GraphEdge, 0, len(edges))
	for _, e := range edges {
		src, oks := kept[e.Source]
		dst, okt := kept[e.Target]
		if !oks || !okt {
			continue
		}
		src.Degree++
		dst.Degree++
		kept[e.Source] = src
		kept[e.Target] = dst
		keptEdges = append(keptEdges, e)
	}

	// Drop isolated nodes (no surviving edge) for readability.
	jnodes := make([]graphNodeJSON, 0, len(kept))
	for _, n := range kept {
		if n.Degree == 0 {
			continue
		}
		jnodes = append(jnodes, graphNodeJSON{
			ID:      n.ObsID,
			Title:   n.Title,
			Project: n.Project,
			Type:    n.Type,
			Degree:  n.Degree,
		})
	}
	// Stable order (degree desc, then id) → deterministic palette + legend.
	sort.Slice(jnodes, func(i, j int) bool {
		if jnodes[i].Degree != jnodes[j].Degree {
			return jnodes[i].Degree > jnodes[j].Degree
		}
		return jnodes[i].ID < jnodes[j].ID
	})

	jedges := make([]graphEdgeJSON, 0, len(keptEdges))
	for _, e := range keptEdges {
		jedges = append(jedges, graphEdgeJSON{Source: e.Source, Target: e.Target, Weight: e.Weight})
	}

	return GraphView{
		Available: true,
		Projects:  projects,
		Project:   project,
		K:         k,
		Min:       minScore,
		Total:     total,
		Connected: len(jnodes),
		EdgeCount: len(jedges),
		Payload:   graphPayloadJSON{Nodes: jnodes, Edges: jedges},
	}
}

// parseIntDefault parses s as an int, returning def when empty or invalid.
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// parseFloatDefault parses s as a float64, returning def when empty or invalid.
func parseFloatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return f
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
