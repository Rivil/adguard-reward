package adguard

import (
	"context"
	"net/http"
)

// Service is one entry of AdGuard's built-in blocked-services catalogue.
type Service struct {
	ID   string
	Name string
	Icon string // icon_svg as served by AdGuard (base64 SVG)
}

// Services fetches the catalogue from GET /control/blocked_services/all at
// runtime. Nothing is vendored: whatever AdGuard serves is what comes back.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	var doc struct {
		BlockedServices []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			IconSVG string `json:"icon_svg"`
		} `json:"blocked_services"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/blocked_services/all", nil, &doc); err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(doc.BlockedServices))
	for _, s := range doc.BlockedServices {
		out = append(out, Service{ID: s.ID, Name: s.Name, Icon: s.IconSVG})
	}
	return out, nil
}
