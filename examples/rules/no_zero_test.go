package rules_test

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"unour/game"
	"unour/internal/room"
	"unour/internal/store/memory"
	configfiles "unour/rules"
)

func TestExtensionRunsThroughTheRealRoomService(t *testing.T) {
	source := fstest.MapFS{}
	entries, err := fs.ReadDir(configfiles.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(configfiles.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		source[entry.Name()] = &fstest.MapFile{Data: data}
	}
	data, err := os.ReadFile("no_zero.json")
	if err != nil {
		t.Fatal(err)
	}
	source["no_zero.json"] = &fstest.MapFile{Data: data}
	registry, err := game.LoadRuleRegistry(source)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(memory.Config{MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err := room.New(store, room.Config{Registry: registry, GameOptions: game.Options{InitialHand: 2, Shuffle: func([]game.Card) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	specs := append(game.DefaultRuleSpecs(), game.RuleSpec{Name: "no_zero"})
	ctx := context.Background()
	if _, err := s.Create(ctx, room.CreateRequest{ID: "extension", Rules: specs}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(ctx, "extension", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, "extension", "Bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, "extension", a.Token, room.ActionRequest{Type: game.Start}); err != nil {
		t.Fatal(err)
	}
	before, err := s.State(ctx, "extension", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, "extension", a.Token, room.ActionRequest{Type: game.Play, CardID: 1}); err == nil {
		t.Fatal("custom zero-card veto was ignored")
	}
	after, err := s.State(ctx, "extension", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Version != before.State.Version || after.State.Players[0].HandCount != 2 {
		t.Fatal("custom rule veto partially committed")
	}
	if _, err := s.Execute(ctx, "extension", a.Token, room.ActionRequest{Type: game.Play, CardID: 3}); err != nil {
		t.Fatal("custom rule blocked an allowed nonzero card", err)
	}
}
