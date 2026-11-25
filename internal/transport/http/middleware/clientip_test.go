package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()

	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			t.Fatalf("parse prefix %q: %v", value, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func requestFrom(remoteAddr string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers", nil)
	request.RemoteAddr = remoteAddr
	return request
}

// Without configured proxies every forwarded header is attacker-controlled and
// must be ignored.
func TestClientIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	resolver := NewClientIPResolver(nil)

	request := requestFrom("198.51.100.9:443")
	request.Header.Set("X-Forwarded-For", "1.1.1.1")
	request.Header.Set("X-Real-IP", "2.2.2.2")

	if got := resolver.ClientIP(request); got != "198.51.100.9" {
		t.Fatalf("ClientIP() = %q, want the peer address", got)
	}
	if resolver.TrustsForwardedHeaders() {
		t.Fatal("resolver claims to trust forwarded headers without configuration")
	}
}

func TestClientIPReadsForwardedChainFromTrustedProxy(t *testing.T) {
	resolver := NewClientIPResolver(mustPrefixes(t, "10.0.0.0/8"))

	request := requestFrom("10.0.0.5:443")
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.4")

	if got := resolver.ClientIP(request); got != "203.0.113.7" {
		t.Fatalf("ClientIP() = %q, want the client address behind the proxy", got)
	}
}

// The leftmost entry is whatever the client sent, so a spoofed value must not
// win over the address the trusted proxy appended.
func TestClientIPRejectsSpoofedLeftmostEntry(t *testing.T) {
	resolver := NewClientIPResolver(mustPrefixes(t, "10.0.0.0/8"))

	request := requestFrom("10.0.0.5:443")
	request.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.7, 10.0.0.4")

	if got := resolver.ClientIP(request); got != "203.0.113.7" {
		t.Fatalf("ClientIP() = %q, want the address appended by the proxy", got)
	}
}

func TestClientIPUsesRealIPOnlyFromTrustedProxy(t *testing.T) {
	trusted := NewClientIPResolver(mustPrefixes(t, "10.0.0.0/8"))
	request := requestFrom("10.0.0.5:443")
	request.Header.Set("X-Real-IP", "203.0.113.9")
	if got := trusted.ClientIP(request); got != "203.0.113.9" {
		t.Fatalf("ClientIP() = %q, want X-Real-IP from a trusted peer", got)
	}

	untrusted := NewClientIPResolver(nil)
	request = requestFrom("198.51.100.9:443")
	request.Header.Set("X-Real-IP", "203.0.113.9")
	if got := untrusted.ClientIP(request); got != "198.51.100.9" {
		t.Fatalf("ClientIP() = %q, want the peer address", got)
	}
}

func TestClientIPHandlesIPv6AndMissingPort(t *testing.T) {
	resolver := NewClientIPResolver(nil)

	if got := resolver.ClientIP(requestFrom("[2001:db8::1]:443")); got != "2001:db8::1" {
		t.Fatalf("ClientIP() = %q, want the IPv6 peer address", got)
	}
	if got := resolver.ClientIP(requestFrom("not-an-address")); got != "unknown" {
		t.Fatalf("ClientIP() = %q, want unknown for an unparsable peer", got)
	}
}
