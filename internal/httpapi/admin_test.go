package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"unour/internal/account"
	"unour/internal/admin"
	"unour/internal/room"
)

func adminRequest(c *client, method, path, cookie, csrf, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/admin"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Admin-CSRF", csrf)
	r.AddCookie(&http.Cookie{Name: "unour_admin", Value: cookie})
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w
}
func loginAdmin(t *testing.T, c *client) (string, string) {
	t.Helper()
	w := adminRequest(c, "POST", "/login", "", "", `{"password":"admin password for tests"}`)
	session := decode[admin.Session](t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/api/v1/admin" {
		t.Fatal("unsafe admin cookie")
	}
	if strings.Contains(w.Body.String(), cookies[0].Value) {
		t.Fatal("admin bearer leaked in response")
	}
	return cookies[0].Value, session.CSRF
}
func TestAdminIsDisabledByDefaultAndPlayerCannotEscalate(t *testing.T) {
	c := setup(t)
	decode[map[string]any](t, adminRequest(c, "POST", "/login", "", "", `{"password":"anything"}`), 404)
	c = setupAdmin(t, true)
	decode[map[string]any](t, adminRequest(c, "GET", "/rooms", c.alice, "", ""), 401)
	decode[map[string]any](t, c.request("GET", "/api/v1/admin/users", c.alice, ""), 401)
	cookie, csrf := loginAdmin(t, c)
	decode[map[string]any](t, adminRequest(c, "POST", "/logout", cookie, "", `{}`), 403)
	decode[admin.Session](t, adminRequest(c, "GET", "/session", cookie, "", ""), 200)
	// Correct CSRF still cannot authorize a hostile Origin.
	r := httptest.NewRequest("POST", "/api/v1/admin/logout", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://attacker.example")
	r.Header.Set("X-Admin-CSRF", csrf)
	r.AddCookie(&http.Cookie{Name: "unour_admin", Value: cookie})
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	decode[map[string]any](t, w, 403)
	decode[map[string]any](t, adminRequest(c, "POST", "/logout", cookie, csrf, `{}`), 200)
	decode[map[string]any](t, adminRequest(c, "GET", "/session", cookie, "", ""), 401)
}
func TestAdminSeesAllHandsAndEditsWithoutLeakingToPlayers(t *testing.T) {
	c := setupAdmin(t, true)
	a, _ := c.start()
	cookie, csrf := loginAdmin(t, c)
	before := decode[room.Inspection](t, adminRequest(c, "GET", "/rooms/table", cookie, "", ""), 200)
	if len(before.State.Players[0].Hand) != 2 || len(before.State.Players[1].Hand) != 2 {
		t.Fatal("missing private hands")
	}
	body := fmt.Sprintf(`{"operation":"swap","card_id":%d,"replacement_id":%d,"expected_version":%d,"expected_revision":%d}`, before.State.Players[0].Hand[0].ID, before.State.Players[1].Hand[0].ID, before.State.Version, before.Revision)
	decode[map[string]any](t, adminRequest(c, "POST", "/rooms/table/cards", cookie, "", body), 403)
	after := decode[room.Inspection](t, adminRequest(c, "POST", "/rooms/table/cards", cookie, csrf, body), 200)
	if after.State.Players[0].Hand[0].ID != 2 {
		t.Fatal("swap not applied")
	}
	decode[map[string]any](t, adminRequest(c, "POST", "/rooms/table/cards", cookie, csrf, body), 409)
	player := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 200)
	if len(player.State.Players[1].Hand) > 0 {
		t.Fatal("private admin cards leaked")
	}
	list := adminRequest(c, "GET", "/rooms", cookie, "", "")
	decode[room.RoomPage](t, list, 200)
	for _, secret := range []string{"password_hash", "session_digests", `"hand":`, `"deck":`, `"had_color":`} {
		if strings.Contains(list.Body.String(), secret) {
			t.Fatal("sensitive room list")
		}
	}
}
func TestAdminManagesUsersAndRejectsInvalidRequests(t *testing.T) {
	c := setupAdmin(t, true)
	cookie, csrf := loginAdmin(t, c)
	page := decode[account.UserPage](t, adminRequest(c, "GET", "/users?q=alice", cookie, "", ""), 200)
	if page.Total != 1 {
		t.Fatal("search failed")
	}
	id := page.Users[0].ID
	decode[map[string]any](t, adminRequest(c, "PUT", "/users/"+id+"/status", cookie, csrf, `{}`), 400)
	decode[map[string]any](t, adminRequest(c, "PUT", "/users/"+id+"/status", cookie, csrf, `{"disabled":true}`), 200)
	decode[map[string]any](t, c.request("GET", "/api/v1/account", c.alice, ""), 401)
	decode[map[string]any](t, adminRequest(c, "PUT", "/users/"+id+"/password", cookie, csrf, `{"password":"short"}`), 400)
	decode[map[string]any](t, adminRequest(c, "GET", "/users?limit=-1", cookie, "", ""), 400)
	audit := adminRequest(c, "GET", "/audit", cookie, "", "")
	decode[json.RawMessage](t, audit, 200)
	if strings.Contains(audit.Body.String(), "password_hash") {
		t.Fatal("audit leaked credentials")
	}
}
func TestAdminPasswordAttemptsAreRateLimited(t *testing.T) {
	c := setupAdmin(t, true)
	for i := 0; i < 5; i++ {
		decode[map[string]any](t, adminRequest(c, "POST", "/login", "", "", `{"password":"incorrect"}`), 401)
	}
	decode[map[string]any](t, adminRequest(c, "POST", "/login", "", "", `{"password":"admin password for tests"}`), 429)
}

func TestAdminCookieIsSecureOverTLS(t *testing.T) {
	c := setupAdmin(t, true)
	r := httptest.NewRequest("POST", "https://uno.example.com/api/v1/admin/login", strings.NewReader(`{"password":"admin password for tests"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	decode[admin.Session](t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("TLS administrator session lacks secure cookie flags")
	}
}
