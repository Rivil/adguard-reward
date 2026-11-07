package adguard

import (
	"context"
	"net/http"
	"slices"
)

// EditUserRules is the one way the app changes AdGuard's custom filtering
// rules: under a dedicated mutex it reads GET /control/filtering/status
// .user_rules (null reads as []), hands fn a copy, and posts fn's result to
// POST /control/filtering/set_rules ({"rules": [...]}, never null) only when
// it differs line-for-line from what was read. Lines go back byte-for-byte;
// nothing is trimmed or reordered here.
//
// A failed read never calls fn, and an fn error posts nothing: both return
// the error. changed reports whether set_rules was accepted. A non-2xx write
// is a *StatusError (400 is *InputRejectedError).
func (c *Client) EditUserRules(ctx context.Context, fn func(current []string) ([]string, error)) (changed bool, err error) {
	c.rulesMu.Lock()
	defer c.rulesMu.Unlock()

	var st struct {
		UserRules []string `json:"user_rules"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/filtering/status", nil, &st); err != nil {
		return false, err
	}
	current := st.UserRules
	if current == nil {
		current = []string{}
	}
	next, err := fn(slices.Clone(current))
	if err != nil {
		return false, err
	}
	if next == nil {
		next = []string{}
	}
	if slices.Equal(current, next) {
		return false, nil
	}
	body := struct {
		Rules []string `json:"rules"`
	}{next}
	if err := rejected(c.do(ctx, http.MethodPost, "/control/filtering/set_rules", body, nil)); err != nil {
		return false, err
	}
	return true, nil
}
