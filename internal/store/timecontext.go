package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/token"
)

const recordedContextScopeNote = "> Recorded-time context includes versioned observations only; sessions and prompts are omitted because they are not versioned.\n\n"

func (s *Store) FormatContextAsOf(project, scope, timestamp string) (string, error) {
	if !s.cfg.TimeTravelEnabled || strings.TrimSpace(timestamp) == "" {
		return s.FormatContext(project, scope)
	}
	at, err := parseObservationTime(timestamp)
	if err != nil {
		return "", fmt.Errorf("invalid as_of timestamp: %w", err)
	}
	if asOfIsFuture(at) {
		return s.FormatContext(project, scope)
	}

	project, _ = NormalizeProject(project)
	rows, err := s.db.Query(`SELECT id FROM observations ORDER BY id`)
	if err != nil {
		return "", err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}

	var pinned, recent []Observation
	for _, id := range ids {
		obs, err := s.StateAsOf(id, timestamp)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return "", err
		}
		obsProject, _ := NormalizeProject(derefString(obs.Project))
		if (project != "" && obsProject != project) ||
			(scope != "" && obs.Scope != normalizeScope(scope)) {
			continue
		}
		if obs.Pinned {
			pinned = append(pinned, *obs)
		} else {
			recent = append(recent, *obs)
		}
	}
	sort.SliceStable(pinned, historicalContextOrder(pinned))
	sort.SliceStable(recent, historicalContextOrder(recent))
	if len(recent) > s.cfg.MaxContextResults {
		recent = recent[:s.cfg.MaxContextResults]
	}
	if s.cfg.ContextTokenBudget > 0 {
		remaining := s.cfg.ContextTokenBudget
		var used int
		pinned, used = token.TrimToBudget(pinned, formatObservationLineTokens, remaining)
		remaining -= used
		recent, _ = token.TrimToBudget(recent, formatObservationLineTokens, remaining)
	}

	if len(pinned) == 0 && len(recent) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(recordedContextScopeNote)
	b.WriteString("## Memory from Previous Sessions\n\n")
	if len(pinned) > 0 {
		b.WriteString("### Pinned\n")
		for _, obs := range pinned {
			b.WriteString(formatObservationLine(obs))
		}
		b.WriteString("\n")
	}
	if len(recent) > 0 {
		b.WriteString("### Recent Observations\n")
		for _, obs := range recent {
			b.WriteString(formatObservationLine(obs))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func historicalContextOrder(observations []Observation) func(int, int) bool {
	return func(i, j int) bool {
		left, leftErr := parseObservationTime(observations[i].UpdatedAt)
		right, rightErr := parseObservationTime(observations[j].UpdatedAt)
		if leftErr == nil && rightErr == nil {
			if !left.Equal(right) {
				return left.After(right)
			}
		} else if observations[i].UpdatedAt != observations[j].UpdatedAt {
			return observations[i].UpdatedAt > observations[j].UpdatedAt
		}
		return observations[i].ID > observations[j].ID
	}
}
