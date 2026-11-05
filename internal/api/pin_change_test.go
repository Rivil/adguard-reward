package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// latestSessionDeviceID is quote(device_id) of the newest session row.
func (h *harness) latestSessionDeviceID() string {
	h.t.Helper()
	var v string
	if err := h.store.DB().QueryRow(`SELECT quote(device_id) FROM sessions ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) loginBodies() []map[string]string {
	h.t.Helper()
	var out []map[string]string
	for _, r := range h.fake.Requests() {
		if r.Path != "/control/login" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(r.Body, &m); err != nil {
			h.t.Fatalf("login body %q: %v", r.Body, err)
		}
		out = append(out, m)
	}
	return out
}

func change(h *harness, session, device string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/pin", body, withCookie(session), withDevice(device))
}

func remove(h *harness, session, device string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodDelete, "/api/v1/pin", body, withCookie(session), withDevice(device))
}

func TestChangePIN_OK(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	if _, err := h.store.DB().Exec(`UPDATE devices SET failed_attempts = 3`); err != nil {
		t.Fatal(err)
	}
	before := h.pinHashes()[0]
	hits := h.loginHits()

	w := change(h, session, device, map[string]string{"pin": "111111", "password": fakePass})
	if w.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	after := h.pinHashes()
	if len(after) != 1 || after[0] == before {
		t.Fatalf("pin_hash unchanged after change")
	}
	if ok, err := auth.VerifyPIN("111111", after[0]); !ok || err != nil {
		t.Fatalf("new hash does not verify the new PIN: %v %v", ok, err)
	}
	if ok, _ := auth.VerifyPIN("583920", after[0]); ok {
		t.Fatal("new hash still verifies the old PIN")
	}
	if n := h.failedAttempts(); n != 0 {
		t.Fatalf("failed_attempts after change = %d, want 0", n)
	}
	if dc := deviceCookie(t, w); dc != nil {
		t.Fatalf("change rewrote the device cookie: %+v", dc)
	}
	if got := h.loginHits(); got != hits+1 {
		t.Fatalf("/control/login hits %d -> %d, want exactly +1", hits, got)
	}
	// The old device cookie unlocks with the new PIN.
	h.clock.Advance(16 * time.Minute)
	if w := h.unlock(device, "111111"); w.Code != http.StatusNoContent {
		t.Fatalf("unlock with the new PIN and the kept cookie: %d", w.Code)
	}
}

func TestChangePIN_WrongPassword(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	before := h.pinHashes()

	for i := 1; i <= 5; i++ {
		var w *httptest.ResponseRecorder
		if i%2 == 1 {
			w = change(h, session, device, map[string]string{"pin": "111111", "password": "nope"})
		} else {
			w = remove(h, session, device, map[string]string{"password": "nope"})
		}
		if w.Code != http.StatusForbidden || decodeErr(t, w).Error != CodeBadCredentials {
			t.Fatalf("wrong password #%d: %d %s, want 403 bad_credentials", i, w.Code, w.Body.String())
		}
	}
	if after := h.pinHashes(); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("device row changed after wrong passwords: %v -> %v", before, after)
	}
	hits := h.loginHits()
	w := change(h, session, device, map[string]string{"pin": "111111", "password": fakePass})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: %d %s, want 429", w.Code, w.Body.String())
	}
	if ra, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q, want numeric >= 1", w.Header().Get("Retry-After"))
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("rate-limited change reached AdGuard: %d -> %d", hits, got)
	}
	// The same budget limits login.
	if w := h.login(fakeUser, fakePass); w.Code != http.StatusTooManyRequests {
		t.Fatalf("login after 5 wrong re-checks: %d, want 429", w.Code)
	}
}

func TestChangePIN_MissingPassword(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	hits := h.loginHits()
	for name, w := range map[string]*httptest.ResponseRecorder{
		"change, no key": change(h, session, device, map[string]string{"pin": "111111"}),
		"change, empty":  change(h, session, device, map[string]string{"pin": "111111", "password": ""}),
		"remove, no key": remove(h, session, device, map[string]string{}),
		"remove, empty":  remove(h, session, device, map[string]string{"password": ""}),
	} {
		if w.Code != http.StatusForbidden || decodeErr(t, w).Error != CodeReauthRequired {
			t.Errorf("%s: %d %s, want 403 reauth_required", name, w.Code, w.Body.String())
		}
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("missing password reached AdGuard: %d -> %d", hits, got)
	}
	if n := h.deviceCount(); n != 1 {
		t.Fatalf("device rows = %d, want 1", n)
	}
}

func TestChangePIN_Username(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	n := len(h.loginBodies())

	if w := change(h, session, device, map[string]string{"pin": "111111", "password": fakePass}); w.Code != http.StatusNoContent {
		t.Fatalf("change: %d", w.Code)
	}
	bodies := h.loginBodies()[n:]
	if len(bodies) != 1 || bodies[0]["name"] != fakeUser {
		t.Fatalf("re-check /control/login bodies = %v, want one naming %q", bodies, fakeUser)
	}

	n = len(h.loginBodies())
	for name, w := range map[string]*httptest.ResponseRecorder{
		"change": change(h, session, device, map[string]string{"pin": "222222", "password": fakePass, "username": "dad"}),
		"remove": remove(h, session, device, map[string]string{"password": fakePass, "username": "dad"}),
	} {
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s with a username key: %d, want 400", name, w.Code)
		}
	}
	if got := len(h.loginBodies()); got != n {
		t.Fatalf("a username-keyed body reached AdGuard")
	}
}

func TestRemovePIN(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")

	w := remove(h, session, device, map[string]string{"password": fakePass})
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("device rows after remove = %d, want 0", n)
	}
	if dc := deviceCookie(t, w); !clearing(dc) {
		t.Fatalf("device Set-Cookie after remove = %+v, want a clearing cookie", dc)
	}
	if v := h.latestSessionDeviceID(); v != "NULL" {
		t.Fatalf("session device_id after remove = %s, want NULL", v)
	}

	h.clock.Advance(16 * time.Minute)
	me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session))
	if me.Code != http.StatusOK {
		t.Fatalf("GET /me at +16m after remove: %d %s, want 200", me.Code, me.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(me.Body.Bytes(), &body)
	exp, err := time.Parse(time.RFC3339, body["expires_at"])
	if want := h.clock.Now().Add(30 * 24 * time.Hour); err != nil || exp.Sub(want).Abs() > time.Minute {
		t.Fatalf("expires_at = %q, want about now+30d (%v)", body["expires_at"], want)
	}
	st := h.do(http.MethodGet, "/api/v1/pin", nil, withCookie(session))
	if st.Code != http.StatusOK || st.Body.String() != "{\"has_pin\":false}\n" {
		t.Fatalf("GET /api/v1/pin after remove: %d %q, want {has_pin:false}", st.Code, st.Body.String())
	}
}

func TestRemovePIN_OtherTaggedSessionGoes(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	// A second session on the same device, from an unlock.
	h.clock.Advance(time.Minute)
	w := h.unlock(device, "583920")
	other := sessionCookie(t, w)
	if w.Code != http.StatusNoContent || other == nil {
		t.Fatalf("unlock: %d", w.Code)
	}
	if w := remove(h, session, device, map[string]string{"password": fakePass}); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", w.Code)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(other.Value)); me.Code != http.StatusUnauthorized {
		t.Fatalf("the device's other tagged session after remove: %d, want 401 (not detached to 30d)", me.Code)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session)); me.Code != http.StatusOK {
		t.Fatalf("the remover's own session after remove: %d, want 200", me.Code)
	}
}

func TestRemovePIN_NoDevice(t *testing.T) {
	h := newHarness(t, nil, nil)
	session := h.loginOK()
	hits := h.loginHits()
	w := h.do(http.MethodDelete, "/api/v1/pin", map[string]string{"password": fakePass}, withCookie(session))
	if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Fatalf("remove on an untagged session: %d %s, want 404 not_found", w.Code, w.Body.String())
	}
	if h.loginHits() != hits {
		t.Fatal("remove without a PIN reached AdGuard")
	}
}
