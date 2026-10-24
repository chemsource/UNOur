// Package memory provides a bounded in-process room store. It is intentionally
// ephemeral: process restarts discard all games and sessions.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"unour/internal/room"
)

type entry struct {
	mu      sync.Mutex
	record  *room.Record
	touched time.Time
	deleted bool
}
type Config struct {
	MaxRooms int
	Now      func() time.Time
}
type Store struct {
	mu       sync.RWMutex
	rooms    map[string]*entry
	maxRooms int
	now      func() time.Time
}

var _ room.Store = (*Store)(nil)

func New(cfg Config) (*Store, error) {
	if cfg.MaxRooms <= 0 {
		return nil, fmt.Errorf("max rooms must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Store{rooms: make(map[string]*entry), maxRooms: cfg.MaxRooms, now: cfg.Now}, nil
}

func (s *Store) Create(ctx context.Context, id string, r *room.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("room record is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rooms[id]; ok {
		return room.ErrExists
	}
	if len(s.rooms) >= s.maxRooms {
		return room.ErrCapacity
	}
	s.rooms[id] = &entry{record: r, touched: s.now()}
	return nil
}

func (s *Store) lookup(ctx context.Context, id string) (*entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	e, ok := s.rooms[id]
	s.mu.RUnlock()
	if !ok {
		return nil, room.ErrNotFound
	}
	return e, nil
}

func (s *Store) Transact(ctx context.Context, id string, fn func(*room.Record) error) error {
	return s.access(ctx, id, fn, true)
}

func (s *Store) Read(ctx context.Context, id string, fn func(*room.Record) error) error {
	return s.access(ctx, id, fn, false)
}

func (s *Store) access(ctx context.Context, id string, fn func(*room.Record) error, touch bool) error {
	e, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.deleted {
		return room.ErrNotFound
	}
	if err := fn(e.record); err != nil {
		return err
	}
	// Read after the callback so a queued/long transaction cannot be swept using
	// the timestamp at which it originally entered the waiting queue.
	if touch {
		e.touched = s.now()
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string, authorize func(*room.Record) error) error {
	e, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.deleted {
		return room.ErrNotFound
	}
	if err := authorize(e.record); err != nil {
		return err
	}
	s.remove(id, e)
	return nil
}

// remove holds the entry lock before acquiring the directory lock. No code
// waits for an entry lock while holding the directory lock.
func (s *Store) remove(id string, e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rooms[id] == e {
		delete(s.rooms, id)
		e.deleted = true
	}
}

func (s *Store) Sweep(ctx context.Context, before time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	type item struct {
		id string
		e  *entry
	}
	s.mu.RLock()
	items := make([]item, 0, len(s.rooms))
	for id, e := range s.rooms {
		items = append(items, item{id, e})
	}
	s.mu.RUnlock()
	removed := 0
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !it.e.mu.TryLock() {
			continue
		}
		if !it.e.deleted && !it.e.touched.After(before) {
			s.remove(it.id, it.e)
			removed++
		}
		it.e.mu.Unlock()
	}
	return removed, nil
}
