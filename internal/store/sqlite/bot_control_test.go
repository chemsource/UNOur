package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/room"
)

type botControlFixture struct {
	store   *Store
	rooms   *room.Service
	manager *room.Manager
	joined  room.JoinResult
	bot     game.PlayerID
	now     *time.Time
	path    string
}

func controlFixture(t *testing.T) botControlFixture {
	t.Helper()
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "control.db")
	store := openTest(t, path, clock, 10)
	svc, err := room.New(store, room.Config{Now: clock, BotRandom: func() float64 { return 0.5 }, GameOptions: game.Options{InitialHand: 2, Shuffle: func([]game.Card) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := svc.CreateSolo(context.Background(), "human-account", "Alice", room.SoloRequest{BotCount: 2, Difficulty: bot.Normal})
	if err != nil {
		t.Fatal(err)
	}
	// Only the host acts before the fixture is handed to an operator.
	result, err := svc.Execute(context.Background(), joined.Room.ID, joined.Token, room.ActionRequest{Type: game.Draw})
	if err != nil {
		t.Fatal(err)
	}
	if result.Room.State.CanPass {
		if _, err = svc.Execute(context.Background(), joined.Room.ID, joined.Token, room.ActionRequest{Type: game.Pass}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Second)
	return botControlFixture{store: store, rooms: svc, manager: room.NewManager(svc, store), joined: joined, bot: joined.Room.State.Players[1].ID, now: &now, path: path}
}

func (f botControlFixture) acquire(t *testing.T) room.BotControlView {
	t.Helper()
	view, err := f.manager.Inspect(context.Background(), f.joined.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	control, err := f.manager.AcquireBot(context.Background(), view.ID, f.bot, view.Instance, strings.Repeat("a", 43))
	if err != nil {
		t.Fatal(err)
	}
	return control
}

func TestBotControlFencesInFlightAIAndRestoresItsQueue(t *testing.T) {
	f := controlFixture(t)
	ctx := context.Background()
	id, owner := f.joined.Room.ID, strings.Repeat("a", 43)
	task, err := f.rooms.NextBotTask(ctx, id)
	if err != nil || task == nil || task.PlayerID != f.bot {
		t.Fatal("missing scheduled bot", err)
	}
	control := f.acquire(t)
	if next, err := f.rooms.NextBotTask(ctx, id); err != nil || next != nil {
		t.Fatal("controlled bot was still scheduled", err)
	}
	if ids, err := f.store.DueBots(ctx, *f.now, 10); err != nil || len(ids) != 0 {
		t.Fatal("controlled turn busy-looped in the durable queue", ids, err)
	}
	command := game.Command{Type: game.Draw, PlayerID: f.bot}
	if err := f.rooms.ApplyBot(ctx, id, *task, command); !errors.Is(err, room.ErrConflict) {
		t.Fatal("in-flight AI crossed the control boundary", err)
	}
	if err := f.rooms.RetryBot(ctx, id, *task, time.Second); !errors.Is(err, room.ErrConflict) {
		t.Fatal("stale AI retry changed the controlled schedule", err)
	}
	*f.now = f.now.Add(11 * time.Second)
	if _, err := f.manager.RenewBot(ctx, id, f.bot, owner, control.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.ReleaseBot(ctx, id, f.bot, owner, control.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := f.rooms.ApplyBot(ctx, id, *task, command); !errors.Is(err, room.ErrConflict) {
		t.Fatal("old AI revived after release", err)
	}
	*f.now = f.now.Add(3 * time.Second)
	next, err := f.rooms.NextBotTask(ctx, id)
	if err != nil || next == nil || next.PlayerID != f.bot {
		t.Fatal("AI did not resume", err)
	}
	if err := f.rooms.ApplyBot(ctx, id, *next, command); err != nil {
		t.Fatal("resumed AI could not act", err)
	}
}

func TestBotControlPersistsAndExpiresWithoutClientCleanup(t *testing.T) {
	f := controlFixture(t)
	ctx := context.Background()
	control := f.acquire(t)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return *f.now }
	store := openTest(t, f.path, clock, 10)
	svc := serviceTest(t, store, clock)
	manager := room.NewManager(svc, store)
	owner, id := strings.Repeat("a", 43), f.joined.Room.ID
	if _, err := manager.BotState(ctx, id, f.bot, owner, control.LeaseID); err != nil {
		t.Fatal("control lease lost in SQLite restore", err)
	}
	if task, err := svc.NextBotTask(ctx, id); err != nil || task != nil {
		t.Fatal("AI ignored restored control", err)
	}
	*f.now = f.now.Add(31 * time.Second)
	if _, err := manager.BotState(ctx, id, f.bot, owner, control.LeaseID); !errors.Is(err, room.ErrBotControlLost) {
		t.Fatal("expired control still authorized", err)
	}
	ids, err := store.DueBots(ctx, *f.now, 10)
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal("expired control did not wake the durable queue", ids, err)
	}
	task, err := svc.NextBotTask(ctx, id)
	if err != nil || task == nil || task.PlayerID != f.bot {
		t.Fatal("AI did not resume after lease expiry", err)
	}
}

func TestControlledBotMustDeclareUNOAndStillReceivesTimeoutPenalty(t *testing.T) {
	for _, declare := range []bool{false, true} {
		t.Run(map[bool]string{false: "missed", true: "declared"}[declare], func(t *testing.T) {
			f := controlFixture(t)
			ctx := context.Background()
			control := f.acquire(t)
			var play game.LegalAction
			for _, action := range control.Room.State.LegalActions {
				if action.Action == game.Play {
					play = action
					break
				}
			}
			if play.Action == "" {
				t.Fatal("fixture lacks a legal bot play")
			}
			version := control.Room.State.Version
			result, err := f.manager.ExecuteBot(ctx, f.joined.Room.ID, f.bot, strings.Repeat("a", 43), control.LeaseID, room.ActionRequest{Type: game.Play, CardID: play.CardID, Color: play.Color, ExpectedVersion: &version})
			if err != nil {
				t.Fatal(err)
			}
			var window *game.UNOWindow
			for _, player := range result.Room.State.Players {
				if player.ID == f.bot {
					window = player.UNO
				}
			}
			if window == nil {
				t.Fatal("manual bot play did not open an ordinary UNO window")
			}
			*f.now = f.now.Add(time.Second)
			if task, err := f.rooms.NextBotTask(ctx, f.joined.Room.ID); err != nil || task != nil && task.PlayerID == f.bot {
				t.Fatal("AI declared UNO for the human operator", err)
			}
			if declare {
				_, err = f.manager.ExecuteBot(ctx, f.joined.Room.ID, f.bot, strings.Repeat("a", 43), control.LeaseID, room.ActionRequest{Type: game.SayUNO, WindowID: window.ID})
				if err != nil {
					t.Fatal(err)
				}
			}
			*f.now = window.Deadline.Add(150 * time.Millisecond)
			// Another bot's turn can be due first; only consume its normal turn.
			for i := 0; i < 4; i++ {
				task, err := f.rooms.NextBotTask(ctx, f.joined.Room.ID)
				if err != nil {
					t.Fatal(err)
				}
				if task == nil {
					break
				}
				if task.PlayerID == f.bot {
					if declare || task.Type != game.PenalizeUNO {
						t.Fatal("unexpected controlled bot task", task.Type)
					}
					if err := f.rooms.ApplyBot(ctx, f.joined.Room.ID, *task, game.Command{Type: game.PenalizeUNO, PlayerID: f.bot, WindowID: window.ID}); err != nil {
						t.Fatal(err)
					}
					break
				}
				if err := f.rooms.ApplyBot(ctx, f.joined.Room.ID, *task, game.Command{Type: game.Draw, PlayerID: task.PlayerID}); err != nil {
					t.Fatal(err)
				}
			}
			view, err := f.manager.BotState(ctx, f.joined.Room.ID, f.bot, strings.Repeat("a", 43), control.LeaseID)
			if err != nil {
				t.Fatal(err)
			}
			for _, player := range view.Room.State.Players {
				if player.ID != f.bot {
					continue
				}
				if declare && (player.HandCount != 1 || player.UNODeclared != window.ID) || !declare && (player.HandCount != 2 || player.UNOPenalized != window.ID) {
					t.Fatal("manual UNO did not preserve normal declaration and penalty rules")
				}
			}
		})
	}
}

func TestBotControlAcquisitionRollsBackIfAuditFails(t *testing.T) {
	f := controlFixture(t)
	ctx := context.Background()
	view, err := f.manager.Inspect(ctx, f.joined.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.write.Exec(`CREATE TRIGGER reject_control_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.AcquireBot(ctx, view.ID, f.bot, view.Instance, strings.Repeat("a", 43)); err == nil {
		t.Fatal("control committed without its privileged audit")
	}
	if task, err := f.rooms.NextBotTask(ctx, view.ID); err != nil || task == nil {
		t.Fatal("failed acquisition paused the real bot", err)
	}
}

func TestBotControlReleaseNearUNODeadlineResumesWithoutInvalidSchedule(t *testing.T) {
	f := controlFixture(t)
	ctx := context.Background()
	control := f.acquire(t)
	var play game.LegalAction
	for _, action := range control.Room.State.LegalActions {
		if action.Action == game.Play {
			play = action
			break
		}
	}
	if play.Action == "" {
		t.Fatal("fixture lacks legal play")
	}
	version := control.Room.State.Version
	result, err := f.manager.ExecuteBot(ctx, f.joined.Room.ID, f.bot, strings.Repeat("a", 43), control.LeaseID, room.ActionRequest{Type: game.Play, CardID: play.CardID, Color: play.Color, ExpectedVersion: &version})
	if err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	for _, p := range result.Room.State.Players {
		if p.ID == f.bot && p.UNO != nil {
			deadline = p.UNO.Deadline
		}
	}
	if deadline.IsZero() {
		t.Fatal("fixture lacks UNO window")
	}
	*f.now = deadline.Add(-100 * time.Millisecond)
	if err := f.manager.ReleaseBot(ctx, f.joined.Room.ID, f.bot, strings.Repeat("a", 43), control.LeaseID); err != nil {
		t.Fatal("late release could not resume the bot", err)
	}
	if _, err := f.rooms.State(ctx, f.joined.Room.ID, f.joined.Token); err != nil {
		t.Fatal("release persisted an unreadable schedule", err)
	}
}
