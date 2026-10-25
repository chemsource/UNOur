package game

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ruleIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ruleTextValid(s string, max int, required bool) bool {
	return (!required || s != "") && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}
func strictRuleJSON(data []byte, target any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("rule JSON must be valid UTF-8")
	}
	if err := validateRuleJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}
func scalarRuleValue(v any) (ruleValue, error) {
	switch v := v.(type) {
	case bool:
		return boolValue(v), nil
	case string:
		if !ruleTextValid(v, 128, false) {
			return ruleValue{}, fmt.Errorf("invalid string value")
		}
		return stringValue(v), nil
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return ruleValue{}, fmt.Errorf("value must be an integer")
		}
		return boundedRuleInteger(n)
	case int:
		return boundedRuleInteger(int64(v))
	case int64:
		return boundedRuleInteger(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || math.Abs(v) > 1000000 {
			return ruleValue{}, fmt.Errorf("value must be a bounded integer")
		}
		return intValue(int64(v)), nil
	}
	return ruleValue{}, fmt.Errorf("option value must be boolean, integer or string")
}
func boundedRuleInteger(n int64) (ruleValue, error) {
	if n < -1000000 || n > 1000000 {
		return ruleValue{}, fmt.Errorf("option integer exceeds bounded range")
	}
	return intValue(n), nil
}

func ruleScalar(v ruleValue) any {
	switch v.kind {
	case ruleBool:
		return v.boolean
	case ruleInt:
		return v.number
	default:
		return v.text
	}
}

func validateRuleModule(m RuleModule) error {
	if !ruleIdentifier.MatchString(m.Name) || m.Version < 1 || m.Version > 1000000 || m.Order < 0 || m.Order > 1000000 || !ruleTextValid(m.Title, 80, true) || !ruleTextValid(m.Description, 512, false) || !ruleTextValid(m.Group, 80, false) || m.Required && !m.DefaultEnabled {
		return fmt.Errorf("invalid module identity, presentation, version or required/default state")
	}
	if len(m.Options) > 32 || len(m.Steps) > 32 || len(m.Checks) > 16 || len(m.Constraints) > 16 || len(m.Summary) > 32 {
		return fmt.Errorf("module exceeds its bounded size")
	}
	keys := map[string]RuleOption{}
	for _, option := range m.Options {
		if !ruleIdentifier.MatchString(option.Key) || !ruleTextValid(option.Label, 80, true) || !ruleTextValid(option.Description, 512, false) {
			return fmt.Errorf("invalid option metadata")
		}
		if _, exists := keys[option.Key]; exists {
			return fmt.Errorf("duplicate option %q", option.Key)
		}
		value, err := scalarRuleValue(option.Default)
		if err != nil {
			return fmt.Errorf("option %s: %w", option.Key, err)
		}
		switch option.Type {
		case "boolean":
			if value.kind != ruleBool || len(option.Choices) != 0 {
				return fmt.Errorf("boolean option needs a boolean default")
			}
		case "integer":
			if value.kind != ruleInt || option.Min < 0 || option.Max > 1000000 || option.Max < option.Min || value.number < option.Min || value.number > option.Max || len(option.Choices) != 0 {
				return fmt.Errorf("invalid integer option bounds or default")
			}
		case "select":
			if len(option.Choices) < 1 || len(option.Choices) > 32 {
				return fmt.Errorf("select needs 1–32 choices")
			}
			seen := map[ruleValue]bool{}
			found := false
			for _, choice := range option.Choices {
				v, err := scalarRuleValue(choice.Value)
				if err != nil || v.kind != value.kind || seen[v] || !ruleTextValid(choice.Label, 80, true) {
					return fmt.Errorf("invalid select choices")
				}
				seen[v] = true
				found = found || v == value
			}
			if !found {
				return fmt.Errorf("default is not a select choice")
			}
		default:
			return fmt.Errorf("unsupported option type %q", option.Type)
		}
		keys[option.Key] = option
	}
	for _, option := range m.Options {
		if len(option.DependsOn) > 16 {
			return fmt.Errorf("too many option dependencies")
		}
		for _, dep := range option.DependsOn {
			target, ok := keys[dep.Key]
			if !ok || dep.Key == option.Key {
				return fmt.Errorf("unknown or self-referencing dependency")
			}
			want, err := scalarRuleValue(dep.Equals)
			got, _ := scalarRuleValue(target.Default)
			if err != nil || want.kind != got.kind {
				return fmt.Errorf("dependency type mismatch")
			}
		}
	}
	// Reject cycles before the client renders dependent controls. Every edge is
	// local to a module, and the 32-option bound limits this traversal.
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("cyclic option dependency")
		}
		if done[key] {
			return nil
		}
		visiting[key] = true
		for _, dep := range keys[key].DependsOn {
			if err := visit(dep.Key); err != nil {
				return err
			}
		}
		delete(visiting, key)
		done[key] = true
		return nil
	}
	for key := range keys {
		if err := visit(key); err != nil {
			return err
		}
	}
	for _, constraint := range m.Constraints {
		if constraint.Kind != "at_least_one" || len(constraint.Fields) < 1 || len(constraint.Fields) > 32 || !ruleTextValid(constraint.Message, 256, true) {
			return fmt.Errorf("invalid constraint")
		}
		seen := map[string]bool{}
		for _, key := range constraint.Fields {
			option, ok := keys[key]
			if !ok || option.Type != "boolean" || seen[key] {
				return fmt.Errorf("constraint needs unique boolean fields")
			}
			seen[key] = true
		}
	}
	for _, check := range m.Checks {
		if !ruleTextValid(check.Message, 256, true) {
			return fmt.Errorf("invalid validation message")
		}
	}
	for _, step := range m.Steps {
		if (step.Reject == "") == (len(step.Set) == 0) || len(step.Set) > 16 || !ruleTextValid(step.Reject, 256, false) {
			return fmt.Errorf("step needs either effects or a rejection")
		}
	}
	for _, line := range m.Summary {
		if !ruleTextValid(line.Text, 512, true) {
			return fmt.Errorf("invalid summary text")
		}
	}
	return nil
}
func normalizeRuleOptions(m RuleModule, raw json.RawMessage) (map[string]ruleValue, json.RawMessage, error) {
	if len(raw) > 8192 {
		return nil, nil, fmt.Errorf("options exceed 8 KiB")
	}
	supplied := map[string]any{}
	if len(raw) > 0 {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, nil, fmt.Errorf("options must be an object")
		}
		if err := strictRuleJSON(raw, &supplied); err != nil {
			return nil, nil, err
		}
	}
	values := make(map[string]ruleValue, len(m.Options))
	normalized := map[string]any{}
	for _, option := range m.Options {
		value, exists := supplied[option.Key]
		if !exists {
			value = option.Default
		}
		delete(supplied, option.Key)
		v, err := scalarRuleValue(value)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", option.Label, err)
		}
		def, _ := scalarRuleValue(option.Default)
		if v.kind != def.kind {
			return nil, nil, fmt.Errorf("%s: value type differs", option.Label)
		}
		if option.Type == "integer" && (v.number < option.Min || v.number > option.Max) {
			return nil, nil, fmt.Errorf("%s: value outside bounds", option.Label)
		}
		if option.Type == "select" {
			found := false
			for _, choice := range option.Choices {
				x, _ := scalarRuleValue(choice.Value)
				found = found || x == v
			}
			if !found {
				return nil, nil, fmt.Errorf("%s: invalid selection", option.Label)
			}
		}
		values[option.Key] = v
		normalized[option.Key] = ruleScalar(v)
	}
	if len(supplied) > 0 {
		return nil, nil, fmt.Errorf("unknown rule option")
	}
	for _, option := range m.Options {
		current := values[option.Key]
		def, _ := scalarRuleValue(option.Default)
		for _, dependency := range option.DependsOn {
			want, _ := scalarRuleValue(dependency.Equals)
			if values[dependency.Key] != want && current != def {
				return nil, nil, fmt.Errorf("%s: dependency is not enabled", option.Label)
			}
		}
	}
	for _, constraint := range m.Constraints {
		enabled := false
		for _, key := range constraint.Fields {
			enabled = enabled || values[key].boolean
		}
		if !enabled {
			return nil, nil, fmt.Errorf("%s", constraint.Message)
		}
	}
	encoded, err := json.Marshal(normalized)
	return values, encoded, err
}
func compileRuleModule(module RuleModule, raw json.RawMessage) (*configuredRule, error) {
	// Take ownership before retaining anything in immutable compiled closures.
	data, err := json.Marshal(module)
	if err != nil {
		return nil, err
	}
	var m RuleModule
	if err = strictRuleJSON(data, &m); err != nil {
		return nil, err
	}
	if err := validateRuleModule(m); err != nil {
		return nil, err
	}
	options, normalized, err := normalizeRuleOptions(m, raw)
	if err != nil {
		return nil, err
	}
	result := &configuredRule{definition: m, definitionJSON: append(json.RawMessage(nil), data...), optionsJSON: normalized, description: RuleDescription{Name: m.Name, Title: m.Title, Text: []string{}}}
	for _, check := range m.Checks {
		expr, err := compileRuleExpression(check.Expression, options, true)
		if err != nil {
			return nil, err
		}
		if expr.kind != ruleBool {
			return nil, fmt.Errorf("option validation must be boolean")
		}
		v, err := expr.run(nil)
		if err != nil {
			return nil, err
		}
		if !v.boolean {
			return nil, fmt.Errorf("%s", check.Message)
		}
	}
	for _, step := range m.Steps {
		condition := step.When
		if condition == "" {
			condition = "true"
		}
		when, err := compileRuleExpression(condition, options, false)
		if err != nil {
			return nil, err
		}
		if when.kind != ruleBool {
			return nil, fmt.Errorf("step condition must be boolean")
		}
		compiled := compiledRuleStep{when: when, reject: step.Reject}
		keys := make([]string, 0, len(step.Set))
		for key := range step.Set {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			field, ok := ruleEffectFields[key]
			if !ok {
				return nil, fmt.Errorf("unknown effect %q", key)
			}
			expression, err := compileRuleExpression(step.Set[key], options, false)
			if err != nil {
				return nil, err
			}
			if field.kind != expression.kind {
				return nil, fmt.Errorf("effect %s type mismatch", key)
			}
			compiled.assignments = append(compiled.assignments, ruleAssignment{field: field, expression: expression})
		}
		if when.constant == nil || when.constant.boolean {
			result.steps = append(result.steps, compiled)
		}
	}
	for _, line := range m.Summary {
		condition := line.When
		if condition == "" {
			condition = "true"
		}
		expr, err := compileRuleExpression(condition, options, true)
		if err != nil {
			return nil, err
		}
		if expr.kind != ruleBool {
			return nil, fmt.Errorf("summary condition must be boolean")
		}
		v, err := expr.run(nil)
		if err != nil {
			return nil, err
		}
		text, err := formatRuleSummary(line.Text, m.Options, options)
		if err != nil {
			return nil, err
		}
		if v.boolean {
			result.description.Text = append(result.description.Text, text)
		}
	}
	if len(result.description.Text) == 0 && m.Description != "" {
		result.description.Text = append(result.description.Text, m.Description)
	}
	return result, nil
}
func formatRuleSummary(text string, options []RuleOption, values map[string]ruleValue) (string, error) {
	for _, option := range options {
		v := values[option.Key]
		label := fmt.Sprint(ruleScalar(v))
		if v.kind == ruleBool {
			if v.boolean {
				label = "开启"
			} else {
				label = "关闭"
			}
		}
		for _, choice := range option.Choices {
			x, _ := scalarRuleValue(choice.Value)
			if v == x {
				label = choice.Label
			}
		}
		text = strings.ReplaceAll(text, "{{"+option.Key+"}}", label)
	}
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return "", fmt.Errorf("unknown summary placeholder")
	}
	return text, nil
}
