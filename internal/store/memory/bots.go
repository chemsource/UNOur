package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"unour/internal/room"
)

func (s *Store) DueBots(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit < 1 || limit > 64 {
		return nil, fmt.Errorf("bot job batch must contain 1–64 rooms")
	}
	type item struct {
		id    string
		entry *entry
		due   time.Time
	}
	s.mu.RLock()
	items := make([]item, 0, len(s.rooms))
	for id, entry := range s.rooms {
		items = append(items, item{id: id, entry: entry})
	}
	s.mu.RUnlock()
	var due []item
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !it.entry.mu.TryLock() {
			continue
		}
		if !it.entry.deleted {
			it.due = room.NextBotAt(it.entry.record)
			if !it.due.IsZero() && !it.due.After(before) {
				due = append(due, it)
			}
		}
		it.entry.mu.Unlock()
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].due.Equal(due[j].due) {
			return due[i].id < due[j].id
		}
		return due[i].due.Before(due[j].due)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	ids := make([]string, len(due))
	for i, it := range due {
		ids[i] = it.id
	}
	return ids, nil
}
