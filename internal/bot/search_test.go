package bot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"unour/game"
)

func TestDecideFindsImmediateWin(t *testing.T) {
	g, obs := fixture(t, [][]int{{1}, {26, 27}}, []int{4}, nil, nil)
	d, err := New(nil).Decide(context.Background(), obs, Profile{Easy, Balanced}, SearchConfig{Seed: 1, MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}
	if d.Command.Type != game.Play || d.Command.CardID != 1 || d.Command.PlayerID != "a" {
		t.Fatalf("missed immediate win: %+v", d)
	}
	if _, err = g.Apply(d.Command); err != nil {
		t.Fatal(err)
	}
	if g.View("a").Winner != "a" {
		t.Fatal("did not win")
	}
}

func TestLegalActionsUseEngineForPenaltyPassAndChallenge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hands   [][]int
		discard []int
		edit    func(*game.Checkpoint)
		want    map[game.Action]bool
	}{
		{"penalty", [][]int{{1, 102}, {26, 27}}, []int{24}, func(cp *game.Checkpoint) { cp.PendingDraw = 2; cp.PendingKind = game.DrawTwo }, map[game.Action]bool{game.Draw: true}},
		{"drawn", [][]int{{1, 26}, {27, 28}}, []int{4}, func(cp *game.Checkpoint) { cp.DrawnCardID = 26 }, map[game.Action]bool{game.Pass: true}},
		{"challenge", [][]int{{26, 27}, {1, 2}}, []int{4, 102}, func(cp *game.Checkpoint) {
			cp.PendingDraw = 4
			cp.PendingKind = game.WildDrawFour
			cp.Challenge = &game.ChallengeCheckpoint{Offender: "b", HadColor: true}
		}, map[game.Action]bool{game.Draw: true, game.Challenge: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, obs := fixture(t, tc.hands, tc.discard, nil, tc.edit)
			actions, err := legalActions(context.Background(), g)
			if err != nil {
				t.Fatal(err)
			}
			got := map[game.Action]bool{}
			for _, a := range actions {
				got[a.cmd.Type] = true
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("legal actions: %v want %v", got, tc.want)
			}
			d, err := New(nil).Decide(context.Background(), obs, Profile{Normal, Cautious}, SearchConfig{Seed: 7, MaxIterations: 8})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.Apply(d.Command); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFinalPenaltyDoesNotShortCircuitSearch(t *testing.T) {
	_, obs := fixture(t, [][]int{{24}, {26, 27}}, []int{4}, nil, nil)
	d, err := New(nil).Decide(context.Background(), obs, Profile{Normal, Balanced}, SearchConfig{Seed: 10, MaxIterations: 12})
	if err != nil {
		t.Fatal(err)
	}
	if d.Iterations != 12 {
		t.Fatalf("last penalty was treated as immediate win: %+v", d)
	}
}

func TestWildActionsRespectKeepColorRule(t *testing.T) {
	for _, keep := range []bool{false, true} {
		specs := game.DefaultRuleSpecs()
		options, err := json.Marshal(map[string]bool{"keep_color": keep})
		if err != nil {
			t.Fatal(err)
		}
		specs[1].Options = options
		g, _ := fixture(t, [][]int{{101, 102, 1}, {26, 27}}, []int{4}, specs, nil)
		actions, err := legalActions(context.Background(), g)
		if err != nil {
			t.Fatal(err)
		}
		choices := map[int]map[game.Color]bool{101: {}, 102: {}}
		for _, a := range actions {
			if choices[a.cmd.CardID] != nil {
				choices[a.cmd.CardID][a.cmd.Color] = true
			}
		}
		for id, colors := range choices {
			if colors[game.Red] != keep || !colors[game.Blue] || !colors[game.Green] || !colors[game.Yellow] {
				t.Fatalf("keep=%v card=%d wild choices: %v", keep, id, colors)
			}
		}
	}
}

func TestBotConsidersOriginalHandAfterDrawWhenEnabled(t *testing.T) {
	specs := game.DefaultRuleSpecs()
	specs[2].Options = json.RawMessage(`{"play_any_after_draw":true}`)
	g, _ := fixture(t, [][]int{{1, 26}, {27, 28}}, []int{4}, specs, func(cp *game.Checkpoint) { cp.DrawnCardID = 26 })
	actions, err := legalActions(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.cmd.Type == game.Play && action.cmd.CardID == 1 {
			if _, err := g.Apply(action.cmd); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("bot ignored a legal original hand card after drawing")
}

func TestSearchIsRepeatableDetachedAndConcurrent(t *testing.T) {
	_, obs := fixture(t, [][]int{{1, 21, 26, 101}, {28, 29, 30}, {51, 52, 53}}, []int{4}, nil, nil)
	data, _ := json.Marshal(obs)
	brain := New(nil)
	cfg := SearchConfig{Seed: 35, MaxIterations: 24}
	first, err := brain.Decide(context.Background(), obs, Profile{Hard, Aggressive}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			d, err := brain.Decide(context.Background(), obs, Profile{Hard, Aggressive}, cfg)
			if err != nil {
				t.Error(err)
				return
			}
			if !reflect.DeepEqual(first, d) {
				t.Errorf("seed changed decision: %+v vs %+v", first, d)
			}
		})
	}
	wg.Wait()
	after, _ := json.Marshal(obs)
	if string(data) != string(after) {
		t.Fatal("search mutated observation")
	}
}

func TestSearchCancellationAndBudget(t *testing.T) {
	_, obs := fixture(t, [][]int{{1, 21, 26, 101}, {28, 29, 30}}, []int{4}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(nil).Decide(ctx, obs, Profile{Expert, Balanced}, SearchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	d, err := New(nil).Decide(context.Background(), obs, Profile{Expert, Balanced}, SearchConfig{Seed: 9, MaxIterations: 100000, TimeBudget: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if d.Iterations >= 100000 || d.Command.PlayerID != "a" {
		t.Fatalf("budget ignored: %+v", d)
	}
}

func TestSearchUnderRuleCombinations(t *testing.T) {
	for _, specs := range [][]game.RuleSpec{
		{{Name: "matching"}, {Name: "actions"}, {Name: "draw", Options: json.RawMessage(`{"play_drawn_card":false}`)}},
		{{Name: "matching"}, {Name: "actions", Options: json.RawMessage(`{"skip_draw":true,"keep_color":true}`)}, {Name: "draw"}, {Name: "stacking", Options: json.RawMessage(`{"mixed":true,"different_colors":true,"reverse":true}`)}, {Name: "hand_limit", Options: json.RawMessage(`{"limit":12}`)}},
	} {
		g, obs := fixture(t, [][]int{{1, 21, 24, 102}, {26, 27, 28}, {51, 52, 53}}, []int{4}, specs, nil)
		d, err := New(nil).Decide(context.Background(), obs, Profile{Normal, Balanced}, SearchConfig{Seed: 2, MaxIterations: 15})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = g.Apply(d.Command); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkDecide(b *testing.B) {
	_, obs := fixture(b, [][]int{{1, 21, 26, 101, 24, 45, 52}, {28, 29, 30, 31, 32, 33, 34}, {51, 53, 54, 55, 56, 57, 58}}, []int{4}, nil, nil)
	for _, difficulty := range []Difficulty{Easy, Normal, Hard, Expert} {
		b.Run(string(difficulty), func(b *testing.B) {
			brain := New(nil)
			b.ReportAllocs()
			iterations := 0
			for b.Loop() {
				decision, err := brain.Decide(context.Background(), obs, Profile{difficulty, Balanced}, SearchConfig{Seed: 3})
				if err != nil {
					b.Fatal(err)
				}
				iterations += decision.Iterations
			}
			b.ReportMetric(float64(iterations)/float64(b.N), "iterations/op")
		})
	}
}

// A rollout must recover from a high-scoring but illegal candidate instead of
// ending the turn, inventing legality, or mutating the state it was given.
func TestRolloutStepFindsLegalMoveWithoutMutatingInput(t *testing.T) {
	g, _ := fixture(t, [][]int{{1, 26}, {27, 28}}, []int{4}, nil, func(cp *game.Checkpoint) { cp.DrawnCardID = 26 })
	before := g.Checkpoint()
	next, err := rolloutStep(context.Background(), g, g.View("a"), Balanced, rng(1), .2)
	if err != nil {
		t.Fatal(err)
	}
	if next.View("a").Turn != "b" || next.View("a").DrawnCardID != 0 {
		t.Fatal("did not pass restricted unplayable drawn card")
	}
	if !reflect.DeepEqual(g.Checkpoint(), before) {
		t.Fatal("rollout changed input")
	}
}
