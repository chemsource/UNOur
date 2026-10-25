package room_test

import (
	"context"
	"errors"
	"testing"
	"unour/game"
	"unour/internal/room"
	"unour/internal/store/memory"
)

func TestSpectatorsQueueConsensusAndPersistence(t *testing.T) {
	ctx := context.Background()
	store, _ := memory.New(memory.Config{MaxRooms: 10})
	s, _ := room.New(store, room.Config{GameOptions: game.Options{InitialHand: 1, Shuffle: func([]game.Card) error { return nil }}})
	_, _ = s.Create(ctx, room.CreateRequest{ID: "lifecycle"})
	a, _ := s.JoinAccount(ctx, "lifecycle", "account-a", "Alice")
	b, _ := s.JoinAccount(ctx, "lifecycle", "account-b", "Bob")
	_, err := s.Execute(ctx, "lifecycle", a.Token, room.ActionRequest{Type: game.Start})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.JoinAccount(ctx, "lifecycle", "account-c", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	if c.Room.Role != "spectator" || len(c.Room.Spectators) != 1 || !c.Room.Spectators[0].NextRound || len(c.Room.State.Players) != 2 {
		t.Fatalf("midgame join changed seats: %+v", c.Room)
	}
	d, err := s.SpectateAccount(ctx, "lifecycle", "account-d", "Dave")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Room.State.Players {
		if len(p.Hand) > 0 {
			t.Fatal("spectator saw hand")
		}
	}
	if _, err := s.Execute(ctx, "lifecycle", d.Token, room.ActionRequest{Type: game.Draw}); err == nil {
		t.Fatal("spectator acted")
	}
	if _, err := s.VoteRematch(ctx, "lifecycle", d.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 3}); err == nil {
		t.Fatal("spectator voted")
	}
	if err := store.Transact(ctx, "lifecycle", func(r *room.Record) error {
		cp := r.Game.Checkpoint()
		cp.Discard = append(cp.Discard, cp.Players[0].Hand...)
		cp.Players[0].Hand = nil
		cp.ActiveColor = cp.Discard[len(cp.Discard)-1].Color
		cp.Phase = game.Finished
		cp.Winner = a.PlayerID
		rules, _ := game.NewRegistry().Build(r.Rules)
		g, e := game.Restore(cp, rules, nil)
		if e != nil {
			return e
		}
		r.Game = g
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.VoteRematch(ctx, "lifecycle", a.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if first.State.Phase != game.Finished || len(first.RematchVotes) != 1 {
		t.Fatal("started without consensus")
	}
	// Both the queue and approvals survive a complete codec round trip.
	codec := room.NewCodec(nil, nil)
	if err := store.Transact(ctx, "lifecycle", func(r *room.Record) error {
		data, e := codec.Encode(r)
		if e != nil {
			return e
		}
		restored, e := codec.Decode(data)
		if e != nil {
			return e
		}
		*r = *restored
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.VoteRematch(ctx, "lifecycle", b.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 3})
	final, err := s.VoteRematch(ctx, "lifecycle", c.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if final.State.Phase != game.Playing || len(final.State.Players) != 3 || final.Round != 2 || final.State.Version <= first.State.Version || len(final.Spectators) != 1 {
		t.Fatalf("bad rematch: %+v", final)
	}
	cv, _ := s.State(ctx, "lifecycle", c.Token)
	if cv.Role != "player" || len(cv.State.Players[2].Hand) != 1 {
		t.Fatal("queued player not promoted with private hand")
	}
	if err := s.Delete(ctx, "lifecycle", d.Token); !errors.Is(err, room.ErrForbidden) {
		t.Fatal("spectator dissolved room")
	}
	if err := s.Delete(ctx, "lifecycle", a.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State(ctx, "lifecycle", b.Token); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("room survived dissolution")
	}
}

func TestLeaveTransfersHostAndOldSeatCannotBlockRematch(t *testing.T) {
	ctx := context.Background()
	store, _ := memory.New(memory.Config{MaxRooms: 5})
	s, _ := room.New(store, room.Config{})
	_, _ = s.Create(ctx, room.CreateRequest{ID: "leaving"})
	a, _ := s.JoinAccount(ctx, "leaving", "aa", "Alice")
	b, _ := s.JoinAccount(ctx, "leaving", "bb", "Bob")
	_, _ = s.Execute(ctx, "leaving", a.Token, room.ActionRequest{Type: game.Start})
	c, _ := s.JoinAccount(ctx, "leaving", "cc", "Carol")
	if err := s.Leave(ctx, "leaving", a.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State(ctx, "leaving", a.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatal("departed token survived")
	}
	v, _ := s.State(ctx, "leaving", b.Token)
	if v.HostID != b.PlayerID || v.State.Phase != game.Finished || len(v.Departed) != 1 {
		t.Fatalf("host or finish: %+v", v)
	}
	if err := store.Read(ctx, "leaving", func(r *room.Record) error { _, e := room.NewCodec(nil, nil).Encode(r); return e }); err != nil {
		t.Fatal(err)
	}
	_, _ = s.VoteRematch(ctx, "leaving", b.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 4})
	v, err := s.VoteRematch(ctx, "leaving", c.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 4})
	if err != nil || v.State.Phase != game.Playing || len(v.State.Players) != 2 || v.State.Players[0].ID != b.PlayerID {
		t.Fatalf("departed player blocks round: %v %+v", err, v)
	}
	if err := s.Leave(ctx, "leaving", b.Token); err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(ctx, "leaving", c.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State(ctx, "leaving", c.Token); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("empty room not cleaned up")
	}
}

func TestRosterChangeInvalidatesConsentAndRejectsLateVote(t *testing.T) {
	ctx := context.Background()
	s, store, _ := soloService(t, 5)
	j, err := s.CreateSolo(ctx, "aa", "Alice", room.SoloRequest{BotCount: 1, Difficulty: "easy"})
	if err != nil {
		t.Fatal(err)
	}
	// A human joining a bot room reserves a seat; the owner leaving finishes the
	// game and transfers ownership to that queued viewer.
	c, err := s.JoinAccount(ctx, j.Room.ID, "cc", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Leave(ctx, j.Room.ID, j.Token); err != nil {
		t.Fatal(err)
	}
	v, _ := s.State(ctx, j.Room.ID, c.Token)
	old := v.RosterVersion
	// Pure viewing doesn't change the participating roster or invalidate votes.
	d, err := s.SpectateAccount(ctx, j.Room.ID, "dd", "Dave")
	if err != nil {
		t.Fatal(err)
	}
	if d.Room.RosterVersion != old {
		t.Fatal("pure observer changed roster")
	}
	changed, err := s.Participation(ctx, j.Room.ID, d.Token, true)
	if err != nil {
		t.Fatal(err)
	}
	if changed.RosterVersion == old {
		t.Fatal("reservation did not advance roster")
	}
	if _, err = s.VoteRematch(ctx, j.Room.ID, c.Token, room.RematchRequest{Round: 1, RosterVersion: old, Agree: true}); err == nil {
		t.Fatal("old roster consent accepted")
	}
	v, _ = s.State(ctx, j.Room.ID, c.Token)
	if len(v.RematchVotes) != 0 {
		t.Fatal("late vote mutated consent")
	}
	// A roster change clears prior consent; a late vote cannot recreate it.
	if _, err = s.VoteRematch(ctx, j.Room.ID, c.Token, room.RematchRequest{Round: 1, RosterVersion: changed.RosterVersion, Agree: true}); err != nil {
		t.Fatal(err)
	}
	unqueued, err := s.Participation(ctx, j.Room.ID, d.Token, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(unqueued.RematchVotes) != 0 {
		t.Fatal("roster change retained consent")
	}
	if _, err = s.VoteRematch(ctx, j.Room.ID, c.Token, room.RematchRequest{Round: 1, RosterVersion: changed.RosterVersion, Agree: true}); err == nil {
		t.Fatal("late consent was reused")
	}
	// Only Carol remains with a computer: her consent starts immediately.
	next, err := s.VoteRematch(ctx, j.Room.ID, c.Token, room.RematchRequest{Round: 1, RosterVersion: unqueued.RosterVersion, Agree: true})
	if err != nil {
		t.Fatal(err)
	}
	if next.State.Phase != game.Playing || next.Round != 2 || next.HostID != c.PlayerID || len(next.Bots) != 1 {
		t.Fatalf("computer consent/reset failed: %+v", next)
	}
	if err = store.Read(ctx, j.Room.ID, func(r *room.Record) error { _, e := room.NewCodec(nil, nil).Encode(r); return e }); err != nil {
		t.Fatal(err)
	}

}
