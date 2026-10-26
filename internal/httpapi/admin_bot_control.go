package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	"unour/game"
	"unour/internal/room"
)

func adminControlOwner(r *http.Request) string {
	digest := sha256.Sum256([]byte(adminCookie(r)))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (h *handler) adminBotControl(w http.ResponseWriter, r *http.Request) {
	id, player := r.PathValue("room"), game.PlayerID(r.PathValue("player"))
	owner, lease := adminControlOwner(r), r.Header.Get("X-Bot-Control")
	var value room.BotControlView
	var err error
	status := http.StatusOK
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Instance string `json:"instance"`
		}
		if !h.decode(w, r, &req, false) {
			return
		}
		value, err = h.admin.Rooms.AcquireBot(r.Context(), id, player, req.Instance, owner)
		status = http.StatusCreated
	case http.MethodGet:
		value, err = h.admin.Rooms.BotState(r.Context(), id, player, owner, lease)
	case http.MethodPut, http.MethodDelete:
		var req struct{}
		if !h.decode(w, r, &req, true) {
			return
		}
		if r.Method == http.MethodPut {
			value, err = h.admin.Rooms.RenewBot(r.Context(), id, player, owner, lease)
		} else {
			err = h.admin.Rooms.ReleaseBot(r.Context(), id, player, owner, lease)
			if err == nil {
				h.json(w, status, map[string]bool{"ok": true})
				return
			}
		}
	default:
		h.methodNotAllowed(w, "GET, POST, PUT, DELETE")
		return
	}
	if err != nil {
		h.failure(w, r, err)
		return
	}
	view, err := h.decorate(r.Context(), value.Room)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, status, map[string]any{"lease_id": value.LeaseID, "expires_at": value.ExpiresAt, "room": view})
}

func (h *handler) adminBotAction(action game.Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := h.decodeAction(w, r, action)
		if !ok {
			return
		}
		result, err := h.admin.Rooms.ExecuteBot(r.Context(), r.PathValue("room"), game.PlayerID(r.PathValue("player")), adminControlOwner(r), r.Header.Get("X-Bot-Control"), req)
		if err != nil {
			h.failure(w, r, err)
			return
		}
		view, err := h.decorate(r.Context(), result.Room)
		if err != nil {
			h.failure(w, r, err)
			return
		}
		h.json(w, http.StatusOK, map[string]any{"room": view, "events": result.Events})
	}
}
