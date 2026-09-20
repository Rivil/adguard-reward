package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns a resolver for the client address behind a request.
//
// By default the client is the TCP peer and X-Forwarded-For is ignored, so
// nothing on the wire can pick its own address for the login limiter. When
// trusted is non-empty and the peer is inside one of its networks, the
// X-Forwarded-For hops are walked right to left and the first hop outside
// every trusted network is the client; if every hop is trusted, or a hop does
// not parse, the peer is used. An unparseable RemoteAddr yields the zero
// (invalid) Addr rather than a panic — callers key the limiter on it as-is.
func ClientIP(trusted []*net.IPNet) func(*http.Request) netip.Addr {
	prefixes := toPrefixes(trusted)
	return func(r *http.Request) netip.Addr {
		peer := parseAddr(hostOf(r.RemoteAddr))
		if len(prefixes) == 0 || !peer.IsValid() || !inAny(prefixes, peer) {
			return peer
		}
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := parseAddr(strings.TrimSpace(hops[i]))
			if !hop.IsValid() {
				return peer
			}
			if !inAny(prefixes, hop) {
				return hop
			}
		}
		return peer
	}
}

// hostOf strips the port from a host:port (and the brackets from an IPv6
// literal); a bare value with no port is returned as-is.
func hostOf(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// parseAddr canonicalises an address: zone dropped, 4-in-6 unmapped. Anything
// that does not parse is the invalid Addr.
func parseAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.WithZone("").Unmap()
}

func toPrefixes(nets []*net.IPNet) []netip.Prefix {
	var out []netip.Prefix
	for _, n := range nets {
		addr, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		bits, _ := n.Mask.Size()
		if addr.Is4In6() && bits >= 96 {
			bits -= 96
		}
		p, err := addr.Unmap().Prefix(bits)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

func inAny(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
