package httpapi_test

import (
	"strconv"
	"testing"

	"unour/internal/room"
)

func TestHTTPRejectsDisabledMatchingModes(t *testing.T) {
	c := setup(t)
	rules := `"rules":[{"name":"matching","options":{"same_color":false,"same_value":false}},{"name":"actions"},{"name":"draw"}]`
	for _, path := range []string{"/api/v1/rooms", "/api/v1/solo"} {
		body := `{` + rules + `}`
		if path == "/api/v1/solo" {
			body = `{"bot_count":1,"difficulty":"easy",` + rules + `}`
		}
		decode[map[string]any](t, c.request("POST", path, c.alice, body), 400)
	}
}

func TestHTTPPlayOriginalHandAfterDrawOption(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			c := setup(t)
			body := `{"id":"draw-options","rules":[{"name":"matching"},{"name":"actions"},{"name":"draw","options":{"play_any_after_draw":` + strconv.FormatBool(enabled) + `}}]}`
			decode[room.View](t, c.request("POST", "/api/v1/rooms", c.alice, body), 201)
			a := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/draw-options/players", c.alice, `{}`), 201)
			b := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/draw-options/players", c.bob, `{}`), 201)
			c.cookies[a.Token] = c.alice
			decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/draw-options/start", a.Token, `{}`), 200)
			drawn := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/draw-options/draw", a.Token, `{}`), 200)
			if !drawn.Room.State.CanPass || drawn.Room.State.DrawnCardID == 1 {
				t.Fatal("draw did not preserve the original-card test case")
			}
			response := c.request("POST", "/api/v1/rooms/draw-options/play", a.Token, `{"card_id":1}`)
			if !enabled {
				decode[map[string]any](t, response, 422)
				return
			}
			played := decode[room.ActionResult](t, response, 200)
			if played.Room.State.Turn != b.PlayerID || played.Room.State.CanPass || played.Room.State.TopCard.ID != 1 {
				t.Fatal("playing original hand card after draw did not end the turn")
			}
		})
	}
}
