// Package rules owns the adguard-reward managed region of AdGuard's
// user_rules: splicing it into the parent's list, validating custom-service
// domains and rendering the region's block and exception lines.
package rules

import (
	"errors"
	"fmt"
)

// The managed region is delimited by these exact comment lines (spec
// decision marker_format). AdGuard treats `!` lines as inert comments.
const (
	BeginMarker = "! adguard-reward begin"
	EndMarker   = "! adguard-reward end"
)

// ErrMalformedRegion reports user_rules whose markers do not form exactly one
// begin-before-end pair. Splice refuses to repair it: guessing which lines
// belong to the app could rewrite the parent's own rules (r-02).
var ErrMalformedRegion = errors.New("managed rules region is malformed")

// Splice returns current with the lines between the markers replaced by
// managed. Lines outside the markers are copied verbatim, in order. When no
// marker is present the region is appended at the end. A lone marker, an end
// before its begin or more than one pair yields ErrMalformedRegion and nil.
//
// Markers match whole lines exactly; a line merely containing the marker text
// is an ordinary rule.
func Splice(current, managed []string) ([]string, error) {
	for i, l := range managed {
		if l == BeginMarker || l == EndMarker {
			return nil, fmt.Errorf("managed line %d is a region marker", i)
		}
	}

	begin, end := -1, -1
	begins, ends := 0, 0
	for i, l := range current {
		switch l {
		case BeginMarker:
			begins++
			begin = i
		case EndMarker:
			ends++
			end = i
		}
	}

	switch {
	case begins == 0 && ends == 0:
		out := make([]string, 0, len(current)+len(managed)+2)
		out = append(out, current...)
		out = append(out, BeginMarker)
		out = append(out, managed...)
		return append(out, EndMarker), nil
	case begins == 1 && ends == 1 && begin < end:
		out := make([]string, 0, begin+1+len(managed)+len(current)-end)
		out = append(out, current[:begin+1]...)
		out = append(out, managed...)
		return append(out, current[end:]...), nil
	default:
		return nil, ErrMalformedRegion
	}
}
