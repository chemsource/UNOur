package bot

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
	"unour/game"
)

type actionKey struct {
	actor  game.PlayerID
	action game.Action
	card   int
	color  game.Color
}
type action struct {
	cmd game.Command
}

func key(cmd game.Command) actionKey { return actionKey{cmd.PlayerID, cmd.Type, cmd.CardID, cmd.Color} }

// Apply remains the sole legality authority, including registered custom rules.
func legalActions(ctx context.Context, g *game.Game) ([]action, error) {
	public := g.View("")
	if public.Phase != game.Playing {
		return nil, nil
	}
	return legalActionsForView(ctx, g, g.View(public.Turn))
}

func legalActionsForView(ctx context.Context, g *game.Game, v game.View) ([]action, error) {
	var commandBuffer [160]game.Command
	candidates := candidateCommands(v, commandBuffer[:0])
	result := make([]action, 0, len(candidates))
	for _, cmd := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := g.CanApply(cmd); err == nil {
			result = append(result, action{cmd: cmd})
		}
	}
	return result, nil
}

func candidateCommands(v game.View, candidates []game.Command) []game.Command {
	add := func(kind game.Action, id int, color game.Color) {
		candidates = append(candidates, game.Command{Type: kind, PlayerID: v.You, CardID: id, Color: color, At: time.Unix(1, 0)})
	}
	for _, p := range v.Players {
		if p.ID == v.You {
			for _, c := range p.Hand {
				// The rule engine decides whether an original hand card may be
				// played after a draw; custom policies can allow that response.
				if c.IsWild() {
					for _, color := range colors {
						add(game.Play, c.ID, color)
					}
				} else {
					add(game.Play, c.ID, "")
				}
			}
		}
	}
	if v.CanPass {
		add(game.Pass, 0, "")
	} else {
		add(game.Draw, 0, "")
	}
	if v.ChallengeTarget != "" {
		add(game.Challenge, 0, "")
	}
	return candidates
}

// Rollouts need one legal action, so rank candidates and stop at the first
// successful real-engine transition. This wrapper preserves the input game.
func rolloutStep(ctx context.Context, g *game.Game, v game.View, style Style, random *rand.Rand, noise float64) (*game.Game, error) {
	var commandBuffer [160]game.Command
	var scoreBuffer [160]float64
	commands := candidateCommands(v, commandBuffer[:0])
	scores := scoreBuffer[:len(commands)]
	explore := random.Float64() < noise
	for i, cmd := range commands {
		scores[i] = random.Float64()
		if !explore {
			scores[i] = prior(v, cmd, style) + scores[i]*.25
		}
	}
	for len(commands) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		best := 0
		for i := 1; i < len(commands); i++ {
			if scores[i] > scores[best] {
				best = i
			}
		}
		if next, _, err := g.Branch(commands[best]); err == nil {
			return next, nil
		}
		last := len(commands) - 1
		commands[best] = commands[last]
		scores[best] = scores[last]
		commands = commands[:last]
		scores = scores[:last]
	}
	return nil, nil
}

// rolloutStepOwned is used only for a determinized state owned by this search.
// Apply is failure-atomic, so a failed candidate can be discarded without
// constructing another Game value or retaining a branch.
func rolloutStepOwned(ctx context.Context, g *game.Game, v game.View, style Style, random *rand.Rand, noise float64) (*game.Game, error) {
	var commandBuffer [160]game.Command
	var scoreBuffer [160]float64
	commands := candidateCommands(v, commandBuffer[:0])
	scores := scoreBuffer[:len(commands)]
	explore := random.Float64() < noise
	for i, cmd := range commands {
		scores[i] = random.Float64()
		if !explore {
			scores[i] = prior(v, cmd, style) + scores[i]*.25
		}
	}
	for len(commands) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		best := 0
		for i := 1; i < len(commands); i++ {
			if scores[i] > scores[best] {
				best = i
			}
		}
		if _, err := g.ApplyOwned(commands[best]); err == nil {
			return g, nil
		}
		last := len(commands) - 1
		commands[best] = commands[last]
		scores[best] = scores[last]
		commands = commands[:last]
		scores = scores[:last]
	}
	return nil, nil
}

// prior only sees the acting player's cards and public threat counts. It cannot
// inspect the sampled challenge evidence or opponents' hidden cards.
func prior(v game.View, cmd game.Command, style Style) float64 {
	var hand []game.Card
	nextCount := 108
	actorIndex := 0
	for i, p := range v.Players {
		if p.ID == cmd.PlayerID {
			hand = p.Hand
			actorIndex = i
		}
	}
	for step := 1; step < len(v.Players); step++ {
		idx := (actorIndex + v.Direction*step + len(v.Players)*len(v.Players)) % len(v.Players)
		if !v.Players[idx].Eliminated {
			nextCount = v.Players[idx].HandCount
			break
		}
	}
	if cmd.Type == game.Draw {
		return -.6 - .05*float64(v.PendingDraw)
	}
	if cmd.Type == game.Pass {
		return -.35
	}
	if cmd.Type == game.Challenge {
		score := -.15
		if nextCount <= 2 {
			score += .15
		}
		if style == Aggressive {
			score += .12
		}
		if style == Cautious {
			score -= .18
		}
		return score
	}
	var card game.Card
	for _, c := range hand {
		if c.ID == cmd.CardID {
			card = c
			break
		}
	}
	color := card.Color
	if card.IsWild() {
		color = cmd.Color
	}
	chain := 0
	for _, c := range hand {
		if c.ID != card.ID && c.Color == color {
			chain++
		}
	}
	score := .5 + .09*float64(chain)
	if card.IsWild() {
		score -= .25
		if style == Cautious {
			score -= .12
		}
		if len(hand) <= 2 {
			score += .3
		}
	}
	attack := card.Kind == game.DrawTwo || card.Kind == game.WildDrawFour || card.Kind == game.Skip
	if attack {
		score += .08
		if nextCount <= 2 {
			score += .45
		}
		if style == Aggressive {
			score += .18
		}
	}
	if card.Kind == game.Reverse {
		score += .04
		if nextCount <= 2 {
			score += .3
		}
	}
	if card.Kind == game.WildDrawFour {
		for _, c := range hand {
			if c.Color == v.ActiveColor && !c.IsWild() {
				score -= .25
				if style == Cautious {
					score -= .3
				}
				break
			}
		}
	}
	if v.PendingDraw > 0 {
		score += .3
	}
	return score
}

func rolloutChoice(v game.View, actions []action, style Style, random *rand.Rand, noise float64) action {
	if random.Float64() < noise {
		return actions[random.IntN(len(actions))]
	}
	best := actions[0]
	score := math.Inf(-1)
	for _, a := range actions {
		s := prior(v, a.cmd, style) + random.Float64()*.25
		if s > score {
			score = s
			best = a
		}
	}
	return best
}

// Every player receives their own reward. Truncated rollout values are normalized
// estimates from remaining cards, not terminal wins or a claim of win-rate accuracy.
func rewards(g *game.Game) [10]float64 {
	v := g.View("")
	var result [10]float64
	sum := 0.0
	for i, p := range v.Players {
		if p.Eliminated {
			continue
		}
		if v.Phase == game.Finished {
			if p.ID == v.Winner {
				result[i] = 1
			}
			continue
		}
		n := float64(p.HandCount)
		if p.ID == v.Turn {
			n += float64(v.PendingDraw) * .65
		}
		result[i] = 1 / math.Pow(n+1, 1.5)
		sum += result[i]
	}
	if v.Phase != game.Finished && sum > 0 {
		for i := range result {
			result[i] /= sum
		}
	}
	return result
}
