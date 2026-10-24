package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/room"
)

func TestBotScheduleSurvivesRestartAndCommitsWithAction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bots.db")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := openTest(t, path, clock, 10)
	svc, err := room.New(store, room.Config{Now: clock, BotRandom: func() float64 { return .5 }})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := svc.CreateSolo(ctx, "account-a", "Alice", room.SoloRequest{BotCount: 2, Difficulty: bot.Expert})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []game.Action{game.Draw, game.Pass} {
		if _, err := svc.Execute(ctx, joined.Room.ID, joined.Token, room.ActionRequest{Type: action}); err != nil {
			t.Fatal(err)
		}
	}
	if due, err := store.DueBots(ctx, now, 10); err != nil || len(due) != 0 {
		t.Fatal("early bot task", due, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	store = openTest(t, path, clock, 10)
	svc, err = room.New(store, room.Config{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.DueBots(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0] != joined.Room.ID {
		t.Fatalf("restart lost indexed job: %v, %v", due, err)
	}
	task, err := svc.NextBotTask(ctx, joined.Room.ID)
	if err != nil || task == nil || task.Profile.Difficulty != bot.Expert || len(task.Observation.Memory.Discard) != 1 {
		t.Fatal("restart lost computer state", err)
	}
	if err := svc.ApplyBot(ctx, joined.Room.ID, *task, game.Command{Type: game.Draw, PlayerID: task.PlayerID}); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DueBots(ctx, now, 10); err != nil || len(due) != 0 {
		t.Fatal("job deadline not committed with action", due, err)
	}
	if err := svc.Delete(ctx, joined.Room.ID, joined.Token); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DueBots(ctx, now.Add(time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatal("deleted room still queued", due, err)
	}
}

func TestSchemaTwoUpgradePreservesHumanRoomsWithoutBotJobs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	store := openTest(t, path, nil, 10)
	if err := store.Create(ctx, "human", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP INDEX rooms_next_bot_at`, `ALTER TABLE rooms DROP COLUMN next_bot_at`, `PRAGMA user_version=2`} {
		if _, err := store.write.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTest(t, path, nil, 10)
	var version int
	if err := store.read.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatal("schema not upgraded", version, err)
	}
	if err := store.Read(ctx, "human", func(r *room.Record) error {
		if r.Game.View("").Phase != game.Lobby || len(r.Bots) != 0 {
			t.Fatal("human room changed during schema upgrade")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DueBots(ctx, time.Now().Add(time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatal("human room was queued", due, err)
	}
}
