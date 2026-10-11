package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the caller's IP address. Behind a reverse proxy (the edge
// nginx in production) every request comes from the proxy, so with trustProxy the
// last X-Forwarded-For entry — the one the proxy itself appended — is used.
// Without trustProxy the header is ignored, since clients can forge it.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
