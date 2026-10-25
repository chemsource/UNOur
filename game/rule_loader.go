package game

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"

	configfiles "unour/rules"
)

type ruleCatalogSnapshot struct {
	modules  map[string]RuleModule
	ordered  []RuleModule
	revision string
}

var packagedRules struct {
	once    sync.Once
	catalog *ruleCatalogSnapshot
}

func bundledRuleCatalog() *ruleCatalogSnapshot {
	holder := &packagedRules
	holder.once.Do(func() {
		var source fs.FS = configfiles.Files
		catalog, err := readRuleCatalog(source)
		if err != nil {
			panic("invalid packaged rule configuration: " + err.Error())
		}
		holder.catalog = catalog
	})
	return holder.catalog
}
func LoadRuleRegistry(source fs.FS) (*Registry, error) {
	catalog, err := readRuleCatalog(source)
	if err != nil {
		return nil, err
	}
	return &Registry{factories: map[string]RuleFactory{}, catalog: catalog}, nil
}

// Reload publishes the whole validated directory at once. Existing compiled
// rules and persisted module bundles are independent of this pointer swap.
func (r *Registry) Reload(source fs.FS) error {
	candidate, err := readRuleCatalog(source)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.factories {
		if _, exists := candidate.modules[name]; exists {
			return fmt.Errorf("module %q collides with a registered extension", name)
		}
	}
	r.catalog = candidate
	return nil
}
func readRuleCatalog(source fs.FS) (*ruleCatalogSnapshot, error) {
	if source == nil {
		return nil, fmt.Errorf("rule source is required")
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, err
	}
	catalog := &ruleCatalogSnapshot{modules: map[string]RuleModule{}}
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("rule file %s must be regular", entry.Name())
		}
		file, err := source.Open(entry.Name())
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		total += len(data)
		if len(data) > 64<<10 || total > 256<<10 || len(catalog.ordered) >= 32 {
			return nil, fmt.Errorf("rule directory exceeds 32 modules, 64 KiB per file or 256 KiB total")
		}
		var module RuleModule
		if err := strictRuleJSON(data, &module); err != nil {
			return nil, fmt.Errorf("rule file %s: %w", entry.Name(), err)
		}
		if _, exists := catalog.modules[module.Name]; exists {
			return nil, fmt.Errorf("duplicate rule module %q", module.Name)
		}
		if _, err := cachedRuleModule(module, nil); err != nil {
			return nil, fmt.Errorf("rule file %s: %w", entry.Name(), err)
		}
		catalog.modules[module.Name] = module
		catalog.ordered = append(catalog.ordered, module)
	}
	if len(catalog.ordered) == 0 {
		return nil, fmt.Errorf("rule directory contains no JSON modules")
	}
	sort.Slice(catalog.ordered, func(i, j int) bool {
		a, b := catalog.ordered[i], catalog.ordered[j]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Name < b.Name
	})
	data, err := json.Marshal(catalog.ordered)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	catalog.revision = hex.EncodeToString(digest[:])
	return catalog, nil
}
func (r *Registry) Catalog() RuleCatalog {
	r.mu.RLock()
	snapshot := r.catalog
	r.mu.RUnlock()
	result := RuleCatalog{Revision: snapshot.revision, Modules: make([]RuleDescriptor, 0, len(snapshot.ordered))}
	for _, module := range snapshot.ordered {
		descriptor := RuleDescriptor{Name: module.Name, Version: module.Version, Order: module.Order, Title: module.Title, Description: module.Description, Group: module.Group, Required: module.Required, DefaultEnabled: module.DefaultEnabled, Options: module.Options, Constraints: module.Constraints}
		if descriptor.Options == nil {
			descriptor.Options = []RuleOption{}
		}
		if descriptor.Constraints == nil {
			descriptor.Constraints = []RuleConstraint{}
		}
		result.Modules = append(result.Modules, descriptor)
	}
	// Detach all nested slices/defaults before returning the public catalog.
	data, _ := json.Marshal(result)
	var detached RuleCatalog
	_ = json.Unmarshal(data, &detached)
	return detached
}
func defaultSpecs(snapshot *ruleCatalogSnapshot) []RuleSpec {
	result := []RuleSpec{}
	for _, module := range snapshot.ordered {
		if module.DefaultEnabled {
			result = append(result, RuleSpec{Name: module.Name})
		}
	}
	return result
}
func BuildFrozenRules(modules []RuleModule, specs []RuleSpec) (RuleSet, error) {
	if len(modules) != len(specs) || len(modules) == 0 || len(modules) > 32 {
		return RuleSet{}, fail(InvalidInput, "invalid frozen module bundle")
	}
	definitions := make(map[string]RuleModule, len(modules))
	total := 0
	for _, module := range modules {
		if _, exists := definitions[module.Name]; exists {
			return RuleSet{}, fail(InvalidInput, "duplicate frozen module")
		}
		data, err := json.Marshal(module)
		if err != nil {
			return RuleSet{}, err
		}
		total += len(data)
		if len(data) > 64<<10 || total > 256<<10 {
			return RuleSet{}, fail(InvalidInput, "frozen rule bundle exceeds limits")
		}
		definitions[module.Name] = module
	}
	return buildModuleRules(definitions, nil, specs)
}
