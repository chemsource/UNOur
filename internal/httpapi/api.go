// Package httpapi adapts REST requests to room use cases. It owns no games,
// session state, global router, or storage implementation.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"unour/game"
	"unour/internal/account"
	"unour/internal/room"
)

type Rooms interface {
	RulesCatalog() game.RuleCatalog
	Create(context.Context, room.CreateRequest) (room.View, error)
	CreateSolo(context.Context, string, string, room.SoloRequest) (room.JoinResult, error)
	JoinAccount(context.Context, string, string, string) (room.JoinResult, error)
	State(context.Context, string, string) (room.View, error)
	Execute(context.Context, string, string, room.ActionRequest) (room.ActionResult, error)
	Delete(context.Context, string, string) error
	SpectateAccount(context.Context, string, string, string) (room.JoinResult, error)
	Participation(context.Context, string, string, bool) (room.View, error)
	VoteRematch(context.Context, string, string, room.RematchRequest) (room.View, error)
	Leave(context.Context, string, string) error
}

type Config struct {
	Admin          *Administration
	Accounts       *account.Service
	SecureCookies  bool
	Logger         *slog.Logger
	AllowedOrigins []string
	TrustedProxies []netip.Prefix
}
type handler struct {
	admin            *Administration
	adminLimit       attemptLimiter
	adminCredentials chan struct{}
	accounts         *account.Service
	secureCookies    bool
	loginLimit       attemptLimiter
	credentials      chan struct{}
	rooms            Rooms
	logger           *slog.Logger
	origins          map[string]bool
	trustedProxies   []netip.Prefix
}

func New(rooms Rooms, cfg Config) (http.Handler, error) {
	if rooms == nil || cfg.Accounts == nil {
		return nil, fmt.Errorf("room and account services are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	h := &handler{rooms: rooms, accounts: cfg.Accounts, secureCookies: cfg.SecureCookies, credentials: make(chan struct{}, 4), logger: cfg.Logger, origins: make(map[string]bool)}
	h.trustedProxies = append([]netip.Prefix(nil), cfg.TrustedProxies...)
	if cfg.Admin != nil {
		if cfg.Admin.Sessions == nil || cfg.Admin.Users == nil || cfg.Admin.Rooms == nil || cfg.Admin.Audit == nil {
			return nil, fmt.Errorf("all administration services are required when enabled")
		}
		copy := *cfg.Admin
		h.admin = &copy
		h.adminLimit.maxCount = 5
		h.adminCredentials = make(chan struct{}, 2)
	}
	for _, origin := range cfg.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("invalid allowed origin %q", origin)
		}
		h.origins[origin] = true
	}
	mux := http.NewServeMux()
	if h.admin != nil {
		h.registerAdmin(mux)
	}
	mux.HandleFunc("/api/v1/rules", h.only("GET", func(w http.ResponseWriter, r *http.Request) { h.json(w, 200, h.rooms.RulesCatalog()) }))
	mux.HandleFunc("/healthz", h.only("GET", func(w http.ResponseWriter, r *http.Request) { h.json(w, 200, map[string]string{"status": "ok"}) }))
	mux.HandleFunc("/api/v1/auth/register", h.only("POST", h.credentialsEndpoint(true)))
	mux.HandleFunc("/api/v1/auth/login", h.only("POST", h.credentialsEndpoint(false)))
	mux.HandleFunc("/api/v1/auth/logout", h.only("POST", h.authenticated(h.logout)))
	mux.HandleFunc("/api/v1/account", h.only("GET", h.authenticated(h.me)))
	mux.HandleFunc("/api/v1/account/avatar", h.authenticated(h.accountAvatar))
	mux.HandleFunc("/api/v1/avatars/{digest}", h.only("GET", h.avatar))
	mux.HandleFunc("/api/v1/rooms", h.only("POST", h.authenticated(h.create)))
	mux.HandleFunc("/api/v1/solo", h.only("POST", h.authenticated(h.solo)))
	mux.HandleFunc("/api/v1/rooms/{room}/leave", h.only("POST", h.authenticated(h.leave)))
	mux.HandleFunc("/api/v1/rooms/{room}/spectators", h.only("POST", h.authenticated(h.spectators)))
	mux.HandleFunc("/api/v1/rooms/{room}/participation", h.only("POST", h.authenticated(h.participation)))
	mux.HandleFunc("/api/v1/rooms/{room}/rematch", h.only("POST", h.authenticated(h.rematch)))
	mux.HandleFunc("/api/v1/rooms/{room}/players", h.only("POST", h.authenticated(h.join)))
	mux.HandleFunc("/api/v1/rooms/{room}", h.authenticated(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.state(w, r)
		case http.MethodDelete:
			h.delete(w, r)
		default:
			h.methodNotAllowed(w, "GET, DELETE")
		}
	}))
	for suffix, action := range map[string]game.Action{"start": game.Start, "play": game.Play, "draw": game.Draw, "pass": game.Pass, "challenge": game.Challenge, "uno": game.SayUNO, "uno-penalty": game.PenalizeUNO} {
		mux.HandleFunc("/api/v1/rooms/{room}/"+suffix, h.only("POST", h.authenticated(h.action(action))))
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
	h.roomJSON(w, r, 201, v)
}
func (h *handler) join(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !h.decode(w, r, &req, true) {
		return
	}
	user := currentUser(r)
	v, err := h.rooms.JoinAccount(r.Context(), r.PathValue("room"), user.ID, user.Username)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.joinJSON(w, r, v)
}
func (h *handler) solo(w http.ResponseWriter, r *http.Request) {
	var req room.SoloRequest
	if !h.decode(w, r, &req, false) {
		return
	}
	user := currentUser(r)
	v, err := h.rooms.CreateSolo(r.Context(), user.ID, user.Username, req)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/rooms/"+v.Room.ID)
	h.joinJSON(w, r, v)
}
func (h *handler) joinJSON(w http.ResponseWriter, r *http.Request, v room.JoinResult) {
	view, err := h.decorate(r.Context(), v.Room)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 201, map[string]any{"player_id": v.PlayerID, "token": v.Token, "room": view})
}
func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	v, err := h.rooms.State(r.Context(), r.PathValue("room"), bearer(r))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.roomJSON(w, r, 200, v)
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
		view, err := h.decorate(r.Context(), result.Room)
		if err != nil {
			h.failure(w, r, err)
			return
		}
		h.json(w, 200, map[string]any{"room": view, "events": result.Events})
	}
}

func bearer(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
