package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"unour/internal/room"
	"unour/internal/store/memory"
)

func TestTransactionsArePerRoomAndSweepSkipsBusyRoom(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	s, err := memory.New(memory.Config{MaxRooms: 2, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"busy", "other"} {
		if err := s.Create(ctx, id, &room.Record{}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Transact(ctx, "busy", func(*room.Record) error { close(entered); <-release; return nil })
	}()
	<-entered
	otherDone := make(chan error, 1)
	go func() { otherDone <- s.Transact(ctx, "other", func(*room.Record) error { return nil }) }()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one room blocked another")
	}
	n, err := s.Sweep(ctx, now.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep busy room: n=%d err=%v", n, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, "busy", func(*room.Record) error { return nil }); err != nil {
		t.Fatal("busy room was deleted", err)
	}
}

func TestConcurrentCreateHasSingleWinner(t *testing.T) {
	s, err := memory.New(memory.Config{MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Create(context.Background(), "same", &room.Record{}) }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, room.ErrExists) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("created %d rooms with same ID", winners)
	}
}

func TestCancelledAndDeletedTransactionsDoNotRun(t *testing.T) {
	s, err := memory.New(memory.Config{MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Create(ctx, "a", &room.Record{}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	err = s.Transact(cancelled, "a", func(*room.Record) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled transaction ran")
	}
	denied := errors.New("denied")
	if err := s.Delete(ctx, "a", func(*room.Record) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, "a", func(*room.Record) error { return nil }); err != nil {
		t.Fatal("failed delete removed room")
	}
	if err := s.Delete(ctx, "a", func(*room.Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, "a", func(*room.Record) error { called = true; return nil }); !errors.Is(err, room.ErrNotFound) || called {
		t.Fatal("deleted room transaction ran")
	}
}
