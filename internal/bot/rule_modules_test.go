package bot

import (
	"context"
	"testing"
	"unour/game"
)

func TestDecideUsesFrozenConfigurationWithoutInstalledModule(t *testing.T) {
	g, obs := fixture(t, [][]int{{1}, {26, 27}}, []int{4}, nil, nil)
	cp := g.Checkpoint()
	module := game.RuleModule{Name: "custom_no_zero", Version: 1, Title: "禁零", Steps: []game.RuleStep{{When: `action == "play" && card.kind == "number" && card.number == 0`, Reject: "禁止零"}}}
	obs.Rules = append(obs.Rules, game.RuleSpec{Name: module.Name, Version: 1})
	obs.RuleModules = append(cp.RuleModules, module)
	brain := New(nil)
	decision, err := brain.Decide(context.Background(), obs, Profile{Easy, Balanced}, SearchConfig{Seed: 1, MaxIterations: 3})
	if err != nil {
		t.Fatal("search depends on current catalog", err)
	}
	if decision.Command.Type != game.Draw {
		t.Fatal("bot ignored frozen rule", decision.Command)
	}
	// No registry in this test has this module; success above must use the bundle.
	obs.RuleModules = nil
	if _, err := brain.Decide(context.Background(), obs, Profile{Easy, Balanced}, SearchConfig{Seed: 1, MaxIterations: 3}); err == nil {
		t.Fatal("fixture does not exercise the frozen module boundary")
	}
}
