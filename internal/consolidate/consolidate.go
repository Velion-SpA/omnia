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
		// SaveObservation silently reuses an EXISTING row (Decision "noop"
		// or "update") instead of inserting a new one. Only "save"/"relate"
		// mean a genuinely NEW digest observation was created this run —
		// counting/auditing/relating a reused row would both inflate the
		// printed count past the actual number of digests written AND
		// misattribute this cluster's sources onto an unrelated earlier
		// digest, so that cluster is skipped entirely rather than either.
		if saveResult.Decision != store.WriteGateDecisionSave && saveResult.Decision != store.WriteGateDecisionRelate {
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
