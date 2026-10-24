package game

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type RuleSpec struct {
	Name    string          `json:"name"`
	Options json.RawMessage `json:"options,omitempty"`
}

type RuleFactory func(json.RawMessage) (Rule, error)

// Registry is local to the composition root, and is safe to share across rooms.
// Factories must return immutable rules and be safe for concurrent calls.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]RuleFactory
}

func NewRegistry() *Registry {
	r := &Registry{factories: make(map[string]RuleFactory)}
	r.factories["matching"] = factory(func() *MatchingRule { return &MatchingRule{SameColor: true, SameValue: true} })
	r.factories["actions"] = factory(func() *ActionRule { return &ActionRule{} })
	r.factories["stacking"] = factory(func() *StackingRule { return &StackingRule{} })
	r.factories["draw"] = factory(func() *DrawRule { return &DrawRule{PlayDrawnCard: true} })
	r.factories["challenge"] = factory(func() *ChallengeRule { return &ChallengeRule{FailureMultiplier: 150} })
	r.factories["hand_limit"] = factory(func() *HandLimitRule { return &HandLimitRule{Limit: 25} })
	return r
}

func factory[T Rule](makeRule func() T) RuleFactory {
	return func(raw json.RawMessage) (Rule, error) {
		rule := makeRule()
		if len(raw) > 0 {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, fmt.Errorf("options must be an object")
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if err := d.Decode(rule); err != nil {
				return nil, err
			}
			if err := d.Decode(new(any)); err != io.EOF {
				return nil, fmt.Errorf("options must contain one JSON object")
			}
		}
		return rule, nil
	}
}

func (r *Registry) Register(name string, makeRule RuleFactory) error {
	if name == "" || makeRule == nil {
		return fail(InvalidInput, "rule name and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.factories[name]; ok {
		return fail(InvalidInput, "rule %q already registered", name)
	}
	r.factories[name] = makeRule
	return nil
}

func (r *Registry) Build(specs []RuleSpec) (RuleSet, error) {
	if len(specs) == 0 || len(specs) > 32 {
		return RuleSet{}, fail(InvalidInput, "a room must have 1–32 rules")
	}
	rules := make([]Rule, 0, len(specs))
	for _, spec := range specs {
		r.mu.RLock()
		makeRule, ok := r.factories[spec.Name]
		r.mu.RUnlock()
		if !ok {
			return RuleSet{}, fail(InvalidInput, "unknown rule %q", spec.Name)
		}
		rule, err := makeRule(append(json.RawMessage(nil), spec.Options...))
		if err != nil {
			return RuleSet{}, fail(InvalidInput, "rule %s options: %v", spec.Name, err)
		}
		rules = append(rules, rule)
	}
	return NewRuleSet(rules...)
}

func DefaultRuleSpecs() []RuleSpec {
	return []RuleSpec{{Name: "matching"}, {Name: "actions"}, {Name: "draw"}, {Name: "challenge"}}
}
