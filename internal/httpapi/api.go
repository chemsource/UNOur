// Package httpapi adapts REST requests to room use cases. It owns no games,
// session state, global router, or storage implementation.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"unour/game"
	"unour/internal/room"
)

type Rooms interface {
	Create(context.Context, room.CreateRequest) (room.View, error)
	Join(context.Context, string, string) (room.JoinResult, error)
	State(context.Context, string, string) (room.View, error)
	Execute(context.Context, string, string, room.ActionRequest) (room.ActionResult, error)
	Delete(context.Context, string, string) error
}

type Config struct {
	Logger         *slog.Logger
	AllowedOrigins []string
}
type handler struct {
	rooms   Rooms
	logger  *slog.Logger
	origins map[string]bool
}

func New(rooms Rooms, cfg Config) (http.Handler, error) {
	if rooms == nil {
		return nil, fmt.Errorf("room service is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	h := &handler{rooms: rooms, logger: cfg.Logger, origins: make(map[string]bool)}
	for _, origin := range cfg.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("invalid allowed origin %q", origin)
		}
		h.origins[origin] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.only("GET", func(w http.ResponseWriter, r *http.Request) { h.json(w, 200, map[string]string{"status": "ok"}) }))
	mux.HandleFunc("/api/v1/rooms", h.only("POST", h.create))
	mux.HandleFunc("/api/v1/rooms/{room}/players", h.only("POST", h.join))
	mux.HandleFunc("/api/v1/rooms/{room}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.state(w, r)
		case http.MethodDelete:
			h.delete(w, r)
		default:
			h.methodNotAllowed(w, "GET, DELETE")
		}
	})
	for suffix, action := range map[string]game.Action{"start": game.Start, "play": game.Play, "draw": game.Draw, "pass": game.Pass, "challenge": game.Challenge, "uno": game.SayUNO, "uno-penalty": game.PenalizeUNO} {
		mux.HandleFunc("/api/v1/rooms/{room}/"+suffix, h.only("POST", h.action(action)))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.problem(w, 404, "not_found", "endpoint does not exist")
	})
	return h.middleware(mux), nil
}

func (h *handler) only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			h.methodNotAllowed(w, method)
			return
		}
		next(w, r)
	}
}
func (h *handler) methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	h.problem(w, 405, "method_not_allowed", "method is not allowed for this endpoint")
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var req room.CreateRequest
	if !h.decode(w, r, &req, false) {
		return
	}
	v, err := h.rooms.Create(r.Context(), req)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/rooms/"+v.ID)
	h.json(w, 201, v)
}
func (h *handler) join(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	v, err := h.rooms.Join(r.Context(), r.PathValue("room"), req.Name)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 201, v)
}
func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	v, err := h.rooms.State(r.Context(), r.PathValue("room"), bearer(r))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, v)
}
func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.rooms.Delete(r.Context(), r.PathValue("room"), bearer(r)); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *handler) action(action game.Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := room.ActionRequest{Type: action}
		switch action {
		case game.Play:
			var body struct {
				CardID          int        `json:"card_id"`
				Color           game.Color `json:"color,omitempty"`
				ExpectedVersion *uint64    `json:"expected_version,omitempty"`
			}
			if !h.decode(w, r, &body, false) {
				return
			}
			if body.CardID <= 0 {
				h.problem(w, 400, "invalid_input", "card_id must be positive")
				return
			}
			req.CardID = body.CardID
			req.Color = body.Color
			req.ExpectedVersion = body.ExpectedVersion
		case game.SayUNO, game.PenalizeUNO:
			var body struct {
				WindowID        uint64  `json:"window_id"`
				ExpectedVersion *uint64 `json:"expected_version,omitempty"`
			}
			if !h.decode(w, r, &body, false) {
				return
			}
			if body.WindowID == 0 {
				h.problem(w, 400, "invalid_input", "window_id must be positive")
				return
			}
			req.WindowID = body.WindowID
			req.ExpectedVersion = body.ExpectedVersion
		default:
			var body struct {
				ExpectedVersion *uint64 `json:"expected_version,omitempty"`
			}
			if !h.decode(w, r, &body, true) {
				return
			}
			req.ExpectedVersion = body.ExpectedVersion
		}
		result, err := h.rooms.Execute(r.Context(), r.PathValue("room"), bearer(r), req)
		if err != nil {
			h.failure(w, r, err)
			return
		}
		h.json(w, 200, result)
	}
}

func bearer(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
