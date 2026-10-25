package game

import (
	"reflect"
	"testing"
	"time"
)

func TestAdminEditingPreservesCardsAndDrawRestriction(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	drawn := g.s.drawnCard
	replacement := g.s.players[1].hand[0].ID
	version := g.s.version
	if err := g.EditCards(CardEdit{Operation: "swap", CardID: drawn, ReplacementID: replacement}, version); err != nil {
		t.Fatal(err)
	}
	if g.s.drawnCard != replacement || g.s.version != version+1 {
		t.Fatal("draw restriction/version not updated")
	}
	if g.CanApply(Command{Type: Draw, PlayerID: "a"}) == nil {
		t.Fatal("editing allowed second draw")
	}
	assertCards(t, g)
	before := g.Checkpoint()
	for _, edit := range []CardEdit{
		{Operation: "swap", CardID: before.Discard[0].ID, ReplacementID: replacement},
		{Operation: "swap", CardID: replacement, ReplacementID: replacement},
		{Operation: "swap", CardID: replacement, ReplacementID: 999},
		{Operation: "move", CardID: replacement, ToPlayerID: "missing"},
		{Operation: "invalid", CardID: replacement},
	} {
		if err := g.EditCards(edit, g.s.version); err == nil {
			t.Fatalf("accepted %+v", edit)
		}
		if !reflect.DeepEqual(before, g.Checkpoint()) {
			t.Fatal("failed edit mutated state")
		}
	}
	if err := g.EditCards(CardEdit{Operation: "move", CardID: replacement, ToPlayerID: "b"}, version); err == nil {
		t.Fatal("stale version accepted")
	}
	// A move must not erase an outstanding draw restriction.
	if err := g.EditCards(CardEdit{Operation: "move", CardID: replacement, ToPlayerID: "b"}, g.s.version); err == nil {
		t.Fatal("removed active drawn card without replacement")
	}
}

func TestAdminMoveCancelsUNOAndCannotEmptyLiveHand(t *testing.T) {
	g := checkpointGame(t, 1)
	g.s.players[0].uno = &UNOWindow{ID: g.s.version, Deadline: time.Now().Add(time.Second)}
	if err := g.EditCards(CardEdit{Operation: "move", CardID: g.s.draw[0].ID, ToPlayerID: "a"}, g.s.version); err != nil {
		t.Fatal(err)
	}
	if g.s.players[0].uno != nil || len(g.s.players[0].hand) != 2 {
		t.Fatal("UNO window survived hand size change")
	}
	assertCards(t, g)
	if err := g.EditCards(CardEdit{Operation: "move", CardID: g.s.players[0].hand[0].ID}, g.s.version); err != nil {
		t.Fatal(err)
	}
	before := g.Checkpoint()
	if err := g.EditCards(CardEdit{Operation: "move", CardID: g.s.players[0].hand[0].ID}, g.s.version); err == nil {
		t.Fatal("removed last live card")
	}
	if !reflect.DeepEqual(before, g.Checkpoint()) {
		t.Fatal("failed move mutated state")
	}
	assertCards(t, g)
}
