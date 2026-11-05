package api

import (
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
)

// outcomeCanceled means the client went away while queued for the login
// slot; the caller writes nothing.
const outcomeCanceled = "canceled"

// recheck is the result of one password check against AdGuard.
type recheck struct {
	// outcome is outcomeOK, outcomeBadCreds, outcomeUnreachable,
	// outcomeRateLimited or outcomeCanceled.
	outcome string
	// ip is the trusted-proxy-resolved client IP the budget was charged to.
	ip netip.Addr
	// retry is set for outcomeRateLimited.
	retry time.Duration
}

// recheckPassword checks username/password against AdGuard's /control/login
// under the login rate limit: the same per-IP and global budget, the same
// in-flight slot. Login and PIN change/remove both go through here so a
// guesser cannot get a second budget by switching endpoints.
//
// The limiter is consulted twice: once before queueing for the slot (fast
// refusal, no AdGuard call) and again while holding it, so a burst that
// arrived together cannot all pass the first check before any failure is
// counted. The slot is released only after Fail has run. Only a wrong
// password spends budget; an unreachable or erroring AdGuard does not.
//
// username is always the caller's choice — the login form's field, or the
// session's own username for a re-check — never read from r here.
func (a *API) recheckPassword(r *http.Request, username, password string) recheck {
	ip := a.deps.ClientIP(r)
	if ok, retry := a.deps.Limiter.Check(ip); !ok {
		return recheck{outcome: outcomeRateLimited, ip: ip, retry: retry}
	}

	select {
	case a.loginSlot <- struct{}{}:
	case <-r.Context().Done():
		return recheck{outcome: outcomeCanceled, ip: ip}
	}
	defer func() { <-a.loginSlot }()

	if ok, retry := a.deps.Limiter.Check(ip); !ok {
		return recheck{outcome: outcomeRateLimited, ip: ip, retry: retry}
	}

	err := a.deps.AdGuard.Login(r.Context(), username, password)
	switch {
	case err == nil:
		return recheck{outcome: outcomeOK, ip: ip}
	case errors.Is(err, adguard.ErrBadCredentials):
		a.deps.Limiter.Fail(ip)
		return recheck{outcome: outcomeBadCreds, ip: ip}
	default:
		// 5xx, transport failure, or AdGuard's own 429: no budget consumed
		// — the parent did nothing wrong.
		a.log.Warn("adguard login unavailable", "err", err)
		return recheck{outcome: outcomeUnreachable, ip: ip}
	}
}
