package game

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestOptionalPlayAnyCardAfterDraw(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			g := fixture(t)
			specs := DefaultRuleSpecs()
			specs[2].Options = json.RawMessage(`{"play_any_after_draw":` + strconv.FormatBool(enabled) + `}`)
			var err error
			g.rules, err = NewRegistry().Build(specs)
			if err != nil {
				t.Fatal(err)
			}
			g.s.players[0].hand[1].Number = 4
			apply(t, g, Command{Type: Draw, PlayerID: "a"})
			before := g.s.clone()
			for _, cmd := range []Command{{Type: Draw, PlayerID: "a"}, {Type: Play, PlayerID: "a", CardID: 2}} {
				if _, err := g.Apply(cmd); err == nil {
					t.Fatal("drew twice or played an unmatched original card after drawing")
				}
				if !reflect.DeepEqual(before, g.s) {
					t.Fatal("rejected action after drawing mutated state")
				}
			}
			cmd := Command{Type: Play, PlayerID: "a", CardID: 1, At: testTime}
			if err := g.CanApply(cmd); (err == nil) != enabled {
				t.Fatalf("CanApply play_any_after_draw=%v: %v", enabled, err)
			}
			_, err = g.Apply(cmd)
			if (err == nil) != enabled {
				t.Fatalf("play_any_after_draw=%v: %v", enabled, err)
			}
			if enabled {
				if got := g.View("a"); got.Turn != "b" || got.DrawnCardID != 0 || got.CanPass || got.Players[0].HandCount != 3 {
					t.Fatalf("original card did not end the draw phase: %+v", got)
				}
			} else {
				if !reflect.DeepEqual(before, g.s) {
					t.Fatal("disabled option changed game on rejection")
				}
				apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 9})
			}
		})
	}
}

func TestPlayAnyAfterDrawRequiresPostDrawPlay(t *testing.T) {
	_, err := NewRegistry().Build([]RuleSpec{{Name: "draw", Options: json.RawMessage(`{"play_drawn_card":false,"play_any_after_draw":true}`)}})
	if err == nil {
		t.Fatal("contradictory draw options were accepted")
	}
}

func TestMatchingRequiresAtLeastOneMode(t *testing.T) {
	if _, err := NewRuleSet(MatchingRule{}); err == nil {
		t.Fatal("matching with both modes disabled was accepted")
	}
	_, err := NewRegistry().Build([]RuleSpec{{Name: "matching", Options: json.RawMessage(`{"same_color":false,"same_value":false}`)}})
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != InvalidInput {
		t.Fatalf("invalid matching configuration must be rejected as input: %v", err)
	}
}

func TestOptionalMatchingModes(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		sameColor, sameValue bool
		color                Color
		number               int
		allowed              bool
	}{
		{"color match enabled", true, false, Red, 4, true},
		{"number match disabled", true, false, Blue, 3, false},
		{"color match disabled", false, true, Red, 4, false},
		{"number match enabled", false, true, Blue, 3, true},
		{"both modes enabled", true, true, Red, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := fixture(t)
			rules := g.rules.Rules()
			rules[0] = MatchingRule{SameColor: tc.sameColor, SameValue: tc.sameValue}
			var err error
			g.rules, err = NewRuleSet(rules...)
			if err != nil {
				t.Fatal(err)
			}
			g.s.players[0].hand[0].Color = tc.color
			g.s.players[0].hand[0].Number = tc.number
			before := g.s.clone()
			_, err = g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1, At: testTime})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v want=%v: %v", err == nil, tc.allowed, err)
			}
			if err != nil && !reflect.DeepEqual(before, g.s) {
				t.Fatal("rejected matching action changed the game")
			}
		})
	}
}

func TestNumberOnlyMatchingFollowsWildChosenColor(t *testing.T) {
	for _, kind := range []Kind{Wild, WildDrawFour} {
		t.Run(string(kind), func(t *testing.T) {
			g := fixture(t)
			rules := g.rules.Rules()
			rules[0] = MatchingRule{SameValue: true}
			g.rules, _ = NewRuleSet(rules...)
			g.s.players[0].hand[2].Kind = kind
			apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 3, Color: Blue})
			receiver, wrong, right := PlayerID("b"), 4, 5
			if kind == WildDrawFour {
				apply(t, g, Command{Type: Draw, PlayerID: "b"})
				receiver, wrong, right = "c", 6, 7
			}
			before := g.s.clone()
			if _, err := g.Apply(Command{Type: Play, PlayerID: receiver, CardID: wrong, At: testTime}); err == nil {
				t.Fatal("off-color ordinary card followed a wild")
			}
			if !reflect.DeepEqual(before, g.s) {
				t.Fatal("invalid response to wild changed the game")
			}
			apply(t, g, Command{Type: Play, PlayerID: receiver, CardID: right})
			if got := g.View(receiver); got.ActiveColor != Blue || got.TopCard.ID != right {
				t.Fatalf("wild chosen color was not followed: %+v", got)
			}
		})
	}
}

func TestWildMustChangeColorUnlessKeepColorEnabled(t *testing.T) {
	for _, kind := range []Kind{Wild, WildDrawFour} {
		for _, keep := range []bool{false, true} {
			for _, chosen := range []Color{Red, KeepColor, Blue} {
				t.Run(string(kind)+"/"+string(chosen)+"/"+map[bool]string{true: "keep", false: "change"}[keep], func(t *testing.T) {
					g := fixture(t)
					rules := g.rules.Rules()
					rules[1] = ActionRule{KeepColor: keep}
					var err error
					g.rules, err = NewRuleSet(rules...)
					if err != nil {
						t.Fatal(err)
					}
					g.s.players[0].hand[2].Kind = kind
					before := g.s.clone()
					_, err = g.Apply(Command{Type: Play, PlayerID: "a", CardID: 3, Color: chosen, At: testTime})
					allowed := keep || chosen == Blue
					if (err == nil) != allowed {
						t.Fatalf("keep=%v chosen=%s allowed=%v: %v", keep, chosen, err == nil, err)
					}
					if err != nil && !reflect.DeepEqual(before, g.s) {
						t.Fatal("rejected color choice changed the game")
					}
					if err == nil {
						want := chosen
						if chosen == KeepColor {
							want = Red
						}
						if g.View("a").ActiveColor != want || g.View("a").TopCard.Color != WildColor {
							t.Fatal("chosen color or physical wild identity was lost")
						}
					}
				})
			}
		}
	}
}

func TestOptionalSkipDraw(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		g := fixture(t)
		rules := g.rules.Rules()
		rules[1] = ActionRule{SkipDraw: enabled}
		g.rules, _ = NewRuleSet(rules...)
		g.s.players[0].hand[0].Kind = Skip
		apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
		want := 2
		if enabled {
			want = 3
		}
		if got := g.View("b"); got.Turn != "c" || got.Players[1].HandCount != want {
			t.Fatalf("skip_draw=%v: %+v", enabled, got)
		}
	}
}

func TestConsecutivePenaltyReverses(t *testing.T) {
	g := fixture(t, StackingRule{Reverse: true})
	g.s.pending, g.s.pendingKind = 6, WildDrawFour
	g.s.players[0].hand[0] = card(1, Red, Reverse)
	g.s.players[2].hand[0] = card(6, Blue, Reverse)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	if got := g.View("c"); got.Turn != "c" || got.PendingDraw != 6 || got.Direction != -1 {
		t.Fatalf("first reverse: %+v", got)
	}
	apply(t, g, Command{Type: Play, PlayerID: "c", CardID: 6})
	if got := g.View("a"); got.Turn != "a" || got.PendingDraw != 6 || got.Direction != 1 || got.ActiveColor != Blue {
		t.Fatalf("consecutive reverse: %+v", got)
	}
}

func TestFailedChallengeMultiplierOnMixedPenalty(t *testing.T) {
	for _, tc := range []struct {
		multiplier, penalty int
	}{{0, 9}, {150, 9}, {200, 12}} {
		g := fixture(t, StackingRule{Mixed: true})
		rules := g.rules.Rules()
		rules[3] = ChallengeRule{FailureMultiplier: tc.multiplier}
		g.rules, _ = NewRuleSet(rules...)
		for id := 20; id < 40; id++ {
			g.s.draw = append(g.s.draw, card(id, Yellow, Number))
		}
		g.s.players[0].hand[0].Kind = DrawTwo
		g.s.players[1].hand[0] = card(4, WildColor, WildDrawFour)
		apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
		apply(t, g, Command{Type: Play, PlayerID: "b", CardID: 4, Color: Blue})
		if g.View("c").PendingDraw != 6 {
			t.Fatal("mixed stack did not form a six-card penalty")
		}
		apply(t, g, Command{Type: Challenge, PlayerID: "c"})
		if got := g.View("c"); got.Players[2].HandCount != 2+tc.penalty || got.PendingDraw != 0 || got.Turn != "a" {
			t.Fatalf("multiplier=%d: %+v", tc.multiplier, got)
		}
	}
}
