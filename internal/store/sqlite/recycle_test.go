package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"unour/game"
	"unour/internal/room"
)

func TestImmediateRecyclePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recycle.db")
	store := openTest(t, path, nil, 10)
	service := serviceTest(t, store, nil)
	a, _ := started(t, service)
	if err := store.Transact(ctx, "table", func(r *room.Record) error {
		cp := r.Game.Checkpoint()
		cp.Discard = append(append([]game.Card(nil), cp.Deck[1:]...), cp.Discard...)
		cp.Deck = cp.Deck[:1]
		rules, err := game.NewRegistry().Build(r.Rules)
		if err != nil {
			return err
		}
		r.Game, err = game.Restore(cp, rules, testShuffle)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	drawn, err := service.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Draw})
	if err != nil {
		t.Fatal(err)
	}
	if v := drawn.Room.State; v.DrawPileCount != 102 || v.DiscardCount != 1 || v.TopCard.ID != 4 || !v.CanPass {
		t.Fatalf("recycled deck was not returned and saved: %+v", v)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTest(t, path, nil, 10)
	service = serviceTest(t, store, nil)
	restored, err := service.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(drawn.Room, restored) {
		t.Fatal("recycled deck or draw phase changed after restart")
	}
	if _, err := service.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Pass}); err != nil {
		t.Fatal(err)
	}
}
