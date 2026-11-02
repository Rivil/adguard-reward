// Package ratelimit is the login failure budget: per-IP with an escalating
// lockout, plus a flat global cap.
//
// It is pure policy — no logging, no net/http, an injected clock. Only Fail
// consumes budget, so successful logins and 429s cannot count by
// construction: there is no Success or Rejected method to call.
package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

// Config sets the thresholds. Zero values are filled from Defaults().
type Config struct {
	// PerIPFailures failures inside one Window trip the IP.
	PerIPFailures int
	// Window is the fixed counting window, anchored at the first failure
	// after the previous window or lockout ended (not calendar-aligned).
	Window time.Duration
	// BaseLockout is the first lockout; each further trip doubles it up to
	// MaxLockout.
	BaseLockout time.Duration
	MaxLockout  time.Duration
	// EscalationReset is how long an IP must stay past its last lockout's end
	// without tripping again before the doubling starts over from BaseLockout.
	EscalationReset time.Duration
	// GlobalFailures failures inside one Window, from any IPs, deny every IP
	// until that window ends. It never escalates.
	GlobalFailures int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Defaults are the locked thresholds: 5 failures a minute per IP escalating
// from a 1 m lockout to 1 h, 20 a minute globally.
func Defaults() Config {
	return Config{
		PerIPFailures:   5,
		Window:          time.Minute,
		BaseLockout:     time.Minute,
		MaxLockout:      time.Hour,
		EscalationReset: time.Hour,
		GlobalFailures:  20,
	}
}

// maxShift clamps the doubling exponent so BaseLockout<<shift can never
// overflow however many trips accumulate; 1m<<6 = 64m already exceeds the
// 1 h cap.
const maxShift = 6

type ipState struct {
	failures    int
	windowStart time.Time // zero when no window is open
	trips       int
	lockedUntil time.Time // zero when never locked
}

// Limiter is safe for concurrent use.
type Limiter struct {
	cfg Config
	now func() time.Time

	mu          sync.Mutex
	ips         map[netip.Addr]*ipState
	globalStart time.Time // zero when no global window is open
	globalFails int
}

// New returns a limiter; zero Config fields take their Defaults() value.
func New(cfg Config) *Limiter {
	d := Defaults()
	if cfg.PerIPFailures == 0 {
		cfg.PerIPFailures = d.PerIPFailures
	}
	if cfg.Window == 0 {
		cfg.Window = d.Window
	}
	if cfg.BaseLockout == 0 {
		cfg.BaseLockout = d.BaseLockout
	}
	if cfg.MaxLockout == 0 {
		cfg.MaxLockout = d.MaxLockout
	}
	if cfg.EscalationReset == 0 {
		cfg.EscalationReset = d.EscalationReset
	}
	if cfg.GlobalFailures == 0 {
		cfg.GlobalFailures = d.GlobalFailures
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Limiter{cfg: cfg, now: now, ips: map[netip.Addr]*ipState{}}
}

// Check reports whether ip may attempt a login now. When it may not,
// retryAfter is the time until it may, rounded up to whole seconds and never
// below one second.
func (l *Limiter) Check(ip netip.Addr) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if st := l.ips[ip]; st != nil {
		l.normalize(st, now)
		if now.Before(st.lockedUntil) {
			return false, ceilSeconds(st.lockedUntil.Sub(now))
		}
	}
	if l.globalDenied(now) {
		return false, ceilSeconds(l.globalStart.Add(l.cfg.Window).Sub(now))
	}
	return true, 0
}

// Fail records one bad-credential outcome for ip. Recording the
// PerIPFailures-th failure inside a window trips the IP into its next
// lockout. A failure while the IP is already locked out is ignored.
func (l *Limiter) Fail(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	st := l.ips[ip]
	if st == nil {
		st = &ipState{}
		l.ips[ip] = st
	}
	l.normalize(st, now)
	if now.Before(st.lockedUntil) {
		return
	}

	// Global window, same first-failure anchor; flat.
	if l.globalStart.IsZero() || !now.Before(l.globalStart.Add(l.cfg.Window)) {
		l.globalStart, l.globalFails = now, 0
	}
	l.globalFails++

	if st.windowStart.IsZero() || !now.Before(st.windowStart.Add(l.cfg.Window)) {
		st.windowStart, st.failures = now, 0
	}
	st.failures++
	if st.failures >= l.cfg.PerIPFailures {
		st.trips++
		shift := min(st.trips-1, maxShift)
		st.lockedUntil = now.Add(min(l.cfg.BaseLockout<<shift, l.cfg.MaxLockout))
		st.failures, st.windowStart = 0, time.Time{}
	}

	l.prune(now)
}

// normalize applies the passage of time to st: a lockout that has ended
// clears the failure counter, and one that ended EscalationReset ago (with
// no trip since) forgets the escalation.
func (l *Limiter) normalize(st *ipState, now time.Time) {
	if st.lockedUntil.IsZero() || now.Before(st.lockedUntil) {
		return
	}
	// Only a window opened before the lockout is stale; one opened after it
	// ended is live and must keep counting.
	if !st.windowStart.IsZero() && st.windowStart.Before(st.lockedUntil) {
		st.failures, st.windowStart = 0, time.Time{}
	}
	if now.Sub(st.lockedUntil) >= l.cfg.EscalationReset {
		st.trips = 0
	}
}

func (l *Limiter) globalDenied(now time.Time) bool {
	if l.globalStart.IsZero() || !now.Before(l.globalStart.Add(l.cfg.Window)) {
		return false
	}
	return l.globalFails >= l.cfg.GlobalFailures
}

// Prune drops IPs that carry no state worth keeping: no active lockout, no
// failures in an open window, and no lockout recent enough to still
// escalate. Fail also prunes lazily.
func (l *Limiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(l.now())
}

func (l *Limiter) prune(now time.Time) {
	for ip, st := range l.ips {
		if now.Before(st.lockedUntil) {
			continue
		}
		if !st.windowStart.IsZero() && now.Before(st.windowStart.Add(l.cfg.Window)) {
			continue
		}
		if !st.lockedUntil.IsZero() && now.Sub(st.lockedUntil) < l.cfg.EscalationReset {
			continue
		}
		delete(l.ips, ip)
	}
}

// Len is the number of IPs currently tracked.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips)
}

// ceilSeconds rounds d up to whole seconds, never below one.
func ceilSeconds(d time.Duration) time.Duration {
	s := d / time.Second
	if d%time.Second != 0 {
		s++
	}
	if s < 1 {
		s = 1
	}
	return s * time.Second
}
