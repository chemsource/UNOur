package room

import (
	"context"
	"fmt"
	"time"

	"unour/game"
	"unour/internal/audit"
	"unour/internal/bot"
)

// AdministrationStore is a separate privileged port, not part of player access.
type AdministrationStore interface {
	ListRooms(context.Context, RoomQuery) (RoomPage, error)
	ReadManaged(context.Context, string, func(*Record, ManagementMetadata) error) error
	// TransactManaged commits the room and audit entry atomically; either both
	// persist or neither does, including when encoding/audit/commit fails.
	// The callback observes loaded metadata; the return value describes the commit.
	TransactManaged(context.Context, string, string, func(*Record, ManagementMetadata) error, audit.Entry) (ManagementMetadata, error)
	DeleteManaged(context.Context, string, string, func(*Record) error, audit.Entry) error
}
type Manager struct {
	rooms *Service
	store AdministrationStore
}

func NewManager(rooms *Service, store AdministrationStore) *Manager {
	return &Manager{rooms: rooms, store: store}
}

type RoomSummary struct {
	ManagementMetadata
	ID         string     `json:"id"`
	Phase      game.Phase `json:"phase"`
	Players    int        `json:"players"`
	Spectators int        `json:"spectators"`
	Bots       int        `json:"bots"`
	Round      uint64     `json:"round"`
	Revision   uint64     `json:"revision"`
	CreatedAt  time.Time  `json:"created_at"`
	HostName   string     `json:"host_name"`
	Humans     int        `json:"humans"`
	JoinLocked bool       `json:"join_locked"`
}
type RoomPage struct {
	Rooms []RoomSummary `json:"rooms"`
	Total int           `json:"total"`
	Stats RoomStats     `json:"stats"`
}

// Instance identifies a room lifetime, not a credential or a changing turn.
// It prevents a stale administrative command from touching a recreated room.
type ManagementMetadata struct {
	Instance  string    `json:"instance"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Inspection explicitly adds private cards, without exposing storage credentials
// or historical challenge evidence. It is only for authenticated administrators.
type Inspection struct {
	View
	ManagementMetadata
	Deck []game.Card `json:"deck"`
}
type EditRequest struct {
	game.CardEdit
	Instance         string `json:"instance"`
	ExpectedVersion  uint64 `json:"expected_version"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func inspection(id string, r *Record, metadata ManagementMetadata) Inspection {
	v := roomView(id, r, "")
	v.Role = "admin"
	cp := r.Game.Checkpoint()
	v.State.DrawnCardID = cp.DrawnCardID
	v.State.CanPass = cp.DrawnCardID != 0
	for i := range v.State.Players {
		v.State.Players[i].Hand = cp.Players[i].Hand
	}
	return Inspection{View: v, Deck: cp.Deck, ManagementMetadata: metadata}
}
func (m *Manager) Inspect(ctx context.Context, id string) (Inspection, error) {
	if !validID(id) {
		return Inspection{}, ErrInvalid
	}
	var result Inspection
	err := m.store.ReadManaged(ctx, id, func(r *Record, meta ManagementMetadata) error { result = inspection(id, r, meta); return nil })
	return result, err
}
func (m *Manager) Edit(ctx context.Context, id string, req EditRequest) (Inspection, error) {
	if !validID(id) {
		return Inspection{}, ErrInvalid
	}
	if !validInstance(req.Instance) || req.ExpectedVersion == 0 || req.ExpectedRevision == 0 {
		return Inspection{}, &Error{Code: "invalid_input", Message: "instance, expected_version and expected_revision are required"}
	}
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.cards", Target: id, Detail: fmt.Sprintf("%s card=%d replacement=%d destination=%s", req.Operation, req.CardID, req.ReplacementID, req.ToPlayerID)}
	// Bound audit data to known IDs before recording an operator-provided target.
	if req.ToPlayerID != "" && !validID(string(req.ToPlayerID)) {
		return Inspection{}, ErrInvalid
	}
	var result Inspection
	meta, err := m.store.TransactManaged(ctx, id, req.Instance, func(r *Record, meta ManagementMetadata) error {
		if v := roomView(id, r, ""); v.Revision != req.ExpectedRevision || v.State.Version != req.ExpectedVersion {
			return ErrConflict
		}
		before := r.Game.View("")
		if err := r.Game.EditCards(req.CardEdit, req.ExpectedVersion); err != nil {
			return err
		}
		if len(r.Bots) > 0 {
			// Never feed privileged hand edits into a bot's hidden-card inference.
			r.BotMemory = bot.Memory{Discard: r.Game.Checkpoint().Discard, ChallengeColor: r.BotMemory.ChallengeColor}
			m.rooms.observeBots(r, before, game.Command{At: m.rooms.now().UTC()}, nil)
		}
		result = inspection(id, r, meta)
		return nil
	}, entry)
	if err == nil {
		result.ManagementMetadata = meta
	}
	return result, err
}
