package game

import (
	"sort"
	"testing"
)

func TestTwoPlayerPenaltyReverseRedirectsPool(t *testing.T) {
	g := fixture(t, StackingRule{Reverse: true})
	g.s.players = g.s.players[:2]
	g.s.turn = 1
	g.s.pending = 2
	g.s.pendingKind = DrawTwo
	g.s.players[1].hand[0] = card(4, Red, Reverse)
	apply(t, g, Command{Type: Play, PlayerID: "b", CardID: 4})
	if got := g.View("a"); got.Turn != "a" || got.PendingDraw != 2 || got.Direction != -1 {
		t.Fatalf("penalty did not return to opponent: %+v", got)
	}
}

func TestMixedStackStillHonorsDrawTwoColorRestriction(t *testing.T) {
	for _, different := range []bool{false, true} {
		g := fixture(t, StackingRule{Mixed: true, DifferentColors: different})
		g.s.pending = 4
		g.s.pendingKind = WildDrawFour
		g.s.players[0].hand[0] = card(1, Blue, DrawTwo)
		_, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1, At: testTime})
		if (err == nil) != different {
			t.Fatalf("different_colors=%v allowed=%v err=%v", different, err == nil, err)
		}
	}
}

func TestMaximumDealReservesAnOpeningNumber(t *testing.T) {
	g, err := New(Options{MaxPlayers: 10, InitialHand: 10, Shuffle: func(cards []Card) error {
		// 76 number cards first; the final eight undealt cards are all actions.
		sort.SliceStable(cards, func(i, j int) bool { return cards[i].Kind == Number && cards[j].Kind != Number })
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []PlayerID{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		if err := g.Join(id, string(id)); err != nil {
			t.Fatal(err)
		}
	}
	apply(t, g, Command{Type: Start, PlayerID: "a"})
	if g.View("a").DrawPileCount != 7 || g.View("a").TopCard.Kind != Number {
		t.Fatal("maximum hand deal has invalid opening")
	}
	assertCards(t, g)
}
