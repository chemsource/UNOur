package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"unour/game"
	"unour/internal/account"
	"unour/internal/admin"
	"unour/internal/audit"
	"unour/internal/room"
)

type Administration struct {
	Sessions *admin.Service
	Users    *account.Manager
	Rooms    *room.Manager
	Audit    audit.Reader
}

const adminCookieName = "unour_admin"

func adminCookie(r *http.Request) string {
	c, err := r.Cookie(adminCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
func (h *handler) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := h.admin.Sessions.Authenticate(adminCookie(r), r.Header.Get("X-Admin-CSRF"), r.Method != http.MethodGet)
		if err != nil {
			h.failure(w, r, err)
			return
		}
		next(w, r)
	}
}
func (h *handler) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/login", h.only("POST", h.adminLogin))
	mux.HandleFunc("/api/v1/admin/session", h.only("GET", h.adminOnly(h.adminSession)))
	mux.HandleFunc("/api/v1/admin/logout", h.only("POST", h.adminOnly(h.adminLogout)))
	mux.HandleFunc("/api/v1/admin/users", h.only("GET", h.adminOnly(h.adminUsers)))
	mux.HandleFunc("/api/v1/admin/users/{user}/status", h.only("PUT", h.adminOnly(h.adminUserStatus)))
	mux.HandleFunc("/api/v1/admin/users/{user}/password", h.only("PUT", h.adminOnly(h.adminUserPassword)))
	mux.HandleFunc("/api/v1/admin/users/{user}/revoke", h.only("POST", h.adminOnly(h.adminUserRevoke)))
	mux.HandleFunc("/api/v1/admin/rooms", h.only("GET", h.adminOnly(h.adminRooms)))
	mux.HandleFunc("/api/v1/admin/rooms/{room}", h.only("GET", h.adminOnly(h.adminRoom)))
	mux.HandleFunc("/api/v1/admin/rooms/{room}/cards", h.only("POST", h.adminOnly(h.adminCards)))
	mux.HandleFunc("/api/v1/admin/rooms/batch/delete", h.only("POST", h.adminOnly(h.adminDeleteRooms)))
	mux.HandleFunc("/api/v1/admin/rooms/{room}/access", h.only("PUT", h.adminOnly(h.adminRoomAccess)))
	mux.HandleFunc("/api/v1/admin/rooms/{room}/members/{player}/kick", h.only("POST", h.adminOnly(h.adminKickMember)))
	mux.HandleFunc("/api/v1/admin/audit", h.only("GET", h.adminOnly(h.adminAudit)))
}
func (h *handler) adminSetCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	maxAge := int(admin.SessionTTL.Seconds())
	if token == "" {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: token, Path: "/api/v1/admin", HttpOnly: true, Secure: h.secureCookies || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: expires})
}
func (h *handler) adminLogin(w http.ResponseWriter, r *http.Request) {
	if !h.adminLimit.allow(requestClientIP(r, h.trustedProxies), time.Now()) {
		w.Header().Set("Retry-After", "300")
		h.problem(w, 429, "auth_rate_limited", "too many admin login attempts; try again in five minutes")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	select {
	case h.adminCredentials <- struct{}{}:
		defer func() { <-h.adminCredentials }()
	default:
		w.Header().Set("Retry-After", "2")
		h.problem(w, 503, "auth_busy", "admin authentication is busy")
		return
	}
	session, err := h.admin.Sessions.Login(req.Password)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.adminSetCookie(w, r, session.Token, session.ExpiresAt)
	h.json(w, 200, session)
}
func (h *handler) adminSession(w http.ResponseWriter, r *http.Request) {
	session, err := h.admin.Sessions.Authenticate(adminCookie(r), "", false)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, session)
}
func (h *handler) adminLogout(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !h.decode(w, r, &req, true) {
		return
	}
	h.admin.Sessions.Logout(adminCookie(r))
	h.adminSetCookie(w, r, "", time.Time{})
	h.json(w, 200, map[string]bool{"ok": true})
}
func (h *handler) pagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 20, 0
	var err error
	if q := r.URL.Query().Get("limit"); q != "" {
		limit, err = strconv.Atoi(q)
	}
	if err == nil {
		if q := r.URL.Query().Get("offset"); q != "" {
			offset, err = strconv.Atoi(q)
		}
	}
	if err != nil || limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		h.problem(w, 400, "invalid_input", "invalid pagination")
		return 0, 0, false
	}
	return limit, offset, true
}
func (h *handler) adminUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := h.pagination(w, r)
	if !ok {
		return
	}
	page, err := h.admin.Users.List(r.Context(), r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, page)
}
func (h *handler) adminUserStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disabled *bool `json:"disabled"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	if req.Disabled == nil {
		h.problem(w, 400, "invalid_input", "disabled is required")
		return
	}
	if err := h.admin.Users.SetDisabled(r.Context(), r.PathValue("user"), *req.Disabled); err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, map[string]bool{"ok": true})
}
func (h *handler) adminUserPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !h.decode(w, r, &req, false) {
		return
	}
	select {
	case h.adminCredentials <- struct{}{}:
		defer func() { <-h.adminCredentials }()
	default:
		h.problem(w, 503, "auth_busy", "admin password processing is busy")
		return
	}
	if err := h.admin.Users.ResetPassword(r.Context(), r.PathValue("user"), req.Password); err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, map[string]bool{"ok": true})
}
func (h *handler) adminUserRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !h.decode(w, r, &req, true) {
		return
	}
	if err := h.admin.Users.RevokeSessions(r.Context(), r.PathValue("user")); err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, map[string]bool{"ok": true})
}
func (h *handler) adminRooms(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := h.pagination(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.admin.Rooms.List(r.Context(), room.RoomQuery{Limit: limit, Offset: offset, Query: q.Get("q"), Phase: game.Phase(q.Get("phase")), Kind: q.Get("kind"), Access: q.Get("access"), Sort: q.Get("sort")})
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, page)
}
func (h *handler) adminRoom(w http.ResponseWriter, r *http.Request) {
	v, err := h.admin.Rooms.Inspect(r.Context(), r.PathValue("room"))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, v)
}
func (h *handler) adminCards(w http.ResponseWriter, r *http.Request) {
	var req room.EditRequest
	if !h.decode(w, r, &req, false) {
		return
	}
	v, err := h.admin.Rooms.Edit(r.Context(), r.PathValue("room"), req)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, v)
}
func (h *handler) adminAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := h.pagination(w, r)
	if !ok {
		return
	}
	page, err := h.admin.Audit.AuditLog(r.Context(), limit, offset)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	h.json(w, 200, page)
}
