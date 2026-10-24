package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"unour/game"
	"unour/internal/httpapi"
	"unour/internal/room"
	"unour/internal/store/memory"
)

type client struct {
	t       *testing.T
	handler http.Handler
	now     time.Time
}

func setup(t *testing.T) *client {
	t.Helper()
	c := &client{t: t, now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	store, err := memory.New(memory.Config{MaxRooms: 10, Now: func() time.Time { return c.now }})
	if err != nil {
		t.Fatal(err)
	}
	s, err := room.New(store, room.Config{Now: func() time.Time { return c.now }, GameOptions: game.Options{InitialHand: 2, Shuffle: func([]game.Card) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	c.handler, err = httpapi.New(s, httpapi.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), AllowedOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (c *client) request(method, path, token, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w
}
func decode[T any](t *testing.T, w *httptest.ResponseRecorder, want int) T {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func (c *client) start() (room.JoinResult, room.JoinResult) {
	c.t.Helper()
	decode[room.View](c.t, c.request("POST", "/api/v1/rooms", "", `{"id":"table"}`), 201)
	a := decode[room.JoinResult](c.t, c.request("POST", "/api/v1/rooms/table/players", "", `{"name":"Alice"}`), 201)
	b := decode[room.JoinResult](c.t, c.request("POST", "/api/v1/rooms/table/players", "", `{"name":"Bob"}`), 201)
	decode[room.ActionResult](c.t, c.request("POST", "/api/v1/rooms/table/start", a.Token, `{}`), 200)
	return a, b
}

func TestHTTPGameFlowPrivacyAndUNOPenalty(t *testing.T) {
	c := setup(t)
	a, b := c.start()
	view := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 200)
	if len(view.State.Players[0].Hand) != 2 || len(view.State.Players[1].Hand) != 0 {
		t.Fatal("hands are not private")
	}
	played := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/play", a.Token, `{"card_id":1}`), 200)
	w := played.Room.State.Players[0].UNO
	if w == nil {
		t.Fatal("play did not publish UNO window")
	}
	body, _ := json.Marshal(map[string]any{"window_id": w.ID})
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/uno-penalty", a.Token, string(body)), 409)
	c.now = c.now.Add(2 * time.Second)
	penalized := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/uno-penalty", a.Token, string(body)), 200)
	if penalized.Room.State.Turn != b.PlayerID || penalized.Room.State.Players[0].HandCount != 2 {
		t.Fatal("UNO penalty changed turn or wrong count")
	}
	again := decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/uno-penalty", a.Token, string(body)), 200)
	if again.Room.State.Version != penalized.Room.State.Version || again.Room.State.Players[0].HandCount != 2 {
		t.Fatal("HTTP retry duplicated penalty")
	}
	view = decode[room.View](t, c.request("GET", "/api/v1/rooms/table", b.Token, ""), 200)
	if len(view.State.Players[0].Hand) != 0 || len(view.State.Players[1].Hand) != 2 {
		t.Fatal("other session got wrong hand")
	}
	decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/draw", b.Token, ""), 200)
	decode[room.ActionResult](t, c.request("POST", "/api/v1/rooms/table/pass", b.Token, ""), 200)
}

func TestHTTPRejectsUnauthenticatedAndForgedIdentity(t *testing.T) {
	c := setup(t)
	a, b := c.start()
	for _, token := range []string{"", "forged"} {
		decode[map[string]any](t, c.request("GET", "/api/v1/rooms/table", token, ""), 401)
	}
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/play", a.Token, `{"card_id":1,"player_id":"other"}`), 400)
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms/table/play", a.Token, `{"card_id":1,"expected_version":0}`), 409)
	decode[map[string]any](t, c.request("DELETE", "/api/v1/rooms/table", b.Token, ""), 403)
	w := c.request("DELETE", "/api/v1/rooms/table", a.Token, "")
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	decode[map[string]any](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 404)
}

func TestHTTPJSONValidationAndRouting(t *testing.T) {
	c := setup(t)
	for _, body := range []string{"", `null`, `[]`, `{`, `{"unknown":true}`, `{} {}`} {
		w := c.request("POST", "/api/v1/rooms", "", body)
		decode[map[string]any](t, w, 400)
		if w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("error is not JSON")
		}
	}
	decode[map[string]any](t, c.request("POST", "/api/v1/rooms", "", `{"id":"`+strings.Repeat("a", 17000)+`"}`), 413)
	r := httptest.NewRequest("POST", "/api/v1/rooms", bytes.NewBufferString(`{}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	decode[map[string]any](t, w, 415)
	decode[map[string]any](t, c.request("GET", "/api/v1/rooms", "", ""), 405)
	decode[map[string]any](t, c.request("GET", "/does-not-exist", "", ""), 404)
	decode[map[string]any](t, c.request("GET", "/healthz", "", ""), 200)
}

func TestCORSPreflightAndSessionResponsesAreNotCached(t *testing.T) {
	c := setup(t)
	r := httptest.NewRequest("OPTIONS", "/api/v1/rooms", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("preflight: %d %+v", w.Code, w.Header())
	}
	r = httptest.NewRequest("POST", "/api/v1/rooms", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://untrusted.example")
	w = httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("untrusted origin: %d", w.Code)
	}
	w = c.request("POST", "/api/v1/rooms", "", `{}`)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive responses can be cached")
	}
}

// Keep the application interface grounded in the real service, rather than a
// permissive mock that could hide a mismatch between handler and use case.
var _ httpapi.Rooms = (*room.Service)(nil)
