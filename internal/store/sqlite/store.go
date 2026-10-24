package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"unour/game"
	"unour/internal/room"
)

type storedRoom struct {
	record     *room.Record
	generation []byte
	revision   int64
}

func (s *Store) load(ctx context.Context, id string) (storedRoom, error) {
	var r storedRoom
	var payload []byte
	err := s.read.QueryRowContext(ctx, `SELECT payload,generation,revision FROM rooms WHERE id=?`, id).Scan(&payload, &r.generation, &r.revision)
	if errors.Is(err, sql.ErrNoRows) {
		return r, room.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := validateRevision(r.generation, r.revision); err != nil {
		return r, err
	}
	r.record, err = s.codec.Decode(payload)
	if err != nil {
		return r, &game.Error{Code: game.InternalFailure, Message: "could not restore stored room", Cause: fmt.Errorf("room %q: %w", id, err)}
	}
	return r, nil
}

func validateRevision(generation []byte, revision int64) error {
	if len(generation) != 16 || revision <= 0 || revision == math.MaxInt64 {
		return fmt.Errorf("invalid room storage revision")
	}
	return nil
}

func (s *Store) Create(ctx context.Context, id string, r *room.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := s.codec.Encode(r)
	if err != nil {
		return &game.Error{Code: game.InternalFailure, Message: "could not encode stored room", Cause: err}
	}
	generation := make([]byte, 16)
	if _, err := rand.Read(generation); err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return room.ErrExists
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM rooms`).Scan(&count); err != nil {
		return err
	}
	if count >= s.maxRooms {
		return room.ErrCapacity
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rooms(id,generation,revision,payload,touched_at) VALUES(?,?,1,?,?)`, id, generation, payload, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// Read decodes one committed row and performs no database writes or room-lock
// acquisition. A concurrent command may still be computing its next candidate.
func (s *Store) Read(ctx context.Context, id string, fn func(*room.Record) error) error {
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(r.record)
}

func (s *Store) Transact(ctx context.Context, id string, fn func(*room.Record) error) error {
	unlock, _, err := s.locks.acquire(ctx, id, false)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := fn(r.record); err != nil {
		return err
	}
	payload, err := s.codec.Encode(r.record)
	if err != nil {
		return &game.Error{Code: game.InternalFailure, Message: "could not encode stored room", Cause: err}
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE rooms SET payload=?,revision=revision+1,touched_at=? WHERE id=? AND generation=? AND revision=?`, payload, s.now().UnixMilli(), id, r.generation, r.revision)
	if err != nil {
		return err
	}
	if err := affected(ctx, tx, id, result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Delete(ctx context.Context, id string, authorize func(*room.Record) error) error {
	unlock, _, err := s.locks.acquire(ctx, id, false)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := authorize(r.record); err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE id=? AND generation=? AND revision=?`, id, r.generation, r.revision)
	if err != nil {
		return err
	}
	if err := affected(ctx, tx, id, result); err != nil {
		return err
	}
	return tx.Commit()
}

func affected(ctx context.Context, tx *sql.Tx, id string, result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return room.ErrNotFound
	}
	return room.ErrConflict
}

func (s *Store) Sweep(ctx context.Context, before time.Time) (int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,generation,revision FROM rooms WHERE touched_at<=?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id         string
		generation []byte
		revision   int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.generation, &c.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return 0, err
	}
	removed := 0
	for _, c := range candidates {
		unlock, acquired, err := s.locks.acquire(ctx, c.id, true)
		if err != nil {
			return removed, err
		}
		if !acquired {
			continue
		}
		n, err := s.sweepOne(ctx, c.id, c.generation, c.revision, before)
		unlock()
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

func (s *Store) sweepOne(ctx context.Context, id string, generation []byte, revision int64, before time.Time) (int, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE id=? AND generation=? AND revision=? AND touched_at<=?`, id, generation, revision, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}
