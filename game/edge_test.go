package game

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestChallengeEvidenceDoesNotFollowLaterHandChanges(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand[2].Kind = WildDrawFour
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: Blue})
	// A new rule/other effect may change an offender's hand after their play.
	// The factual challenge evidence must still refer to the original hand.
	g.s.players[0].hand[0].Color = Green
	apply(t, g, Command{Type: Challenge, PlayerID: "b"})
	if g.s.turn != 1 || len(g.s.players[0].hand) != 6 {
		t.Fatal("challenge inspected current hand instead of captured evidence")
	}
}

func TestMatchingAndStackingOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  StackingRule
		kind  Kind
		color Color
		want  bool
	}{
		{"same color two", StackingRule{}, DrawTwo, Red, true},
		{"off color denied", StackingRule{}, DrawTwo, Blue, false},
		{"off color enabled", StackingRule{DifferentColors: true}, DrawTwo, Blue, true},
		{"mixed denied", StackingRule{}, WildDrawFour, WildColor, false},
		{"mixed enabled", StackingRule{Mixed: true}, WildDrawFour, WildColor, true},
		{"reverse denied", StackingRule{}, Reverse, Red, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := fixture(t, tc.rule)
			g.s.pending = 2
			g.s.pendingKind = DrawTwo
			g.s.players[0].hand[0] = card(1, tc.color, tc.kind)
			c := Command{Type: Play, PlayerID: "a", CardID: 1, At: testTime}
			if tc.color == WildColor {
				c.Color = Green
			}
			_, err := g.Apply(c)
			if (err == nil) != tc.want {
				t.Fatalf("allowed=%v want=%v err=%v", err == nil, tc.want, err)
			}
		})
	}
}

func TestSkipExtraDrawAndKeepColor(t *testing.T) {
	g := fixture(t)
	rules := g.rules.Rules()
	rules[1] = ActionRule{SkipDraw: true, KeepColor: true}
	var err error
	g.rules, err = NewRuleSet(rules...)
	if err != nil {
		t.Fatal(err)
	}
	g.s.players[0].hand[0].Kind = Skip
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	if len(g.s.players[1].hand) != 3 || g.s.turn != 2 {
		t.Fatal("skip draw did not punish and skip next player")
	}
	g.s.turn = 0
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: KeepColor})
	if g.s.active != Red || g.s.discard[len(g.s.discard)-1].Color != WildColor {
		t.Fatal("keep color changed physical wild")
	}
}

func TestDeckExhaustionSettlesAvailableCardsWithoutDuplication(t *testing.T) {
	g := fixture(t)
	g.s.draw = nil
	g.s.pending = 4
	g.s.pendingKind = WildDrawFour
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	if len(g.s.players[0].hand) != 3 || g.s.pending != 0 || g.s.turn != 1 {
		t.Fatal("empty physical deck caused phantom cards or stalled turn")
	}
}

func TestUNOBoundaryAndSupersededWindow(t *testing.T) {
	g := fixture(t)
	g.s.players[0].hand = g.s.players[0].hand[:2]
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	w := g.s.players[0].uno
	before := g.s.clone()
	if _, err := g.Apply(Command{Type: SayUNO, PlayerID: "a", WindowID: w.ID, At: w.Deadline}); err == nil {
		t.Fatal("declaration at deadline accepted")
	}
	if !reflect.DeepEqual(before, g.s) {
		t.Fatal("expired declaration mutated state")
	}
	g.s.turn = 0
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	if _, err := g.Apply(Command{Type: PenalizeUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(10 * time.Second)}); err == nil {
		t.Fatal("cancelled window accepted")
	}
}

func TestRegistryRejectsMalformedOptionsAndBuildsIndependentRules(t *testing.T) {
	r := NewRegistry()
	for _, options := range []string{`null`, `[]`, `{"limit":1}`, `{"limit":25,"unknown":true}`, `{} {}`} {
		if _, err := r.Build([]RuleSpec{{Name: "hand_limit", Options: json.RawMessage(options)}}); err == nil {
			t.Fatalf("accepted options %s", options)
		}
	}
	if err := r.Register("draw", func(json.RawMessage) (Rule, error) { return DrawRule{}, nil }); err == nil {
		t.Fatal("factory replaced silently")
	}
	if _, err := r.Build([]RuleSpec{{Name: "draw"}, {Name: "draw"}}); err == nil {
		t.Fatal("duplicate policies accepted")
	}
	if _, err := NewRuleSet(HandLimitRule{Limit: 1}); err == nil {
		t.Fatal("invalid direct policy accepted")
	}
}

type badEffects struct{}

func (badEffects) ID() string                           { return "bad-effects" }
func (badEffects) Evaluate(_ Context, e *Effects) error { e.AddPenalty = -1; return nil }
func TestInvalidExtensionEffectsCannotCorruptState(t *testing.T) {
	g := fixture(t, badEffects{})
	before := g.s.clone()
	if _, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1}); err == nil {
		t.Fatal("invalid plan accepted")
	}
	if !reflect.DeepEqual(before, g.s) {
		t.Fatal("invalid extension corrupted state")
	}
}
