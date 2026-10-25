package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"unour/game"
	"unour/internal/room"
)

func TestRestartPreservesQueueVotesDepartureAndNextRound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	store := openTest(t, path, nil, 10)
	svc := serviceTest(t, store, nil)
	a, b := started(t, svc)
	c, err := svc.JoinAccount(ctx, "table", "carol", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.SpectateAccount(ctx, "table", "dave", "Dave")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Leave(ctx, "table", b.Token); err != nil {
		t.Fatal(err)
	}
	first, err := svc.VoteRematch(ctx, "table", a.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTest(t, path, nil, 10)
	svc = serviceTest(t, store, nil)
	restored, err := svc.State(ctx, "table", c.Token)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != first.Revision || len(restored.RematchVotes) != 1 || len(restored.Spectators) != 2 || len(restored.Departed) != 1 {
		t.Fatalf("metadata lost: %+v", restored)
	}
	next, err := svc.VoteRematch(ctx, "table", c.Token, room.RematchRequest{Round: 1, Agree: true, RosterVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	if next.Round != 2 || next.State.Phase != game.Playing || len(next.State.Players) != 2 {
		t.Fatalf("round failed: %+v", next)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTest(t, path, nil, 10)
	svc = serviceTest(t, store, nil)
	observed, err := svc.State(ctx, "table", d.Token)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Round != 2 || observed.Role != "spectator" {
		t.Fatal("new round not durable")
	}
	for _, p := range observed.State.Players {
		if len(p.Hand) > 0 {
			t.Fatal("hidden hand exposed after restart")
		}
	}
}
