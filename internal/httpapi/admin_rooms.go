package httpapi

import (
	"net/http"

	"unour/game"
	"unour/internal/room"
)

func (h *handler) adminDeleteRooms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rooms []room.DeleteTarget `json:"rooms"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	result, err := h.admin.Rooms.Delete(r.Context(), req.Rooms)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, http.StatusOK, result)
}

func (h *handler) adminRoomAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instance   string `json:"instance"`
		JoinLocked *bool  `json:"join_locked"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	if req.JoinLocked == nil {
		h.problem(w, 400, "invalid_input", "join_locked is required")
		return
	}
	result, err := h.admin.Rooms.SetAccess(r.Context(), r.PathValue("room"), room.AccessRequest{Instance: req.Instance, JoinLocked: *req.JoinLocked})
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, http.StatusOK, result)
}

func (h *handler) adminKickMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instance string `json:"instance"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	result, err := h.admin.Rooms.Kick(r.Context(), r.PathValue("room"), game.PlayerID(r.PathValue("player")), req.Instance)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, http.StatusOK, result)
}
