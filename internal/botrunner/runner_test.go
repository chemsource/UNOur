package botrunner_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/botrunner"
	"unour/internal/room"
	"unour/internal/store/memory"
)

func dueTables(t *testing.T, count int) (*memory.Store, *room.Service, []room.JoinResult, func() time.Time) {
	t.Helper()
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	store, err := memory.New(memory.Config{MaxRooms: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := room.New(store, room.Config{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var tables []room.JoinResult
	for i := 0; i < count; i++ {
		joined, err := rooms.CreateSolo(context.Background(), "account-a", "Alice", room.SoloRequest{BotCount: 1, Difficulty: bot.Easy})
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []game.Action{game.Draw, game.Pass} {
			if _, err := rooms.Execute(context.Background(), joined.Room.ID, joined.Token, room.ActionRequest{Type: action}); err != nil {
				t.Fatal(err)
			}
		}
		tables = append(tables, joined)
	}
	clock.Add(int64(3 * time.Second))
	return store, rooms, tables, now
}

func TestRunnerAdvancesRealComputerWithoutPlayerRequests(t *testing.T) {
	store, rooms, tables, now := dueTables(t, 1)
	before, err := rooms.State(context.Background(), tables[0].Room.ID, tables[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := botrunner.New(store, rooms, bot.New(nil), botrunner.Config{Now: now, Interval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("background computer never acted")
		case <-tick.C:
			view, err := rooms.State(context.Background(), tables[0].Room.ID, tables[0].Token)
			if err != nil {
				t.Fatal(err)
			}
			if view.State.Version > before.State.Version {
				for _, p := range view.State.Players {
					if p.ID != view.State.You && len(p.Hand) > 0 {
						t.Fatal("background update leaked another hand")
					}
				}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("runner failed to stop")
				}
				return
			}
		}
	}
}

type waitingBrain struct {
	started chan struct{}
	stopped atomic.Int32
}

func (b *waitingBrain) Decide(ctx context.Context, _ bot.Observation, _ bot.Profile, _ bot.SearchConfig) (bot.Decision, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	b.stopped.Add(1)
	return bot.Decision{}, ctx.Err()
}

func TestRunnerBoundsConcurrentSearchAndCancellationWaitsForWorkers(t *testing.T) {
	store, rooms, tables, now := dueTables(t, 5)
	brain := &waitingBrain{started: make(chan struct{}, 10)}
	runner, err := botrunner.New(store, rooms, brain, botrunner.Config{Now: now, Workers: 2, Interval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-brain.started:
		case <-time.After(time.Second):
			t.Fatal("workers never started")
		}
	}
	select {
	case <-brain.started:
		t.Fatal("worker concurrency exceeded two")
	case <-time.After(40 * time.Millisecond):
	}
	// A real room write must finish while both searches are blocked.
	recovered := make(chan error, 1)
	go func() {
		_, err := rooms.JoinAccount(context.Background(), tables[0].Room.ID, "account-a", "Alice")
		recovered <- err
	}()
	select {
	case err := <-recovered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("search held the room write lock")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner cancellation left workers running")
	}
	if brain.stopped.Load() != 2 {
		t.Fatal("Run returned before both searches stopped")
	}
}
