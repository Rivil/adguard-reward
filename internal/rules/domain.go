package rules

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidDomain is wrapped by every NormalizeDomain rejection.
var ErrInvalidDomain = errors.New("not a plain hostname")

const (
	maxDomainLen = 253
	maxLabelLen  = 63
)

// NormalizeDomain returns d as a lowercase hostname without a trailing dot,
// or an error wrapping ErrInvalidDomain that names d. Only plain ASCII
// letter-digit-hyphen hostnames of two or more labels are accepted (punycode
// xn-- labels included): schemes, paths, ports, wildcards, whitespace and any
// filter-rule syntax are rejected, so a stored domain can never smuggle rule
// text into user_rules (r-04).
func NormalizeDomain(d string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("domain %q: %w (%s)", d, ErrInvalidDomain, why)
	}
	if d == "" {
		return bad("empty")
	}
	for i := 0; i < len(d); i++ {
		if d[i] >= 0x80 {
			return bad("non-ASCII; use the xn-- punycode form")
		}
	}
	h := strings.ToLower(strings.TrimSuffix(d, "."))
	if len(h) > maxDomainLen {
		return bad("longer than 253 characters")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return bad("needs at least two labels, like example.com")
	}
	for _, l := range labels {
		if l == "" {
			return bad("empty label")
		}
		if len(l) > maxLabelLen {
			return bad("label longer than 63 characters")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return bad("label starts or ends with a hyphen")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return bad(fmt.Sprintf("character %q is not allowed", c))
			}
		}
	}
	return h, nil
}
