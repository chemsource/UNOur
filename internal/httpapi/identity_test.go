package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unour/internal/account"
)

func TestStaleBrowserIdentityCannotMutateNewCookieAccount(t *testing.T) {
	c := setup(t)
	current := decode[struct {
		User account.Profile `json:"user"`
	}](t, c.request("GET", "/api/v1/account", c.alice, ""), 200)
	for _, path := range []string{"/api/v1/rooms", "/api/v1/auth/logout"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Account-ID", current.User.ID)
		r.AddCookie(&http.Cookie{Name: "unour_account", Value: c.bob})
		w := httptest.NewRecorder()
		c.handler.ServeHTTP(w, r)
		problem := decode[map[string]map[string]string](t, w, 401)
		if problem["error"]["code"] != "account_changed" {
			t.Fatalf("wrong stale-account error: %s", w.Body)
		}
	}
	decode[map[string]any](t, c.request("GET", "/api/v1/account", c.bob, ""), 200)
}
