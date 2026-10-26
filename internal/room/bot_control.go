package room

import (
	"context"
	"time"

	"unour/game"
	"unour/internal/audit"
)

// BotControlTTL bounds interruption when a browser disconnects or an admin
// session expires. Controllers renew independently of the game's turn clock.
const BotControlTTL = 30 * time.Second

var (
	ErrBotControlLost = &Error{Code: "bot_control_lost", Message: "bot control has ended; return to room management"}
	ErrBotControlBusy = &Error{Code: "bot_control_busy", Message: "this bot is controlled by another administrator session"}
	errBotUnavailable = &Error{Code: "bot_unavailable", Message: "select an active bot in a running game"}
)

// This private lease is persisted, never included in a player or inspector DTO.
// Owner is a digest of an authenticated admin session, not a bearer credential.
type botControl struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BotControlView struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Room      View      `json:"room"`
}

func botControlled(r *Record, player game.PlayerID, at time.Time) bool {
	lease, ok := r.botControls[player]
	return ok && at.Before(lease.ExpiresAt)
}

func botControlView(id string, r *Record, player game.PlayerID) BotControlView {
	lease := r.botControls[player]
	return BotControlView{LeaseID: lease.ID, ExpiresAt: lease.ExpiresAt, Room: roomView(id, r, player)}
}

func validControlIdentity(id string) bool { return len(id) == 43 && validID(id) }

func authorizeBotControl(r *Record, player game.PlayerID, owner, leaseID string, now time.Time) error {
	lease, ok := r.botControls[player]
	if !ok || !validControlIdentity(owner) || !validControlIdentity(leaseID) ||
		lease.Owner != owner || lease.ID != leaseID || !now.Before(lease.ExpiresAt) {
		return ErrBotControlLost
	}
	return nil
}

func (m *Manager) AcquireBot(ctx context.Context, id string, player game.PlayerID, instance, owner string) (BotControlView, error) {
	if !validID(id) || !validID(string(player)) || !validInstance(instance) || !validControlIdentity(owner) {
		return BotControlView{}, ErrInvalid
	}
	leaseID, err := randomString(32)
	if err != nil {
		return BotControlView{}, err
	}
	var result BotControlView
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.bot.acquire", Target: id, Detail: "player=" + string(player)}
	_, err = m.store.TransactManaged(ctx, id, instance, func(r *Record, _ ManagementMetadata) error {
		now := m.rooms.now().UTC()
		if _, ok := r.Bots[player]; !ok || r.Game.View("").Phase != game.Playing {
			return errBotUnavailable
		}
		for _, p := range r.Game.View("").Players {
			if p.ID == player && p.Eliminated {
				return errBotUnavailable
			}
		}
		lease := r.botControls[player]
		if botControlled(r, player, now) {
			if lease.Owner != owner {
				return ErrBotControlBusy
			}
		} else {
			if r.botControlEpoch == ^uint64(0) {
				return ErrConflict
			}
			lease = botControl{ID: leaseID, Owner: owner}
			r.botControlEpoch++
		}
		lease.ExpiresAt = now.Add(BotControlTTL)
		if r.botControls == nil {
			r.botControls = make(map[game.PlayerID]botControl)
		}
		r.botControls[player] = lease
		// A declaration belongs to its actual operator. An expired UNO window
		// still receives the same automatic penalty if the operator disconnects.
		for _, p := range r.Game.View("").Players {
			if p.ID == player && p.UNO != nil {
				r.BotUNO[player] = BotUNO{WindowID: p.UNO.ID, At: p.UNO.Deadline.Add(100 * time.Millisecond)}
			}
		}
		result = botControlView(id, r, player)
		return nil
	}, entry)
	return result, err
}

func (m *Manager) BotState(ctx context.Context, id string, player game.PlayerID, owner, leaseID string) (BotControlView, error) {
	if !validID(id) || !validID(string(player)) {
		return BotControlView{}, ErrInvalid
	}
	var result BotControlView
	err := m.rooms.store.Read(ctx, id, func(r *Record) error {
		if err := authorizeBotControl(r, player, owner, leaseID, m.rooms.now().UTC()); err != nil {
			return err
		}
		result = botControlView(id, r, player)
		return nil
	})
	return result, err
}

// Renewal updates only private scheduling data. It neither advances public room
// revisions nor produces a repeating audit record for every heartbeat.
func (m *Manager) RenewBot(ctx context.Context, id string, player game.PlayerID, owner, leaseID string) (BotControlView, error) {
	if !validID(id) || !validID(string(player)) {
		return BotControlView{}, ErrInvalid
	}
	var result BotControlView
	err := m.rooms.store.Transact(ctx, id, func(r *Record) error {
		now := m.rooms.now().UTC()
		if err := authorizeBotControl(r, player, owner, leaseID, now); err != nil {
			return err
		}
		lease := r.botControls[player]
		lease.ExpiresAt = now.Add(BotControlTTL)
		r.botControls[player] = lease
		result = botControlView(id, r, player)
		return nil
	})
	return result, err
}

func (m *Manager) ReleaseBot(ctx context.Context, id string, player game.PlayerID, owner, leaseID string) error {
	if !validID(id) || !validID(string(player)) {
		return ErrInvalid
	}
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.bot.release", Target: id, Detail: "player=" + string(player)}
	_, err := m.store.TransactManaged(ctx, id, "", func(r *Record, _ ManagementMetadata) error {
		lease, ok := r.botControls[player]
		if !ok || !validControlIdentity(owner) || !validControlIdentity(leaseID) || lease.Owner != owner || lease.ID != leaseID {
			return ErrBotControlLost
		}
		if r.botControlEpoch == ^uint64(0) {
			return ErrConflict
		}
		delete(r.botControls, player)
		r.botControlEpoch++
		before := r.Game.View("")
		if before.Turn == player {
			r.BotTurnAt = time.Time{}
		}
		delete(r.BotUNO, player)
		m.rooms.observeBots(r, before, game.Command{At: m.rooms.now().UTC()}, nil)
		return nil
	}, entry)
	return err
}

func (m *Manager) ExecuteBot(ctx context.Context, id string, player game.PlayerID, owner, leaseID string, req ActionRequest) (ActionResult, error) {
	if !validID(id) || !validID(string(player)) {
		return ActionResult{}, ErrInvalid
	}
	switch req.Type {
	case game.Play, game.Draw, game.Pass, game.Challenge:
		if req.ExpectedVersion == nil || *req.ExpectedVersion == 0 {
			return ActionResult{}, &Error{Code: "invalid_input", Message: "expected_version is required"}
		}
	case game.SayUNO, game.PenalizeUNO:
	default:
		return ActionResult{}, ErrForbidden
	}
	var result ActionResult
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.bot." + string(req.Type), Target: id, Detail: "player=" + string(player)}
	_, err := m.store.TransactManaged(ctx, id, "", func(r *Record, _ ManagementMetadata) error {
		now := m.rooms.now().UTC()
		if err := authorizeBotControl(r, player, owner, leaseID, now); err != nil {
			return err
		}
		command := game.Command{Type: req.Type, PlayerID: player, CardID: req.CardID, Color: req.Color, WindowID: req.WindowID, ExpectedVersion: req.ExpectedVersion, At: now}
		before := r.Game.View("")
		outcome, err := r.Game.Apply(command)
		if err != nil {
			return err
		}
		m.rooms.observeBots(r, before, command, outcome.Events)
		result = ActionResult{Room: roomView(id, r, player), Events: outcome.Events}
		return nil
	}, entry)
	return result, err
}
