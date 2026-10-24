package bot

import (
	"fmt"
	"math"
	"math/rand/v2"
	"unour/game"
)

var colors = [...]game.Color{game.Red, game.Blue, game.Green, game.Yellow}

// determinize constructs a hypothetical checkpoint exclusively from public
// observations and sampled unknown cards. Never pass a live checkpoint here.
func determinize(obs Observation, rules game.RuleSet, random *rand.Rand) (*game.Game, error) {
	v := obs.View
	if v.Phase != game.Playing || v.You == "" || v.You != v.Turn || v.TopCard == nil || v.DiscardCount < 1 {
		return nil, fmt.Errorf("bot: observation must be the acting player's running view")
	}
	deck := game.StandardDeck()
	seen := [109]bool{}
	take := func(c game.Card) error {
		if c.ID < 1 || c.ID > 108 || seen[c.ID] || deck[c.ID-1] != c {
			return fmt.Errorf("bot: duplicate or invalid known card")
		}
		seen[c.ID] = true
		return nil
	}
	cp := game.Checkpoint{FormatVersion: 1, Version: v.Version, Phase: v.Phase, Direction: v.Direction, ActiveColor: v.ActiveColor, PendingDraw: v.PendingDraw, PendingKind: v.PendingDrawKind, DrawnCardID: v.DrawnCardID, MaxPlayers: 10, InitialHand: 7}
	for _, r := range rules.Rules() {
		cp.RuleIDs = append(cp.RuleIDs, r.ID())
	}
	ownFound := false
	total := v.DrawPileCount + v.DiscardCount
	for i, p := range v.Players {
		if p.HandCount < 0 || p.HandCount > 108 {
			return nil, fmt.Errorf("bot: invalid hand count")
		}
		total += p.HandCount
		pc := game.PlayerCheckpoint{ID: p.ID, Name: p.Name, Eliminated: p.Eliminated}
		if p.UNO != nil {
			u := *p.UNO
			pc.UNO = &u
		}
		if p.ID == v.You {
			ownFound = true
			cp.TurnIndex = i
			if len(p.Hand) != p.HandCount {
				return nil, fmt.Errorf("bot: own hand is missing")
			}
			for _, c := range p.Hand {
				if err := take(c); err != nil {
					return nil, err
				}
			}
			pc.Hand = append([]game.Card(nil), p.Hand...)
		}
		cp.Players = append(cp.Players, pc)
	}
	if !ownFound || total != 108 || v.DrawPileCount < 0 {
		return nil, fmt.Errorf("bot: inconsistent public card counts")
	}
	discard := obs.Memory.Discard
	if len(discard) == 0 {
		discard = []game.Card{*v.TopCard}
	}
	if len(discard) > v.DiscardCount || discard[len(discard)-1] != *v.TopCard {
		return nil, fmt.Errorf("bot: stale discard memory")
	}
	for _, c := range discard {
		if err := take(c); err != nil {
			return nil, err
		}
	}
	pool := make([]game.Card, 0, 108)
	for _, c := range deck {
		if !seen[c.ID] {
			pool = append(pool, c)
		}
	}
	// Missing historical discards are unknown, never inferred from real state.
	missing := v.DiscardCount - len(discard)
	for i := 0; i < missing; i++ {
		idx := random.IntN(len(pool))
		cp.Discard = append(cp.Discard, pool[idx])
		pool = append(pool[:idx], pool[idx+1:]...)
	}
	cp.Discard = append(cp.Discard, discard...)
	for i, p := range v.Players {
		if p.ID == v.You {
			continue
		}
		for n := 0; n < p.HandCount; n++ {
			sum := 0.0
			for _, c := range pool {
				sum += beliefWeight(p.ID, c, obs.Memory.Beliefs)
			}
			x := random.Float64() * sum
			pick := len(pool) - 1
			for j, c := range pool {
				x -= beliefWeight(p.ID, c, obs.Memory.Beliefs)
				if x < 0 {
					pick = j
					break
				}
			}
			cp.Players[i].Hand = append(cp.Players[i].Hand, pool[pick])
			pool = append(pool[:pick], pool[pick+1:]...)
		}
	}
	random.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	cp.Deck = pool
	if v.ChallengeTarget != "" {
		previous := obs.Memory.ChallengeColor
		if !previous.Valid() {
			for i := len(discard) - 2; i >= 0; i-- {
				if !discard[i].IsWild() {
					previous = discard[i].Color
					break
				}
			}
		}
		if !previous.Valid() {
			previous = colors[random.IntN(len(colors))]
		}
		cp.Challenge = &game.ChallengeCheckpoint{Offender: v.ChallengeTarget}
		for _, p := range cp.Players {
			if p.ID == v.ChallengeTarget {
				for _, c := range p.Hand {
					if c.Color == previous && !c.IsWild() {
						cp.Challenge.HadColor = true
					}
				}
			}
		}
	}
	return game.Restore(cp, rules, func(cards []game.Card) error {
		random.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })
		return nil
	})
}

func beliefWeight(player game.PlayerID, card game.Card, beliefs []ColorBelief) float64 {
	w := 1.0
	for _, belief := range beliefs {
		if belief.PlayerID == player && belief.Color == card.Color && !math.IsNaN(belief.Weight) {
			w *= math.Exp(math.Max(-1.2, math.Min(1.2, belief.Weight)))
		}
	}
	return w
}
