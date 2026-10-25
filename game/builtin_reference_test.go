package game

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Reference policies preserve pre-module test cases; production uses JSON.
type MatchingRule struct {
	SameColor bool `json:"same_color"`
	SameValue bool `json:"same_value"`
}

func (MatchingRule) ID() string { return "matching" }
func (r MatchingRule) Validate() error {
	if !r.SameColor && !r.SameValue {
		return fmt.Errorf("at least one of same_color and same_value must be enabled")
	}
	return nil
}
func (r MatchingRule) Evaluate(c Context, e *Effects) error {
	if c.Action() != Play || c.PendingDraw() > 0 {
		return nil
	}
	top, ok := c.TopCard()
	if !ok {
		return fail(InvalidState, "no discard card")
	}
	card := c.Card()
	// A wild's chosen color remains binding even in number-only rooms, so a
	// wild cannot strand every ordinary card until another wild is drawn.
	if card.IsWild() || ((r.SameColor || top.IsWild()) && card.Color == c.ActiveColor()) || (r.SameValue && card.Kind == top.Kind && (card.Kind != Number || card.Number == top.Number)) {
		e.Allow = true
	}
	return nil
}

type ActionRule struct {
	SkipDraw  bool `json:"skip_draw"`
	KeepColor bool `json:"keep_color"`
}

func (ActionRule) ID() string { return "actions" }
func (r ActionRule) Evaluate(c Context, e *Effects) error {
	if c.Action() != Play {
		return nil
	}
	e.KeepColor = r.KeepColor
	switch c.Card().Kind {
	case Skip:
		e.TurnSteps = 2
		if r.SkipDraw {
			e.DrawNext = 1
		}
	case Reverse:
		e.Reverse = true
		// An ordinary two-player reverse acts as a skip. A penalty reverse
		// must instead pass the unresolved pool back to the opponent.
		if c.AlivePlayers() == 2 && c.PendingDraw() == 0 {
			e.TurnSteps = 2
		}
	case DrawTwo:
		e.AddPenalty = 2
	case WildDrawFour:
		e.AddPenalty = 4
	}
	return nil
}

// Including this rule enables stacking. Mixed and DifferentColors independently
// control +2/+4 mixing and off-color +2 responses. Reverse redirects the pool.
type StackingRule struct {
	Mixed           bool `json:"mixed"`
	DifferentColors bool `json:"different_colors"`
	Reverse         bool `json:"reverse"`
}

func (StackingRule) ID() string { return "stacking" }
func (r StackingRule) Evaluate(c Context, e *Effects) error {
	if c.Action() != Play || c.PendingDraw() == 0 {
		return nil
	}
	card := c.Card()
	switch card.Kind {
	case DrawTwo:
		e.Allow = (c.PendingKind() == DrawTwo || (r.Mixed && c.PendingKind() == WildDrawFour)) && (r.DifferentColors || card.Color == c.ActiveColor())
	case WildDrawFour:
		e.Allow = c.PendingKind() == WildDrawFour || r.Mixed
	case Reverse:
		top, _ := c.TopCard()
		e.Allow = r.Reverse && (card.Color == c.ActiveColor() || top.Kind == Reverse)
	}
	return nil
}

type DrawRule struct {
	PlayDrawnCard    bool `json:"play_drawn_card"`
	PlayAnyAfterDraw bool `json:"play_any_after_draw"`
}

func (DrawRule) ID() string { return "draw" }
func (r DrawRule) Validate() error {
	if r.PlayAnyAfterDraw && !r.PlayDrawnCard {
		return fmt.Errorf("play_any_after_draw requires play_drawn_card")
	}
	return nil
}
func (r DrawRule) Evaluate(_ Context, e *Effects) error {
	e.PlayDrawnCard = r.PlayDrawnCard
	e.PlayAnyAfterDraw = r.PlayAnyAfterDraw
	return nil
}

type ChallengeRule struct {
	FailureMultiplier int `json:"failure_multiplier"`
}

func (ChallengeRule) ID() string { return "challenge" }
func (r ChallengeRule) Validate() error {
	if r.FailureMultiplier != 0 && (r.FailureMultiplier < 100 || r.FailureMultiplier > 300) {
		return fmt.Errorf("failure_multiplier must be 100–300")
	}
	return nil
}
func (r ChallengeRule) Evaluate(_ Context, e *Effects) error {
	e.AllowChallenge = true
	e.ChallengeMultiplier = r.FailureMultiplier
	if e.ChallengeMultiplier == 0 {
		e.ChallengeMultiplier = 150
	}
	return nil
}

type HandLimitRule struct {
	Limit int `json:"limit"`
}

func (HandLimitRule) ID() string { return "hand_limit" }
func (r HandLimitRule) Validate() error {
	if r.Limit < 2 || r.Limit > 108 {
		return fmt.Errorf("limit must be 2–108")
	}
	return nil
}
func (r HandLimitRule) Evaluate(_ Context, e *Effects) error { e.HandLimit = r.Limit; return nil }

func TestConfiguredBuiltinsMatchReferencePolicies(t *testing.T) {
	for bits := 0; bits < 8; bits++ {
		on := func(bit int) bool { return bits&(1<<bit) != 0 }
		policies := []Rule{MatchingRule{SameColor: !on(0), SameValue: true}, ActionRule{SkipDraw: on(0), KeepColor: on(1)}, StackingRule{Mixed: on(0), DifferentColors: on(1), Reverse: on(2)}, DrawRule{PlayDrawnCard: true, PlayAnyAfterDraw: on(0)}, ChallengeRule{FailureMultiplier: 150 + 50*(bits%2)}, HandLimitRule{Limit: 25}}
		for _, reference := range policies {
			options, err := json.Marshal(reference)
			if err != nil {
				t.Fatal(err)
			}
			configured, err := NewRegistry().Build([]RuleSpec{{Name: reference.ID(), Options: options}})
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []Action{Play, Draw, Pass, Challenge} {
				for _, pending := range []int{0, 2, 4} {
					for _, kind := range []Kind{Number, Skip, Reverse, DrawTwo, Wild, WildDrawFour} {
						for _, color := range []Color{Red, Blue, WildColor} {
							for _, pendingKind := range []Kind{DrawTwo, WildDrawFour} {
								state := state{phase: Playing, active: Red, pending: pending, pendingKind: pendingKind, discard: []Card{{Kind: Reverse, Color: Blue}}, players: []player{{id: "a"}, {id: "b"}}}
								context := Context{s: &state, command: Command{Type: action, PlayerID: "a"}, card: Card{Kind: kind, Color: color, Number: 3}}
								expected, actual := Effects{TurnSteps: 1}, Effects{TurnSteps: 1}
								if err := reference.Evaluate(context, &expected); err != nil {
									t.Fatal(err)
								}
								if err := configured.rules[0].Evaluate(context, &actual); err != nil {
									t.Fatal(err)
								}
								if expected != actual {
									t.Fatalf("policy %s diverged: %+v vs %+v", reference.ID(), actual, expected)
								}
							}
						}
					}
				}
			}
		}
	}
}
