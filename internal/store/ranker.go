package store

import "fmt"

// RankerTrainingRow exposes existing observation and judged-relation signals
// for the optional local ranker. It adds no persistence or retrieval behavior.
type RankerTrainingRow struct {
	SyncID    string
	Type      string
	UpdatedAt string
	Outcome   string
	Judgment  string
}

func (s *Store) ListRankerTrainingRows() ([]RankerTrainingRow, error) {
	rows, err := s.db.Query(`
SELECT o.sync_id, o.type, o.updated_at, ifnull(o.outcome,''),
       ifnull((SELECT r.relation FROM memory_relations r
          WHERE r.judgment_status = 'judged' AND (r.source_id=o.sync_id OR r.target_id=o.sync_id)
            AND r.relation IN ('compatible','supersedes','conflicts_with')
          ORDER BY r.updated_at DESC, r.id ASC LIMIT 1), '')
FROM observations o WHERE o.deleted_at IS NULL
ORDER BY o.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("ListRankerTrainingRows: query: %w", err)
	}
	defer rows.Close()
	out := []RankerTrainingRow{}
	for rows.Next() {
		var row RankerTrainingRow
		if err := rows.Scan(&row.SyncID, &row.Type, &row.UpdatedAt, &row.Outcome, &row.Judgment); err != nil {
			return nil, fmt.Errorf("ListRankerTrainingRows: scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListRankerTrainingRows: rows: %w", err)
	}
	return out, nil
}
