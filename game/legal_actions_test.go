package game

import (
	"reflect"
	"testing"
)

func TestLegalActionsUseBoundRulesAndPreserveState(t *testing.T) {
	m := RuleModule{Name: "only_green", Version: 1, Title: "只能选绿", Steps: []RuleStep{{When: `action == "play" && card.wild && chosen_color != "green"`, Reject: "只能选绿"}}}
	rule, err := compileRuleModule(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := fixture(t, rule)
	before := g.Checkpoint()
	for _, viewer := range []PlayerID{"b", "observer", ""} {
		if len(g.LegalActions(viewer)) != 0 {
			t.Fatal("non-actor received actions")
		}
	}
	actions := g.LegalActions("a")
	wildColors := []Color{}
	for _, a := range actions {
		if err := g.CanApply(Command{Type: a.Action, PlayerID: "a", CardID: a.CardID, Color: a.Color}); err != nil {
			t.Fatal("invalid action advertised", a, err)
		}
		if a.CardID == 3 {
			wildColors = append(wildColors, a.Color)
		}
	}
	if !reflect.DeepEqual(wildColors, []Color{Green}) {
		t.Fatal("configured choice restriction ignored", wildColors)
	}
	if !reflect.DeepEqual(before, g.Checkpoint()) {
		t.Fatal("legality read mutated state")
	}
}

func TestChosenColorConfigurationCannotBeBypassedWithKeepAlias(t *testing.T) {
	m := RuleModule{Name: "no_red_wild", Version: 1, Title: "万能牌不能选红", Steps: []RuleStep{{Set: map[string]string{"keep_color": "true"}}, {When: `action == "play" && card.wild && chosen_color == "red"`, Reject: "不能选红"}}}
	rule, err := compileRuleModule(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := fixture(t, rule)
	for _, color := range []Color{Red, KeepColor} {
		if err := g.CanApply(Command{Type: Play, PlayerID: "a", CardID: 3, Color: color}); err == nil {
			t.Fatal("color alias bypassed configured restriction", color)
		}
	}
}
