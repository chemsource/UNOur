package room

import (
	"context"
	"fmt"

	"unour/game"
	"unour/internal/audit"
	"unour/internal/bot"
)

// AdministrationStore is a separate privileged port, not part of player access.
type AdministrationStore interface {
	RoomIDs(context.Context, int, int) ([]string, int, error)
	Read(context.Context, string, func(*Record) error) error
	// TransactAudited commits the room and audit entry atomically; either both
	// persist or neither does, including when encoding/audit/commit fails.
	TransactAudited(context.Context, string, func(*Record) error, audit.Entry) error
}
type Manager struct {
	rooms *Service
	store AdministrationStore
}

func NewManager(rooms *Service, store AdministrationStore) *Manager {
	return &Manager{rooms: rooms, store: store}
}

type RoomSummary struct {
	ID         string     `json:"id"`
	Phase      game.Phase `json:"phase"`
	Players    int        `json:"players"`
	Spectators int        `json:"spectators"`
	Bots       int        `json:"bots"`
	Round      uint64     `json:"round"`
	Revision   uint64     `json:"revision"`
}
type RoomPage struct {
	Rooms []RoomSummary `json:"rooms"`
	Total int           `json:"total"`
}

// Inspection explicitly adds private cards, without exposing storage credentials
// or historical challenge evidence. It is only for authenticated administrators.
type Inspection struct {
	View
	Deck []game.Card `json:"deck"`
}
type EditRequest struct {
	game.CardEdit
	ExpectedVersion  uint64 `json:"expected_version"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func inspection(id string, r *Record) Inspection {
	v := roomView(id, r, "")
	v.Role = "admin"
	cp := r.Game.Checkpoint()
	v.State.DrawnCardID = cp.DrawnCardID
	v.State.CanPass = cp.DrawnCardID != 0
	for i := range v.State.Players {
		v.State.Players[i].Hand = cp.Players[i].Hand
	}
	return Inspection{View: v, Deck: cp.Deck}
}
func (m *Manager) List(ctx context.Context, limit, offset int) (RoomPage, error) {
	page := RoomPage{Rooms: []RoomSummary{}}
	if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return page, &Error{Code: "invalid_input", Message: "invalid room pagination"}
	}
	ids, total, err := m.store.RoomIDs(ctx, limit, offset)
	if err != nil {
		return page, err
	}
	page.Total = total
	for _, id := range ids {
		err := m.store.Read(ctx, id, func(r *Record) error {
			v := roomView(id, r, "")
			page.Rooms = append(page.Rooms, RoomSummary{ID: id, Phase: v.State.Phase, Players: len(v.State.Players), Spectators: len(r.Spectators), Bots: len(r.Bots), Round: v.Round, Revision: v.Revision})
			return nil
		})
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return page, err
		}
	}
	return page, nil
}
func (m *Manager) Inspect(ctx context.Context, id string) (Inspection, error) {
	if !validID(id) {
		return Inspection{}, ErrInvalid
	}
	var result Inspection
	err := m.store.Read(ctx, id, func(r *Record) error { result = inspection(id, r); return nil })
	return result, err
}
func (m *Manager) Edit(ctx context.Context, id string, req EditRequest) (Inspection, error) {
	if !validID(id) {
		return Inspection{}, ErrInvalid
	}
	if req.ExpectedVersion == 0 || req.ExpectedRevision == 0 {
		return Inspection{}, &Error{Code: "invalid_input", Message: "expected_version and expected_revision are required"}
	}
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.cards", Target: id, Detail: fmt.Sprintf("%s card=%d replacement=%d destination=%s", req.Operation, req.CardID, req.ReplacementID, req.ToPlayerID)}
	// Bound audit data to known IDs before recording an operator-provided target.
	if req.ToPlayerID != "" && !validID(string(req.ToPlayerID)) {
		return Inspection{}, ErrInvalid
	}
	var result Inspection
	err := m.store.TransactAudited(ctx, id, func(r *Record) error {
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
		result = inspection(id, r)
		return nil
	}, entry)
	return result, err
}
