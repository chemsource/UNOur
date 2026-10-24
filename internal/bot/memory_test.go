package bot

import (
	"reflect"
	"testing"
	"unour/game"
)

func TestMemoryTracksPublicDiscardAndRecycle(t *testing.T) {
	deck := game.StandardDeck()
	m := Memory{Discard: []game.Card{deck[7]}}
	start := game.View{TopCard: &deck[0], DiscardCount: 1}
	m.Observe(game.View{Phase: game.Lobby}, start, game.Command{Type: game.Start}, []game.Event{{Type: "started"}})
	if len(m.Discard) != 1 || m.Discard[0].ID != 1 {
		t.Fatal("start did not reset discard")
	}
	after := game.View{TopCard: &deck[101], DiscardCount: 2, ChallengeTarget: "a", ActiveColor: game.Blue}
	start.ActiveColor = game.Red
	m.Observe(start, after, game.Command{Type: game.Play, PlayerID: "a", CardID: 102, Color: game.Blue}, []game.Event{{Type: "played", PlayerID: "a"}})
	if len(m.Discard) != 2 || m.ChallengeColor != game.Red {
		t.Fatalf("lost public play: %+v", m)
	}
	cloned := m.Clone()
	cloned.Discard[0] = deck[3]
	cloned.Beliefs[0].Weight = 99
	if m.Discard[0].ID != 1 || m.Beliefs[0].Weight == 99 {
		t.Fatal("memory clone aliases input")
	}
	recycled := after
	recycled.DiscardCount = 1
	recycled.ChallengeTarget = ""
	m.Observe(after, recycled, game.Command{Type: game.Draw, PlayerID: "b"}, []game.Event{{Type: "drew", PlayerID: "b", Count: 4}})
	if len(m.Discard) != 1 || m.Discard[0].ID != 102 || m.ChallengeColor != "" {
		t.Fatalf("recycled cards remained known: %+v", m)
	}
}

func TestMemoryIgnoresHandsAndVoluntaryDrawIsSoft(t *testing.T) {
	deck := game.StandardDeck()
	before := game.View{ActiveColor: game.Red, TopCard: &deck[0], DiscardCount: 1}
	after := before
	m := Memory{}
	m.Observe(before, after, game.Command{Type: game.Draw, PlayerID: "b"}, []game.Event{{Type: "drew", PlayerID: "b", Count: 1}})
	other := Memory{}
	before.Players = []game.PlayerView{{ID: "b", Hand: deck[1:10]}}
	other.Observe(before, after, game.Command{Type: game.Draw, PlayerID: "b"}, []game.Event{{Type: "drew", PlayerID: "b", Count: 1}})
	if !reflect.DeepEqual(m, other) {
		t.Fatal("memory inspected hidden hand")
	}
	if len(m.Beliefs) != 1 || m.Beliefs[0].Weight >= 0 || m.Beliefs[0].Weight < -2 {
		t.Fatal("draw must provide only weak negative evidence")
	}
}
