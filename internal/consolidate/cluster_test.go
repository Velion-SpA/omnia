package consolidate

import (
	"github.com/velion/omnia/internal/embed"
	"testing"
)

func TestClustersUnionConnectedEdges(t *testing.T) {
	nodes := []embed.GraphNode{{ObsID: 1}, {ObsID: 2}, {ObsID: 3}}
	edges := []embed.GraphEdge{{Source: 1, Target: 2, Weight: .8}, {Source: 2, Target: 3, Weight: .7}}
	got := Clusters(nodes, edges, .5, 8, 3, 20)
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("got %#v", got)
	}
}
func TestClustersRespectBoundsWithoutDroppingSources(t *testing.T) {
	nodes := make([]embed.GraphNode, 40)
	edges := make([]embed.GraphEdge, 0, 39)
	for i := range nodes {
		nodes[i].ObsID = i + 1
		if i > 0 {
			edges = append(edges, embed.GraphEdge{Source: i, Target: i + 1, Weight: .9})
		}
	}
	got := Clusters(nodes, edges, .5, 8, 3, 20)
	seen := map[int]bool{}
	for _, c := range got {
		for _, n := range c {
			seen[n.ObsID] = true
		}
	}
	if len(seen) != 40 {
		t.Fatalf("dropped sources: %d", len(seen))
	}
}
func TestClustersSplitWithoutDroppingSmallRemainder(t *testing.T) {
 nodes:=make([]embed.GraphNode,21); edges:=make([]embed.GraphEdge,0,20)
 for i:=range nodes { nodes[i].ObsID=i+1; if i>0 {edges=append(edges,embed.GraphEdge{Source:i,Target:i+1,Weight:.9})} }
 got:=Clusters(nodes,edges,.5,8,3,20); seen:=map[int]bool{}
 for _,c:=range got { for _,n:=range c {seen[n.ObsID]=true} }
 if len(seen)!=21 {t.Fatalf("dropped sources: %d",len(seen))}
}
