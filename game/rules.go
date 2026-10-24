package game

import (
	"fmt"
	"reflect"
)

// Rule implementations are immutable policies. Evaluate may inspect Context and
// modify Effects, but must not retain either or perform external side effects.
// Rules execute in registration order; a veto always aborts the whole command.
type Rule interface {
	ID() string
	Evaluate(Context, *Effects) error
}

type RuleSet struct{ rules []Rule }

func NewRuleSet(rules ...Rule) (RuleSet, error) {
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if rule == nil || (reflect.ValueOf(rule).Kind() == reflect.Ptr && reflect.ValueOf(rule).IsNil()) {
			return RuleSet{}, fail(InvalidInput, "nil rule")
		}
		id := rule.ID()
		if id == "" || seen[id] {
			return RuleSet{}, fail(InvalidInput, "empty or duplicate rule ID %q", id)
		}
		seen[id] = true
		if v, ok := rule.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return RuleSet{}, fail(InvalidInput, "rule %s: %v", id, err)
			}
		}
	}
	return RuleSet{rules: append([]Rule(nil), rules...)}, nil
}

func (r RuleSet) Rules() []Rule { return append([]Rule(nil), r.rules...) }

// Context intentionally exposes no writable slices or pointers to game state.
type Context struct {
	s       *state
	command Command
	card    Card
}

func (c Context) Action() Action     { return c.command.Type }
func (c Context) Actor() PlayerID    { return c.command.PlayerID }
func (c Context) Card() Card         { return c.card }
func (c Context) ActiveColor() Color { return c.s.active }
func (c Context) PendingDraw() int   { return c.s.pending }
func (c Context) PendingKind() Kind  { return c.s.pendingKind }
func (c Context) TopCard() (Card, bool) {
	if len(c.s.discard) == 0 {
		return Card{}, false
	}
	return c.s.discard[len(c.s.discard)-1], true
}
func (c Context) HandSize(id PlayerID) int {
	if i := c.s.index(id); i >= 0 {
		return len(c.s.players[i].hand)
	}
	return 0
}
func (c Context) HasColor(id PlayerID, color Color) bool {
	i := c.s.index(id)
	if i < 0 {
		return false
	}
	for _, card := range c.s.players[i].hand {
		if !card.IsWild() && card.Color == color {
			return true
		}
	}
	return false
}
func (c Context) AlivePlayers() int {
	n := 0
	for _, p := range c.s.players {
		if !p.eliminated {
			n++
		}
	}
	return n
}

// Effects is a bounded plan, never a handle to mutable game state. Later rules
// may refine it. The engine validates all values before executing the plan.
type Effects struct {
	Allow               bool
	TurnSteps           int
	Reverse             bool
	AddPenalty          int
	DrawNext            int
	HandLimit           int
	KeepColor           bool
	AllowChallenge      bool
	ChallengeMultiplier int // percentage; 150 means 1.5 times the pool
	PlayDrawnCard       bool
}

func (e Effects) validate() error {
	if e.TurnSteps < 1 || e.TurnSteps > 10 || e.AddPenalty < 0 || e.AddPenalty > 108 || e.DrawNext < 0 || e.DrawNext > 108 || e.HandLimit < 0 || e.HandLimit > 108 || e.HandLimit == 1 {
		return fail(RuleViolation, "rule produced an invalid effect plan")
	}
	if e.AllowChallenge && (e.ChallengeMultiplier < 100 || e.ChallengeMultiplier > 300) {
		return fail(RuleViolation, "challenge multiplier must be between 100 and 300")
	}
	return nil
}

type MatchingRule struct {
	SameColor bool `json:"same_color"`
	SameValue bool `json:"same_value"`
}

func (MatchingRule) ID() string { return "matching" }
func (r MatchingRule) Evaluate(c Context, e *Effects) error {
	if c.Action() != Play || c.PendingDraw() > 0 {
		return nil
	}
	top, ok := c.TopCard()
	if !ok {
		return fail(InvalidState, "no discard card")
	}
	card := c.Card()
	if card.IsWild() || (r.SameColor && card.Color == c.ActiveColor()) || (r.SameValue && card.Kind == top.Kind && (card.Kind != Number || card.Number == top.Number)) {
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
	PlayDrawnCard bool `json:"play_drawn_card"`
}

func (DrawRule) ID() string { return "draw" }
func (r DrawRule) Evaluate(_ Context, e *Effects) error {
	e.PlayDrawnCard = r.PlayDrawnCard
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
