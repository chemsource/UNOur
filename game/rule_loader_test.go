package game

import (
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
)

func testModuleFS(t *testing.T, m RuleModule) fstest.MapFS {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"custom.json": {Data: data}}
}
func TestModuleDirectoryIsStrictAndReloadIsAtomic(t *testing.T) {
	m := configurableTestModule()
	m.DefaultEnabled = true
	source := testModuleFS(t, m)
	registry, err := LoadRuleRegistry(source)
	if err != nil {
		t.Fatal(err)
	}
	initial := registry.Catalog()
	if len(initial.Modules) != 1 || initial.Modules[0].Name != m.Name || initial.Revision == "" {
		t.Fatal("missing dynamic catalog", initial)
	}
	source["invalid.json"] = &fstest.MapFile{Data: []byte(`{"name":"bad","unknown":true}`)}
	if err := registry.Reload(source); err == nil {
		t.Fatal("invalid directory accepted")
	}
	if registry.Catalog().Revision != initial.Revision {
		t.Fatal("failed reload replaced live catalog")
	}
	delete(source, "invalid.json")
	m.Title = "修改后的模块"
	updated := testModuleFS(t, m)
	if err := registry.Reload(updated); err != nil {
		t.Fatal(err)
	}
	if registry.Catalog().Revision == initial.Revision {
		t.Fatal("updated catalog reused identity")
	}
	if _, _, err := registry.Resolve(nil, initial.Revision); err == nil {
		t.Fatal("stale catalog was accepted")
	}
	if _, _, err := registry.Resolve(nil, registry.Catalog().Revision); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []fstest.MapFS{
		{},
		{"one.json": source["custom.json"], "two.json": source["custom.json"]},
		{"linked.json": {Data: []byte("other.json"), Mode: fs.ModeSymlink}},
	} {
		if _, err := LoadRuleRegistry(bad); err == nil {
			t.Fatal("invalid module source accepted")
		}
	}
}

func TestModuleDirectoryRejectsAmbiguousJSONAndCyclicDependencies(t *testing.T) {
	for _, source := range []fstest.MapFS{
		{"rule.json": {Data: []byte(`{"name":"duplicate","version":1,"title":"A","title":"B","steps":[]}`)}},
		{"rule.json": {Data: []byte(`{"name":"cycle","version":1,"title":"循环","options":[{"key":"a","label":"A","type":"boolean","default":false,"depends_on":[{"key":"b","equals":true}]},{"key":"b","label":"B","type":"boolean","default":false,"depends_on":[{"key":"a","equals":true}]}],"steps":[]}`)}},
		{"rule.json": {Data: []byte(`{"name":"large","version":1,"title":"大整数","options":[{"key":"value","label":"数值","type":"select","default":9223372036854775807,"choices":[{"value":9223372036854775807,"label":"溢出前端精度"}]}],"steps":[]}`)}},
	} {
		if _, err := LoadRuleRegistry(source); err == nil {
			t.Fatal("ambiguous or unsafe configuration accepted")
		}
	}
}

func TestCatalogAndFrozenDefinitionsAreDetached(t *testing.T) {
	registry := NewRegistry()
	catalog := registry.Catalog()
	original := catalog.Modules[0].Title
	catalog.Modules[0].Title = "改写"
	catalog.Modules[0].Options[0].Default = false
	if registry.Catalog().Modules[0].Title != original {
		t.Fatal("catalog returned a writable internal definition")
	}
	g := fixture(t)
	modules := g.RuleModules()
	modules[0].Steps[1].Set["allow"] = "false"
	modules[0].Options[0].Default = false
	if err := g.CanApply(Command{Type: Play, PlayerID: "a", CardID: 1}); err != nil {
		t.Fatal("checkpoint definitions mutated a live policy", err)
	}
}
