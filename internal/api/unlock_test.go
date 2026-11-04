package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

func (h *harness) unlock(device string, pin any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	var body any = map[string]any{"pin": pin}
	if s, ok := pin.(rawBody); ok {
		body = string(s)
	}
	if device != "" {
		opts = append([]reqOpt{withDevice(device)}, opts...)
	}
	return h.do(http.MethodPost, "/api/v1/unlock", body, opts...)
}

// rawBody sends the string verbatim as the unlock body.
type rawBody string

func (h *harness) unlockEvents() []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatalf("log line %q: %v", line, err)
		}
		if m["event"] == "unlock" {
			out = append(out, m)
		}
	}
	return out
}

func (h *harness) failedAttempts() int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow(`SELECT failed_attempts FROM devices`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

type unlockReply struct {
	Error             string `json:"error"`
	AttemptsRemaining *int   `json:"attempts_remaining"`
}

func replyOf(t *testing.T, w *httptest.ResponseRecorder) unlockReply {
	t.Helper()
	var r unlockReply
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return r
}

func expectBadPIN(t *testing.T, w *httptest.ResponseRecorder, left int) {
	t.Helper()
	r := replyOf(t, w)
	if w.Code != http.StatusUnauthorized || r.Error != CodeBadPIN || r.AttemptsRemaining == nil || *r.AttemptsRemaining != left {
		t.Fatalf("wrong PIN: %d %s, want 401 bad_pin attempts_remaining %d", w.Code, w.Body.String(), left)
	}
}

func expectRevoked(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusUnauthorized || replyOf(t, w).Error != CodeDeviceRevoked {
		t.Fatalf("%d %s, want 401 device_revoked", w.Code, w.Body.String())
	}
}

func TestUnlock_OK(t *testing.T) {
	h := newHarness(t, nil, nil)
	old, device := h.setPIN("583920")
	h.clock.Advance(16 * time.Minute) // the PIN session has lapsed
	hits := h.loginHits()

	w := h.unlock(device, "583920")
	if w.Code != http.StatusNoContent {
		t.Fatalf("unlock: %d %s", w.Code, w.Body.String())
	}
	sc := sessionCookie(t, w)
	if sc == nil || sc.Value == "" || sc.Value == old {
		t.Fatalf("unlock session cookie = %+v, want a new one", sc)
	}
	me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(sc.Value), withDevice(device))
	if me.Code != http.StatusOK {
		t.Fatalf("GET /me with the unlocked session: %d %s", me.Code, me.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(me.Body.Bytes(), &body)
	if body["username"] != fakeUser {
		t.Fatalf("/me username = %q, want %q", body["username"], fakeUser)
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("unlock hit /control/login: %d -> %d", hits, got)
	}
}

func TestUnlock_SessionTagged(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	h.clock.Advance(16 * time.Minute)
	w := h.unlock(device, "583920")
	sc := sessionCookie(t, w)
	if w.Code != http.StatusNoContent || sc == nil {
		t.Fatalf("unlock: %d", w.Code)
	}
	h.clock.Advance(15 * time.Minute)
	me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(sc.Value), withDevice(device))
	if me.Code != http.StatusUnauthorized || decodeErr(t, me).Error != auth.CodePINRequired {
		t.Fatalf("15m idle after unlock: %d %s, want 401 pin_required", me.Code, me.Body.String())
	}
}

func TestUnlock_FiveStrikes(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	for left := 4; left >= 1; left-- {
		expectBadPIN(t, h.unlock(device, "000000"), left)
	}
	w := h.unlock(device, "000000")
	expectRevoked(t, w)
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("devices after the 5th strike = %d, want 0", n)
	}
	dc := deviceCookie(t, w)
	if dc == nil || dc.MaxAge >= 0 {
		t.Fatalf("5th strike device Set-Cookie = %+v, want a clearing cookie", dc)
	}
	// Even the right PIN cannot bring it back.
	expectRevoked(t, h.unlock(device, "583920"))
}

func TestUnlock_ResetOnSuccess(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	for left := 4; left >= 2; left-- {
		expectBadPIN(t, h.unlock(device, "000000"), left)
	}
	if w := h.unlock(device, "583920"); w.Code != http.StatusNoContent {
		t.Fatalf("correct PIN after 3 wrong: %d %s", w.Code, w.Body.String())
	}
	if n := h.failedAttempts(); n != 0 {
		t.Fatalf("failed_attempts after success = %d, want 0", n)
	}
	expectBadPIN(t, h.unlock(device, "000000"), 4)
}

func TestUnlock_Parallel(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")

	const n = 20
	ws := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ws[i] = h.unlock(device, "000000")
		}()
	}
	close(start)
	wg.Wait()

	var left []int
	revoked := 0
	for _, w := range ws {
		r := replyOf(t, w)
		switch {
		case w.Code == http.StatusUnauthorized && r.Error == CodeBadPIN && r.AttemptsRemaining != nil:
			left = append(left, *r.AttemptsRemaining)
		case w.Code == http.StatusUnauthorized && r.Error == CodeDeviceRevoked:
			revoked++
		default:
			t.Fatalf("unexpected reply %d %s", w.Code, w.Body.String())
		}
	}
	sort.Ints(left)
	if fmt.Sprint(left) != "[1 2 3 4]" || revoked != 16 {
		t.Fatalf("20 parallel wrong unlocks: bad_pin %v and %d device_revoked; want [1 2 3 4] and 16", left, revoked)
	}
	if c := h.deviceCount(); c != 0 {
		t.Fatalf("devices after the parallel burst = %d, want 0", c)
	}
}

func TestUnlock_RevokeKillsSession(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session), withDevice(device)); w.Code != http.StatusOK {
		t.Fatalf("precondition: live PIN session: %d", w.Code)
	}
	for range auth.MaxPINAttempts {
		h.unlock(device, "000000")
	}
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session), withDevice(device)); w.Code != http.StatusUnauthorized {
		t.Fatalf("tagged session after the 5th strike: %d %s, want 401", w.Code, w.Body.String())
	}
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session)); w.Code != http.StatusUnauthorized {
		t.Fatalf("tagged session (no device cookie) after the 5th strike: %d, want 401", w.Code)
	}
}

func TestUnlock_DeviceLifetime(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	const day = 24 * time.Hour

	h.clock.Advance(89*day + 23*time.Hour)
	w := h.unlock(device, "583920")
	if w.Code != http.StatusNoContent {
		t.Fatalf("unlock at device age 89d23h: %d %s", w.Code, w.Body.String())
	}
	var expires int64
	if err := h.store.DB().QueryRow(`SELECT expires_at FROM devices`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if want := h.clock.Now().Add(90 * day).Unix(); expires != want {
		t.Fatalf("device expires_at = %d, want now+90d = %d", expires, want)
	}
	dc := deviceCookie(t, w)
	if dc == nil || dc.Value != device || dc.MaxAge != int(90*day/time.Second) {
		t.Fatalf("device cookie on unlock = %+v, want the same token with Max-Age 90d", dc)
	}

	h.clock.Advance(90*day + time.Second)
	expectRevoked(t, h.unlock(device, "583920"))
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("expired device row survived: %d", n)
	}
}

func TestUnlock_BadBody(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	for name, body := range map[string]rawBody{
		"malformed": `{"pin":`,
		"oversize":  rawBody(`{"pin":"` + strings.Repeat("1", 5000) + `"}`),
		"number":    `{"pin":583920}`,
		"missing":   `{}`,
	} {
		before := len(h.unlockEvents())
		w := h.unlock(device, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
		if n := h.failedAttempts(); n != 0 {
			t.Errorf("%s consumed an attempt: failed_attempts %d", name, n)
		}
		ev := h.unlockEvents()[before:]
		if len(ev) != 1 || ev[0]["outcome"] != unlockBadRequest || ev[0]["username"] != fakeUser {
			t.Errorf("%s: unlock log lines %v, want one bad_request for %s", name, ev, fakeUser)
		}
	}

	// No device cookie: username "".
	before := len(h.unlockEvents())
	if w := h.unlock("", rawBody(`{"pin":`)); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed, no device: %d", w.Code)
	}
	if ev := h.unlockEvents()[before:]; len(ev) != 1 || ev[0]["outcome"] != unlockBadRequest || ev[0]["username"] != "" {
		t.Fatalf("malformed, no device: log %v, want one bad_request with username \"\"", ev)
	}

	// A well-formed body with a non-digit PIN is a wrong PIN, not a bad body.
	expectBadPIN(t, h.unlock(device, "12a4"), 4)
	if n := h.failedAttempts(); n != 1 {
		t.Fatalf("\"12a4\" consumed %d attempts, want 1", n)
	}
}

func TestUnlock_Log(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	h := newHarness(t, []*net.IPNet{loopback}, nil)
	via := []reqOpt{from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.7")}
	session, device := h.setPIN("583920")
	unknown := strings.Repeat("A", 43) // well-formed: 32 zero bytes, no such device

	var newSession string
	steps := []struct {
		name, username, outcome string
		send                    func() *httptest.ResponseRecorder
	}{
		{"ok", fakeUser, unlockOK, func() *httptest.ResponseRecorder {
			w := h.unlock(device, "583920", via...)
			if c := sessionCookie(t, w); c != nil {
				newSession = c.Value
			}
			return w
		}},
		{"bad_pin", fakeUser, unlockBadPIN, func() *httptest.ResponseRecorder { return h.unlock(device, "000000", via...) }},
		{"no_device", "", unlockNoDevice, func() *httptest.ResponseRecorder { return h.unlock("", "583920", via...) }},
		{"malformed cookie", "", unlockNoDevice, func() *httptest.ResponseRecorder { return h.unlock("short", "583920", via...) }},
		{"unknown token", "", unlockRevoked, func() *httptest.ResponseRecorder { return h.unlock(unknown, "583920", via...) }},
		{"bad_request", fakeUser, unlockBadRequest, func() *httptest.ResponseRecorder { return h.unlock(device, rawBody(`nope`), via...) }},
		{"bad_pin 2", fakeUser, unlockBadPIN, func() *httptest.ResponseRecorder { return h.unlock(device, "000000", via...) }},
		{"bad_pin 3", fakeUser, unlockBadPIN, func() *httptest.ResponseRecorder { return h.unlock(device, "000000", via...) }},
		{"bad_pin 4", fakeUser, unlockBadPIN, func() *httptest.ResponseRecorder { return h.unlock(device, "000000", via...) }},
		{"5th strike", fakeUser, unlockRevoked, func() *httptest.ResponseRecorder { return h.unlock(device, "000000", via...) }},
	}
	for _, s := range steps {
		before := len(h.unlockEvents())
		s.send()
		ev := h.unlockEvents()[before:]
		if len(ev) != 1 {
			t.Fatalf("%s: %d unlock lines, want 1: %v", s.name, len(ev), ev)
		}
		e := ev[0]
		if e["level"] != "INFO" || e["username"] != s.username || e["outcome"] != s.outcome || e["ip"] != "198.51.100.7" {
			t.Errorf("%s: line %v, want INFO {username %q, outcome %q, ip 198.51.100.7}", s.name, e, s.username, s.outcome)
		}
		for _, k := range []string{"event", "username", "ip", "outcome"} {
			if _, ok := e[k]; !ok {
				t.Errorf("%s: line lacks %s: %v", s.name, k, e)
			}
		}
	}
	if newSession == "" {
		t.Fatal("positive control: ok step set no session cookie")
	}

	var stripped strings.Builder
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(line), &m)
		delete(m, "time")
		b, _ := json.Marshal(m)
		stripped.Write(b)
	}
	for _, secret := range []string{"583920", "000000", device, newSession, session} {
		if strings.Contains(stripped.String(), secret) {
			t.Errorf("log contains a secret %q", secret)
		}
	}
}

func TestUnlock_BadBodyDeadCookieCleared(t *testing.T) {
	h := newHarness(t, nil, nil)
	unknown := strings.Repeat("A", 43)
	w := h.unlock(unknown, rawBody(`{"pin":`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body, dead cookie: %d, want 400", w.Code)
	}
	if dc := deviceCookie(t, w); dc == nil || dc.MaxAge >= 0 {
		t.Fatalf("bad body, dead cookie: device Set-Cookie %+v, want a clearing cookie", dc)
	}
}

func TestUnlock_StoreError(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	before := len(h.unlockEvents())
	w := h.unlock(device, "583920")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("closed store: %d %s, want 500", w.Code, w.Body.String())
	}
	ev := h.unlockEvents()[before:]
	if len(ev) != 1 || ev[0]["outcome"] != unlockError {
		t.Fatalf("closed store: unlock lines %v, want one with outcome error", ev)
	}
	if dc := deviceCookie(t, w); dc != nil {
		t.Fatalf("store error cleared the device cookie: %+v", dc)
	}
}

func TestUnlock_SpentDeviceRevokedOnSight(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	// A 5th strike whose revoke failed: counter at the cap, row and tagged
	// session still present.
	if _, err := h.store.DB().Exec(`UPDATE devices SET failed_attempts = 5`); err != nil {
		t.Fatal(err)
	}
	expectRevoked(t, h.unlock(device, "583920"))
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("spent device row survived: %d", n)
	}
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session)); w.Code != http.StatusUnauthorized {
		t.Fatalf("spent device's tagged session still live: %d", w.Code)
	}
}
