package room_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/room"
	"unour/internal/store/memory"
)

func soloService(t *testing.T, capacity int) (*room.Service, *memory.Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store, err := memory.New(memory.Config{MaxRooms: capacity, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	s, err := room.New(store, room.Config{
		Now: func() time.Time { return now }, BotRandom: func() float64 { return 0.5 },
		GameOptions: game.Options{Shuffle: func([]game.Card) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, store, &now
}

func TestSoloCreatesStartedTableAndRecoversOwner(t *testing.T) {
	s, store, _ := soloService(t, 3)
	ctx := context.Background()
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 2, Difficulty: bot.Hard})
	if err != nil {
		t.Fatal(err)
	}
	v := joined.Room
	if v.HostID != joined.PlayerID || v.State.You != joined.PlayerID || v.State.Phase != game.Playing || len(v.State.Players) != 3 || len(v.Bots) != 2 {
		t.Fatalf("not a started solo table: %+v", v)
	}
	for _, p := range v.State.Players {
		if p.ID == joined.PlayerID {
			if len(p.Hand) != 7 {
				t.Fatal("owner did not receive own hand")
			}
		} else if len(p.Hand) != 0 || v.Bots[p.ID].Difficulty != bot.Hard {
			t.Fatal("computer hand leaked or difficulty lost")
		}
	}
	if err := store.Read(ctx, v.ID, func(r *room.Record) error {
		if len(r.Sessions) != 1 || len(r.Accounts) != 1 {
			t.Fatal("computers received client credentials")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if guest, err := s.JoinAccount(ctx, v.ID, "account-b", "Bob"); err != nil || guest.Room.Role != "spectator" {
		t.Fatalf("invited guest did not spectate: %v", err)
	}
	again, err := s.JoinAccount(ctx, v.ID, "account-a", "Alice")
	if err != nil || again.PlayerID != joined.PlayerID || len(again.Room.State.Players) != 3 || again.Token == joined.Token {
		t.Fatalf("owner recovery failed: %+v, %v", again, err)
	}
	if _, err := s.State(ctx, v.ID, joined.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatal("old credential still valid")
	}
}

func TestSoloInvalidSetupDoesNotConsumeCapacity(t *testing.T) {
	s, _, _ := soloService(t, 1)
	for _, req := range []room.SoloRequest{
		{BotCount: 0, Difficulty: bot.Normal}, {BotCount: 10, Difficulty: bot.Normal},
		{BotCount: -1, Difficulty: bot.Normal}, {BotCount: 1, Difficulty: "unknown"},
	} {
		if _, err := s.CreateSolo(context.Background(), "account-a", "Alice", req); err == nil {
			t.Fatalf("invalid setup accepted: %+v", req)
		}
	}
	joined, err := s.CreateSolo(context.Background(), "account-a", "Alice", room.SoloRequest{BotCount: 9, Difficulty: bot.Easy})
	if err != nil || len(joined.Room.State.Players) != 10 {
		t.Fatalf("maximum table failed or invalid attempts used capacity: %v", err)
	}
	wantNames := []string{"Alice", "Bot1", "Bot2", "Bot3", "Bot4", "Bot5", "Bot6", "Bot7", "Bot8", "Bot9"}
	for i, player := range joined.Room.State.Players {
		if player.Name != wantNames[i] {
			t.Fatalf("seat %d has name %q, want %q", i, player.Name, wantNames[i])
		}
	}
}

func TestPersistedBotDisplayNamesAreCanonicalWithoutChangingHumanNames(t *testing.T) {
	s, store, _ := soloService(t, 1)
	ctx := context.Background()
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Normal})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, joined.Room.ID, func(r *room.Record) error {
		cp := r.Game.Checkpoint()
		if cp.Players[1].Name != "Bot1" {
			t.Fatal("new computer did not receive its canonical name")
		}
		cp.Players[1].Name = "小北"
		rules, err := game.NewRegistry().Build(r.Rules)
		if err != nil {
			return err
		}
		r.Game, err = game.Restore(cp, rules, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view, err := s.State(ctx, joined.Room.ID, joined.Token)
	if err != nil {
		t.Fatal(err)
	}
	if view.State.Players[0].Name != "Alice" || view.State.Players[1].Name != "Bot1" {
		t.Fatalf("unexpected public names: %+v", view.State.Players)
	}
}

func TestBotUNODeclarationIsPublishedToHuman(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store, err := memory.New(memory.Config{MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err := room.New(store, room.Config{
		Now: func() time.Time { return now }, BotRandom: func() float64 { return 0.5 },
		GameOptions: game.Options{InitialHand: 2, Shuffle: func([]game.Card) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Expert})
	if err != nil {
		t.Fatal(err)
	}
	passHuman(t, s, joined)
	now = now.Add(3 * time.Second)
	play, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || play == nil {
		t.Fatalf("missing bot turn: %v", err)
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *play, game.Command{Type: game.Play, PlayerID: play.PlayerID, CardID: 2}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	call, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || call == nil || call.Type != game.SayUNO {
		t.Fatalf("missing bot declaration: %+v, %v", call, err)
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *call, game.Command{Type: game.SayUNO, PlayerID: call.PlayerID, WindowID: call.WindowID}); err != nil {
		t.Fatal(err)
	}
	// The human only polls after both the play and declaration have completed.
	view, err := s.State(ctx, joined.Room.ID, joined.Token)
	if err != nil {
		t.Fatal(err)
	}
	computer := view.State.Players[1]
	if computer.UNODeclared != call.WindowID || computer.UNO != nil || len(computer.Hand) != 0 {
		t.Fatalf("computer declaration missing or private hand leaked: %+v", computer)
	}
}

func passHuman(t *testing.T, s *room.Service, joined room.JoinResult) {
	t.Helper()
	for _, action := range []game.Action{game.Draw, game.Pass} {
		if _, err := s.Execute(context.Background(), joined.Room.ID, joined.Token, room.ActionRequest{Type: action}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBotTaskIsDetachedAndStaleDecisionCannotActTwice(t *testing.T) {
	s, store, now := soloService(t, 1)
	ctx := context.Background()
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Normal})
	if err != nil {
		t.Fatal(err)
	}
	passHuman(t, s, joined)
	if task, err := s.NextBotTask(ctx, joined.Room.ID); err != nil || task != nil {
		t.Fatal("computer acted without its reaction delay", err)
	}
	*now = now.Add(3 * time.Second)
	task, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || task == nil {
		t.Fatal("missing due task", err)
	}
	for _, p := range task.Observation.View.Players {
		if p.ID != task.PlayerID && len(p.Hand) != 0 {
			t.Fatal("opponent hand reached AI input")
		}
	}
	task.Observation.Memory.Discard[0].ID = -1
	if err := store.Read(ctx, joined.Room.ID, func(r *room.Record) error {
		if r.BotMemory.Discard[0].ID <= 0 {
			t.Fatal("AI input aliases live room memory")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cmd := game.Command{Type: game.Draw, PlayerID: task.PlayerID}
	if err := s.ApplyBot(ctx, joined.Room.ID, *task, cmd); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *task, cmd); !errors.Is(err, room.ErrConflict) {
		t.Fatalf("late decision applied twice: %v", err)
	}
	view, err := s.State(ctx, joined.Room.ID, joined.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range view.State.Players {
		if p.ID == task.PlayerID && p.HandCount != 8 {
			t.Fatalf("expected exactly one physical draw: %d", p.HandCount)
		}
	}
}

func TestExpiredBotUNOAfterDowntimeReceivesRealPenalty(t *testing.T) {
	s, store, now := soloService(t, 1)
	ctx := context.Background()
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Normal})
	if err != nil {
		t.Fatal(err)
	}
	passHuman(t, s, joined)
	*now = now.Add(3 * time.Second)
	if err := store.Transact(ctx, joined.Room.ID, func(r *room.Record) error {
		cp := r.Game.Checkpoint()
		cp.Deck = append(cp.Deck, cp.Players[1].Hand[2:]...)
		cp.Players[1].Hand = cp.Players[1].Hand[:2]
		rules, err := game.NewRegistry().Build(r.Rules)
		if err != nil {
			return err
		}
		r.Game, err = game.Restore(cp, rules, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	task, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || task == nil {
		t.Fatal("missing task", err)
	}
	var card game.Card
	for _, p := range task.Observation.View.Players {
		if p.ID == task.PlayerID {
			card = p.Hand[0]
		}
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *task, game.Command{Type: game.Play, PlayerID: task.PlayerID, CardID: card.ID}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Second)
	uno, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || uno == nil || uno.Type != game.PenalizeUNO {
		t.Fatalf("missed UNO was not recovered as penalty: %+v, %v", uno, err)
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *uno, game.Command{Type: uno.Type, PlayerID: uno.PlayerID, WindowID: uno.WindowID}); err != nil {
		t.Fatal(err)
	}
	view, err := s.State(ctx, joined.Room.ID, joined.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range view.State.Players {
		if p.ID == uno.PlayerID && (p.HandCount != 2 || p.UNO != nil) {
			t.Fatal("UNO penalty did not move exactly one real card")
		}
	}
}

func TestBotRetryDefersPersistentTaskWithoutAcceptingOldDecision(t *testing.T) {
	s, store, now := soloService(t, 1)
	ctx := context.Background()
	joined, err := s.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Normal})
	if err != nil {
		t.Fatal(err)
	}
	passHuman(t, s, joined)
	*now = now.Add(3 * time.Second)
	task, err := s.NextBotTask(ctx, joined.Room.ID)
	if err != nil || task == nil {
		t.Fatal("missing due task", err)
	}
	if err := s.RetryBot(ctx, joined.Room.ID, *task, time.Second); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DueBots(ctx, *now, 10); err != nil || len(due) != 0 {
		t.Fatal("failed task was not deferred", due, err)
	}
	if err := s.ApplyBot(ctx, joined.Room.ID, *task, game.Command{Type: game.Draw, PlayerID: task.PlayerID}); !errors.Is(err, room.ErrConflict) {
		t.Fatalf("old decision survived reschedule: %v", err)
	}
	*now = now.Add(time.Second)
	if task, err := s.NextBotTask(ctx, joined.Room.ID); err != nil || task == nil {
		t.Fatal("deferred task disappeared", err)
	}
}
