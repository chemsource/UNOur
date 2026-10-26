package httpapi

import (
	"net/http"

	"unour/game"
	"unour/internal/room"
)

// Both player and administrator adapters decode exactly the same game protocol.
func (h *handler) decodeAction(w http.ResponseWriter, r *http.Request, action game.Action) (room.ActionRequest, bool) {
	req := room.ActionRequest{Type: action}
	switch action {
	case game.Play:
		var body struct {
			CardID          int        `json:"card_id"`
			Color           game.Color `json:"color,omitempty"`
			ExpectedVersion *uint64    `json:"expected_version,omitempty"`
		}
		if !h.decode(w, r, &body, false) {
			return req, false
		}
		if body.CardID <= 0 {
			h.problem(w, 400, "invalid_input", "card_id must be positive")
			return req, false
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
			return req, false
		}
		if body.WindowID == 0 {
			h.problem(w, 400, "invalid_input", "window_id must be positive")
			return req, false
		}
		req.WindowID = body.WindowID
		req.ExpectedVersion = body.ExpectedVersion
	default:
		var body struct {
			ExpectedVersion *uint64 `json:"expected_version,omitempty"`
		}
		if !h.decode(w, r, &body, true) {
			return req, false
		}
		req.ExpectedVersion = body.ExpectedVersion
	}
	return req, true
}
