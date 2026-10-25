// Package bot chooses UNO actions from a player's public observation only.
package bot

import (
	"time"
	"unour/game"
)

type Difficulty string

const (
	Easy   Difficulty = "easy"
	Normal Difficulty = "normal"
	Hard   Difficulty = "hard"
	Expert Difficulty = "expert"
)

func (d Difficulty) Valid() bool { return d == Easy || d == Normal || d == Hard || d == Expert }

type Style string

const (
	Balanced   Style = "balanced"
	Aggressive Style = "aggressive"
	Cautious   Style = "cautious"
)

func (s Style) Valid() bool { return s == Balanced || s == Aggressive || s == Cautious }

type Profile struct {
	Difficulty Difficulty `json:"difficulty"`
	Style      Style      `json:"style"`
}

// ColorBelief contains soft, decaying evidence, never a claim of card ownership.
type ColorBelief struct {
	PlayerID game.PlayerID `json:"player_id"`
	Color    game.Color    `json:"color"`
	Weight   float64       `json:"weight"`
}
type Memory struct {
	Discard        []game.Card   `json:"discard,omitempty"`
	Beliefs        []ColorBelief `json:"beliefs,omitempty"`
	ChallengeColor game.Color    `json:"challenge_color,omitempty"`
}
type Observation struct {
	RuleModules []game.RuleModule
	View        game.View
	Memory      Memory
	Rules       []game.RuleSpec
}
type SearchConfig struct {
	Seed          uint64
	MaxIterations int
	TimeBudget    time.Duration
}
type Decision struct {
	Command    game.Command
	Iterations int
}
type RuleBuilder interface {
	Build([]game.RuleSpec) (game.RuleSet, error)
}

// Brain has no per-search mutable state. Its registry must support concurrent Build.
type Brain struct{ registry RuleBuilder }

func New(registry RuleBuilder) *Brain {
	if registry == nil {
		registry = game.NewRegistry()
	}
	return &Brain{registry: registry}
}
