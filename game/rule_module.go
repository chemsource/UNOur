package game

import "encoding/json"

// RuleModule is a data-only, versioned definition. The interpreter only exposes
// the Context vocabulary and bounded Effects; modules cannot mutate game state.
type RuleModule struct {
	Name           string            `json:"name"`
	Version        int               `json:"version"`
	Order          int               `json:"order"`
	Title          string            `json:"title"`
	Description    string            `json:"description,omitempty"`
	Group          string            `json:"group,omitempty"`
	Required       bool              `json:"required,omitempty"`
	DefaultEnabled bool              `json:"default_enabled,omitempty"`
	Options        []RuleOption      `json:"options,omitempty"`
	Constraints    []RuleConstraint  `json:"constraints,omitempty"`
	Checks         []RuleCheck       `json:"checks,omitempty"`
	Steps          []RuleStep        `json:"steps"`
	Summary        []RuleSummaryLine `json:"summary,omitempty"`
}
type RuleOption struct {
	Key         string           `json:"key"`
	Type        string           `json:"type"`
	Label       string           `json:"label"`
	Description string           `json:"description,omitempty"`
	Default     any              `json:"default"`
	Min         int64            `json:"min,omitempty"`
	Max         int64            `json:"max,omitempty"`
	Choices     []RuleChoice     `json:"choices,omitempty"`
	DependsOn   []RuleDependency `json:"depends_on,omitempty"`
}
type RuleChoice struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}
type RuleDependency struct {
	Key    string `json:"key"`
	Equals any    `json:"equals"`
}
type RuleConstraint struct {
	Kind    string   `json:"kind"`
	Fields  []string `json:"fields"`
	Message string   `json:"message"`
}
type RuleCheck struct {
	Expression string `json:"expression"`
	Message    string `json:"message"`
}
type RuleStep struct {
	When   string            `json:"when,omitempty"`
	Set    map[string]string `json:"set,omitempty"`
	Reject string            `json:"reject,omitempty"`
}
type RuleSummaryLine struct {
	When string `json:"when,omitempty"`
	Text string `json:"text"`
}

type RuleDescriptor struct {
	Name           string           `json:"name"`
	Version        int              `json:"version"`
	Order          int              `json:"order"`
	Title          string           `json:"title"`
	Description    string           `json:"description,omitempty"`
	Group          string           `json:"group,omitempty"`
	Required       bool             `json:"required"`
	DefaultEnabled bool             `json:"default_enabled"`
	Options        []RuleOption     `json:"options"`
	Constraints    []RuleConstraint `json:"constraints"`
}
type RuleCatalog struct {
	Revision string           `json:"revision"`
	Modules  []RuleDescriptor `json:"modules"`
}
type RuleDescription struct {
	Name  string   `json:"name"`
	Title string   `json:"title"`
	Text  []string `json:"text"`
}

type configuredRule struct {
	definition     RuleModule
	definitionJSON json.RawMessage
	optionsJSON    json.RawMessage
	steps          []compiledRuleStep
	description    RuleDescription
}
type compiledRuleStep struct {
	when        ruleExpression
	assignments []ruleAssignment
	reject      string
}
type ruleAssignment struct {
	field      ruleEffectField
	expression ruleExpression
}

func (r *configuredRule) ID() string { return r.definition.Name }
func (r *configuredRule) Evaluate(c Context, e *Effects) error {
	env := ruleEnvironment{context: c, effects: e}
	for _, step := range r.steps {
		when, err := step.when.run(&env)
		if err != nil {
			return err
		}
		if !when.boolean {
			continue
		}
		if step.reject != "" {
			return fail(RuleViolation, "%s", step.reject)
		}
		// Evaluate an assignment object against one previous plan. Map order cannot
		// change behavior, and an evaluation error cannot publish half a step.
		var values [16]ruleValue
		for i, assignment := range step.assignments {
			value, err := assignment.expression.run(&env)
			if err != nil {
				return err
			}
			if value.kind == ruleInt && (value.number < 0 || value.number > 1000000) {
				return fail(RuleViolation, "effect integer is outside the bounded range")
			}
			values[i] = value
		}
		for i, assignment := range step.assignments {
			assignment.field.write(e, values[i])
		}
	}
	return nil
}
