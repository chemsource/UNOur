package game

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// Arrange full physical cards, so every transition can also cross persistence.
func turnFixture(t *testing.T, specs []RuleSpec, hands [][]int, top int) *Game {
	t.Helper()
	rules, err := NewRegistry().Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Rules: rules, Shuffle: func([]Card) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	deck := StandardDeck()
	used := map[int]bool{top: true}
	g.s = state{phase: Playing, version: 20, direction: 1, active: deck[top-1].Color, discard: []Card{deck[top-1]}}
	if !g.s.active.Valid() {
		g.s.active = Red
	}
	for i, ids := range hands {
		p := player{id: PlayerID(string(rune('a' + i))), name: fmt.Sprintf("Player%d", i)}
		for _, id := range ids {
			if used[id] {
				t.Fatal("duplicate fixture card", id)
			}
			used[id] = true
			p.hand = append(p.hand, deck[id-1])
		}
		g.s.players = append(g.s.players, p)
	}
	for _, card := range deck {
		if !used[card.ID] {
			g.s.draw = append(g.s.draw, card)
		}
	}
	assertCards(t, g)
	return g
}
func closingSpecs() []RuleSpec {
	return append(DefaultRuleSpecs(), RuleSpec{Name: "stacking", Options: json.RawMessage(`{"mixed":true,"different_colors":true,"reverse":true}`)})
}
func TestLastDrawTwoSettlesOnceBeforeWinning(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			g := turnFixture(t, closingSpecs(), [][]int{{24}, {22, 25, 26}}, 4)
			cmd := Command{Type: Play, PlayerID: "a", CardID: 24}
			var err error
			if owned {
				_, err = g.ApplyOwned(cmd)
			} else {
				_, err = g.Apply(cmd)
			}
			if err != nil {
				t.Fatal(err)
			}
			v := g.View("b")
			if v.Phase != Finished || v.Winner != "a" || v.Players[1].HandCount != 5 || v.PendingDraw != 0 {
				t.Fatalf("last +2 left an open penalty loop: %+v", v)
			}
			g = restore(t, g)
			if g.CanApply(Command{Type: Draw, PlayerID: "a"}) == nil || g.CanApply(Command{Type: Draw, PlayerID: "b"}) == nil {
				t.Fatal("finished game permits another draw")
			}
			assertCards(t, g)
		})
	}
}
func TestLastDrawFourOnlyAllowsSettlementNotStacking(t *testing.T) {
	for _, resolution := range []Action{Draw, Challenge} {
		t.Run(string(resolution), func(t *testing.T) {
			g := turnFixture(t, closingSpecs(), [][]int{{102}, {47, 104, 26}}, 4)
			apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue})
			g = restore(t, g)
			if v := g.View("b"); v.Phase != Playing || v.ChallengeTarget != "a" || v.Turn != "b" {
				t.Fatal("last +4 lost its challenge window", v)
			}
			before := g.Checkpoint()
			for _, cmd := range []Command{{Type: Play, PlayerID: "b", CardID: 47}, {Type: Play, PlayerID: "b", CardID: 104, Color: Green}} {
				if _, err := g.Apply(cmd); err == nil {
					t.Fatal("last +4 could be redirected back to an empty player")
				}
				if !reflect.DeepEqual(before, g.Checkpoint()) {
					t.Fatal("rejected closing action changed state")
				}
			}
			for _, action := range g.LegalActions("b") {
				if action.Action != Draw && action.Action != Challenge {
					t.Fatal("advertised a non-settlement action", action)
				}
			}
			apply(t, g, Command{Type: resolution, PlayerID: "b"})
			want := 7
			if resolution == Challenge {
				want = 9
			}
			if v := g.View("b"); v.Phase != Finished || v.Winner != "a" || v.Players[1].HandCount != want {
				t.Fatal("last +4 failed to settle", v)
			}
			assertCards(t, g)
		})
	}
}
func TestLastDrawFourWithoutChallengeSettlesImmediately(t *testing.T) {
	specs := closingSpecs()
	specs = append(specs[:3], specs[4:]...)
	g := turnFixture(t, specs, [][]int{{102}, {47, 104, 26}}, 4)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue})
	if v := g.View("a"); v.Phase != Finished || v.Winner != "a" || v.Players[1].HandCount != 7 {
		t.Fatal("unchallengeable final +4 remained live", v)
	}
	assertCards(t, g)
}
func TestSkipDrawCannotBecomeAPostDrawPlayOrSecondDraw(t *testing.T) {
	specs := DefaultRuleSpecs()
	specs[1].Options = json.RawMessage(`{"skip_draw":true}`)
	specs[2].Options = json.RawMessage(`{"play_drawn_card":true,"play_any_after_draw":true}`)
	g := turnFixture(t, specs, [][]int{{20, 1, 2}, {26, 27}, {51, 52}}, 4)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 20})
	before := g.Checkpoint()
	v := g.View("b")
	if v.Turn != "c" || v.Players[1].HandCount != 3 || v.CanPass || len(g.LegalActions("b")) != 0 {
		t.Fatal("skipped draw retained an active turn", v)
	}
	for _, cmd := range []Command{{Type: Draw, PlayerID: "b"}, {Type: Pass, PlayerID: "b"}, {Type: Play, PlayerID: "b", CardID: 26}} {
		if _, err := g.Apply(cmd); err == nil {
			t.Fatal("skipped player could act after automatic draw", cmd)
		}
		if !reflect.DeepEqual(before, g.Checkpoint()) {
			t.Fatal("rejected skipped action changed hand")
		}
	}
	g = restore(t, g)
	if len(g.LegalActions("b")) != 0 {
		t.Fatal("restart removed skip restriction")
	}
	assertCards(t, g)
}

func TestClosingAndUNOPenaltyRemainVisibleAfterReload(t *testing.T) {
	g := turnFixture(t, closingSpecs(), [][]int{{102}, {26, 27}}, 4)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue})
	data, _ := json.Marshal(restore(t, g).View("a"))
	var closing map[string]any
	_ = json.Unmarshal(data, &closing)
	if closing["finishing_player"] != "a" {
		t.Fatal("empty hand has no settlement explanation")
	}
	g = turnFixture(t, DefaultRuleSpecs(), [][]int{{1, 2}, {26, 27}}, 4)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	window := g.View("a").Players[0].UNO
	apply(t, g, Command{Type: PenalizeUNO, PlayerID: "a", WindowID: window.ID, At: window.Deadline})
	data, _ = json.Marshal(restore(t, g).View("a"))
	var state struct {
		Players []struct {
			Penalized uint64 `json:"uno_penalized"`
		} `json:"players"`
	}
	_ = json.Unmarshal(data, &state)
	if state.Players[0].Penalized != window.ID {
		t.Fatal("automatic UNO penalty is indistinguishable from a normal draw")
	}
}

func TestTwoPlayerSkipPenaltyDoesNotGiveVictimADrawPhase(t *testing.T) {
	specs := DefaultRuleSpecs()
	specs[1].Options = json.RawMessage(`{"skip_draw":true}`)
	specs[2].Options = json.RawMessage(`{"play_any_after_draw":true}`)
	g := turnFixture(t, specs, [][]int{{20, 1, 2}, {26, 27}}, 4)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 20})
	v := g.View("b")
	if v.Turn != "a" || v.Players[1].HandCount != 3 || v.CanPass || len(g.LegalActions("b")) != 0 {
		t.Fatal("two-player skip gave the victim another action", v)
	}
	if g.CanApply(Command{Type: Draw, PlayerID: "b"}) == nil {
		t.Fatal("victim could manually draw after the automatic penalty")
	}
	assertCards(t, g)
}

func TestFinalPenaltyOwnedFailureIsAtomic(t *testing.T) {
	g := turnFixture(t, closingSpecs(), [][]int{{24}, {26, 27}}, 4)
	g.s.players[1].hand = append(g.s.players[1].hand, g.s.draw[1:]...)
	g.s.draw = g.s.draw[:1:1]
	g.shuffle = func([]Card) error { return fmt.Errorf("recycle failed") }
	before := g.Checkpoint()
	if _, err := g.ApplyOwned(Command{Type: Play, PlayerID: "a", CardID: 24}); err == nil {
		t.Fatal("final settlement ignored recycle error")
	}
	if !reflect.DeepEqual(before, g.Checkpoint()) {
		t.Fatal("failed final settlement partially committed")
	}
	assertCards(t, g)
}

func TestPenaltyDrawEndsTurnEvenWhenAnyHandCardIsAllowedAfterNormalDraw(t *testing.T) {
	for _, id := range []int{24, 102} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			specs := DefaultRuleSpecs()
			specs[2].Options = json.RawMessage(`{"play_any_after_draw":true}`)
			g := turnFixture(t, specs, [][]int{{id, 1, 2}, {26, 27}}, 4)
			cmd := Command{Type: Play, PlayerID: "a", CardID: id}
			if id == 102 {
				cmd.Color = Blue
			}
			apply(t, g, cmd)
			apply(t, g, Command{Type: Draw, PlayerID: "b"})
			g = restore(t, g)
			v := g.View("b")
			if v.Turn != "a" || v.CanPass || v.DrawnCardID != 0 || v.PendingDraw != 0 {
				t.Fatal("forced penalty draw entered the normal post-draw phase", v)
			}
			for _, cmd := range []Command{{Type: Play, PlayerID: "b", CardID: 26}, {Type: Draw, PlayerID: "b"}, {Type: Pass, PlayerID: "b"}} {
				if g.CanApply(cmd) == nil {
					t.Fatal("penalty recipient could act twice", cmd)
				}
			}
			assertCards(t, g)
		})
	}
}
