package game

import (
	"errors"
	"reflect"
	"testing"
)

func TestCloneHasDetachedState(t *testing.T) {
	original := checkpointGame(t, 2)
	before := original.Checkpoint()
	cloned := original.Clone()
	apply(t, cloned, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue})
	if !reflect.DeepEqual(original.Checkpoint(), before) {
		t.Fatal("clone play changed original")
	}
	sibling := cloned.Clone()
	apply(t, sibling, Command{Type: Challenge, PlayerID: "b"})
	if cloned.View("b").PendingDraw != 4 {
		t.Fatal("clone challenge changed source")
	}
}

func TestBranchIsFailureAtomicAndDetachesChangedCards(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Game)
		cmd   Command
	}{
		{"play", nil, Command{Type: Play, PlayerID: "a", CardID: 1}},
		{"draw", nil, Command{Type: Draw, PlayerID: "a"}},
		{"recycle", func(g *Game) {
			g.shuffle = func([]Card) error { return nil }
			g.s.discard = append(g.s.draw, g.s.discard...)
			g.s.draw = nil
		}, Command{Type: Draw, PlayerID: "a"}},
		{"challenge", func(g *Game) { apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue}) }, Command{Type: Challenge, PlayerID: "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := checkpointGame(t, 2)
			if tc.setup != nil {
				tc.setup(g)
			}
			before := g.Checkpoint()
			branch, _, err := g.Branch(tc.cmd)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g.Checkpoint(), before) {
				t.Fatal("branch changed original")
			}
			if reflect.DeepEqual(branch.Checkpoint(), before) {
				t.Fatal("branch did not advance")
			}
			assertCards(t, branch)
		})
	}
	g := checkpointGame(t, 2)
	before := g.Checkpoint()
	_, _, err := g.Branch(Command{Type: Play, PlayerID: "a", CardID: 999})
	if err == nil {
		t.Fatal("illegal branch accepted")
	}
	if !reflect.DeepEqual(g.Checkpoint(), before) {
		t.Fatal("failed branch changed source")
	}
}

func TestSiblingBranchesStayIsolatedAcrossLaterActions(t *testing.T) {
	source := checkpointGame(t, 2)
	sourceBefore := source.Checkpoint()
	played, _, err := source.Branch(Command{Type: Play, PlayerID: "a", CardID: 1})
	if err != nil {
		t.Fatal(err)
	}
	drawn, _, err := source.Branch(Command{Type: Draw, PlayerID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	playedBefore := played.Checkpoint()
	drawnBefore := drawn.Checkpoint()
	apply(t, played, Command{Type: Draw, PlayerID: "b"})
	apply(t, drawn, Command{Type: Pass, PlayerID: "a"})
	if !reflect.DeepEqual(source.Checkpoint(), sourceBefore) {
		t.Fatal("later actions on branches changed source")
	}
	if reflect.DeepEqual(played.Checkpoint(), playedBefore) || reflect.DeepEqual(drawn.Checkpoint(), drawnBefore) {
		t.Fatal("branch action did not advance")
	}
	assertCards(t, played)
	assertCards(t, drawn)
}

type testSkipDrawRule struct{}

func (testSkipDrawRule) ID() string { return "test_skip_draw" }
func (testSkipDrawRule) Evaluate(c Context, e *Effects) error {
	if c.Action() == Play && c.Card().Kind == Skip {
		e.DrawNext = 1
	}
	return nil
}

type testRejectRule struct{}

func (testRejectRule) ID() string { return "test_reject" }
func (testRejectRule) Evaluate(Context, *Effects) error {
	return errors.New("rejected")
}

func TestFailedBranchAfterPlayDoesNotMutateSharedCardStorage(t *testing.T) {
	g := checkpointGame(t, 2)
	rules, err := NewRuleSet(append(g.rules.Rules(), testSkipDrawRule{})...)
	if err != nil {
		t.Fatal(err)
	}
	g.rules = rules
	old := g.s.players[0].hand[0]
	for i, c := range g.s.draw {
		if c.ID == 20 {
			g.s.draw[i] = old
			g.s.players[0].hand[0] = StandardDeck()[19]
			break
		}
	}
	g.s.discard = append(g.s.draw, g.s.discard...)
	g.s.draw = nil
	g.shuffle = func([]Card) error { return errors.New("recycle failed") }
	before := g.Checkpoint()
	if _, _, err := g.Branch(Command{Type: Play, PlayerID: "a", CardID: 20}); err == nil {
		t.Fatal("branch ignored recycle failure")
	}
	if !reflect.DeepEqual(g.Checkpoint(), before) {
		t.Fatal("failed branch changed the source after beginning a play")
	}
}

func TestApplyOwnedPlayMatchesFailureAtomicApply(t *testing.T) {
	base := checkpointGame(t, 2)
	regular := base.Clone()
	owned := base.Clone()
	cmd := Command{Type: Play, PlayerID: "a", CardID: 1}
	if _, err := regular.Apply(cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := owned.ApplyOwned(cmd); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(regular.Checkpoint(), owned.Checkpoint()) {
		t.Fatal("owned simulation play differed from Apply")
	}
	assertCards(t, owned)

	before := owned.Checkpoint()
	if _, err := owned.ApplyOwned(Command{Type: Play, PlayerID: "a", CardID: 999}); err == nil {
		t.Fatal("owned simulation accepted an invalid action")
	}
	if !reflect.DeepEqual(owned.Checkpoint(), before) {
		t.Fatal("invalid owned action changed state")
	}
}

func TestApplyOwnedKeepsFailureAtomicity(t *testing.T) {
	t.Run("rule veto", func(t *testing.T) {
		g := checkpointGame(t, 2)
		rules, err := NewRuleSet(append(g.rules.Rules(), testRejectRule{})...)
		if err != nil {
			t.Fatal(err)
		}
		g.rules = rules
		before := g.Checkpoint()
		if _, err := g.ApplyOwned(Command{Type: Play, PlayerID: "a", CardID: 1}); err == nil {
			t.Fatal("owned simulation ignored rule veto")
		}
		if !reflect.DeepEqual(g.Checkpoint(), before) {
			t.Fatal("rule veto changed owned state")
		}
	})

	t.Run("draw next recycle failure", func(t *testing.T) {
		g := checkpointGame(t, 2)
		rules, err := NewRuleSet(append(g.rules.Rules(), testSkipDrawRule{})...)
		if err != nil {
			t.Fatal(err)
		}
		g.rules = rules
		old := g.s.players[0].hand[0]
		for i, c := range g.s.draw {
			if c.ID == 20 {
				g.s.draw[i] = old
				g.s.players[0].hand[0] = StandardDeck()[19]
				break
			}
		}
		g.s.discard = append(g.s.draw, g.s.discard...)
		g.s.draw = nil
		g.shuffle = func([]Card) error { return errors.New("recycle failed") }
		before := g.Checkpoint()
		if _, err := g.ApplyOwned(Command{Type: Play, PlayerID: "a", CardID: 20}); err == nil {
			t.Fatal("owned simulation ignored recycle failure")
		}
		if !reflect.DeepEqual(g.Checkpoint(), before) {
			t.Fatal("recycle failure changed owned state")
		}
	})
}

func TestCanApplyAgreesWithApplyAndDoesNotShuffle(t *testing.T) {
	for _, setup := range []func(*Game){nil, func(g *Game) { apply(t, g, Command{Type: Draw, PlayerID: "a"}) }, func(g *Game) { apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue}) }} {
		g := checkpointGame(t, 2)
		if setup != nil {
			setup(g)
		}
		before := g.Checkpoint()
		v := g.View("")
		actor := v.Turn
		commands := []Command{{Type: Draw, PlayerID: actor}, {Type: Pass, PlayerID: actor}, {Type: Challenge, PlayerID: actor}, {Type: Play, PlayerID: actor, CardID: 999}}
		for _, c := range StandardDeck() {
			for _, color := range []Color{"", Red, Blue, Green, Yellow, KeepColor} {
				commands = append(commands, Command{Type: Play, PlayerID: actor, CardID: c.ID, Color: color})
			}
		}
		for _, cmd := range commands {
			preview := g.CanApply(cmd)
			clone := g.Clone()
			_, actual := clone.Apply(cmd)
			if (preview == nil) != (actual == nil) {
				t.Fatalf("preview differs for %+v: %v vs %v", cmd, preview, actual)
			}
		}
		if !reflect.DeepEqual(before, g.Checkpoint()) {
			t.Fatal("legality check mutated source")
		}
	}
	g := checkpointGame(t, 2)
	g.s.discard = append(g.s.draw, g.s.discard...)
	g.s.draw = nil
	g.shuffle = func([]Card) error { t.Fatal("precheck shuffled"); return nil }
	if err := g.CanApply(Command{Type: Draw, PlayerID: "a"}); err != nil {
		t.Fatal(err)
	}
}
