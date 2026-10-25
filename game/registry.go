package game

import (
	"encoding/json"
	"sort"
	"sync"
)

type RuleSpec struct {
	Name    string          `json:"name"`
	Version int             `json:"version,omitempty"`
	Options json.RawMessage `json:"options,omitempty"`
}
type RuleFactory func(json.RawMessage) (Rule, error)

// Registry owns an immutable catalog snapshot. Factories are retained for
// embedded Go integrations; the server's standard policies all come from JSON.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]RuleFactory
	catalog   *ruleCatalogSnapshot
}

func NewRegistry() *Registry {
	return &Registry{factories: map[string]RuleFactory{}, catalog: bundledRuleCatalog()}
}
func (r *Registry) Register(name string, makeRule RuleFactory) error {
	if !ruleIdentifier.MatchString(name) || makeRule == nil {
		return fail(InvalidInput, "rule name and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, module := r.catalog.modules[name]
	if _, exists := r.factories[name]; exists || module {
		return fail(InvalidInput, "rule %q already registered", name)
	}
	r.factories[name] = makeRule
	return nil
}
func (r *Registry) snapshot() (*ruleCatalogSnapshot, map[string]RuleFactory) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factories := make(map[string]RuleFactory, len(r.factories))
	for k, v := range r.factories {
		factories[k] = v
	}
	return r.catalog, factories
}
func (r *Registry) Build(specs []RuleSpec) (RuleSet, error) {
	snapshot, factories := r.snapshot()
	return buildModuleRules(snapshot.modules, factories, specs)
}
func buildModuleRules(modules map[string]RuleModule, factories map[string]RuleFactory, specs []RuleSpec) (RuleSet, error) {
	if len(specs) < 1 || len(specs) > 32 {
		return RuleSet{}, fail(InvalidInput, "a room must have 1–32 rules")
	}
	rules := make([]Rule, 0, len(specs))
	for _, spec := range specs {
		var rule Rule
		var err error
		if module, exists := modules[spec.Name]; exists {
			if spec.Version != 0 && spec.Version != module.Version {
				return RuleSet{}, fail(InvalidInput, "rule %s version is unavailable", spec.Name)
			}
			rule, err = cachedRuleModule(module, spec.Options)
		} else if factory := factories[spec.Name]; factory != nil {
			rule, err = factory(append(json.RawMessage(nil), spec.Options...))
		} else {
			return RuleSet{}, fail(InvalidInput, "unknown rule %q", spec.Name)
		}
		if err != nil {
			return RuleSet{}, fail(InvalidInput, "rule %s options: %v", spec.Name, err)
		}
		rules = append(rules, rule)
	}
	return NewRuleSet(rules...)
}
func (r *Registry) Resolve(specs []RuleSpec, revision string) (RuleSet, []RuleSpec, error) {
	snapshot, factories := r.snapshot()
	if revision != "" && revision != snapshot.revision {
		return RuleSet{}, nil, fail(RuleCatalogChanged, "规则配置已更新，请刷新后重新确认。")
	}
	if specs == nil {
		specs = defaultSpecs(snapshot)
	}
	copied := append([]RuleSpec(nil), specs...)
	selected := map[string]bool{}
	for _, spec := range copied {
		selected[spec.Name] = true
	}
	for _, module := range snapshot.ordered {
		if module.Required && !selected[module.Name] {
			return RuleSet{}, nil, fail(InvalidInput, "required rule %s is missing", module.Name)
		}
	}
	sort.SliceStable(copied, func(i, j int) bool {
		a, aok := snapshot.modules[copied[i].Name]
		b, bok := snapshot.modules[copied[j].Name]
		if aok && bok {
			return a.Order < b.Order || a.Order == b.Order && a.Name < b.Name
		}
		return aok && !bok
	})
	rules, err := buildModuleRules(snapshot.modules, factories, copied)
	if err != nil {
		return RuleSet{}, nil, err
	}
	for i, rule := range rules.rules {
		if configured, ok := rule.(*configuredRule); ok {
			copied[i].Version = configured.definition.Version
			copied[i].Options = append(json.RawMessage(nil), configured.optionsJSON...)
		}
	}
	return rules, copied, nil
}
func DefaultRuleSpecs() []RuleSpec { return defaultSpecs(bundledRuleCatalog()) }
