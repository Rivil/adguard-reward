package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func nets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func want(t *testing.T, got netip.Addr, s string) {
	t.Helper()
	if s == "" {
		if got.IsValid() {
			t.Fatalf("got %s, want invalid Addr", got)
		}
		return
	}
	if got != netip.MustParseAddr(s) {
		t.Fatalf("got %s, want %s", got, s)
	}
}

func TestClientIP_Spoof(t *testing.T) {
	ip := ClientIP(nil)
	want(t, ip(req("203.0.113.9:4444", "1.2.3.4")), "203.0.113.9")
	want(t, ip(req("203.0.113.9:4444")), "203.0.113.9")
}

func TestClientIP_Proxied(t *testing.T) {
	ip := ClientIP(nets(t, "10.0.0.0/8"))
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"first untrusted hop from the right", "10.0.0.5:1", []string{"1.2.3.4, 5.6.7.8, 10.0.0.7"}, "5.6.7.8"},
		{"all hops trusted", "10.0.0.5:1", []string{"10.0.0.7"}, "10.0.0.5"},
		{"garbage hop falls back to peer", "10.0.0.5:1", []string{"garbage, 10.0.0.7"}, "10.0.0.5"},
		{"untrusted peer ignores XFF", "198.51.100.2:1", []string{"1.2.3.4, 5.6.7.8, 10.0.0.7"}, "198.51.100.2"},
		{"two header lines", "10.0.0.5:1", []string{"1.2.3.4", "5.6.7.8"}, "5.6.7.8"},
		{"no header", "10.0.0.5:1", nil, "10.0.0.5"},
		{"empty header", "10.0.0.5:1", []string{""}, "10.0.0.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want(t, ip(req(c.remote, c.xff...)), c.want)
		})
	}
}

func TestClientIP_V6(t *testing.T) {
	want(t, ClientIP(nil)(req("[2001:db8::1]:5")), "2001:db8::1")
	want(t, ClientIP(nil)(req("[fe80::1%eth0]:5")), "fe80::1")

	ip := ClientIP(nets(t, "10.0.0.0/8"))
	want(t, ip(req("[::ffff:10.0.0.2]:80", "1.2.3.4")), "1.2.3.4")
	want(t, ip(req("[::ffff:10.0.0.2]:80")), "10.0.0.2")
}

func TestClientIP_Malformed(t *testing.T) {
	for _, remote := range []string{"pipe", "nonsense", "", ":80", "[::1"} {
		want(t, ClientIP(nets(t, "10.0.0.0/8"))(req(remote, "1.2.3.4")), "")
		want(t, ClientIP(nil)(req(remote)), "")
	}
}

func TestClientIP_BareIPNet(t *testing.T) {
	// A /32 built from a bare IP (as config.parseCIDR does) trusts exactly
	// that peer.
	single := &net.IPNet{IP: net.ParseIP("10.0.0.5").To4(), Mask: net.CIDRMask(32, 32)}
	ip := ClientIP([]*net.IPNet{single})
	want(t, ip(req("10.0.0.5:1", "1.2.3.4")), "1.2.3.4")
	want(t, ip(req("10.0.0.6:1", "1.2.3.4")), "10.0.0.6")
}
