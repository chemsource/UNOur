package bot

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
	"unour/game"
)

type searchLimits struct {
	iterations, depth int
	budget            time.Duration
	noise             float64
	// workBudget bounds simulated state transitions across tree search and
	// rollouts, in addition to the iteration and wall-clock limits.
	workBudget int
}

func limits(d Difficulty) searchLimits {
	switch d {
	case Easy:
		return searchLimits{48, 12, 30 * time.Millisecond, .34, 384}
	case Hard:
		return searchLimits{384, 48, 150 * time.Millisecond, .09, 4_500}
	case Expert:
		return searchLimits{900, 72, 250 * time.Millisecond, .04, 12_000}
	default:
		return searchLimits{144, 24, 75 * time.Millisecond, .18, 1_500}
	}
}

type node struct {
	children          map[actionKey]*node
	visits, available int
	value             [10]float64
}

// Decide implements a bounded single-observer information-set MCTS variant.
// Each iteration samples hidden information anew; action nodes merge across
// determinizations and availability-aware UCB uses the acting player's reward.
func (b *Brain) Decide(ctx context.Context, obs Observation, profile Profile, cfg SearchConfig) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if !profile.Difficulty.Valid() || !profile.Style.Valid() {
		return Decision{}, fmt.Errorf("bot: invalid profile")
	}
	if cfg.MaxIterations < 0 || cfg.TimeBudget < 0 {
		return Decision{}, fmt.Errorf("bot: invalid search limits")
	}
	l := limits(profile.Difficulty)
	// An explicit iteration limit without an explicit deadline is deterministic.
	if cfg.MaxIterations > 0 {
		l.iterations = cfg.MaxIterations
		l.budget = 0
		l.workBudget = 0
	}
	if cfg.TimeBudget > 0 {
		l.budget = cfg.TimeBudget
	}
	started := time.Now()
	expired := func() bool { return l.budget > 0 && time.Since(started) >= l.budget }
	seed := cfg.Seed
	if seed == 0 {
		var bytes [8]byte
		if _, err := cryptorand.Read(bytes[:]); err != nil {
			return Decision{}, err
		}
		seed = binary.LittleEndian.Uint64(bytes[:])
	}
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	specs := obs.Rules
	if len(specs) == 0 {
		specs = game.DefaultRuleSpecs()
	}
	rules, err := b.registry.Build(specs)
	if err != nil {
		return Decision{}, err
	}
	initial, err := determinize(obs, rules, random)
	if err != nil {
		return Decision{}, err
	}
	rootActions, err := legalActionsForView(ctx, initial, obs.View)
	if err != nil {
		return Decision{}, err
	}
	if len(rootActions) == 0 {
		return Decision{}, fmt.Errorf("bot: no legal action")
	}
	if ownHandCount(obs.View) == 1 {
		for _, a := range rootActions {
			if a.cmd.Type != game.Play {
				continue
			}
			next, _, err := initial.Branch(a.cmd)
			if err == nil && next.View("").Winner == obs.View.You {
				cmd := a.cmd
				cmd.At = time.Time{}
				return Decision{Command: cmd}, nil
			}
		}
	}
	fallback := rolloutChoice(obs.View, rootActions, profile.Style, random, 0).cmd
	root := &node{children: map[actionKey]*node{}}
	actorIndex := 0
	for i, p := range obs.View.Players {
		if p.ID == obs.View.You {
			actorIndex = i
		}
	}
	iterations := 0
	work := 0
	pathBuffer := make([]*node, 0, l.depth+1)
	for iterations < l.iterations && !expired() && (l.workBudget == 0 || work < l.workBudget) {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		sim, err := determinize(obs, rules, random)
		if err != nil {
			return Decision{}, err
		}
		current := root
		path := pathBuffer[:1]
		path[0] = root
		expanded := false
		for depth := 0; depth < l.depth && !expired() && (l.workBudget == 0 || work < l.workBudget); depth++ {
			if err := ctx.Err(); err != nil {
				return Decision{}, err
			}
			public := sim.View("")
			if public.Phase != game.Playing {
				break
			}
			view := sim.View(public.Turn)
			if expanded {
				next, err := rolloutStepOwned(ctx, sim, view, profile.Style, random, l.noise)
				if err != nil {
					return Decision{}, err
				}
				if next == nil {
					break
				}
				sim = next
				work++
				continue
			}
			actions, err := legalActionsForView(ctx, sim, view)
			if err != nil {
				return Decision{}, err
			}
			if len(actions) == 0 {
				break
			}
			actor := 0
			for i, p := range view.Players {
				if p.ID == view.You {
					actor = i
				}
			}
			bestScore := math.Inf(-1)
			chosen := actions[0]
			var child *node
			for _, a := range actions {
				k := key(a.cmd)
				candidate := current.children[k]
				if candidate == nil {
					candidate = &node{children: map[actionKey]*node{}}
					current.children[k] = candidate
				}
				candidate.available++
				p := prior(view, a.cmd, profile.Style)
				score := 1e6 + p + random.Float64()*.3
				if candidate.visits > 0 {
					score = candidate.value[actor]/float64(candidate.visits) + 1.15*math.Sqrt(math.Log(float64(candidate.available)+1)/float64(candidate.visits)) + .25*p/float64(candidate.visits+1)
				}
				if score > bestScore {
					bestScore = score
					chosen = a
					child = candidate
				}
			}
			expanded = child.visits == 0
			current = child
			path = append(path, child)
			next, _, err := sim.Branch(chosen.cmd)
			if err != nil {
				return Decision{}, fmt.Errorf("bot: applying selected legal action: %w", err)
			}
			sim = next
			work++
		}
		value := rewards(sim)
		for _, n := range path {
			n.visits++
			for i := range value {
				n.value[i] += value[i]
			}
		}
		iterations++
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	// Restrict the final choice to legal actions from the original information set.
	// The bounded score perturbation gives styles/difficulties controlled variety.
	bestScore := math.Inf(-1)
	best := fallback
	for _, a := range rootActions {
		n := root.children[key(a.cmd)]
		if n == nil || n.visits == 0 {
			continue
		}
		score := .55*float64(n.visits)/float64(max(1, iterations)) + .45*n.value[actorIndex]/float64(n.visits) + .035*prior(obs.View, a.cmd, profile.Style) + (random.Float64()-.5)*l.noise
		if score > bestScore {
			bestScore = score
			best = a.cmd
		}
	}
	best.At = time.Time{}
	return Decision{Command: best, Iterations: iterations}, nil
}

func ownHandCount(v game.View) int {
	for _, p := range v.Players {
		if p.ID == v.You {
			return p.HandCount
		}
	}
	return -1
}
