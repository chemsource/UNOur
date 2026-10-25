package game

import (
	"encoding/json"
	"reflect"
	"testing"
)

func configurableTestModule() RuleModule {
	return RuleModule{Name: "custom_skip_draw", Version: 1, Title: "自定义禁后摸牌", Group: "动作", Options: []RuleOption{{Key: "amount", Type: "integer", Label: "摸牌数", Default: 2, Min: 1, Max: 10}}, Steps: []RuleStep{{When: `action == "play" && card.kind == "skip"`, Set: map[string]string{"draw_next": "options.amount"}}}, Summary: []RuleSummaryLine{{Text: "被跳过者摸 {{amount}} 张牌。"}}}
}

func TestConfigurableRuleChangesBehaviorWithoutNamedGoImplementation(t *testing.T) {
	module := configurableTestModule()
	rule, err := compileRuleModule(module, json.RawMessage(`{"amount":3}`))
	if err != nil {
		t.Fatal(err)
	}
	g := fixture(t, rule)
	g.s.players[0].hand[0].Kind = Skip
	if _, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1}); err != nil {
		t.Fatal(err)
	}
	if got := g.View("b").Players[1].HandCount; got != 5 {
		t.Fatalf("configuration did not execute: %d", got)
	}
	if rule.description.Text[0] != "被跳过者摸 3 张牌。" {
		t.Fatal("option summary was not rendered", rule.description)
	}
}

func TestModuleAssignmentsReadOnePlanAndVetoIsAtomic(t *testing.T) {
	module := RuleModule{Name: "plan", Version: 1, Title: "计划", Steps: []RuleStep{{Set: map[string]string{"turn_steps": "2", "draw_next": "effects.turn_steps"}}}}
	rule, err := compileRuleModule(module, nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := Effects{TurnSteps: 1}
	if err := rule.Evaluate(Context{s: &state{}}, &effects); err != nil || effects.TurnSteps != 2 || effects.DrawNext != 1 {
		t.Fatal("assignment map order changed execution", effects, err)
	}
	module.Steps = append(module.Steps, RuleStep{When: `action == "play"`, Reject: "禁止出牌"})
	rule, err = compileRuleModule(module, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := fixture(t, rule)
	before := g.Checkpoint()
	if _, err := g.Apply(Command{Type: Play, PlayerID: "a", CardID: 1}); err == nil {
		t.Fatal("configuration veto ignored")
	}
	if !reflect.DeepEqual(before, g.Checkpoint()) {
		t.Fatal("veto partially committed")
	}
}

func TestModuleRejectsInvalidOptionsDefinitionsAndEffects(t *testing.T) {
	for _, raw := range []json.RawMessage{[]byte(`{"amount":0}`), []byte(`{"amount":1.5}`), []byte(`{"amount":true}`), []byte(`{"unknown":2}`), []byte(`null`)} {
		if _, err := compileRuleModule(configurableTestModule(), raw); err == nil {
			t.Fatal("invalid options accepted", string(raw))
		}
	}
	for _, set := range []map[string]string{{"unknown": "1"}, {"draw_next": "true"}, {"allow": "1"}, {"draw_next": "options.missing"}} {
		m := configurableTestModule()
		m.Steps[0].Set = set
		if _, err := compileRuleModule(m, nil); err == nil {
			t.Fatal("invalid effects accepted", set)
		}
	}
	m := configurableTestModule()
	m.Checks = []RuleCheck{{Expression: "options.amount < 5", Message: "摸牌数必须小于 5"}}
	if _, err := compileRuleModule(m, []byte(`{"amount":8}`)); err == nil {
		t.Fatal("custom option validation ignored")
	}
}
