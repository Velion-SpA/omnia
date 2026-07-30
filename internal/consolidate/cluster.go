// Package consolidate builds opt-in digest candidates from the existing semantic graph.
package consolidate

import (
	"github.com/velion/omnia/internal/embed"
	"sort"
)

// Clusters returns qualifying connected components. Oversized components are split
// deterministically, retaining every source rather than silently truncating one.
func Clusters(nodes []embed.GraphNode, edges []embed.GraphEdge, minScore float32, k, minSize, maxSize int) [][]embed.GraphNode {
	byID := map[int]embed.GraphNode{}
	parent := map[int]int{}
	degree := map[int]int{}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		a, b = find(a), find(b)
		if a != b {
			parent[b] = a
		}
	}
	for _, n := range nodes {
		byID[n.ObsID] = n
		parent[n.ObsID] = n.ObsID
	}
	for _, e := range edges {
		if e.Weight >= minScore {
			if _, ok := parent[e.Source]; ok {
				if _, ok := parent[e.Target]; ok {
					union(e.Source, e.Target)
					degree[e.Source]++
					degree[e.Target]++
				}
			}
		}
	}
	groups := map[int][]embed.GraphNode{}
	for _, n := range nodes {
		groups[find(n.ObsID)] = append(groups[find(n.ObsID)], n)
	}
	var out [][]embed.GraphNode
	for _, group := range groups {
		if len(group) < minSize {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			if degree[group[i].ObsID] == degree[group[j].ObsID] {
				return group[i].ObsID < group[j].ObsID
			}
			return degree[group[i].ObsID] > degree[group[j].ObsID]
		})
		if maxSize <= 0 || len(group) <= maxSize {
			out = append(out, group)
			continue
		}
		// Split into balanced chunks so the final remainder never falls below
		// minSize and silently loses source pointers.
		parts := (len(group) + maxSize - 1) / maxSize
		if len(group)/parts < minSize {
			out = append(out, group) // retaining sources beats truncation.
			continue
		}
		base, extra := len(group)/parts, len(group)%parts
		for start, i := 0, 0; i < parts; i++ {
			size := base
			if i < extra {
				size++
			}
			out = append(out, group[start:start+size])
			start += size
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].ObsID < out[j][0].ObsID })
	return out
}
