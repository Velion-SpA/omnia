package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/velion/omnia/internal/engramdb"
	"github.com/velion/omnia/internal/meta"
)

// Stats summarizes a reconciliation run.
type Stats struct {
	Embedded int // newly embedded or re-embedded (changed content/model/dim)
	Reused   int // unchanged, kept as-is
	Pruned   int // removed because the source memory is gone or soft-deleted
	Skipped  int // empty content or missing sync_id — not embeddable
	Errors   int // Ollama or upsert failures (one bad row never aborts the run)
}

// Reconcile brings the embeddings store in sync with the live observations in
// engram.db. It reads all live rows in one short read pass (so the slow embed
// loop never holds an engram snapshot), embeds new/changed rows, and prunes
// vanished ones. Idempotent and resumable: a crash mid-run just re-checks hashes
// next time. An Ollama outage degrades to "embedded 0, reused all" — it logs and
// continues rather than failing.
//
// A row is (re)embedded when its content hash, model, or dim differs from the
// stored row (or when force is true). model/dim participate in the trigger so a
// model change cleanly rebuilds the store.
func Reconcile(ctx context.Context, reader *engramdb.DB, store *Store, emb Embedder, model string, dim int, force bool, logger *slog.Logger) (Stats, error) {
	var stats Stats

	live, err := reader.ListForEmbedding(ctx)
	if err != nil {
		return stats, fmt.Errorf("embed: reconcile list: %w", err)
	}
	stored, err := store.Stored(ctx)
	if err != nil {
		return stats, fmt.Errorf("embed: reconcile stored: %w", err)
	}

	liveSyncIDs := make([]string, 0, len(live))
	for _, o := range live {
		if o.SyncID == "" {
			stats.Skipped++ // cannot key without sync_id (extremely rare)
			continue
		}

		input := embedInput(o)
		if strings.TrimSpace(input) == "" {
			// No embeddable content. Do NOT mark this sync_id live, so that if the
			// row previously had content its now-stale embedding is pruned below.
			stats.Skipped++
			continue
		}
		liveSyncIDs = append(liveSyncIDs, o.SyncID)

		hash := hashInput(input)

		if !force {
			if prev, ok := stored[o.SyncID]; ok &&
				prev.ContentHash == hash && prev.Model == model && prev.Dim == dim {
				stats.Reused++
				continue
			}
		}

		vec, err := embedDocument(ctx, emb, input)
		if err != nil {
			if logger != nil {
				logger.Warn("embed failed; skipping row", "sync_id", o.SyncID, "err", err)
			}
			stats.Errors++
			continue
		}

		row := Row{
			SyncID:      o.SyncID,
			ObsID:       o.ID,
			Project:     o.Project,
			Type:        o.Type,
			TopicKey:    o.TopicKey,
			Title:       o.Title,
			UpdatedAt:   o.UpdatedAt,
			ContentHash: hash,
			Model:       model,
			Dim:         dim,
			Vector:      vec,
			EmbeddedAt:  nowUTC(),
		}
		if err := store.Upsert(ctx, row); err != nil {
			if logger != nil {
				logger.Warn("embed upsert failed", "sync_id", o.SyncID, "err", err)
			}
			stats.Errors++
			continue
		}
		stats.Embedded++
	}

	pruned, err := store.Prune(ctx, liveSyncIDs)
	if err != nil {
		return stats, fmt.Errorf("embed: reconcile prune: %w", err)
	}
	stats.Pruned = pruned
	return stats, nil
}

// embedBudgets are the input rune caps tried, in order, by embedDocument.
// Ollama's nomic-embed-text /api/embeddings returns HTTP 500 once the prompt
// exceeds the model context (~2048 tokens); empirically dense content fails
// above ~5000 chars. We cap at 4000 first (title + leading content is a strong
// topical signal), then shrink on a residual failure so even pathologically
// token-dense memories still get a vector. A future enhancement could chunk long
// memories into several vectors instead of truncating.
var embedBudgets = []int{4000, 2000, 1000}

// embedDocument embeds input, truncating to successively smaller rune budgets if
// the model rejects an over-long prompt. Returns the first successful vector, or
// the last error if every budget fails.
func embedDocument(ctx context.Context, emb Embedder, input string) ([]float32, error) {
	var lastErr error
	for _, budget := range embedBudgets {
		vec, err := emb.Embed(ctx, truncateRunes(input, budget), TaskDocument)
		if err == nil {
			return vec, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// truncateRunes returns s capped at n runes (UTF-8 safe — never splits a rune).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// embedInput builds the text we embed: title + content with the omnia-meta block
// stripped (the identical boilerplate block would cluster ingested memories).
func embedInput(o engramdb.Observation) string {
	body := strings.TrimSpace(meta.Strip(o.Content))
	title := strings.TrimSpace(o.Title)
	switch {
	case title == "":
		return body
	case body == "":
		return title
	default:
		return title + "\n\n" + body
	}
}

func hashInput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}
