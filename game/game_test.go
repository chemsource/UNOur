package game

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func card(id int, color Color, kind Kind) Card {
	return Card{ID: id, Color: color, Kind: kind, Number: 3}
}

func fixture(t testing.TB, extra ...Rule) *Game {
	t.Helper()
	registry := NewRegistry()
	rules, err := registry.Build(DefaultRuleSpecs())
	if err != nil {
		t.Fatal(err)
	}
	all := append(rules.Rules(), extra...)
	rules, err = NewRuleSet(all...)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Rules: rules, Shuffle: func([]Card) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	g.s = state{
		phase: Playing, direction: 1, active: Red, version: 5,
		players: []player{
			{id: "a", name: "Alice", hand: []Card{card(1, Red, Number), card(2, Blue, Number), card(3, WildColor, Wild)}},
			{id: "b", name: "Bob", hand: []Card{card(4, Green, Number), card(5, Blue, Number)}},
			{id: "c", name: "Carol", hand: []Card{card(6, Yellow, Number), card(7, Blue, Number)}},
		},
		discard: []Card{card(8, Red, Number)},
		draw:    []Card{card(9, Yellow, Number), card(10, Green, Number), card(11, Blue, Number), card(12, Red, Number), card(13, Red, Number), card(14, Blue, Number), card(15, Red, Number), card(16, Blue, Number)},
	}
	return g
}

func apply(t testing.TB, g *Game, c Command) Result {
	t.Helper()
	if c.At.IsZero() {
		c.At = testTime
	}
	r, err := g.Apply(c)
	if err != nil {
		t.Fatalf("%s: %v", c.Type, err)
	}
	return r
}

func TestDeckAndLifecycle(t *testing.T) {
	deck := StandardDeck()
	if len(deck) != 108 {
		t.Fatalf("deck has %d cards", len(deck))
	}
	seen := map[int]bool{}
	wilds, zeroes := 0, 0
	for _, c := range deck {
		if seen[c.ID] {
			t.Fatalf("duplicate card %d", c.ID)
		}
		seen[c.ID] = true
		if c.Color == WildColor {
			wilds++
		}
		if c.Kind == Number && c.Number == 0 {
			zeroes++
		}
	}
	if wilds != 8 || zeroes != 4 {
		t.Fatalf("wilds=%d zeroes=%d", wilds, zeroes)
	}
	g, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Join("a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(Command{Type: Start, PlayerID: "a"}); err == nil {
		t.Fatal("started with one player")
	}
	if err := g.Join("b", "Bob"); err != nil {
		t.Fatal(err)
	}
	apply(t, g, Command{Type: Start, PlayerID: "a"})
	v := g.View("a")
	if v.Phase != Playing || len(v.Players[0].Hand) != 7 || v.Players[1].HandCount != 7 || v.DrawPileCount != 93 || v.TopCard.Kind != Number {
		t.Fatalf("bad initial state: %+v", v)
	}
	if len(v.Players[1].Hand) != 0 {
		t.Fatal("opponent hand leaked")
	}
	if err := g.Join("c", "Carol"); err == nil {
		t.Fatal("joined running game")
	}
	before := g.View("a")
	if _, err := g.Apply(Command{Type: Start, PlayerID: "a"}); err == nil {
		t.Fatal("restarted running game")
	}
	if !reflect.DeepEqual(before, g.View("a")) {
		t.Fatal("rejected start changed state")
	}
}

func TestInvalidCommandsAreAtomic(t *testing.T) {
	for _, c := range []Command{
		{Type: Play, PlayerID: "b", CardID: 4},
		{Type: Play, PlayerID: "a", CardID: 999},
		{Type: Play, PlayerID: "a", CardID: 3, Color: "purple"},
		{Type: Play, PlayerID: "a", CardID: 3},
		{Type: Pass, PlayerID: "a"},
		{Type: Challenge, PlayerID: "a"},
		{Type: "unknown", PlayerID: "a"},
	} {
		t.Run(string(c.Type)+string(c.PlayerID)+string(c.Color), func(t *testing.T) {
			g := fixture(t)
			before := g.s.clone()
			if _, err := g.Apply(c); err == nil {
				t.Fatal("invalid action accepted")
			}
			if !reflect.DeepEqual(before, g.s) {
				t.Fatal("rejected command mutated aggregate")
			}
		})
	}
}

func TestSkipAndTwoPlayerReverse(t *testing.T) {
	for _, kind := range []Kind{Skip, Reverse} {
		t.Run(string(kind), func(t *testing.T) {
			g := fixture(t)
			g.s.players[0].hand[0].Kind = kind
			if kind == Reverse {
				g.s.players = g.s.players[:2]
			}
			apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
			want := PlayerID("c")
			if kind == Reverse {
				want = "a"
			}
			if got := g.View("a").Turn; got != want {
				t.Fatalf("turn=%s want=%s", got, want)
			}
		})
	}
}

func TestPenaltyCannotBeBypassedWithMatchingCard(t *testing.T) {
	g := fixture(t)
	g.s.pending = 2
	g.s.pendingKind = DrawTwo
	before := g.s.clone()
	if _, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1}); err == nil {
		t.Fatal("normal card bypassed penalty")
	}
	if !reflect.DeepEqual(before, g.s) {
		t.Fatal("penalty rejection changed state")
	}
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	v := g.View("a")
	if v.PendingDraw != 0 || v.Players[0].HandCount != 5 || v.Turn != "b" {
		t.Fatalf("bad penalty draw: %+v", v)
	}
}

func TestStackingAndReversePenalty(t *testing.T) {
	g := fixture(t, StackingRule{Mixed: true, DifferentColors: true, Reverse: true})
	g.s.pending = 4
	g.s.pendingKind = WildDrawFour
	g.s.players[0].hand[0].Kind = DrawTwo
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	if g.View("a").PendingDraw != 6 {
		t.Fatal("mixed stack did not add two")
	}
	g.s.players[1].hand[0] = card(4, Red, Reverse)
	apply(t, g, Command{Type: Play, PlayerID: "b", CardID: 4})
	v := g.View("a")
	if v.PendingDraw != 6 || v.Turn != "a" || v.Direction != -1 {
		t.Fatalf("bad reverse penalty: %+v", v)
	}
}

func TestWildIdentityAndDrawnCardRestriction(t *testing.T) {
	g := fixture(t)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: Green})
	v := g.View("b")
	if v.TopCard.Color != WildColor || v.ActiveColor != Green {
		t.Fatal("wild card identity was overwritten")
	}
	apply(t, g, Command{Type: Draw, PlayerID: "b"})
	if !g.View("b").CanPass {
		t.Fatal("normal draw should permit pass")
	}
	if _, err := g.Apply(Command{Type: Play, PlayerID: "b", CardID: 4}); err == nil {
		t.Fatal("played original card after drawing")
	}
	if _, err := g.Apply(Command{Type: Draw, PlayerID: "b"}); err == nil {
		t.Fatal("drew twice")
	}
	apply(t, g, Command{Type: Pass, PlayerID: "b"})
	if g.View("b").Turn != "c" {
		t.Fatal("pass did not advance")
	}
}

func TestChallengeUsesActualOffenderAndPreservesChallengerTurn(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand[2].Kind = WildDrawFour
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: Blue})
	if g.View("b").ChallengeTarget != "a" {
		t.Fatal("missing actual challenge target")
	}
	apply(t, g, Command{Type: Challenge, PlayerID: "b"})
	v := g.View("a")
	if v.Turn != "b" || v.PendingDraw != 0 || v.Players[0].HandCount != 6 {
		t.Fatalf("successful challenge: %+v", v)
	}
}

func TestLastDrawFourDefersWinnerAndFailedChallengeSettles(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand = []Card{card(3, WildColor, WildDrawFour)}
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: Blue})
	if g.View("a").Phase != Playing {
		t.Fatal("winner declared before challenge window")
	}
	apply(t, g, Command{Type: Challenge, PlayerID: "b"})
	v := g.View("b")
	if v.Phase != Finished || v.Winner != "a" || v.Players[1].HandCount != 8 {
		t.Fatalf("failed challenge: %+v", v)
	}
}

func TestRecyclingConservesCardIdentityAndRollsBackFailure(t *testing.T) {
	g := fixture(t)
	g.s.draw = nil
	g.s.discard = []Card{card(20, WildColor, Wild), card(21, Red, Number), card(8, Red, Number)}
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	v := g.View("a")
	if v.TopCard.ID != 8 || v.DiscardCount != 1 || v.DrawPileCount != 1 || v.Players[0].Hand[3].ID != 20 || v.Players[0].Hand[3].Color != WildColor {
		t.Fatalf("bad recycle: %+v", v)
	}
	g = fixture(t)
	g.s.draw = nil
	g.s.discard = append(g.s.discard, card(22, Red, Number))
	g.shuffle = func([]Card) error { return errors.New("entropy unavailable") }
	before := g.s.clone()
	if _, err := g.Apply(Command{Type: Draw, PlayerID: "a"}); err == nil {
		t.Fatal("shuffle error ignored")
	}
	if !reflect.DeepEqual(before, g.s) {
		t.Fatal("shuffle error partially committed")
	}
}

func TestUNOPenaltyIsOneCardOffTurnAndIdempotent(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand = g.s.players[0].hand[:2]
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	window := g.View("a").Players[0].UNO
	if window == nil {
		t.Fatal("missing UNO window")
	}
	c := Command{Type: PenalizeUNO, PlayerID: "a", WindowID: window.ID, At: testTime.Add(2 * time.Second)}
	before := g.View("a")
	c.At = testTime.Add(time.Second)
	if _, err := g.Apply(c); err == nil {
		t.Fatal("penalty before deadline accepted")
	}
	if !reflect.DeepEqual(before, g.View("a")) {
		t.Fatal("early penalty changed state")
	}
	c.At = testTime.Add(2 * time.Second)
	apply(t, g, c)
	v := g.View("a")
	if v.Players[0].HandCount != 2 || v.Turn != "b" || v.Players[0].UNO != nil {
		t.Fatalf("bad UNO penalty: %+v", v)
	}
	apply(t, g, c)
	if !reflect.DeepEqual(v, g.View("a")) {
		t.Fatal("duplicate penalty was applied")
	}
}

func TestDeclaredUNOCannotBePenalized(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand = g.s.players[0].hand[:2]
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	w := g.View("a").Players[0].UNO
	apply(t, g, Command{Type: SayUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(time.Second)})
	apply(t, g, Command{Type: SayUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(time.Second)})
	if _, err := g.Apply(Command{Type: PenalizeUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(3 * time.Second)}); err == nil {
		t.Fatal("declared UNO was penalized")
	}
}

func TestEliminationAndOrdinaryWin(t *testing.T) {
	g := fixture(t, HandLimitRule{Limit: 4})
	g.s.pending = 2
	g.s.pendingKind = DrawTwo
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	if !g.View("a").Players[0].Eliminated || g.View("a").Turn != "b" {
		t.Fatal("elimination did not skip actor")
	}
	g = fixture(t)
	g.s.players[0].hand = g.s.players[0].hand[:1]
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	if v := g.View("a"); v.Phase != Finished || v.Winner != "a" {
		t.Fatalf("ordinary win: %+v", v)
	}
}

func TestSnapshotsDoNotAliasAggregate(t *testing.T) {
	g := fixture(t)
	v := g.View("a")
	v.Players[0].Hand[0].ID = 999
	v.TopCard.Color = Blue
	if got := g.View("a"); got.Players[0].Hand[0].ID != 1 || got.TopCard.Color != Red {
		t.Fatal("view aliases aggregate")
	}
}

func TestRuleVetoIsAtomicAndDuplicateRulesRejected(t *testing.T) {
	g := fixture(t, vetoRule{})
	before := g.s.clone()
	if _, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1}); err == nil {
		t.Fatal("rule veto ignored")
	}
	if !reflect.DeepEqual(before, g.s) {
		t.Fatal("veto partially committed")
	}
	if _, err := NewRuleSet(vetoRule{}, vetoRule{}); err == nil {
		t.Fatal("duplicate rule accepted")
	}
}

type vetoRule struct{}

func (vetoRule) ID() string { return "veto" }
func (vetoRule) Evaluate(c Context, _ *Effects) error {
	if c.Action() == Play {
		return errors.New("card blocked")
	}
	return nil
}
