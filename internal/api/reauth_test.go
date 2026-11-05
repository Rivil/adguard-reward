package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

// reauthHarness keeps the *API itself so tests can call recheckPassword
// directly and still drive /api/v1/login through the same limiter.
type reauthHarness struct {
	t    *testing.T
	api  *API
	h    http.Handler
	fake *adguardtest.Server
}

func newReauthHarness(t *testing.T) *reauthHarness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	fake := adguardtest.New(t, adguardtest.Options{User: fakeUser, Pass: fakePass})
	client, err := adguard.New(fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := &clock{t: t0}
	cfg := ratelimit.Defaults()
	cfg.Now = c.Now
	a := New(Deps{
		AdGuard:  client,
		ClientIP: ClientIP(nil),
		Log:      log,
		Auth:     auth.New(st, auth.Options{Now: c.Now, Log: log, ErrorWriter: UnauthorizedWriter}),
		Limiter:  ratelimit.New(cfg),
		Sessions: st,
	})
	return &reauthHarness{t: t, api: a, h: a.Handler(), fake: fake}
}

func (h *reauthHarness) recheck(remote, user, pass string) recheck {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pin", nil)
	r.RemoteAddr = remote
	return h.api.recheckPassword(r, user, pass)
}

func (h *reauthHarness) login(remote, user, pass string) *httptest.ResponseRecorder {
	h.t.Helper()
	b, _ := json.Marshal(creds(user, pass))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(string(b)))
	r.RemoteAddr = remote
	r.Header.Set(CSRFHeader, CSRFValue)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func (h *reauthHarness) loginHits() int {
	n := 0
	for _, r := range h.fake.Requests() {
		if r.Path == "/control/login" {
			n++
		}
	}
	return n
}

func TestRecheckPassword_SharedBudget(t *testing.T) {
	h := newReauthHarness(t)
	const peer = "203.0.113.1:4444"
	for i := 1; i <= 5; i++ {
		if res := h.recheck(peer, fakeUser, "nope"); res.outcome != outcomeBadCreds {
			t.Fatalf("wrong re-check %d: outcome %q, want %q", i, res.outcome, outcomeBadCreds)
		}
	}
	if got := h.loginHits(); got != 5 {
		t.Fatalf("5 wrong re-checks made %d /control/login hits, want 5", got)
	}

	res := h.recheck(peer, fakeUser, fakePass)
	if res.outcome != outcomeRateLimited || res.retry < time.Second {
		t.Fatalf("6th re-check = %+v, want rate limited with retry >= 1s", res)
	}
	if res.ip.String() != "203.0.113.1" {
		t.Fatalf("6th re-check charged ip %s, want 203.0.113.1", res.ip)
	}
	if got := h.loginHits(); got != 5 {
		t.Fatalf("rate-limited re-check reached AdGuard: hits %d, want 5", got)
	}

	// Login from the same IP shares the spent budget.
	w := h.login(peer, fakeUser, fakePass)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("login after 5 wrong re-checks: status %d, want 429", w.Code)
	}
	if got := h.loginHits(); got != 5 {
		t.Fatalf("rate-limited login reached AdGuard: hits %d, want 5", got)
	}

	// ...and the other direction: login failures limit the re-check.
	h2 := newReauthHarness(t)
	for range 5 {
		h2.login(peer, fakeUser, "nope")
	}
	if res := h2.recheck(peer, fakeUser, fakePass); res.outcome != outcomeRateLimited {
		t.Fatalf("re-check after 5 wrong logins: outcome %q, want rate limited", res.outcome)
	}
}

func TestRecheckPassword_UpstreamDown(t *testing.T) {
	h := newReauthHarness(t)
	const peer = "203.0.113.1:4444"
	h.fake.SetStatus("/control/login", http.StatusBadGateway)
	for i := 1; i <= 6; i++ {
		if res := h.recheck(peer, fakeUser, fakePass); res.outcome != outcomeUnreachable {
			t.Fatalf("re-check %d against a 502: outcome %q, want %q", i, res.outcome, outcomeUnreachable)
		}
	}
	h.fake.SetResponse("/control/login", 0, nil)
	if res := h.recheck(peer, fakeUser, fakePass); res.outcome != outcomeOK {
		t.Fatalf("correct re-check after 6 upstream failures: outcome %q, want ok", res.outcome)
	}
}

func TestRecheckPassword_Username(t *testing.T) {
	h := newReauthHarness(t)
	h.recheck("203.0.113.1:4444", "dad", "secret")
	var bodies []map[string]string
	for _, r := range h.fake.Requests() {
		if r.Path != "/control/login" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatalf("login body %q: %v", r.Body, err)
		}
		bodies = append(bodies, m)
	}
	if len(bodies) != 1 || bodies[0]["name"] != "dad" || bodies[0]["password"] != "secret" {
		t.Fatalf("/control/login bodies = %v, want one naming dad/secret", bodies)
	}
}

func TestRecheckPassword_Canceled(t *testing.T) {
	h := newReauthHarness(t)
	h.api.loginSlot <- struct{}{} // someone else holds the slot
	defer func() { <-h.api.loginSlot }()

	r := httptest.NewRequest(http.MethodPost, "/api/v1/pin", nil)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	if res := h.api.recheckPassword(r.WithContext(ctx), fakeUser, fakePass); res.outcome != outcomeCanceled {
		t.Fatalf("canceled while queued: outcome %q, want %q", res.outcome, outcomeCanceled)
	}
	if got := h.loginHits(); got != 0 {
		t.Fatalf("canceled re-check reached AdGuard: hits %d", got)
	}
}

// TestLogin_Burst20 repeats the login burst bound after the extraction, at
// 20 parallel wrong logins.
func TestLogin_Burst20(t *testing.T) {
	h := newReauthHarness(t)
	h.fake.Hang("/control/login", 20*time.Millisecond)
	const n = 20
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.login("203.0.113.1:4444", fakeUser, "nope").Code
		}()
	}
	wg.Wait()
	limited := 0
	for _, c := range codes {
		switch c {
		case http.StatusTooManyRequests:
			limited++
		case http.StatusUnauthorized:
		default:
			t.Fatalf("unexpected status %d in burst", c)
		}
	}
	if hits := h.loginHits(); hits > 5 || hits+limited != n {
		t.Fatalf("burst of %d: %d AdGuard hits, %d 429s; want at most 5 hits and the rest 429", n, hits, limited)
	}
}
