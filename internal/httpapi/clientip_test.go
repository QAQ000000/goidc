package httpapi

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
)

func testCIDR(t *testing.T, value string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func testClientIP(t *testing.T, s *Server, remoteAddr string, headers map[string]string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	r.RemoteAddr = remoteAddr
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	return s.clientIP(r)
}

func TestClientIPIgnoresForwardedHeadersWithoutTrustedProxies(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	if got := testClientIP(t, s, "198.51.100.7:5555", map[string]string{"X-Forwarded-For": "203.0.113.9"}); got != "198.51.100.7" {
		t.Fatalf("untrusted X-Forwarded-For was honoured: %q", got)
	}
	if got := testClientIP(t, s, "198.51.100.7:5555", map[string]string{"X-Real-IP": "203.0.113.9"}); got != "198.51.100.7" {
		t.Fatalf("untrusted X-Real-IP was honoured: %q", got)
	}
	// A port-less RemoteAddr is still used verbatim, matching the previous
	// historical behaviour.
	if got := testClientIP(t, s, "198.51.100.7", nil); got != "198.51.100.7" {
		t.Fatalf("port-less peer misparsed: %q", got)
	}
}

func TestClientIPUsesRightmostUntrustedForwardedHop(t *testing.T) {
	s := &Server{cfg: config.Config{TrustedProxyCIDRs: []*net.IPNet{
		testCIDR(t, "127.0.0.1/32"),
		testCIDR(t, "10.0.0.0/8"),
	}}}
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{"rightmost non-trusted hop wins", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"}, "203.0.113.9"},
		{"client-supplied leftmost value ignored", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"single forwarded value", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, "203.0.113.9"},
		{"X-Real-IP fallback", "127.0.0.1:5000", map[string]string{"X-Real-IP": "203.0.113.30"}, "203.0.113.30"},
		{"all hops trusted falls back to peer", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "10.0.0.1, 10.0.0.2"}, "127.0.0.1"},
		{"unparsable hops fall back to peer", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "not-an-address"}, "127.0.0.1"},
		{"untrusted peer keeps its own address", "198.51.100.7:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testClientIP(t, s, tc.remoteAddr, tc.headers); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
	ipv6 := &Server{cfg: config.Config{TrustedProxyCIDRs: []*net.IPNet{testCIDR(t, "2001:db8::/32")}}}
	if got := testClientIP(t, ipv6, "[2001:db8::1]:5000", map[string]string{"X-Forwarded-For": "2001:db8::99, 198.51.100.5"}); got != "198.51.100.5" {
		t.Fatalf("client IP = %q, want 198.51.100.5", got)
	}
}

func TestClientIPBoundsForwardedHops(t *testing.T) {
	s := &Server{cfg: config.Config{TrustedProxyCIDRs: []*net.IPNet{testCIDR(t, "127.0.0.1/32")}}}
	// A spoofed address buried more than maxForwardedHops entries deep is
	// never inspected, so resolution stays bounded on hostile headers.
	value := "198.51.100.99"
	for i := 0; i < maxForwardedHops+2; i++ {
		value += ", 127.0.0.1"
	}
	if got := testClientIP(t, s, "127.0.0.1:5000", map[string]string{"X-Forwarded-For": value}); got != "127.0.0.1" {
		t.Fatalf("out-of-bound hop was honoured: %q", got)
	}
	// With no usable forwarded value the peer is authoritative, and IPv6
	// peers keep their bracketed form stripped.
	s6 := &Server{cfg: config.Config{TrustedProxyCIDRs: []*net.IPNet{testCIDR(t, "2001:db8::/32")}}}
	if got := testClientIP(t, s6, "[2001:db8::1]:5000", nil); got != "2001:db8::1" {
		t.Fatalf("IPv6 peer misparsed: %q", got)
	}
}
