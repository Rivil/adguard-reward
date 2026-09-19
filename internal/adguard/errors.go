package adguard

import (
	"errors"
	"fmt"
)

// ErrBadCredentials is returned when AdGuard rejects a credential: a parent's
// login (400/401/403 on /control/login) or the service credential on any
// other call (401/403). It always wraps the underlying *StatusError.
var ErrBadCredentials = errors.New("adguard: bad credentials")

// ErrRateLimited is returned on 429 — AdGuard's login rate limiter — so a
// throttled attempt is never reported as a wrong password.
var ErrRateLimited = errors.New("adguard: rate limited")

// StatusError is a non-2xx response from AdGuard.
type StatusError struct {
	Method  string
	Path    string
	Code    int
	Snippet string // first bytes of the response body, bounded
}

func (e *StatusError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("adguard: %s %s: status %d", e.Method, e.Path, e.Code)
	}
	return fmt.Sprintf("adguard: %s %s: status %d: %s", e.Method, e.Path, e.Code, e.Snippet)
}
