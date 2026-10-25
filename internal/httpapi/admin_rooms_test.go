package httpapi_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"unour/internal/room"
)

func TestAdminRoomControlsRequireIndependentSessionAndCSRF(t *testing.T) {
	c := setupAdmin(t, true)
	a, b := c.start()
	cookie, csrf := loginAdmin(t, c)
	before := decode[room.Inspection](t, adminRequest(c, "GET", "/rooms/table", cookie, "", ""), 200)
	access := fmt.Sprintf(`{"instance":%q,"join_locked":true}`, before.Instance)
	kick := fmt.Sprintf(`{"instance":%q}`, before.Instance)
	batch, _ := json.Marshal(map[string]any{"rooms": []room.DeleteTarget{{ID: "table", Instance: before.Instance, Phase: before.State.Phase}}})
	for _, req := range []struct{ method, path, body string }{
		{"PUT", "/rooms/table/access", access},
		{"POST", "/rooms/table/members/" + string(b.PlayerID) + "/kick", kick},
		{"POST", "/rooms/batch/delete", string(batch)},
	} {
		decode[map[string]any](t, adminRequest(c, req.method, req.path, c.alice, csrf, req.body), 401)
		decode[map[string]any](t, adminRequest(c, req.method, req.path, cookie, "", req.body), 403)
	}
	decode[map[string]any](t, adminRequest(c, "PUT", "/rooms/table/access", cookie, csrf, kick), 400)
	locked := decode[room.Inspection](t, adminRequest(c, "PUT", "/rooms/table/access", cookie, csrf, access), 200)
	if !locked.JoinLocked {
		t.Fatal("room was not locked")
	}
	page := decode[room.RoomPage](t, adminRequest(c, "GET", "/rooms?q=ALICE&phase=playing&kind=human&access=locked&sort=oldest", cookie, "", ""), 200)
	if page.Total != 1 || page.Stats.Playing != 1 || page.Rooms[0].Instance != before.Instance {
		t.Fatal("invalid room directory", page)
	}
	decode[map[string]any](t, adminRequest(c, "GET", "/rooms?sort=invalid", cookie, "", ""), 400)
	decode[map[string]any](t, adminRequest(c, "POST", "/rooms/batch/delete", cookie, csrf, `{"rooms":[]}`), 400)
	decode[map[string]any](t, adminRequest(c, "POST", "/rooms/batch/delete", cookie, csrf, `{"rooms":[{"id":"table"}]}`), 400)
	removed := decode[room.Inspection](t, adminRequest(c, "POST", "/rooms/table/members/"+string(b.PlayerID)+"/kick", cookie, csrf, kick), 200)
	if removed.State.Phase != "finished" {
		t.Fatal("kick did not forfeit running player")
	}
	decode[map[string]any](t, c.request("GET", "/api/v1/rooms/table", b.Token, ""), 401)
	decode[map[string]any](t, adminRequest(c, "POST", "/rooms/table/members/"+string(a.PlayerID)+"/kick", cookie, csrf, kick), 409)
	// Previous consent was for a running room. Reconfirm its finished phase.
	changed := decode[room.DeleteBatch](t, adminRequest(c, "POST", "/rooms/batch/delete", cookie, csrf, string(batch)), 200)
	if changed.Results[0].Status != "changed" {
		t.Fatal("phase drift ignored", changed)
	}
	batch, _ = json.Marshal(map[string]any{"rooms": []room.DeleteTarget{{ID: "table", Instance: before.Instance, Phase: removed.State.Phase}}})
	deleted := decode[room.DeleteBatch](t, adminRequest(c, "POST", "/rooms/batch/delete", cookie, csrf, string(batch)), 200)
	if deleted.Deleted != 1 {
		t.Fatal("deletion failed", deleted)
	}
	decode[map[string]any](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 404)
	decode[map[string]any](t, adminRequest(c, "GET", "/rooms/table", cookie, "", ""), 404)
}

func TestBatchEndpointDoesNotShadowRoomNamedDelete(t *testing.T) {
	c := setupAdmin(t, true)
	decode[room.View](t, c.request("POST", "/api/v1/rooms", c.alice, `{"id":"delete"}`), 201)
	cookie, _ := loginAdmin(t, c)
	v := decode[room.Inspection](t, adminRequest(c, "GET", "/rooms/delete", cookie, "", ""), 200)
	if v.ID != "delete" {
		t.Fatal("batch route shadows a valid room ID")
	}
}
