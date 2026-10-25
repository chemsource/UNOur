package game

import (
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
	PlayAnyAfterDraw    bool
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
