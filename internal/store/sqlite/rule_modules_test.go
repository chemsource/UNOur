package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"unour/game"
	"unour/internal/room"
)

func TestRoomRulesSurviveRestartCatalogRemovalAndRematch(t *testing.T) {
	source := func(name, title string) fstest.MapFS {
		return fstest.MapFS{"custom.json": {Data: []byte(`{"name":"` + name + `","version":1,"order":10,"title":"` + title + `","default_enabled":true,"steps":[{"set":{"allow":"true"}}],"summary":[{"text":"` + title + `"}]}`)}}
	}
	registry, err := game.LoadRuleRegistry(source("original", "原始玩法"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "frozen.db")
	open := func() *Store {
		store, err := Open(Config{Path: path, MaxRooms: 10, Codec: room.NewCodec(testShuffle)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	service := func() *room.Service {
		service, err := room.New(store, room.Config{Registry: registry, GameOptions: game.Options{InitialHand: 2, Shuffle: testShuffle}})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	svc := service()
	ctx := context.Background()
	a, b := started(t, svc)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(source("replacement", "新玩法")); err != nil {
		t.Fatal(err)
	}
	store = open()
	svc = service()
	restored, err := svc.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RuleDescriptions[0].Title != "原始玩法" {
		t.Fatal("restart used the installed catalog")
	}
	if _, err := svc.SpectateAccount(ctx, "table", "viewer-account", "Viewer"); err != nil {
		t.Fatal("viewer requires a removed module", err)
	}
	for _, step := range []struct {
		token string
		id    int
		color game.Color
	}{{a.Token, 1, ""}, {b.Token, 26, ""}, {a.Token, 102, game.Green}} {
		if _, err := svc.Execute(ctx, "table", step.token, room.ActionRequest{Type: game.Play, CardID: step.id, Color: step.color}); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := svc.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State.Phase != game.Finished {
		t.Fatal("fixture failed to finish")
	}
	request := room.RematchRequest{Round: finished.Round, RosterVersion: finished.RosterVersion, Agree: true}
	if _, err := svc.VoteRematch(ctx, "table", a.Token, request); err != nil {
		t.Fatal(err)
	}
	rematch, err := svc.VoteRematch(ctx, "table", b.Token, request)
	if err != nil {
		t.Fatal(err)
	}
	if rematch.Round != 2 || rematch.RuleDescriptions[0].Title != "原始玩法" {
		t.Fatal("rematch changed frozen rules")
	}
	created, err := svc.Create(ctx, room.CreateRequest{ID: "new-table"})
	if err != nil {
		t.Fatal(err)
	}
	if created.RuleDescriptions[0].Title != "新玩法" {
		t.Fatal("new room did not use new catalog")
	}
}
