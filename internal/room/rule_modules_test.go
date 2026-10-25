package room

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"unour/game"
)

func moduleSource(title string) fstest.MapFS {
	return fstest.MapFS{"custom.json": {Data: []byte(`{"name":"custom","version":1,"order":10,"title":"` + title + `","default_enabled":true,"steps":[{"set":{"allow":"true"}}],"summary":[{"text":"` + title + `"}]}`)}}
}

func TestCodecFreezesModulesAcrossCatalogReplacement(t *testing.T) {
	registry, err := game.LoadRuleRegistry(moduleSource("原始规则"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := registry.Build([]game.RuleSpec{{Name: "custom"}})
	if err != nil {
		t.Fatal(err)
	}
	r := codecRecord(t)
	r.Game, err = game.New(game.Options{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Game.Join("a", "Alice"); err != nil {
		t.Fatal(err)
	}
	r.Rules = []game.RuleSpec{{Name: "custom", Version: 1}}
	c := NewCodec(nil)
	data, err := c.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Game struct {
			Modules []json.RawMessage `json:"rule_modules"`
		} `json:"game"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Game.Modules) != 1 {
		t.Fatal("checkpoint omitted frozen rule modules")
	}
	source := moduleSource("替换后的规则")
	source["custom.json"].Data = []byte(`{"name":"replacement","version":2,"order":10,"title":"替换后的规则","default_enabled":true,"steps":[{"set":{"allow":"false"}}]}`)
	if err := registry.Reload(source); err != nil {
		t.Fatal(err)
	}
	restored, err := c.Decode(data)
	if err != nil {
		t.Fatalf("old room depends on current catalog: %v", err)
	}
	if _, err := c.Encode(restored); err != nil {
		t.Fatal(err)
	}
}
