package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"unour/game"
	"unour/internal/account"
	"unour/internal/room"
)

const maxBodyBytes = 16 << 10

func (h *handler) decode(w http.ResponseWriter, r *http.Request, dst any, optional bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.problem(w, 413, "body_too_large", "request body exceeds 16 KiB")
		} else {
			h.problem(w, 400, "invalid_json", "could not read request body")
		}
		return false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		if optional {
			return true
		}
		h.problem(w, 400, "invalid_json", "a JSON object is required")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.problem(w, 415, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	if body[0] != '{' {
		h.problem(w, 400, "invalid_json", "body must be a JSON object")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		h.problem(w, 400, "invalid_json", "body must be a JSON object with known fields and valid values")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		h.problem(w, 400, "invalid_json", "body must contain exactly one JSON object")
		return false
	}
	return true
}

func (h *handler) json(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		h.logger.Error("encode response", "error", err)
		status = 500
		body = []byte(`{"error":{"code":"internal_error","message":"internal server error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		h.logger.Debug("write response", "error", err)
	}
}
func (h *handler) problem(w http.ResponseWriter, status int, code, message string) {
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="uno"`)
	}
	h.json(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (h *handler) failure(w http.ResponseWriter, r *http.Request, err error) {
	var auth *account.Error
	if errors.As(err, &auth) {
		status := 400
		switch auth.Code {
		case "account_unauthorized", "invalid_credentials":
			status = 401
		case "username_taken":
			status = 409
		case "account_not_found", "avatar_not_found":
			status = 404
		case "avatar_too_large":
			status = 413
		case "avatar_busy":
			status = 503
			w.Header().Set("Retry-After", "2")
		}
		h.problem(w, status, auth.Code, auth.Message)
		return
	}
	var domain *game.Error
	if errors.As(err, &domain) {
		status := 409
		switch domain.Code {
		case game.InvalidInput, game.CardNotOwned:
			status = 400
		case game.RuleViolation, game.CardNotPlayable:
			status = 422
		case game.InternalFailure:
			status = 500
		}
		if status == 500 {
			h.logger.Error("game command failed", "path", r.URL.Path, "error", err, "cause", domain.Cause)
			h.problem(w, 500, "internal_error", "internal server error")
			return
		}
		h.problem(w, status, string(domain.Code), domain.Message)
		return
	}
	var app *room.Error
	if errors.As(err, &app) {
		status := 400
		switch app.Code {
		case "room_not_found":
			status = 404
		case "room_exists", "version_conflict", "rematch_unavailable", "roster_changed":
			status = 409
		case "unauthorized":
			status = 401
		case "forbidden", "spectator_read_only", "spectator_required":
			status = 403
		case "capacity_exceeded":
			status = 503
		}
		h.problem(w, status, app.Code, app.Message)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		h.problem(w, 408, "request_timeout", "request was cancelled or timed out")
		return
	}
	h.logger.Error("request failed", "path", r.URL.Path, "error", err)
	h.problem(w, 500, "internal_error", "internal server error")
}
