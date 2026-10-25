package httpapi

import (
	"net/http"
	"unour/internal/room"
)

func (h *handler) spectators(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !h.decode(w, r, &body, true) {
		return
	}
	user := currentUser(r)
	v, err := h.rooms.SpectateAccount(r.Context(), r.PathValue("room"), user.ID, user.Username)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.joinJSON(w, r, v)
}
func (h *handler) participation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NextRound *bool `json:"next_round"`
	}
	if !h.decode(w, r, &body, false) {
		return
	}
	if body.NextRound == nil {
		h.problem(w, 400, "invalid_input", "next_round is required")
		return
	}
	v, err := h.rooms.Participation(r.Context(), r.PathValue("room"), bearer(r), *body.NextRound)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.roomJSON(w, r, 200, v)
}
func (h *handler) rematch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RosterVersion *uint64 `json:"roster_version"`
		Agree         *bool   `json:"agree"`
		Round         uint64  `json:"round"`
	}
	if !h.decode(w, r, &body, false) {
		return
	}
	if body.Agree == nil || body.Round == 0 || body.RosterVersion == nil {
		h.problem(w, 400, "invalid_input", "agree, round and roster_version are required")
		return
	}
	v, err := h.rooms.VoteRematch(r.Context(), r.PathValue("room"), bearer(r), room.RematchRequest{Round: body.Round, Agree: *body.Agree, RosterVersion: *body.RosterVersion})
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.roomJSON(w, r, 200, v)
}

func (h *handler) leave(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !h.decode(w, r, &body, true) {
		return
	}
	if err := h.rooms.Leave(r.Context(), r.PathValue("room"), bearer(r)); err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, map[string]bool{"left": true})
}
