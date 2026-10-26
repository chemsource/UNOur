package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"unour/internal/room"
)

type controlledBot struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Room      room.View `json:"room"`
}

func botControlRequest(c *client, method, path, cookie, csrf, lease, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/admin"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Admin-CSRF", csrf)
	r.Header.Set("X-Bot-Control", lease)
	r.AddCookie(&http.Cookie{Name: "unour_admin", Value: cookie})
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w
}

func TestAdminBotControlUsesNormalRulesWithoutPublicChanges(t *testing.T) {
	c := setupAdmin(t, true)
	joined := decode[room.JoinResult](t, c.request("POST", "/api/v1/solo", c.alice, `{"bot_count":2,"difficulty":"normal"}`), 201)
	c.cookies[joined.Token] = c.alice
	id := joined.Room.ID
	bot := joined.Room.State.Players[1].ID
	path := "/rooms/" + id + "/bots/" + string(bot)
	cookie, csrf := loginAdmin(t, c)
	inspected := decode[room.Inspection](t, adminRequest(c, "GET", "/rooms/"+id, cookie, "", ""), 200)
	body := fmt.Sprintf(`{"instance":%q}`, inspected.Instance)
	before := c.request("GET", "/api/v1/rooms/"+id, joined.Token, "")
	decode[map[string]any](t, adminRequest(c, "POST", path+"/control", c.alice, "", body), 401)
	decode[map[string]any](t, adminRequest(c, "POST", path+"/control", cookie, "", body), 403)
	control := decode[controlledBot](t, adminRequest(c, "POST", path+"/control", cookie, csrf, body), 201)
	if len(control.LeaseID) != 43 || !control.ExpiresAt.After(c.now) || control.Room.State.You != bot || control.Room.Role != "player" {
		t.Fatal("missing private bot control session")
	}
	for _, p := range control.Room.State.Players {
		if p.ID == bot && len(p.Hand) != 2 || p.ID != bot && len(p.Hand) != 0 {
			t.Fatal("controlled view did not preserve normal hand privacy")
		}
	}
	after := c.request("GET", "/api/v1/rooms/"+id, joined.Token, "")
	if before.Body.String() != after.Body.String() {
		t.Fatal("taking control changed the player's public room state")
	}
	otherCookie, otherCSRF := loginAdmin(t, c)
	decode[map[string]any](t, adminRequest(c, "POST", path+"/control", otherCookie, otherCSRF, body), 409)
	decode[map[string]any](t, botControlRequest(c, "GET", path+"/control", otherCookie, "", control.LeaseID, ""), 409)
	decode[map[string]any](t, botControlRequest(c, "GET", path+"/control", cookie, "", "wrong", ""), 409)
	version := control.Room.State.Version
	decode[map[string]any](t, botControlRequest(c, "POST", path+"/draw", cookie, csrf, control.LeaseID, fmt.Sprintf(`{"expected_version":%d}`, version)), 409)
	// Let the human finish a normal draw so the same bot can act in turn.
	result := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/"+id+"/draw", joined.Token, `{}`), 200)
	if result.Room.State.CanPass {
		decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/"+id+"/pass", joined.Token, `{}`), 200)
	}
	current := decode[controlledBot](t, botControlRequest(c, "GET", path+"/control", cookie, "", control.LeaseID, ""), 200)
	if current.Room.State.Turn != bot {
		t.Fatal("fixture did not reach controlled bot's turn")
	}
	decode[map[string]any](t, botControlRequest(c, "POST", path+"/draw", cookie, csrf, control.LeaseID, `{}`), 400)
	command := fmt.Sprintf(`{"expected_version":%d}`, current.Room.State.Version)
	drawn := decode[room.ActionResult](t, botControlRequest(c, "POST", path+"/draw", cookie, csrf, control.LeaseID, command), 200)
	if drawn.Room.State.You != bot || !drawn.Room.State.CanPass || drawn.Room.State.Players[1].HandCount != 3 {
		t.Fatal("controlled draw did not follow ordinary game rules")
	}
	decode[map[string]any](t, botControlRequest(c, "POST", path+"/draw", cookie, csrf, control.LeaseID, command), 409)
	command = fmt.Sprintf(`{"expected_version":%d}`, drawn.Room.State.Version)
	decode[map[string]any](t, botControlRequest(c, "POST", path+"/draw", cookie, csrf, control.LeaseID, command), 409)
	before = c.request("GET", "/api/v1/rooms/"+id, joined.Token, "")
	decode[map[string]any](t, botControlRequest(c, "DELETE", path+"/control", cookie, csrf, control.LeaseID, `{}`), 200)
	after = c.request("GET", "/api/v1/rooms/"+id, joined.Token, "")
	if before.Body.String() != after.Body.String() {
		t.Fatal("releasing control changed the player's public room state")
	}
	decode[map[string]any](t, botControlRequest(c, "GET", path+"/control", cookie, "", control.LeaseID, ""), 409)
	for _, key := range []string{"lease_id", "expires_at", "bot_controls", "controller", "bot_control_epoch", "session_digests"} {
		if strings.Contains(after.Body.String(), `"`+key+`"`) {
			t.Fatal("private control metadata exposed to human player", key)
		}
	}
}

func TestAdminBotControlExpiryCannotBeRevivedByAnOldLease(t *testing.T) {
	c := setupAdmin(t, true)
	joined := decode[room.JoinResult](t, c.request("POST", "/api/v1/solo", c.alice, `{"bot_count":1,"difficulty":"easy"}`), 201)
	id := joined.Room.ID
	bot := joined.Room.State.Players[1].ID
	path := "/rooms/" + id + "/bots/" + string(bot)
	cookie, csrf := loginAdmin(t, c)
	inspected := decode[room.Inspection](t, adminRequest(c, "GET", "/rooms/"+id, cookie, "", ""), 200)
	body := fmt.Sprintf(`{"instance":%q}`, inspected.Instance)
	control := decode[controlledBot](t, adminRequest(c, "POST", path+"/control", cookie, csrf, body), 201)
	c.now = c.now.Add(20 * time.Second)
	renewed := decode[controlledBot](t, botControlRequest(c, "PUT", path+"/control", cookie, csrf, control.LeaseID, `{}`), 200)
	if !renewed.ExpiresAt.After(control.ExpiresAt) {
		t.Fatal("heartbeat did not extend control")
	}
	c.now = c.now.Add(31 * time.Second)
	decode[map[string]any](t, botControlRequest(c, "PUT", path+"/control", cookie, csrf, control.LeaseID, `{}`), 409)
	otherCookie, otherCSRF := loginAdmin(t, c)
	replacement := decode[controlledBot](t, adminRequest(c, "POST", path+"/control", otherCookie, otherCSRF, body), 201)
	if replacement.LeaseID == control.LeaseID {
		t.Fatal("expired control fencing token reused")
	}
	decode[map[string]any](t, botControlRequest(c, "DELETE", path+"/control", cookie, csrf, control.LeaseID, `{}`), 409)
	decode[controlledBot](t, botControlRequest(c, "GET", path+"/control", otherCookie, "", replacement.LeaseID, ""), 200)
	humanPath := "/rooms/" + id + "/bots/" + string(joined.PlayerID) + "/control"
	decode[map[string]any](t, adminRequest(c, "POST", humanPath, cookie, csrf, body), 409)
	decode[map[string]any](t, adminRequest(c, "POST", path+"/control", cookie, csrf, `{"instance":"00000000000000000000000000000000"}`), 409)
}

func TestBotControlHeaderIsAllowedOnlyForConfiguredOrigins(t *testing.T) {
	c := setupAdmin(t, true)
	r := httptest.NewRequest("OPTIONS", "/api/v1/admin/rooms/table/bots/bot/control", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	r.Header.Set("Access-Control-Request-Headers", "content-type, x-admin-csrf, x-bot-control")
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if w.Code != 204 || !strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), "x-bot-control") {
		t.Fatal("configured frontend could not renew a private bot lease", w.Code)
	}
	r.Header.Set("Origin", "https://untrusted.example")
	w = httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted origin admitted", w.Code)
	}
}
