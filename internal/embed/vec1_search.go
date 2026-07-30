package embed

import (
	"context"
	"sort"
	"strings"
)

// This file implements v0.4's `sqlite-vec-index` capability, Phase 3 slice
// (design capability 7 "Score parity" / "Production composition and PR
// ownership": "PR 3 alone owns KNN score conversion and read routing",
// spec REQ-462, REQ-463, REQ-465, REQ-466, REQ-468, REQ-469): the KNN read
// routing and score conversion for Search/SearchScoped/Graph/GraphScoped
// (store.go). Every function here is an additive, fail-safe attempt — the
// existing brute-force bodies in store.go remain the permanent fallback.

// vecUnboundedK is the Vec1 query-parameter K used for Search/SearchScoped's
// k<=0 ("all ranked hits") contract. Vec1 requires an explicit K (or a
// visible SQL LIMIT); this sentinel is comfortably larger than any real
// Omnia embeddings store, so it always returns every currently indexed row
// in one round trip without an extra COUNT(*) query.
const vecUnboundedK = 1 << 30

// vecScore converts a Vec1 flat/cos distance into Omnia's existing
// dot-product-equivalent score, per the pinned v0.35.2 contract locked by
// spec REQ-469: normalized self/orthogonal/antipodal vectors give scores of
// exactly 1, 0, and -1 — the PINNED bundled version computes `d = 1 -
// cosine`, NOT the newer Vec1 trunk's `d = 2 - cosine` semantics (verified
// empirically against the pinned dependency during implementation).
func vecScore(distance float64) float32 {
	return float32(1 - distance)
}

// vecKParam returns the Vec1 query-parameter K for a Search/SearchScoped
// call: k unchanged when positive, else the unbounded sentinel for the
// existing "k<=0 returns all ranked hits" contract.
func vecKParam(k int) int {
	if k > 0 {
		return k
	}
	return vecUnboundedK
}

// tryVecSearch attempts the additive Vec1 KNN path for Search/SearchScoped
// (spec REQ-462/465/466/469). ok=false means the caller MUST fall back to
// the existing brute-force scan unchanged — either because Vec1 isn't
// ready/healthy for this Store, the query dimension doesn't match the
// established active dimension (REQ-466), or a genuine Vec1 failure
// occurred (REQ-465, in which case Vec1 is marked unhealthy for the rest of
// this Store's life before falling back).
func (s *Store) tryVecSearch(ctx context.Context, query []float32, k int, project string) ([]Hit, bool) {
	if !s.vec.usable() || len(query) != s.vec.dim() {
		return nil, false
	}

	q := `
SELECT e.sync_id, e.obs_id, v.distance
FROM vec_embeddings(?, ?) AS v
JOIN embeddings AS e ON e.rowid = v.rowid`
	args := []any{encodeNativeVector(query), vecKParam(k)}
	if project != "" {
		q += ` WHERE v.project = ?`
		args = append(args, project)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		s.vec.markUnhealthy(err)
		return nil, false
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		var dist float64
		if err := rows.Scan(&h.SyncID, &h.ObsID, &dist); err != nil {
			s.vec.markUnhealthy(err)
			return nil, false
		}
		h.Score = vecScore(dist)
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		s.vec.markUnhealthy(err)
		return nil, false
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, true
}

// vec1Neighbor is a candidate edge endpoint discovered by tryVecGraph's
// per-node Vec1 KNN query below. This is deliberately a PRIVATE, Vec1-only
// type — it must never be shared with store.go's brute-force GraphScoped,
// which keeps its own self-contained neighbor/edge-selection logic on
// purpose (review remediation: "never edit the brute-force path, only wrap
// it in a new branch" is a harder constraint than avoiding duplication
// between the two paths; a future Vec1-only tuning change to this type or
// vec1FinishGraph must never be able to silently regress the brute-force
// fallback).
type vec1Neighbor struct {
	idx   int
	score float32
}

// vec1FinishGraph applies the top-k-per-node selection and undirected
// edge-dedup/degree/sort logic to tryVecGraph's per-node neighbor candidate
// list. This is a PRIVATE COPY of the same selection logic store.go's
// GraphScoped keeps inline for the brute-force path — the duplication is
// intentional (see vec1Neighbor's doc comment) so this helper can evolve
// independently without ever touching the brute-force fallback. nodes is
// mutated in place (Degree accumulation) and returned for convenience.
func vec1FinishGraph(nodes []GraphNode, nbrs [][]vec1Neighbor, k int) ([]GraphNode, []GraphEdge) {
	type pair struct{ a, b int }
	edgeScore := make(map[pair]float32)
	for i := range nbrs {
		list := nbrs[i]
		sort.Slice(list, func(x, y int) bool { return list[x].score > list[y].score })
		if k > 0 && len(list) > k {
			list = list[:k]
		}
		for _, nb := range list {
			a, b := i, nb.idx
			if a > b {
				a, b = b, a
			}
			edgeScore[pair{a, b}] = nb.score
		}
	}

	edges := make([]GraphEdge, 0, len(edgeScore))
	for p, sc := range edgeScore {
		nodes[p.a].Degree++
		nodes[p.b].Degree++
		edges = append(edges, GraphEdge{
			Source: nodes[p.a].ObsID,
			Target: nodes[p.b].ObsID,
			Weight: sc,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})
	return nodes, edges
}

// tryVecGraph attempts the additive Vec1 KNN path for Graph/GraphScoped
// (spec REQ-462/463/465/466). ok=false means the caller MUST fall back to
// the existing brute-force O(N^2) scan unchanged. Any mixed-dimension row
// within the requested scope disqualifies the WHOLE call from the Vec1 path
// (conservative-but-correct: the brute-force fallback already handles mixed
// dimensions itself via its own per-pair skip).
func (s *Store) tryVecGraph(projects []string, k int, minScore float32) ([]GraphNode, []GraphEdge, bool) {
	if !s.vec.usable() {
		return nil, nil, false
	}

	q := `SELECT rowid, sync_id, obs_id, COALESCE(project,''), COALESCE(type,''), COALESCE(title,''), vector FROM embeddings`
	var args []any
	if len(projects) > 0 {
		placeholders := make([]string, len(projects))
		for i, p := range projects {
			placeholders[i] = "?"
			args = append(args, p)
		}
		q += ` WHERE project IN (` + strings.Join(placeholders, ", ") + `)`
	}

	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		s.vec.markUnhealthy(err)
		return nil, nil, false
	}

	type record struct {
		rowid int64
		node  GraphNode
		vec   []float32
	}
	var recs []record
	activeDim := s.vec.dim()
	for rows.Next() {
		var rec record
		var blob []byte
		if err := rows.Scan(&rec.rowid, &rec.node.SyncID, &rec.node.ObsID, &rec.node.Project, &rec.node.Type, &rec.node.Title, &blob); err != nil {
			rows.Close()
			s.vec.markUnhealthy(err)
			return nil, nil, false
		}
		vec, derr := decodeVector(blob)
		if derr != nil {
			continue // skip undecodable vectors defensively, mirrors brute force
		}
		if len(vec) != activeDim {
			// Mixed dimensions in scope: bail the WHOLE call to brute force
			// (REQ-466) rather than partially routing.
			rows.Close()
			return nil, nil, false
		}
		rec.vec = vec
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.vec.markUnhealthy(err)
		return nil, nil, false
	}
	rows.Close()

	n := len(recs)
	rowidToIdx := make(map[int64]int, n)
	for i, r := range recs {
		rowidToIdx[r.rowid] = i
	}

	var projWhere string
	if len(projects) > 0 {
		placeholders := make([]string, len(projects))
		for i := range projects {
			placeholders[i] = "?"
		}
		projWhere = ` WHERE v.project IN (` + strings.Join(placeholders, ", ") + `)`
	}
	knnQuery := `SELECT v.rowid, v.distance FROM vec_embeddings(?, ?) AS v` + projWhere

	nbrs := make([][]vec1Neighbor, n)
	for i, r := range recs {
		qArgs := append([]any{encodeNativeVector(r.vec), n}, args...)
		nrows, err := s.db.QueryContext(context.Background(), knnQuery, qArgs...)
		if err != nil {
			s.vec.markUnhealthy(err)
			return nil, nil, false
		}
		var list []vec1Neighbor
		for nrows.Next() {
			var rowid int64
			var dist float64
			if err := nrows.Scan(&rowid, &dist); err != nil {
				nrows.Close()
				s.vec.markUnhealthy(err)
				return nil, nil, false
			}
			j, ok := rowidToIdx[rowid]
			if !ok || j == i {
				continue // self-match or a row outside our node set
			}
			score := vecScore(dist)
			if score >= minScore {
				list = append(list, vec1Neighbor{idx: j, score: score})
			}
		}
		if err := nrows.Err(); err != nil {
			nrows.Close()
			s.vec.markUnhealthy(err)
			return nil, nil, false
		}
		nrows.Close()
		nbrs[i] = list
	}

	nodes := make([]GraphNode, n)
	for i := range recs {
		nodes[i] = recs[i].node
	}
	outNodes, edges := vec1FinishGraph(nodes, nbrs, k)
	return outNodes, edges, true
}
