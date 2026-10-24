package room_test

import (
	"context"
	"errors"
	"testing"

	"unour/internal/room"
)

func TestAccountOwnsOneSeatAndRotatesRoomSession(t *testing.T) {
	s := service(t, 10, nil)
	ctx := context.Background()
	if _, err := s.Create(ctx, room.CreateRequest{ID: "account-room"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.JoinAccount(ctx, "account-room", "account-a", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.JoinAccount(ctx, "account-room", "account-a", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if again.PlayerID != a.PlayerID || len(again.Room.State.Players) != 1 || again.Token == a.Token {
		t.Fatal("rejoining must recover same seat and rotate token")
	}
	if _, err := s.State(room.WithAccount(ctx, "account-a"), "account-room", a.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatal("old room token was not revoked")
	}
	if _, err := s.State(room.WithAccount(ctx, "account-b"), "account-room", again.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatal("another account used room token")
	}
	view, err := s.State(room.WithAccount(ctx, "account-a"), "account-room", again.Token)
	if err != nil {
		t.Fatal(err)
	}
	if view.Accounts[again.PlayerID] != "account-a" {
		t.Fatal("profile ownership not included in detached internal view")
	}
}
