package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unour/internal/account"
	"unour/internal/room"
	"unour/internal/store/sqlite"
)

func TestClientIPTrustsOnlyConfiguredProxyPeers(t *testing.T) {
	for _, test := range []struct {
		name, peer, header, prefix, want string
	}{
		{"direct client cannot spoof", "203.0.113.1:1234", "198.51.100.1", "172.16.0.0/12", "203.0.113.1"},
		{"headers disabled by default", "172.20.0.3:1234", "198.51.100.1", "", "172.20.0.3"},
		{"trusted proxy first client", "172.20.0.3:1234", "198.51.100.1", "172.16.0.0/12", "198.51.100.1"},
		{"trusted proxy other client", "172.20.0.3:1234", "198.51.100.2", "172.16.0.0/12", "198.51.100.2"},
		{"IPv6 client", "172.20.0.3:1234", "2001:db8::1", "172.16.0.0/12", "2001:db8::1"},
		{"invalid header falls back", "172.20.0.3:1234", "not-an-ip", "172.16.0.0/12", "172.20.0.3"},
		{"forwarded chain is not a single IP", "172.20.0.3:1234", "198.51.100.1, 198.51.100.2", "172.16.0.0/12", "172.20.0.3"},
		{"IPv6 proxy", "[fd00::2]:1234", "198.51.100.1", "fd00::/64", "198.51.100.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			r.RemoteAddr = test.peer
			r.Header.Set("X-Real-IP", test.header)
			var trusted []netip.Prefix
			if test.prefix != "" {
				trusted = []netip.Prefix{netip.MustParsePrefix(test.prefix)}
			}
			if got := requestClientIP(r, trusted); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestCredentialLimiterSeparatesClientsBehindTrustedProxy(t *testing.T) {
	store, err := sqlite.Open(sqlite.Config{Path: filepath.Join(t.TempDir(), "proxy.db"), MaxRooms: 1, Codec: room.NewCodec(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accounts, err := account.New(store, account.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{accounts: accounts, credentials: make(chan struct{}, 1), trustedProxies: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}
	reset := time.Now().Add(5 * time.Minute)
	h.loginLimit.entries = map[string]attempt{
		"172.20.0.3":   {count: 20, reset: reset},
		"198.51.100.1": {count: 20, reset: reset},
	}
	for _, test := range []struct {
		ip     string
		status int
	}{
		{"198.51.100.1", http.StatusTooManyRequests},
		{"198.51.100.2", http.StatusCreated},
	} {
		r := httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader(`{"username":"proxy_user","password":"12345678"}`))
		r.RemoteAddr = "172.20.0.3:4321"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Real-IP", test.ip)
		w := httptest.NewRecorder()
		h.credentialsEndpoint(true)(w, r)
		if w.Code != test.status {
			t.Fatalf("client %s status=%d want=%d: %s", test.ip, w.Code, test.status, w.Body)
		}
	}
	if _, err := accounts.Login(context.Background(), "proxy_user", "12345678"); err != nil {
		t.Fatal("unrelated client was unable to register", err)
	}
}
