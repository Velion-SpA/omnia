package consolidate

import (
	"context"
	"fmt"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
	"os"
	"strings"
)

// Run clusters a project's embeddings and writes local digest observations.
// It never mutates or hides source observations.
func Run(ctx context.Context, memories *store.Store, vectors *embed.Store, client *embed.Client, cfg config.ConsolidationConfig, project string) (int, error) {
	if !cfg.Enabled {
		return 0, nil
	}
	projects := []string(nil)
	if project != "" {
		projects = []string{project}
	}
	nodes, edges, err := vectors.GraphScoped(projects, cfg.K, cfg.MinScore)
	if err != nil {
		return 0, err
	}
	clusters := Clusters(nodes, edges, cfg.MinScore, cfg.K, cfg.MinClusterSize, cfg.MaxClusterSize)
	written := 0
	for _, cluster := range clusters {
		parts := make([]string, 0, len(cluster))
		for _, n := range cluster {
			parts = append(parts, n.Title)
		}
		content, err := client.Generate(ctx, "Create a concise memory digest from these related observations. Preserve key decisions and facts.\n\n"+strings.Join(parts, "\n"))
		if err != nil {
			continue
		}
		projectName := project
		if projectName == "" && len(cluster) > 0 {
			projectName = cluster[0].Project
		}
		firstSource, err := memories.GetObservation(int64(cluster[0].ObsID))
		if err != nil {
			return written, err
		}
		saveResult, err := memories.SaveObservation(store.AddObservationParams{SessionID: firstSource.SessionID, Type: "digest", Title: "Consolidated memory digest", Content: content, Project: projectName, Scope: "project", Source: "agent"})
		if err != nil {
			return written, fmt.Errorf("consolidate: write digest: %w", err)
		}
		// finding #2: every cluster's digest shares the SAME literal title
		// ("Consolidated memory digest"), so if two clusters' generated
		// content happens to normalize to the same hash within the store's
		// always-on dedupe window (store.SaveObservation's pre-write-hygiene
		// duplicate check — realistic for short/templated LLM output),
		// SaveObservation silently reuses an EXISTING row instead of
		// inserting a new one. Only WriteGateDecisionNoop means NOTHING
		// happened — an exact/near-duplicate match that left the existing
		// row's content untouched (aside from bumping duplicate_count) — so
		// it alone is skipped: counting/auditing/relating it would both
		// inflate the printed count past the actual number of digests
		// written AND misattribute this cluster's sources onto an unrelated
		// earlier digest whose content never changed.
		//
		// finding #3 (adversarial review of the finding #2 fix):
		// WriteGateDecisionUpdate is NOT a no-op like Noop — read store.go's
		// evaluateWriteGate Update branch: it performs a real
		// `UPDATE ... WHERE id = ?` that overwrites the EXISTING row's
		// title/content/normalized_hash in place (write-hygiene's ladder
		// lands here whenever a cluster's digest scores as a near-duplicate
		// of an earlier cluster's digest — realistic for short/templated LLM
		// output across similar sub-clusters of a split oversized cluster,
		// exactly like the Noop case above, just past the update threshold
		// instead of the noop threshold). Treating Update like Noop was
		// itself a bug: the write went uncounted/unaudited, AND this
		// cluster's sources were never linked via `consolidates`, leaving
		// the row's PRE-EXISTING relations (from whichever cluster last
		// owned it) pointing at content that no longer matched what they
		// were linked to — a silent data-consistency drift. Update must
		// fall through to the same counting/audit/relation-linking path as
		// Save/Relate below: SaveResult.ID's doc comment (store.go)
		// documents it as "the existing/matched row's ID" for Update — the
		// SAME observation being updated, not a new one — and its SyncID is
		// untouched by the UPDATE statement, so digest.SyncID below still
		// correctly names the row this cluster's sources must relate to.
		if saveResult.Decision == store.WriteGateDecisionNoop {
			continue
		}
		id := saveResult.ID
		digest, err := memories.GetObservation(id)
		if err != nil {
			return written, err
		}
		for _, n := range cluster {
			source, err := memories.GetObservation(int64(n.ObsID))
			if err != nil {
				return written, err
			}
			if _, err := memories.JudgeBySemantic(store.JudgeBySemanticParams{SourceID: digest.SyncID, TargetID: source.SyncID, Relation: store.RelationConsolidates, Confidence: 1, Reasoning: "local consolidation", Model: cfg.Model}); err != nil {
				return written, err
			}
		}
		audit.Append(audit.Entry{Ts: audit.Now(), Actor: "omnia", Action: audit.ActionConsolidate, ObservationID: int(id), Project: projectName, Summary: "memory consolidation digest", Result: "ok", SyncID: digest.SyncID})
		written++
		// finding #4: `omnia consolidate` ran for minutes with ZERO stdout on
		// real data (19-25 sequential Ollama-backed digest writes), giving no
		// way to tell "working" from "hung." A line per cluster as its
		// digest is written, to stderr (flushed immediately — os.Stderr
		// writes are unbuffered) and never interfering with any stdout a
		// caller relies on, is enough for a real user or an agent watching
		// command output to tell the process is alive and roughly how far
		// along it is.
		fmt.Fprintf(os.Stderr, "consolidate: digest %d/%d written (cluster size %d)\n", written, len(clusters), len(cluster))
	}
	return written, nil
}
