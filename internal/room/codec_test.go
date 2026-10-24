package room

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"unour/game"
)

func codecRecord(t *testing.T) *Record {
	t.Helper()
	g, err := game.New(game.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Join("a", "Alice"); err != nil {
		t.Fatal(err)
	}
	return &Record{Game: g, HostID: "a", Sessions: map[[32]byte]game.PlayerID{sha256.Sum256([]byte(strings.Repeat("A", 43))): "a"}, Rules: game.DefaultRuleSpecs(), CreatedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
}

func TestRecordCodecRestoresSessionsAndRules(t *testing.T) {
	c := NewCodec(game.NewRegistry(), nil)
	r := codecRecord(t)
	data, err := c.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), strings.Repeat("A", 43)) {
		t.Fatal("raw token persisted")
	}
	round, err := c.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	id, err := authenticate(round, strings.Repeat("A", 43))
	if err != nil || id != "a" {
		t.Fatal("session was lost", err)
	}
	if !reflect.DeepEqual(r.Game.Checkpoint(), round.Game.Checkpoint()) || !reflect.DeepEqual(r.Rules, round.Rules) {
		t.Fatal("game or rules changed")
	}
}

func TestRecordCodecRejectsBadMetadata(t *testing.T) {
	c := NewCodec(game.NewRegistry(), nil)
	for _, change := range []func(*Record){
		func(r *Record) { r.HostID = "other" },
		func(r *Record) { r.Sessions = nil },
		func(r *Record) { r.CreatedAt = time.Time{} },
		func(r *Record) { r.Game = nil },
		func(r *Record) { r.Rules = []game.RuleSpec{{Name: "uninstalled"}} },
		func(r *Record) { r.Rules = []game.RuleSpec{{Name: "draw"}} },
	} {
		r := codecRecord(t)
		change(r)
		if _, err := c.Encode(r); err == nil {
			t.Fatal("invalid record metadata persisted")
		}
	}
	data, err := c.Encode(codecRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["format_version"] = 999
	bad, _ := json.Marshal(raw)
	if _, err := c.Decode(bad); err == nil {
		t.Fatal("future format accepted")
	}
	if _, err := c.Decode(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing data accepted")
	}
	raw["format_version"] = 1
	raw["rules"] = []any{map[string]any{"name": "uninstalled"}}
	bad, _ = json.Marshal(raw)
	if _, err := c.Decode(bad); err == nil {
		t.Fatal("unknown saved rule was silently removed")
	}
}
