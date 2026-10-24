package httpapi

import (
	"net"
	"net/http"
	"net/netip"
)

// X-Real-IP is accepted only from explicitly trusted peers. The deployment's
// last proxy overwrites this single-address header rather than trusting a chain.
func requestClientIP(r *http.Request, trusted []netip.Prefix) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	peer, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	peer = peer.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(peer) {
			if client, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil && client.Zone() == "" {
				return client.Unmap().String()
			}
			break
		}
	}
	return ip
}
