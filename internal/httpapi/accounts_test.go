package httpapi_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"unour/internal/account"
	"unour/internal/room"
)

func TestHTTPAccountFlowCookiesAndRoomOwnership(t *testing.T) {
	c := setup(t)
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms", "", `{}`), 401)
	decode[map[string]any](t, c.request("POST", "/api/v1/auth/register", "", `{"username":"tester","password":"1234567"}`), 400)
	registered := c.request("POST", "/api/v1/auth/register", "", `{"username":"tester","password":"valid-password"}`)
	result := decode[struct {
		User account.Profile `json:"user"`
	}](t, registered, 201)
	if strings.Contains(registered.Body.String(), "password") || strings.Contains(registered.Body.String(), "token") {
		t.Fatal("credentials leaked in JSON")
	}
	cookies := registered.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge <= 0 {
		t.Fatal("unsafe account cookie")
	}
	token := cookies[0].Value
	current := decode[struct {
		User account.Profile `json:"user"`
	}](t, c.request("GET", "/api/v1/account", token, ""), 200)
	if current.User.ID != result.User.ID {
		t.Fatal("wrong account")
	}
	decode[map[string]any](t, c.request("POST", "/api/v1/auth/register", "", `{"username":"TESTER","password":"valid-password"}`), 409)
	decode[map[string]any](t, c.request("POST", "/api/v1/auth/login", "", `{"username":"tester","password":"wrong-password"}`), 401)
	logged := c.request("POST", "/api/v1/auth/login", "", `{"username":"TESTER","password":"valid-password"}`)
	decode[map[string]any](t, logged, 200)
	if logged.Result().Cookies()[0].Value == token {
		t.Fatal("login did not issue a new session")
	}
	decode[map[string]any](t, c.request("POST", "/api/v1/auth/logout", token, `{}`), 200)
	decode[map[string]any](t, c.request("GET", "/api/v1/account", token, ""), 401)
	a, _ := c.start()
	req := httptest.NewRequest("GET", "/api/v1/rooms/table", nil)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.AddCookie(&http.Cookie{Name: "unour_account", Value: c.bob})
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, req)
	decode[map[string]any](t, w, 401)
	// The server takes the player name from the account, not from client input.
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/players", c.bob, `{"name":"Alice"}`), 400)
}

func TestHTTPAvatarAndProfilesWithoutChangingGameVersion(t *testing.T) {
	c := setup(t)
	a, _ := c.start()
	before := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 200)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 30, 60)))
	upload := func(contentType string, data []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/v1/account/avatar", bytes.NewReader(data))
		r.Header.Set("Content-Type", contentType)
		r.AddCookie(&http.Cookie{Name: "unour_account", Value: c.alice})
		w := httptest.NewRecorder()
		c.handler.ServeHTTP(w, r)
		return w
	}
	decode[map[string]any](t, upload("image/svg+xml", []byte("<svg/>")), 415)
	decode[map[string]any](t, upload("image/png", make([]byte, (2<<20)+1)), 413)
	profile := decode[struct {
		User account.Profile `json:"user"`
	}](t, upload("image/png", buf.Bytes()), 200).User
	data := c.request("GET", profile.AvatarURL, "", "")
	if data.Code != 200 || data.Header().Get("Content-Type") != "image/png" {
		t.Fatal("avatar is not served as PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data.Body.Bytes()))
	if err != nil || cfg.Width != 256 || cfg.Height != 256 {
		t.Fatal("invalid avatar dimensions")
	}
	var view struct {
		room.View
		Profiles map[string]account.Profile `json:"profiles"`
	}
	response := c.request("GET", "/api/v1/rooms/table", a.Token, "")
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State.Version != before.State.Version || view.Profiles[string(a.PlayerID)].AvatarURL != profile.AvatarURL {
		t.Fatal("profile update not reflected independently of game version")
	}
	reset := decode[struct {
		User account.Profile `json:"user"`
	}](t, c.request("DELETE", "/api/v1/account/avatar", c.alice, ""), 200)
	if reset.User.AvatarURL != "" {
		t.Fatal("default avatar not restored")
	}
}
