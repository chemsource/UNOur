// Package room implements room use cases and defines their storage port.
package room

import (
	"context"
	"time"

	"unour/game"
	"unour/internal/bot"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

var (
	ErrNotFound     = &Error{Code: "room_not_found", Message: "room does not exist"}
	ErrExists       = &Error{Code: "room_exists", Message: "room ID already exists"}
	ErrCapacity     = &Error{Code: "capacity_exceeded", Message: "server room capacity reached"}
	ErrConflict     = &Error{Code: "version_conflict", Message: "room changed during the operation; read state before retrying"}
	ErrUnauthorized = &Error{Code: "unauthorized", Message: "a valid room session is required"}
	ErrForbidden    = &Error{Code: "forbidden", Message: "only the room host can perform this operation"}
	ErrInvalid      = &Error{Code: "invalid_input", Message: "room ID must contain 1–64 ASCII letters, digits, underscores or hyphens"}
)

// Record is owned by Store. It must only be accessed inside a callback, and must
// never be used as an HTTP response. Session keys are SHA-256 token digests.
type Record struct {
	JoinLocked    bool
	RosterVersion uint64
	Departed      map[game.PlayerID]bool
	Game          *game.Game
	HostID        game.PlayerID
	Sessions      map[[32]byte]game.PlayerID
	Accounts      map[string]game.PlayerID
	Rules         []game.RuleSpec
	CreatedAt     time.Time
	Bots          map[game.PlayerID]bot.Profile
	BotMemory     bot.Memory
	BotTurnAt     time.Time
	BotUNO        map[game.PlayerID]BotUNO
	Spectators    []Spectator
	RematchVotes  map[game.PlayerID]bool
	Round         uint64
	Revision      uint64
}

// Store serializes write callbacks for a room within this process. The callback
// may not retain Record or pointers into it, or recursively call Store. Callbacks
// must leave Record unchanged on error; the Game aggregate ensures this for its
// own commands. Create transfers ownership of Record to the store.
//
// A durable implementation additionally needs atomic commit and room ownership
// across processes. This interface does not provide distributed consistency by
// itself. Sweep skips locally owned write callbacks; a durable adapter must
// prevent a stale callback in another process from resurrecting deleted data.
type Store interface {
	Create(context.Context, string, *Record) error
	// Read callbacks must not modify the record. Reads do not extend retention.
	Read(context.Context, string, func(*Record) error) error
	Transact(context.Context, string, func(*Record) error) error
	Delete(context.Context, string, func(*Record) error) error
	Sweep(context.Context, time.Time) (int, error)
}
