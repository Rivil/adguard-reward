package adguard

import (
	"cmp"
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Lease is one DHCP lease from AdGuard's own DHCP server, dynamic or static.
type Lease struct {
	MAC      string
	IP       string
	Hostname string
}

// Leases reads GET /control/dhcp/status and returns the dynamic leases
// followed by the static ones. Never nil. A failed read returns the error —
// callers decide whether a missing lease table is fatal.
func (c *Client) Leases(ctx context.Context) ([]Lease, error) {
	type wire struct {
		MAC      string `json:"mac"`
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}
	var doc struct {
		Leases       []wire `json:"leases"`
		StaticLeases []wire `json:"static_leases"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/dhcp/status", nil, &doc); err != nil {
		return nil, err
	}
	out := make([]Lease, 0, len(doc.Leases)+len(doc.StaticLeases))
	for _, l := range append(doc.Leases, doc.StaticLeases...) {
		out = append(out, Lease(l))
	}
	return out, nil
}

// UnlabelledDevice is an auto client no persistent client covers: a device
// AdGuard has seen that a parent has not named yet.
type UnlabelledDevice struct {
	IP       string
	Hostname string
	Source   string
	// SuggestedIDs is [IP], or [IP, MAC] when AdGuard's DHCP server leased
	// the IP — a MAC survives the next lease change, an IP may not.
	SuggestedIDs []string
}

// Unlabelled returns every auto client not covered by a persistent client,
// whatever its source (locked: unlabelled_listing). An auto client is
// covered when a persistent id is the same address (compared canonically,
// so IPv6 spellings match), a CIDR containing it, or the MAC its IP is
// leased to. The result is sorted by case-folded hostname, blank hostnames
// last, ties broken by IP. Never nil.
func Unlabelled(persistent []PersistentClient, auto []AutoClient, leases []Lease) []UnlabelledDevice {
	addrs := map[netip.Addr]bool{}
	var prefixes []netip.Prefix
	others := map[string]bool{} // MACs (canonical) and ClientIDs
	for _, pc := range persistent {
		for _, id := range pc.IDs {
			id = strings.TrimSpace(id)
			if a, err := netip.ParseAddr(id); err == nil {
				addrs[a.Unmap()] = true
			} else if p, err := netip.ParsePrefix(id); err == nil {
				prefixes = append(prefixes, p.Masked())
			} else {
				others[canonicalMAC(id)] = true
			}
		}
	}

	leasedTo := map[string]string{} // canonical IP -> canonical MAC
	for _, l := range leases {
		if l.MAC != "" {
			leasedTo[canonicalIP(l.IP)] = canonicalMAC(l.MAC)
		}
	}

	out := []UnlabelledDevice{}
	for _, a := range auto {
		mac := leasedTo[canonicalIP(a.IP)]
		if covered(a.IP, addrs, prefixes) || (mac != "" && others[mac]) {
			continue
		}
		ids := []string{a.IP}
		if mac != "" {
			ids = append(ids, mac)
		}
		out = append(out, UnlabelledDevice{IP: a.IP, Hostname: a.Hostname, Source: a.Source, SuggestedIDs: ids})
	}

	slices.SortStableFunc(out, func(x, y UnlabelledDevice) int {
		xb, yb := x.Hostname == "", y.Hostname == ""
		if xb != yb {
			if xb {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(strings.ToLower(x.Hostname), strings.ToLower(y.Hostname)); c != 0 {
			return c
		}
		return compareIP(x.IP, y.IP)
	})
	return out
}

func covered(ip string, addrs map[netip.Addr]bool, prefixes []netip.Prefix) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	a = a.Unmap()
	if addrs[a] {
		return true
	}
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// canonicalIP renders an address one way whatever its spelling; anything
// that does not parse is returned as given.
func canonicalIP(s string) string {
	if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
		return a.Unmap().String()
	}
	return s
}

// canonicalMAC renders a hardware address lower-case with colons; anything
// that does not parse (a ClientID, say) is lower-cased as given.
func canonicalMAC(s string) string {
	if hw, err := net.ParseMAC(strings.TrimSpace(s)); err == nil {
		return hw.String()
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// compareIP orders parseable addresses numerically before anything else,
// which falls back to a string compare.
func compareIP(x, y string) int {
	xa, xerr := netip.ParseAddr(x)
	ya, yerr := netip.ParseAddr(y)
	switch {
	case xerr == nil && yerr == nil:
		return xa.Compare(ya)
	case xerr == nil:
		return -1
	case yerr == nil:
		return 1
	}
	return cmp.Compare(x, y)
}
