package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver turns a request into a rate limit key. Forwarded headers are
// honoured only when the direct peer is a configured proxy, because any client
// can send them.
type ClientIPResolver struct {
	trusted []netip.Prefix
}

func NewClientIPResolver(trusted []netip.Prefix) ClientIPResolver {
	return ClientIPResolver{trusted: trusted}
}

// TrustsForwardedHeaders reports whether the resolver will read proxy headers.
func (r ClientIPResolver) TrustsForwardedHeaders() bool {
	return len(r.trusted) > 0
}

// ClientIP returns the address to rate limit on. Without trusted proxies it is
// the peer address and every forwarded header is ignored.
func (r ClientIPResolver) ClientIP(request *http.Request) string {
	peer, ok := parseRemoteAddr(request.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !r.trustedPeer(peer) {
		return peer.String()
	}

	// Walk from right to left and take the first address that is not one of our
	// own proxies. Everything further left was supplied by the client and can
	// be forged.
	for _, candidate := range forwardedChain(request) {
		address, err := netip.ParseAddr(candidate)
		if err != nil || r.trustedPeer(address) {
			continue
		}
		return address.String()
	}

	if realIP := strings.TrimSpace(request.Header.Get("X-Real-IP")); realIP != "" {
		if address, err := netip.ParseAddr(realIP); err == nil {
			return address.String()
		}
	}
	return peer.String()
}

func (r ClientIPResolver) trustedPeer(address netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// forwardedChain flattens every X-Forwarded-For header and reverses the result.
func forwardedChain(request *http.Request) []string {
	var chain []string
	for _, value := range request.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				chain = append(chain, trimmed)
			}
		}
	}

	reversed := make([]string, 0, len(chain))
	for index := len(chain) - 1; index >= 0; index-- {
		reversed = append(reversed, chain[index])
	}
	return reversed
}

func parseRemoteAddr(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}
