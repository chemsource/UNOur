// Package sqlite persists complete room aggregates in a local SQLite database.
// SQLite serializes commits; game callbacks and read queries run independently.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"unour/internal/room"
)

type Config struct {
	Path     string
	MaxRooms int
	Now      func() time.Time
	Codec    room.RecordCodec
}
type Store struct {
	read, write *sql.DB
	codec       room.RecordCodec
	now         func() time.Time
	maxRooms    int
	locks       keyLocks
}

var _ room.Store = (*Store)(nil)

func Open(cfg Config) (*Store, error) {
	if cfg.Path == "" || cfg.Path == ":memory:" || cfg.MaxRooms <= 0 {
		return nil, fmt.Errorf("a persistent SQLite file path and positive room capacity are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Codec == nil {
		cfg.Codec = room.NewCodec(nil, nil)
	}
	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := secureSidecars(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{"mode": {"rw"}, "_busy_timeout": {"5000"}, "_foreign_keys": {"on"}, "_synchronous": {"FULL"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	write, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	s := &Store{write: write, codec: cfg.Codec, now: cfg.Now, maxRooms: cfg.MaxRooms}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := write.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	var mode string
	if err := write.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if mode != "wal" {
		return nil, fmt.Errorf("SQLite did not enable WAL mode")
	}
	q.Set("_txlock", "deferred")
	q.Set("_query_only", "true")
	u.RawQuery = q.Encode()
	s.read, err = sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	s.read.SetMaxOpenConns(4)
	s.read.SetMaxIdleConns(4)
	if err := s.read.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect SQLite reader: %w", err)
	}
	if err := s.validateExisting(ctx); err != nil {
		return nil, err
	}
	if err := secureSidecars(path); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func (s *Store) Close() error {
	var readErr, writeErr error
	if s.read != nil {
		readErr = s.read.Close()
	}
	if s.write != nil {
		writeErr = s.write.Close()
	}
	return errors.Join(readErr, writeErr)
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 1 || version < 0 {
		return fmt.Errorf("unsupported SQLite schema version %d", version)
	}
	if version == 0 {
		// No IF NOT EXISTS: an unversioned, unrelated schema is not silently adopted.
		for _, statement := range []string{
			`CREATE TABLE rooms (id TEXT PRIMARY KEY NOT NULL CHECK(length(id) BETWEEN 1 AND 64), generation BLOB NOT NULL CHECK(length(generation)=16), revision INTEGER NOT NULL CHECK(revision>0), payload BLOB NOT NULL, touched_at INTEGER NOT NULL)`,
			`CREATE INDEX rooms_touched_at ON rooms(touched_at)`,
			`PRAGMA user_version=1`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("initialize SQLite schema: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema: %w", err)
	}
	return nil
}

func secureSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure SQLite companion file %s: %w", suffix, err)
		}
	}
	return nil
}

func (s *Store) validateExisting(ctx context.Context) error {
	rows, err := s.read.QueryContext(ctx, `SELECT id,payload,generation,revision FROM rooms`)
	if err != nil {
		return fmt.Errorf("load rooms: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var payload, generation []byte
		var revision int64
		if err := rows.Scan(&id, &payload, &generation, &revision); err != nil {
			return err
		}
		if err := validateRevision(generation, revision); err != nil {
			return fmt.Errorf("cannot restore room %q: %w", id, err)
		}
		if _, err := s.codec.Decode(payload); err != nil {
			return fmt.Errorf("cannot restore room %q: %w", id, err)
		}
	}
	return rows.Err()
}
