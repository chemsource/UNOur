package httpapi_test

import (
	"encoding/json"
	"testing"

	"unour/internal/room"
)

func TestHTTPUNODeclarationReachesOtherPlayerPollingState(t *testing.T) {
	c := setup(t)
	a, b := c.start()
	opened := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/play", a.Token, `{"card_id":1}`), 200)
	if opened.Room.State.Players[0].UNO == nil {
		t.Fatal("UNO window not opened")
	}
	window := opened.Room.State.Players[0].UNO.ID
	body, _ := json.Marshal(map[string]any{"window_id": window})
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/uno", a.Token, string(body)), 200)
	// Bob did not poll during Alice's open UNO window.
	view := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", b.Token, ""), 200)
	if got := view.State.Players[0]; got.ID != a.PlayerID || got.UNODeclared != window || len(got.Hand) != 0 {
		t.Fatalf("other player's poll lost UNO or leaked hand: %+v", got)
	}
}
