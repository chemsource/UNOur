package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"unour/internal/room"
)

func botDeadline(r *room.Record) sql.NullInt64 {
	at := room.NextBotAt(r)
	if at.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: at.UnixMilli(), Valid: true}
}

// DueBots reads only the indexed deadline projection. It neither decodes every
// game nor renews room retention, and deleted rooms cannot leave orphan jobs.
func (s *Store) DueBots(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit < 1 || limit > 64 {
		return nil, fmt.Errorf("bot job batch must contain 1–64 rooms")
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id FROM rooms WHERE next_bot_at<=? ORDER BY next_bot_at,id LIMIT ?`, before.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
