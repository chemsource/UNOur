package game

import (
	"strings"
	"testing"
)

func TestRuleExpressionsAreTypedBoundedAndHaveNoExternalCalls(t *testing.T) {
	options := map[string]ruleValue{"enabled": boolValue(true), "amount": intValue(2)}
	for _, source := range []string{
		"unknown", "options.missing", "options.enabled + 1", "pending_draw && true",
		"card.kind < 1", "os.ReadFile(\"secret\")", "func() {}()", "[]int{1}",
		"effects.allow()", "1 << 60", "1.5", "card.number / 0", "0x100", "true ? 1 : 2",
		strings.Repeat("!", 30) + "true",
	} {
		if _, err := compileRuleExpression(source, options, false); err == nil {
			t.Errorf("accepted unsafe or invalid expression %q", source)
		}
	}
	if _, err := compileRuleExpression("pending_draw > 0", options, true); err == nil {
		t.Fatal("configuration validation reads live game state")
	}
	if _, err := compileRuleExpression(strings.Repeat("true || ", 260)+"false", options, false); err == nil {
		t.Fatal("unbounded expression accepted")
	}
}

func TestRuleExpressionShortCircuitAndCheckedArithmetic(t *testing.T) {
	options := map[string]ruleValue{"amount": intValue(2)}
	env := ruleEnvironment{context: Context{s: &state{pending: 4}}, effects: &Effects{TurnSteps: 1}}
	for _, tc := range []struct {
		source string
		want   ruleValue
	}{
		{"pending_draw + options.amount * 2", intValue(8)},
		{"pending_draw > 0 && !card.wild", boolValue(true)},
		{"true || (8 / (pending_draw - 4) > 0)", boolValue(true)},
		{"false && (8 / (pending_draw - 4) > 0)", boolValue(false)},
		{"effects.turn_steps == 1", boolValue(true)},
	} {
		expr, err := compileRuleExpression(tc.source, options, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.run(&env)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %+v %v", tc.source, got, err)
		}
	}
	for _, source := range []string{"8 / (pending_draw - 4)", "9223372036854775807 + pending_draw", "-9223372036854775807 - pending_draw", "9223372036854775807 * pending_draw"} {
		expr, err := compileRuleExpression(source, options, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := expr.run(&env); err == nil {
			t.Fatal("unchecked arithmetic", source)
		}
	}
}
