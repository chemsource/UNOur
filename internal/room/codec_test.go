package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"unour/game"
	"unour/internal/bot"
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
	c := NewCodec(nil)
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
	id, err := authenticate(context.Background(), round, strings.Repeat("A", 43))
	if err != nil || id != "a" {
		t.Fatal("session was lost", err)
	}
	if !reflect.DeepEqual(r.Game.Checkpoint(), round.Game.Checkpoint()) || !reflect.DeepEqual(r.Rules, round.Rules) {
		t.Fatal("game or rules changed")
	}
}

func TestRecordCodecRejectsBadMetadata(t *testing.T) {
	c := NewCodec(nil)
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
	raw["format_version"] = recordSnapshotFormat
	raw["rules"] = []any{map[string]any{"name": "uninstalled"}}
	bad, _ = json.Marshal(raw)
	if _, err := c.Decode(bad); err == nil {
		t.Fatal("unknown saved rule was silently removed")
	}
}

func codecSoloRecord(t *testing.T) *Record {
	t.Helper()
	r := codecRecord(t)
	if err := r.Game.Join("b", "Computer"); err != nil {
		t.Fatal(err)
	}
	r.Bots = map[game.PlayerID]bot.Profile{"b": {Difficulty: bot.Normal, Style: bot.Balanced}}
	r.Accounts = map[string]game.PlayerID{"account-a": "a"}
	before := r.Game.View("")
	cmd := game.Command{Type: game.Start, PlayerID: "a", At: r.CreatedAt}
	result, err := r.Game.Apply(cmd)
	if err != nil {
		t.Fatal(err)
	}
	r.BotMemory.Observe(before, r.Game.View(""), cmd, result.Events)
	return r
}

func TestCodecRejectsComputerSecretsAndInvalidSchedules(t *testing.T) {
	c := NewCodec(nil)
	for _, change := range []func(*Record){
		func(r *Record) { r.Bots["a"] = bot.Profile{Difficulty: bot.Normal, Style: bot.Balanced} },
		func(r *Record) { r.Bots["b"] = bot.Profile{Difficulty: "cheat", Style: bot.Balanced} },
		func(r *Record) { r.BotMemory.Discard[0] = r.Game.Checkpoint().Players[0].Hand[0] },
		func(r *Record) { r.BotTurnAt = r.CreatedAt.Add(time.Second) },
		func(r *Record) { r.BotUNO = map[game.PlayerID]BotUNO{"b": {WindowID: 99, At: r.CreatedAt}} },
		func(r *Record) { r.Accounts["account-b"] = "b" },
	} {
		r := codecSoloRecord(t)
		change(r)
		if _, err := c.Encode(r); err == nil {
			t.Fatal("invalid computer metadata persisted")
		}
	}
	data, err := c.Encode(codecSoloRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot recordSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Game.TurnIndex = 99
	bad, _ := json.Marshal(snapshot)
	if _, err := c.Decode(bad); err == nil {
		t.Fatal("invalid turn index decoded")
	}
}
