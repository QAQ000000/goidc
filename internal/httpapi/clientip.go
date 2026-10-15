package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// maxForwardedHops bounds how many X-Forwarded-For entries are inspected per
// request so oversized client headers cannot turn address resolution into
// unbounded work.
const maxForwardedHops = 32

// peerHost returns the socket peer address without its port, falling back to
// the raw RemoteAddr when it has no port (as it does not for some test and
// Unix-socket transports).
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (s *Server) trustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range s.cfg.TrustedProxyCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardedClientIP returns the right-most forwarded address that is not
// itself a trusted proxy hop. Proxies append the peer they observed, so the
// right-most non-trusted entry is the first address our infrastructure can
// vouch for; client-supplied values further left are spoofable and ignored.
// It returns "", false when no usable forwarded address is present.
func (s *Server) forwardedClientIP(r *http.Request) (string, bool) {
	hops := make([]string, 0, 4)
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, value := range strings.Split(header, ",") {
			if value = strings.TrimSpace(value); value != "" {
				hops = append(hops, value)
			}
		}
	}
	if len(hops) == 0 {
		if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); value != "" {
			hops = append(hops, value)
		}
	}
	if len(hops) > maxForwardedHops {
		hops = hops[len(hops)-maxForwardedHops:]
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			continue
		}
		if !s.trustedProxy(ip) {
			return ip.String(), true
		}
	}
	return "", false
}

// clientIP attributes a request to a client address for rate limiting,
// blacklists, captcha binding and audit logs. The socket peer is
// authoritative unless it belongs to a configured TRUSTED_PROXY_CIDRS
// network; only then are forwarded headers consulted. With no trusted proxies
// configured this is exactly the previous RemoteAddr-derived behaviour.
func (s *Server) clientIP(r *http.Request) string {
	peer := peerHost(r)
	if peer == "" || len(s.cfg.TrustedProxyCIDRs) == 0 || !s.trustedProxy(net.ParseIP(peer)) {
		return peer
	}
	if forwarded, ok := s.forwardedClientIP(r); ok {
		return forwarded
	}
	// Every forwarded hop is a trusted proxy (or the headers are unusable);
	// attribute the request to the peer we actually accepted it from.
	return peer
}
