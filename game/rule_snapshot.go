package game

import "encoding/json"

// Modules returns detached definitions for a completely configuration-based
// rule set. Embedded Go policies require their own persistence adapter.
func (r RuleSet) Modules() []RuleModule {
	modules := make([]RuleModule, 0, len(r.rules))
	for _, rule := range r.rules {
		configured, ok := rule.(*configuredRule)
		if !ok {
			return nil
		}
		var module RuleModule
		_ = json.Unmarshal(configured.definitionJSON, &module)
		modules = append(modules, module)
	}
	return modules
}
func (r RuleSet) Descriptions() []RuleDescription {
	result := make([]RuleDescription, 0, len(r.rules))
	for _, rule := range r.rules {
		if configured, ok := rule.(*configuredRule); ok {
			description := configured.description
			description.Text = append([]string{}, description.Text...)
			result = append(result, description)
		}
	}
	return result
}
func (g *Game) RuleModules() []RuleModule           { return g.rules.Modules() }
func (g *Game) RuleDescriptions() []RuleDescription { return g.rules.Descriptions() }
