package room_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"unour/game"
	"unour/internal/room"
	"unour/internal/store/memory"
)

func service(t *testing.T, maxRooms int, now func() time.Time) *room.Service {
	t.Helper()
	store, err := memory.New(memory.Config{MaxRooms: maxRooms, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	s, err := room.New(store, room.Config{Now: now, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthenticationHostAndPrivateState(t *testing.T) {
	ctx := context.Background()
	s := service(t, 10, nil)
	if _, err := s.Create(ctx, room.CreateRequest{ID: "table"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(ctx, "table", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Join(ctx, "table", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if a.Token == b.Token || a.PlayerID == b.PlayerID {
		t.Fatal("sessions share identity")
	}
	if _, err := s.State(ctx, "table", "forged"); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("forged token: %v", err)
	}
	if _, err := s.Execute(ctx, "table", b.Token, room.ActionRequest{Type: game.Start}); !errors.Is(err, room.ErrForbidden) {
		t.Fatalf("nonhost start: %v", err)
	}
	r, err := s.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Start})
	if err != nil {
		t.Fatal(err)
	}
	if r.Room.State.You != a.PlayerID || len(r.Room.State.Players[0].Hand) != 7 || len(r.Room.State.Players[1].Hand) != 0 {
		t.Fatal("wrong private view")
	}
	bv, err := s.State(ctx, "table", b.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(bv.State.Players[0].Hand) != 0 || len(bv.State.Players[1].Hand) != 7 {
		t.Fatal("state returned other player's cards")
	}
	if _, err := s.Create(ctx, room.CreateRequest{ID: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State(ctx, "second", a.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("cross-room token accepted: %v", err)
	}
	old := uint64(0)
	if _, err := s.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Draw, ExpectedVersion: &old}); err == nil {
		t.Fatal("stale version accepted")
	}
	if err := s.Delete(ctx, "table", b.Token); !errors.Is(err, room.ErrForbidden) {
		t.Fatalf("nonhost delete: %v", err)
	}
	if err := s.Delete(ctx, "table", a.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State(ctx, "table", a.Token); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("deleted room exists: %v", err)
	}
}

func TestConcurrentJoinHonorsRoomCapacity(t *testing.T) {
	s := service(t, 1, nil)
	ctx := context.Background()
	if _, err := s.Create(ctx, room.CreateRequest{ID: "concurrent"}); err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Join(ctx, "concurrent", "player"); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := success.Load(); got != 10 {
		t.Fatalf("joined %d players, want 10", got)
	}
	if _, err := s.Create(ctx, room.CreateRequest{ID: "overflow"}); !errors.Is(err, room.ErrCapacity) {
		t.Fatalf("unbounded room count: %v", err)
	}
}

func TestInvalidRulesAndInputsDoNotCreateRooms(t *testing.T) {
	s := service(t, 1, nil)
	ctx := context.Background()
	for _, req := range []room.CreateRequest{
		{ID: "bad/room"},
		{ID: "bad", Rules: []game.RuleSpec{{Name: "unknown"}}},
		{ID: "bad", Rules: []game.RuleSpec{{Name: "actions", Options: []byte(`{"typo":true}`)}}},
		{ID: "bad", Rules: []game.RuleSpec{}},
	} {
		if _, err := s.Create(ctx, req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	if _, err := s.Create(ctx, room.CreateRequest{ID: "valid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, "valid", " \n "); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestRoomIdleTTL(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	s := service(t, 10, func() time.Time { return now })
	ctx := context.Background()
	if _, err := s.Create(ctx, room.CreateRequest{ID: "expires"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	n, err := s.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep=%d err=%v", n, err)
	}
	if _, err := s.Join(ctx, "expires", "Alice"); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("expired room exists: %v", err)
	}
}

func TestCreateRejectsAnUnstartableConfiguredHandLimit(t *testing.T) {
	s := service(t, 10, nil)
	specs := append(game.DefaultRuleSpecs(), game.RuleSpec{Name: "hand_limit", Options: []byte(`{"limit":4}`)})
	if _, err := s.Create(context.Background(), room.CreateRequest{ID: "bad-limit", Rules: specs}); err == nil {
		t.Fatal("created a room whose hand limit is below the standard initial hand")
	}
}
