package room

import (
	"context"
	"time"
	"unour/game"
	"unour/internal/bot"
)

// Spectator is a room member, separate from seats in the current game.
// NextRound reserves a seat and includes the member in rematch consensus.
type Spectator struct {
	ID        game.PlayerID `json:"id"`
	Name      string        `json:"name"`
	NextRound bool          `json:"next_round"`
}

func roundNumber(r *Record) uint64 {
	if r.Round == 0 {
		return 1
	}
	return r.Round
}
func nextSeatCount(r *Record) int {
	n := len(r.Game.View("").Players) - len(r.Departed)
	for _, p := range r.Spectators {
		if p.NextRound {
			n++
		}
	}
	return n
}

// syncLobbyHost keeps the room's authority aligned with the engine's start
// invariant. With only viewers present ownership is retained until someone sits.
func syncLobbyHost(r *Record) bool {
	state := r.Game.View("")
	if state.Phase != game.Lobby || len(state.Players) == 0 || r.HostID == state.Players[0].ID {
		return false
	}
	r.HostID = state.Players[0].ID
	return true
}

func (s *Service) SpectateAccount(ctx context.Context, roomID, accountID, name string) (JoinResult, error) {
	if !validID(accountID) {
		return JoinResult{}, ErrUnauthorized
	}
	return s.join(ctx, roomID, name, accountID, true)
}

// Participation changes only a spectator's next-round reservation. Current
// players keep their seats; changing the future roster invalidates approvals.
func (s *Service) Participation(ctx context.Context, roomID, token string, nextRound bool) (View, error) {
	if !validID(roomID) {
		return View{}, ErrInvalid
	}
	var view View
	err := s.store.Transact(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		index := -1
		for i, p := range r.Spectators {
			if p.ID == id {
				index = i
			}
		}
		if index < 0 {
			return &Error{Code: "spectator_required", Message: "only spectators can change a reservation"}
		}
		if r.Spectators[index].NextRound != nextRound {
			if nextRound && nextSeatCount(r) >= r.Game.Checkpoint().MaxPlayers {
				return ErrCapacity
			}
			if nextRound && r.Game.View("").Phase == game.Lobby {
				p := r.Spectators[index]
				if err := r.Game.Join(p.ID, p.Name); err != nil {
					return err
				}
				r.Spectators = append(r.Spectators[:index], r.Spectators[index+1:]...)
				syncLobbyHost(r)
			} else {
				r.Spectators[index].NextRound = nextRound
			}
			r.RematchVotes = nil
			r.RosterVersion++
			r.Revision++
		}
		view = roomView(roomID, r, id)
		return nil
	})
	return view, err
}

// VoteRematch requires consent from every next-round human, while computers
// consent automatically. The last vote and new deal are one atomic commit.
type RematchRequest struct {
	Round         uint64
	RosterVersion uint64
	Agree         bool
}

func (s *Service) VoteRematch(ctx context.Context, roomID, token string, req RematchRequest) (View, error) {
	if !validID(roomID) {
		return View{}, ErrInvalid
	}
	var view View
	err := s.store.Transact(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		before := r.Game.View("")
		if before.Phase != game.Finished || req.Round != roundNumber(r) {
			return &Error{Code: "rematch_unavailable", Message: "rematch vote belongs to a different or unfinished round"}
		}
		if req.RosterVersion != r.RosterVersion {
			return &Error{Code: "roster_changed", Message: "participants changed; review the roster before consenting"}
		}
		roster := make([]game.PlayerIdentity, 0, len(before.Players)+len(r.Spectators))
		for _, p := range before.Players {
			if r.Departed[p.ID] {
				continue
			}
			roster = append(roster, game.PlayerIdentity{ID: p.ID, Name: p.Name})
		}
		for _, p := range r.Spectators {
			if p.NextRound {
				roster = append(roster, game.PlayerIdentity{ID: p.ID, Name: p.Name})
			}
		}
		eligible := false
		votes := make(map[game.PlayerID]bool, len(r.RematchVotes)+1)
		for p, v := range r.RematchVotes {
			votes[p] = v
		}
		for _, p := range roster {
			if p.ID == id {
				eligible = true
			}
		}
		if !eligible {
			return &Error{Code: "spectator_read_only", Message: "spectators cannot vote or play"}
		}
		// The host is first for the engine's start invariant, including when
		// ownership was transferred to a spectator after a departure.
		for i, p := range roster {
			if p.ID == r.HostID {
				roster[0], roster[i] = roster[i], roster[0]
				break
			}
		}
		if req.Agree {
			votes[id] = true
		} else {
			delete(votes, id)
		}
		unanimous := len(roster) >= 2
		for _, p := range roster {
			if _, computer := r.Bots[p.ID]; !computer && !votes[p.ID] {
				unanimous = false
			}
		}
		if unanimous {
			if err := r.Game.Rematch(roster); err != nil {
				return err
			}
			kept := make([]Spectator, 0, len(r.Spectators))
			for _, p := range r.Spectators {
				if !p.NextRound {
					kept = append(kept, p)
				}
			}
			r.Spectators = kept
			r.Departed = nil
			r.Round = roundNumber(r) + 1
			r.RosterVersion++
			r.RematchVotes = nil
			r.BotMemory = bot.Memory{}
			r.BotTurnAt = time.Time{}
			r.BotUNO = nil
			s.observeBots(r, before, game.Command{Type: game.Start, PlayerID: r.HostID, At: s.now().UTC()}, []game.Event{{Type: "started"}})
		} else {
			r.RematchVotes = votes
		}
		r.Revision++
		view = roomView(roomID, r, id)
		return nil
	})
	return view, err
}

// Leave removes credentials and transfers ownership in the same transaction.
// A running player forfeits; their physical cards stay private until reset.
func (s *Service) Leave(ctx context.Context, roomID, token string) error {
	if !validID(roomID) {
		return ErrInvalid
	}
	empty := false
	err := s.store.Transact(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		if err := s.removeMember(r, id); err != nil {
			return err
		}
		empty = len(r.Sessions) == 0
		return nil
	})
	if err != nil {
		return err
	}
	if empty {
		// A concurrent join must never be deleted by the last member's cleanup.
		err = s.store.Delete(ctx, roomID, func(r *Record) error {
			if len(r.Sessions) != 0 {
				return ErrConflict
			}
			return nil
		})
		if err == ErrConflict || err == ErrNotFound {
			return nil
		}
	}
	return err
}

// removeMember is shared by voluntary departure and privileged removal.
// The caller authenticates the member and persists the full lifecycle atomically.
func (s *Service) removeMember(r *Record, id game.PlayerID) error {
	index := -1
	for i, p := range r.Spectators {
		if p.ID == id {
			index = i
		}
	}
	before := r.Game.View("")
	changesRoster := index < 0 || r.Spectators[index].NextRound
	if index >= 0 {
		r.Spectators = append(r.Spectators[:index], r.Spectators[index+1:]...)
	} else {
		if err := r.Game.Leave(id); err != nil {
			return err
		}
		if before.Phase != game.Lobby {
			if r.Departed == nil {
				r.Departed = make(map[game.PlayerID]bool)
			}
			r.Departed[id] = true
		}
	}
	for key, p := range r.Accounts {
		if p == id {
			delete(r.Accounts, key)
		}
	}
	for key, p := range r.Sessions {
		if p == id {
			delete(r.Sessions, key)
		}
	}
	if r.HostID == id {
		r.HostID = ""
		for _, p := range r.Game.View("").Players {
			if _, computer := r.Bots[p.ID]; !computer && !r.Departed[p.ID] {
				r.HostID = p.ID
				break
			}
		}
		if r.HostID == "" && len(r.Spectators) > 0 {
			r.HostID = r.Spectators[0].ID
		}
	}
	if changesRoster {
		r.RematchVotes = nil
		r.RosterVersion++
	}
	r.Revision++
	s.observeBots(r, before, game.Command{Type: "leave", PlayerID: id, At: s.now().UTC()}, nil)
	return nil
}
