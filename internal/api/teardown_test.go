package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// seedDevice inserts a device for username directly, with a fresh token,
// and returns its id and raw token.
func (h *harness) seedDevice(username string) (int64, string) {
	h.t.Helper()
	hash, err := auth.HashPIN("9999")
	if err != nil {
		h.t.Fatal(err)
	}
	mgr := auth.New(h.store, auth.Options{Now: h.clock.Now, Devices: h.store})
	raw, d, err := mgr.IssueDevice(context.Background(), username, hash)
	if err != nil {
		h.t.Fatal(err)
	}
	return d.ID, raw
}

func (h *harness) deviceIDs(username string) []int64 {
	h.t.Helper()
	rows, err := h.store.DB().Query(`SELECT id FROM devices WHERE username = ? ORDER BY id`, username)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func clearing(c *http.Cookie) bool { return c != nil && c.MaxAge < 0 }

func TestLogout_PinDevice(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")

	w := h.do(http.MethodPost, "/api/v1/logout", nil, withCookie(session), withDevice(device))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("devices after logout = %d, want 0 (device and pin_hash gone)", n)
	}
	if sc := sessionCookie(t, w); !clearing(sc) {
		t.Fatalf("session Set-Cookie = %+v, want exactly one clearing cookie", sc)
	}
	if dc := deviceCookie(t, w); !clearing(dc) {
		t.Fatalf("device Set-Cookie = %+v, want exactly one clearing cookie", dc)
	}
	me := h.do(http.MethodGet, "/api/v1/me", nil, withDevice(device))
	if me.Code != http.StatusUnauthorized || decodeErr(t, me).Error != CodeUnauthorized {
		t.Fatalf("GET /me with the old device cookie: %d %s, want plain 401 unauthorized", me.Code, me.Body.String())
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session)); me.Code != http.StatusUnauthorized {
		t.Fatalf("old session after logout: %d", me.Code)
	}
}

func TestLogout_NoPin(t *testing.T) {
	h := newHarness(t, nil, nil)
	dadID, _ := h.seedDevice("dad")
	session := h.loginOK()

	w := h.do(http.MethodPost, "/api/v1/logout", nil, withCookie(session))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if sc := sessionCookie(t, w); !clearing(sc) {
		t.Fatalf("session Set-Cookie = %+v, want a clearing cookie", sc)
	}
	if dc := deviceCookie(t, w); dc != nil {
		t.Fatalf("logout without a PIN touched the device cookie: %+v", dc)
	}
	if ids := h.deviceIDs("dad"); len(ids) != 1 || ids[0] != dadID {
		t.Fatalf("dad's devices after mum's logout = %v, want [%d]", ids, dadID)
	}
}

func revokeAll(t *testing.T, h *harness, session, device string) (revoked, devices int) {
	t.Helper()
	opts := []reqOpt{withCookie(session)}
	if device != "" {
		opts = append(opts, withDevice(device))
	}
	w := h.do(http.MethodPost, "/api/v1/sessions/revoke-all", nil, opts...)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke-all: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Revoked        *int `json:"revoked"`
		RevokedDevices *int `json:"revoked_devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Revoked == nil || body.RevokedDevices == nil {
		t.Fatalf("revoke-all body %s, want {revoked, revoked_devices}", w.Body.String())
	}
	return *body.Revoked, *body.RevokedDevices
}

func TestRevokeAll_Devices(t *testing.T) {
	h := newHarness(t, nil, nil)
	sessA, devA := h.setPIN("583920") // A: the caller
	sessB, devB := h.setPIN("111111") // B
	h.setPIN("222222")                // C
	dadID, _ := h.seedDevice("dad")   // D
	mum := h.deviceIDs(fakeUser)
	if len(mum) != 3 {
		t.Fatalf("precondition: mum has %d devices", len(mum))
	}

	_, devices := revokeAll(t, h, sessA, devA)
	if devices != 2 {
		t.Fatalf("revoked_devices = %d, want 2", devices)
	}
	if ids := h.deviceIDs(fakeUser); len(ids) != 1 || ids[0] != mum[0] {
		t.Fatalf("mum's devices after revoke-all from A = %v, want [%d] (A)", ids, mum[0])
	}
	if ids := h.deviceIDs("dad"); len(ids) != 1 || ids[0] != dadID {
		t.Fatalf("dad's devices = %v, want [%d]", ids, dadID)
	}
	me := h.do(http.MethodGet, "/api/v1/me", nil, withDevice(devB))
	if me.Code != http.StatusUnauthorized || decodeErr(t, me).Error != CodeUnauthorized {
		t.Fatalf("B's device cookie after revoke-all: %d %s, want plain 401 unauthorized", me.Code, me.Body.String())
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(sessB), withDevice(devB)); me.Code != http.StatusUnauthorized {
		t.Fatalf("B's session after revoke-all: %d", me.Code)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(sessA), withDevice(devA)); me.Code != http.StatusOK {
		t.Fatalf("caller A after revoke-all: %d", me.Code)
	}
}

func TestRevokeAll_Untagged(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.setPIN("583920")
	h.setPIN("111111")
	dadID, _ := h.seedDevice("dad")
	caller := h.loginOK() // untagged

	_, devices := revokeAll(t, h, caller, "")
	if devices != 2 {
		t.Fatalf("revoked_devices from an untagged session = %d, want 2", devices)
	}
	if ids := h.deviceIDs(fakeUser); len(ids) != 0 {
		t.Fatalf("mum's devices = %v, want none", ids)
	}
	if ids := h.deviceIDs("dad"); len(ids) != 1 || ids[0] != dadID {
		t.Fatalf("dad's devices = %v, want [%d]", ids, dadID)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(caller)); me.Code != http.StatusOK {
		t.Fatalf("untagged caller after revoke-all: %d", me.Code)
	}
}

func TestLogin_OnPinDevice(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")

	w := h.login(fakeUser, fakePass, withDevice(device))
	sc := sessionCookie(t, w)
	if w.Code != http.StatusNoContent || sc == nil {
		t.Fatalf("login on mum's PIN device: %d", w.Code)
	}
	if dc := deviceCookie(t, w); dc != nil {
		t.Fatalf("login with her own device cookie touched it: %+v", dc)
	}
	h.clock.Advance(15*time.Minute + time.Second)
	me := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(sc.Value), withDevice(device))
	if me.Code != http.StatusUnauthorized || decodeErr(t, me).Error != auth.CodePINRequired {
		t.Fatalf("login session on a PIN device at +15m01s: %d %s, want pin_required", me.Code, me.Body.String())
	}

	// Mum logs in on dad's PIN device: his cookie goes, her session is a
	// plain 30-day one.
	h2 := newHarness(t, nil, nil)
	_, dadDevice := h2.seedDevice("dad")
	w = h2.login(fakeUser, fakePass, withDevice(dadDevice))
	sc = sessionCookie(t, w)
	if w.Code != http.StatusNoContent || sc == nil {
		t.Fatalf("login on dad's device: %d", w.Code)
	}
	if dc := deviceCookie(t, w); !clearing(dc) {
		t.Fatalf("dad's device cookie on mum's login = %+v, want cleared", dc)
	}
	h2.clock.Advance(16 * time.Minute)
	me = h2.do(http.MethodGet, "/api/v1/me", nil, withCookie(sc.Value))
	if me.Code != http.StatusOK {
		t.Fatalf("mum's session on dad's device at +16m: %d, want 200 (untagged)", me.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(me.Body.Bytes(), &body)
	exp, err := time.Parse(time.RFC3339, body["expires_at"])
	if err != nil || exp.Sub(h2.clock.Now()) < 29*24*time.Hour {
		t.Fatalf("mum's session expires_at %q, want about now+30d", body["expires_at"])
	}
	if ids := h2.deviceIDs("dad"); len(ids) != 1 {
		t.Fatalf("dad's device row = %v, want it kept (only the cookie is cleared)", ids)
	}
}

func TestLogin_TrailingBodyRejected(t *testing.T) {
	h := newHarness(t, nil, nil)
	hits := h.loginHits()
	for name, body := range map[string]string{
		"stray brace": `{"username":"mum","password":"pw"}}`,
		"padded":      `{"username":"mum","password":"pw"}` + strings.Repeat(" ", 5000),
	} {
		if w := h.do(http.MethodPost, "/api/v1/login", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	if h.loginHits() != hits {
		t.Fatal("a rejected body reached AdGuard")
	}
}
