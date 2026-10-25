package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"unour/game"
	"unour/internal/account"
	"unour/internal/room"
)

const cookieName = "unour_account"

type userKey struct{}

func currentUser(r *http.Request) account.Profile {
	p, _ := r.Context().Value(userKey{}).(account.Profile)
	return p
}
func sessionCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
func (h *handler) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := h.accounts.Authenticate(r.Context(), sessionCookie(r))
		if err != nil {
			h.failure(w, r, err)
			return
		}
		if expected := r.Header.Get("X-Account-ID"); expected != "" && expected != user.ID {
			h.problem(w, 401, "account_changed", "account changed in another browser tab; refresh the current identity")
			return
		}
		ctx := context.WithValue(room.WithAccount(r.Context(), user.ID), userKey{}, user)
		next(w, r.WithContext(ctx))
	}
}
func (h *handler) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	maxAge := int(account.SessionTTL.Seconds())
	if token == "" {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/api/v1", HttpOnly: true, Secure: h.secureCookies || r.TLS != nil, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge})
}
func (h *handler) credentialsEndpoint(register bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !h.decode(w, r, &req, false) {
			return
		}
		ip := requestClientIP(r, h.trustedProxies)
		if !h.loginLimit.allow(ip, time.Now()) {
			w.Header().Set("Retry-After", "300")
			h.problem(w, 429, "auth_rate_limited", "too many authentication attempts; try again in five minutes")
			return
		}
		select {
		case h.credentials <- struct{}{}:
			defer func() { <-h.credentials }()
		default:
			w.Header().Set("Retry-After", "2")
			h.problem(w, 503, "auth_busy", "authentication is busy; retry shortly")
			return
		}
		var result account.Session
		var err error
		if register {
			result, err = h.accounts.Register(r.Context(), req.Username, req.Password)
		} else {
			result, err = h.accounts.Login(r.Context(), req.Username, req.Password)
		}
		if err != nil {
			h.failure(w, r, err)
			return
		}
		h.setCookie(w, r, result.Token, result.ExpiresAt)
		status := 200
		if register {
			status = 201
		}
		h.json(w, status, result)
	}
}
func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	h.json(w, 200, map[string]any{"user": currentUser(r)})
}
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !h.decode(w, r, &req, true) {
		return
	}
	if err := h.accounts.Logout(r.Context(), sessionCookie(r)); err != nil {
		h.failure(w, r, err)
		return
	}
	h.setCookie(w, r, "", time.Time{})
	h.json(w, 200, map[string]bool{"ok": true})
}
func (h *handler) accountAvatar(w http.ResponseWriter, r *http.Request) {
	var profile account.Profile
	var err error
	switch r.Method {
	case http.MethodPut:
		media, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || (media != "image/png" && media != "image/jpeg") {
			h.problem(w, 415, "invalid_avatar", "avatar must be PNG or JPEG")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, account.MaxAvatarBytes)
		defer r.Body.Close()
		data, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(readErr, &tooLarge) {
				h.failure(w, r, account.ErrAvatarTooLarge)
			} else {
				h.problem(w, 400, "invalid_avatar", "could not read avatar")
			}
			return
		}
		profile, err = h.accounts.SetAvatar(r.Context(), currentUser(r).ID, data)
	case http.MethodDelete:
		profile, err = h.accounts.ResetAvatar(r.Context(), currentUser(r).ID)
	default:
		h.methodNotAllowed(w, "PUT, DELETE")
		return
	}
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, map[string]any{"user": profile})
}
func (h *handler) avatar(w http.ResponseWriter, r *http.Request) {
	digest := r.PathValue("digest")
	data, err := h.accounts.Avatar(r.Context(), digest)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	etag := `"` + digest + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `inline; filename="avatar.png"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

type roomResponse struct {
	room.View
	Profiles map[game.PlayerID]account.Profile `json:"profiles"`
}

func (h *handler) decorate(ctx context.Context, v room.View) (roomResponse, error) {
	ids := make([]string, 0, len(v.Accounts))
	for _, id := range v.Accounts {
		ids = append(ids, id)
	}
	profiles, err := h.accounts.Profiles(ctx, ids)
	if err != nil {
		return roomResponse{}, err
	}
	result := roomResponse{View: v, Profiles: make(map[game.PlayerID]account.Profile, len(profiles))}
	for playerID, accountID := range v.Accounts {
		if profile, ok := profiles[accountID]; ok {
			result.Profiles[playerID] = profile
		}
	}
	return result, nil
}
func (h *handler) roomJSON(w http.ResponseWriter, r *http.Request, status int, v room.View) {
	view, err := h.decorate(r.Context(), v)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, status, view)
}

type attempt struct {
	count int
	reset time.Time
}
type attemptLimiter struct {
	maxCount int
	mu       sync.Mutex
	entries  map[string]attempt
}

func (l *attemptLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]attempt)
	}
	if len(l.entries) >= 4096 {
		for ip, a := range l.entries {
			if !now.Before(a.reset) {
				delete(l.entries, ip)
			}
		}
		if _, ok := l.entries[key]; !ok && len(l.entries) >= 4096 {
			return false
		}
	}
	a := l.entries[key]
	if !now.Before(a.reset) {
		a = attempt{reset: now.Add(5 * time.Minute)}
	}
	maximum := l.maxCount
	if maximum == 0 {
		maximum = 20
	}
	if a.count >= maximum {
		return false
	}
	a.count++
	l.entries[key] = a
	return true
}
