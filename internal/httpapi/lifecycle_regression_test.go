package httpapi_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"unour/game"
	"unour/internal/account"
	"unour/internal/room"
)

func registerRoomMember(t *testing.T, c *client, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": name, "password": "valid-password"})
	response := c.request("POST", "/api/v1/auth/register", "", string(body))
	decode[account.Session](t, response, 201)
	return response.Result().Cookies()[0].Value
}

func TestHTTPMoreThanTenMembersCanReadAndPlay(t *testing.T) {
	c := setup(t)
	a, _ := c.start()
	var observers []room.JoinResult
	for i := 0; i < 9; i++ {
		cookie := registerRoomMember(t, c, fmt.Sprintf("viewer%d", i))
		member := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/table/spectators", cookie, `{}`), 201)
		c.cookies[member.Token] = cookie
		observers = append(observers, member)
	}
	type publicRoom struct {
		room.View
		Profiles map[game.PlayerID]account.Profile `json:"profiles"`
	}
	host := decode[publicRoom](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 200)
	if len(host.Profiles) != 11 || len(host.Spectators) != 9 {
		t.Fatal("member profiles are missing")
	}
	for _, viewer := range observers {
		view := decode[publicRoom](t, c.request("GET", "/api/v1/rooms/table", viewer.Token, ""), 200)
		if view.Role != "spectator" || len(view.Profiles) != 11 {
			t.Fatal("spectator cannot read room")
		}
		for _, p := range view.State.Players {
			if len(p.Hand) > 0 {
				t.Fatal("spectator hand leak")
			}
		}
	}
	// A room must still acknowledge gameplay after its member count grows.
	decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/draw", a.Token, `{}`), 200)
}

func TestHTTPLobbyHostTransferAndSpectatorSeatingCanStart(t *testing.T) {
	for _, seatHostFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("host_seated_first_%t", seatHostFirst), func(t *testing.T) {
			c := setup(t)
			decode[room.View](t, c.request("POST", "/api/v1/rooms", c.alice, `{"id":"table"}`), 201)
			a := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/table/players", c.alice, `{}`), 201)
			c.cookies[a.Token] = c.alice
			b := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/table/spectators", c.bob, `{}`), 201)
			c.cookies[b.Token] = c.bob
			thirdCookie := registerRoomMember(t, c, "Carol")
			d := decode[room.JoinResult](t, c.request("POST", "/api/v1/rooms/table/spectators", thirdCookie, `{}`), 201)
			c.cookies[d.Token] = thirdCookie
			decode[map[string]bool](t, c.request("POST", "/api/v1/rooms/table/leave", a.Token, `{}`), 200)
			first, second := d, b
			if seatHostFirst {
				first, second = b, d
			}
			for _, member := range []room.JoinResult{first, second} {
				decode[room.View](t, c.request("POST", "/api/v1/rooms/table/participation", member.Token, `{"next_round":true}`), 200)
			}
			// The same lobby rule applies regardless of which spectator held ownership.
			view := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", first.Token, ""), 200)
			host, other := first, second
			if view.HostID == second.PlayerID {
				host, other = second, first
			} else if view.HostID != first.PlayerID {
				t.Fatal("lobby host is not a seated player")
			}
			decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/start", other.Token, `{}`), 403)
			result := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/start", host.Token, `{}`), 200)
			if result.Room.State.Phase != game.Playing || len(result.Room.State.Players) != 2 {
				t.Fatal("lobby could not start")
			}
		})
	}
}
