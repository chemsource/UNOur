package game

import (
	"errors"
	"reflect"
	"testing"
)

// Keep every physical card, with a nearly exhausted deck and a long discard.
func recyclingGame(t *testing.T, remaining int) *Game {
	t.Helper()
	g := checkpointGame(t, 2)
	top := g.s.discard[0]
	g.s.discard = append(append([]Card(nil), g.s.draw[remaining:]...), top)
	g.s.draw = g.s.draw[:remaining:remaining]
	assertCards(t, g)
	return g
}

func TestExhaustedDeckRefillsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		remaining, penalty, deck, hand, next int
		turn                                 PlayerID
	}{
		{"ordinary draw", 1, 0, 102, 3, 108, "a"},
		{"penalty consumes old deck exactly", 2, 2, 101, 4, 108, "b"},
		{"penalty continues across a reshuffle", 1, 4, 99, 6, 105, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := recyclingGame(t, tc.remaining)
			g.s.pending = tc.penalty
			if tc.penalty > 0 {
				g.s.pendingKind = DrawTwo
			}
			shuffles := 0
			g.shuffle = func(cards []Card) error {
				shuffles++
				for i, j := 0, len(cards)-1; i < j; i, j = i+1, j-1 {
					cards[i], cards[j] = cards[j], cards[i]
				}
				return nil
			}
			apply(t, g, Command{Type: Draw, PlayerID: "a"})
			v := g.View("a")
			if v.DrawPileCount != tc.deck || v.DiscardCount != 1 || v.TopCard.ID != 4 || v.ActiveColor != Red || v.Players[0].HandCount != tc.hand || v.Turn != tc.turn || v.PendingDraw != 0 {
				t.Fatalf("wrong state after exhaustion: %+v", v)
			}
			if shuffles != 1 || g.s.draw[0].ID != tc.next {
				t.Fatalf("discard was not shuffled exactly once: shuffles=%d first=%+v", shuffles, g.s.draw[0])
			}
			assertCards(t, g)
		})
	}
}

func TestImmediateRecycleRetainsWildAndChosenColor(t *testing.T) {
	g := recyclingGame(t, 1)
	top := len(g.s.discard) - 1
	for i, c := range g.s.discard {
		if c.ID == 101 {
			g.s.discard[i], g.s.discard[top] = g.s.discard[top], c
			break
		}
	}
	g.s.active = Blue
	g.shuffle = func([]Card) error { return nil }
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	if v := g.View("a"); v.TopCard.ID != 101 || v.TopCard.Color != WildColor || v.ActiveColor != Blue || v.DrawPileCount != 102 || v.DiscardCount != 1 {
		t.Fatalf("wild or chosen color changed during recycling: %+v", v)
	}
	assertCards(t, g)
}

func TestEmptyDeckRefillsWhenAPlayCreatesRecyclableDiscard(t *testing.T) {
	for _, owned := range []bool{false, true} {
		g := checkpointGame(t, 2)
		g.s.players[0].hand = append(g.s.players[0].hand, g.s.draw...)
		g.s.draw = nil
		g.shuffle = func([]Card) error { return nil }
		cmd := Command{Type: Play, PlayerID: "a", CardID: 1}
		var err error
		if owned {
			_, err = g.ApplyOwned(cmd)
		} else {
			_, err = g.Apply(cmd)
		}
		if err != nil {
			t.Fatal(err)
		}
		if v := g.View("a"); v.DrawPileCount != 1 || v.DiscardCount != 1 || v.TopCard.ID != 1 || g.s.draw[0].ID != 4 {
			t.Fatalf("owned=%v: empty deck was not refilled after play: %+v", owned, v)
		}
		assertCards(t, g)
	}
}

func TestImmediateRecycleFailureRollsBackWholeCommand(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, action := range []Action{Draw, Play} {
			remaining := 1
			if action == Play {
				remaining = 0
			}
			g := recyclingGame(t, remaining)
			g.shuffle = func(cards []Card) error {
				cards[0], cards[len(cards)-1] = cards[len(cards)-1], cards[0]
				return errors.New("entropy unavailable")
			}
			before := g.Checkpoint()
			cmd := Command{Type: action, PlayerID: "a", CardID: 1}
			var err error
			if owned {
				_, err = g.ApplyOwned(cmd)
			} else {
				_, err = g.Apply(cmd)
			}
			if err == nil {
				t.Fatalf("owned=%v action=%s: ignored recycling failure", owned, action)
			}
			if !reflect.DeepEqual(before, g.Checkpoint()) {
				t.Fatalf("owned=%v action=%s: recycling failure changed the game", owned, action)
			}
			assertCards(t, g)
		}
	}
}

func TestExhaustedDeckWithOnlyTopDiscardCreatesNoCards(t *testing.T) {
	g := checkpointGame(t, 2)
	g.s.players[0].hand = append(g.s.players[0].hand, g.s.draw[1:]...)
	g.s.draw = g.s.draw[:1]
	g.shuffle = func([]Card) error { t.Fatal("no recyclable discard to shuffle"); return nil }
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	if v := g.View("a"); v.DrawPileCount != 0 || v.DiscardCount != 1 || !v.CanPass {
		t.Fatalf("empty discard was recycled or draw phase lost: %+v", v)
	}
	assertCards(t, g)
}
