package bot

import (
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
	"unour/game"
)

// fixture uses the real engine and its validated checkpoint boundary to arrange
// physical cards; IDs not in a hand or discard remain in the full 108-card deck.
func fixture(t testing.TB, hands [][]int, discard []int, specs []game.RuleSpec, edit func(*game.Checkpoint)) (*game.Game, Observation) {
	t.Helper()
	if specs == nil {
		specs = game.DefaultRuleSpecs()
	}
	rules, err := game.NewRegistry().Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	cp := game.Checkpoint{FormatVersion: 1, Version: 20, Phase: game.Playing, Direction: 1, MaxPlayers: 10, InitialHand: 7}
	for _, rule := range rules.Rules() {
		cp.RuleIDs = append(cp.RuleIDs, rule.ID())
	}
	deck := game.StandardDeck()
	used := map[int]bool{}
	for i, ids := range hands {
		id := game.PlayerID(string(rune('a' + i)))
		p := game.PlayerCheckpoint{ID: id, Name: string(id)}
		for _, id := range ids {
			p.Hand = append(p.Hand, deck[id-1])
			used[id] = true
		}
		cp.Players = append(cp.Players, p)
	}
	for _, id := range discard {
		cp.Discard = append(cp.Discard, deck[id-1])
		used[id] = true
	}
	cp.ActiveColor = cp.Discard[len(cp.Discard)-1].Color
	if !cp.ActiveColor.Valid() {
		cp.ActiveColor = game.Red
	}
	for _, c := range deck {
		if !used[c.ID] {
			cp.Deck = append(cp.Deck, c)
		}
	}
	if edit != nil {
		edit(&cp)
	}
	g, err := game.Restore(cp, rules, func([]game.Card) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	obs := Observation{View: g.View("a"), Rules: specs, Memory: Memory{Discard: append([]game.Card(nil), cp.Discard...)}}
	return g, obs
}

func rng(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed+1)) }

func TestSamplingConservesKnownCardsAndIgnoresOtherHands(t *testing.T) {
	_, obs := fixture(t, [][]int{{1, 102, 28}, {7, 8, 9}, {51, 52}}, []int{20, 4}, nil, nil)
	rules, _ := game.NewRegistry().Build(obs.Rules)
	original := obs.Memory.Clone()
	for seed := uint64(1); seed <= 30; seed++ {
		sampled, err := determinize(obs, rules, rng(seed))
		if err != nil {
			t.Fatal(err)
		}
		cp := sampled.Checkpoint()
		if len(cp.Players[0].Hand) != 3 || len(cp.Players[1].Hand) != 3 || len(cp.Players[2].Hand) != 2 || len(cp.Deck) != 98 {
			t.Fatal("hand/deck counts changed")
		}
		seen := map[int]bool{}
		all := append(append([]game.Card(nil), cp.Deck...), cp.Discard...)
		for _, p := range cp.Players {
			all = append(all, p.Hand...)
		}
		for _, c := range all {
			if seen[c.ID] {
				t.Fatal("duplicate physical card")
			}
			seen[c.ID] = true
		}
		if len(seen) != 108 || !reflect.DeepEqual(cp.Players[0].Hand, obs.View.Players[0].Hand) || !reflect.DeepEqual(cp.Discard, obs.Memory.Discard) {
			t.Fatal("known cards changed")
		}
	}
	if !reflect.DeepEqual(original, obs.Memory) {
		t.Fatal("sampling mutated memory")
	}
	first, _ := determinize(obs, rules, rng(5))
	obs.View.Players[1].Hand = game.StandardDeck()[20:23]
	second, _ := determinize(obs, rules, rng(5))
	if !reflect.DeepEqual(first.Checkpoint(), second.Checkpoint()) {
		t.Fatal("sampling peeked at opponent hand")
	}
}

func TestSamplingChallengeEvidenceComesFromSampledHand(t *testing.T) {
	_, obs := fixture(t, [][]int{{26, 27}, {1, 2, 3}}, []int{4, 102}, nil, func(cp *game.Checkpoint) {
		cp.PendingDraw = 4
		cp.PendingKind = game.WildDrawFour
		cp.Challenge = &game.ChallengeCheckpoint{Offender: "b", HadColor: true}
		cp.ActiveColor = game.Blue
	})
	obs.Memory.ChallengeColor = game.Red
	rules, _ := game.NewRegistry().Build(obs.Rules)
	for seed := uint64(1); seed < 20; seed++ {
		g, err := determinize(obs, rules, rng(seed))
		if err != nil {
			t.Fatal(err)
		}
		cp := g.Checkpoint()
		has := false
		for _, c := range cp.Players[1].Hand {
			has = has || c.Color == game.Red
		}
		if cp.Challenge.HadColor != has {
			t.Fatal("challenge evidence did not match sampled hand")
		}
	}
}

func TestSamplingPreservesDrawRestrictionAndUNO(t *testing.T) {
	_, obs := fixture(t, [][]int{{1, 2}, {26}}, []int{4}, nil, func(cp *game.Checkpoint) {
		cp.DrawnCardID = 2
		cp.Players[1].UNO = &game.UNOWindow{ID: 19, Deadline: time.Unix(100, 0)}
	})
	rules, _ := game.NewRegistry().Build(obs.Rules)
	g, err := determinize(obs, rules, rng(1))
	if err != nil {
		t.Fatal(err)
	}
	if g.View("a").DrawnCardID != 2 || g.View("a").Players[1].UNO == nil {
		t.Fatal("lost public metadata")
	}
}
