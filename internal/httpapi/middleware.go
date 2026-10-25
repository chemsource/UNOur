package httpapi

import (
	"net/http"
	"strings"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.Error("request panic", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				if rw.status == 0 {
					h.problem(rw, 500, "internal_error", "internal server error")
				}
			}
			status := rw.status
			if status == 0 {
				status = 200
			}
			// Never log Authorization headers, session tokens, or request bodies.
			h.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(started))
		}()
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if !h.origins[origin] {
				h.problem(rw, 403, "origin_not_allowed", "request origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions {
				method := r.Header.Get("Access-Control-Request-Method")
				if method != "GET" && method != "POST" && method != "PUT" && method != "DELETE" {
					h.problem(rw, 405, "method_not_allowed", "preflight method is not allowed")
					return
				}
				for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
					header = strings.ToLower(strings.TrimSpace(header))
					if header != "" && header != "authorization" && header != "content-type" && header != "x-account-id" && header != "x-admin-csrf" {
						h.problem(rw, 403, "header_not_allowed", "preflight header is not allowed")
						return
					}
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Account-ID, X-Admin-CSRF")
				w.Header().Set("Access-Control-Max-Age", "600")
				rw.WriteHeader(204)
				return
			}
		}
		if origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.problem(rw, 403, "origin_not_allowed", "cross-site mutation is not allowed")
			return
		}
		next.ServeHTTP(rw, r)
	})
}
