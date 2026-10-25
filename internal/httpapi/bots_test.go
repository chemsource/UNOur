package httpapi_test

import (
	"strings"
	"testing"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/room"
)

func TestHTTPSoloStartsConfiguredPrivateGameAndRestrictsRecovery(t *testing.T) {
	c := setup(t)
	decode[map[string]any](t, c.request("POST", "/api/v1/solo", "", `{"bot_count":2,"difficulty":"hard"}`), 401)
	for _, body := range []string{`{"bot_count":0,"difficulty":"hard"}`, `{"bot_count":10,"difficulty":"easy"}`, `{"bot_count":1,"difficulty":"invalid"}`, `{"bot_count":1.5,"difficulty":"normal"}`, `{"bot_count":1,"difficulty":"hard","player_id":"forged"}`} {
		decode[map[string]any](t, c.request("POST", "/api/v1/solo", c.alice, body), 400)
	}
	response := c.request("POST", "/api/v1/solo", c.alice, `{"bot_count":2,"difficulty":"hard"}`)
	joined := decode[room.JoinResult](t, response, 201)
	if joined.Room.State.Phase != game.Playing || len(joined.Room.State.Players) != 3 || len(joined.Room.Bots) != 2 {
		t.Fatal("solo game was not started with selected count")
	}
	for _, p := range joined.Room.State.Players {
		if p.ID == joined.PlayerID {
			if p.Name != "Alice" || len(p.Hand) != 2 {
				t.Fatal("owner identity or own hand missing")
			}
		} else if len(p.Hand) != 0 || joined.Room.Bots[p.ID].Difficulty != bot.Hard {
			t.Fatal("AI difficulty lost or hidden hand exposed")
		}
	}
	for _, forbidden := range []string{"bot_memory", "bot_turn_at", "bot_uno", "session_digests", "deck", "beliefs"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("private AI state exposed: %s", forbidden)
		}
	}
	guest := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/"+joined.Room.ID+"/players", c.bob, `{}`), 201)
	if guest.Room.Role != "spectator" {
		t.Fatal("midgame guest not spectating")
	}
	again := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/"+joined.Room.ID+"/players", c.alice, `{}`), 201)
	if again.PlayerID != joined.PlayerID || len(again.Room.State.Players) != 3 {
		t.Fatal("recovery added a duplicate seat")
	}
	c.cookies[again.Token] = c.alice
	decode[room.View](t, c.request("GET", "/api/v1/rooms/"+joined.Room.ID, again.Token, ""), 200)
}
