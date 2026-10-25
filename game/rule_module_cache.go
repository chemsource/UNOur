package game

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"sync"
)

type ruleCacheEntry struct {
	key    [32]byte
	rule   *configuredRule
	weight int
}

var compiledRuleCache = struct {
	sync.Mutex
	entries map[[32]byte]*list.Element
	order   *list.List
	bytes   int
}{entries: map[[32]byte]*list.Element{}, order: list.New()}

// Bound both entry count and estimated retained closure/definition memory. A
// frozen room does not require recompilation on each read or bot simulation.
func cachedRuleModule(module RuleModule, options json.RawMessage) (*configuredRule, error) {
	data, err := json.Marshal(module)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	digest.Write(data)
	digest.Write([]byte{0})
	digest.Write(options)
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	compiledRuleCache.Lock()
	if element := compiledRuleCache.entries[key]; element != nil {
		compiledRuleCache.order.MoveToFront(element)
		rule := element.Value.(ruleCacheEntry).rule
		compiledRuleCache.Unlock()
		return rule, nil
	}
	compiledRuleCache.Unlock()
	rule, err := compileRuleModule(module, options)
	if err != nil {
		return nil, err
	}
	weight := len(data)*24 + len(options)*4 + 1024
	compiledRuleCache.Lock()
	defer compiledRuleCache.Unlock()
	if element := compiledRuleCache.entries[key]; element != nil {
		compiledRuleCache.order.MoveToFront(element)
		return element.Value.(ruleCacheEntry).rule, nil
	}
	if weight > 32<<20 {
		return rule, nil
	}
	for len(compiledRuleCache.entries) >= 2048 || compiledRuleCache.bytes+weight > 32<<20 {
		last := compiledRuleCache.order.Back()
		if last == nil {
			break
		}
		old := last.Value.(ruleCacheEntry)
		delete(compiledRuleCache.entries, old.key)
		compiledRuleCache.bytes -= old.weight
		compiledRuleCache.order.Remove(last)
	}
	compiledRuleCache.entries[key] = compiledRuleCache.order.PushFront(ruleCacheEntry{key: key, rule: rule, weight: weight})
	compiledRuleCache.bytes += weight
	return rule, nil
}
