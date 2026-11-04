package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDevices is an in-memory DeviceStore with error injection.
type fakeDevices struct {
	mu       sync.Mutex
	nextID   int64
	rows     map[int64]Device
	err      error // returned by DeviceByTokenHash when set
	revoked  []int64
	sweptAt  []time.Time
	sessions *fakeStore // RevokeDevice drops its tagged sessions here
}

func newFakeDevices(sessions *fakeStore) *fakeDevices {
	return &fakeDevices{rows: map[int64]Device{}, sessions: sessions}
}

func (f *fakeDevices) InsertDevice(_ context.Context, d Device) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	d.ID, d.FailedAttempts = f.nextID, 0
	f.rows[d.ID] = d
	return d.ID, nil
}

func (f *fakeDevices) DeviceByTokenHash(_ context.Context, h [32]byte) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Device{}, f.err
	}
	for _, d := range f.rows {
		if d.TokenHash == h {
			return d, nil
		}
	}
	return Device{}, ErrDeviceNotFound
}

func (f *fakeDevices) DeviceByID(_ context.Context, id int64) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.rows[id]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	return d, nil
}

func (f *fakeDevices) ReserveAttempt(context.Context, int64) (int, error) {
	return 0, errors.New("unused")
}
func (f *fakeDevices) ResetAttempts(context.Context, int64) error { return errors.New("unused") }
func (f *fakeDevices) ReplacePIN(context.Context, int64, string) error {
	return errors.New("unused")
}

func (f *fakeDevices) TouchDevice(_ context.Context, id int64, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.rows[id]
	d.ExpiresAt = expires
	f.rows[id] = d
	return nil
}

func (f *fakeDevices) RevokeDevice(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	f.revoked = append(f.revoked, id)
	if f.sessions != nil {
		f.sessions.mu.Lock()
		for sid, s := range f.sessions.rows {
			if s.DeviceID == id {
				delete(f.sessions.rows, sid)
			}
		}
		f.sessions.mu.Unlock()
	}
	return nil
}

func (f *fakeDevices) DeleteDevice(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	return nil
}

func (f *fakeDevices) DeleteOtherDevices(context.Context, string, int64) (int, error) {
	return 0, errors.New("unused")
}

func (f *fakeDevices) DeleteExpiredDevices(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweptAt = append(f.sweptAt, now)
	n := 0
	for id, d := range f.rows {
		if !d.ExpiresAt.After(now) {
			delete(f.rows, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeDevices) has(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[id]
	return ok
}

func (f *fakeDevices) expiresAt(id int64) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[id].ExpiresAt
}

func newDeviceTest(t *testing.T, opts Options) (*Manager, *fakeStore, *fakeDevices, *clock) {
	t.Helper()
	f := newFake()
	devs := newFakeDevices(f)
	c := &clock{t: t0}
	if opts.Now == nil {
		opts.Now = c.Now
	}
	if opts.ErrorWriter == nil {
		opts.ErrorWriter = jsonErr
	}
	opts.Devices = devs
	return New(f, opts), f, devs, c
}

const testPHC = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA"

// doWith sends GET /api/v1/me with an optional session and device cookie.
func doWith(h http.Handler, session, device string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: session})
	}
	if device != "" {
		r.AddCookie(&http.Cookie{Name: DeviceCookieName, Value: device})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func setCookies(h http.Header, name string) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return body["error"]
}

func TestIssueDevice(t *testing.T) {
	m, _, devs, _ := newDeviceTest(t, Options{})
	raw, d, err := m.IssueDevice(ctx, "mum", testPHC)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 43 {
		t.Fatalf("raw device token %q is %d chars, want 43 (256 bits base64url)", raw, len(raw))
	}
	b, err := tokenEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		t.Fatalf("raw token decodes to %d bytes (%v), want 32", len(b), err)
	}
	stored, err := devs.DeviceByID(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash != sha256.Sum256(b) {
		t.Fatal("stored token_hash is not sha256 of the decoded token")
	}
	if stored.Username != "mum" || stored.PINHash != testPHC ||
		!stored.CreatedAt.Equal(t0) || !stored.ExpiresAt.Equal(t0.Add(90*day)) {
		t.Fatalf("stored device = %+v, want mum, the PIN hash, created t0, expires t0+90d", stored)
	}
	raw2, _, _ := m.IssueDevice(ctx, "mum", testPHC)
	if raw2 == raw {
		t.Fatal("two device tokens are identical")
	}
}

func TestRequireSession_PinRequired(t *testing.T) {
	m, _, _, c := newDeviceTest(t, Options{})
	h := m.RequireSession(&spy{})
	dev, d, _ := m.IssueDevice(ctx, "mum", testPHC)
	sess, _, _ := m.IssueForDevice(ctx, "mum", d.ID)

	c.Set(t0.Add(15*time.Minute + time.Second))
	w := doWith(h, sess, dev)
	if w.Code != http.StatusUnauthorized || errCode(t, w) != CodePINRequired {
		t.Fatalf("expired PIN session + live device: %d %s, want 401 pin_required", w.Code, w.Body.String())
	}
	sc := setCookies(w.Header(), CookieName)
	if len(sc) != 1 || sc[0].MaxAge >= 0 {
		t.Fatalf("session Set-Cookie = %v, want one clearing cookie", sc)
	}
	if dc := setCookies(w.Header(), DeviceCookieName); len(dc) != 0 {
		t.Fatalf("device Set-Cookie = %v, want none", dc)
	}
}

func TestRequireSession_NoSessionCookie(t *testing.T) {
	m, _, _, _ := newDeviceTest(t, Options{})
	next := &spy{}
	h := m.RequireSession(next)
	dev, _, _ := m.IssueDevice(ctx, "mum", testPHC)

	w := doWith(h, "", dev)
	if w.Code != http.StatusUnauthorized || errCode(t, w) != CodePINRequired {
		t.Fatalf("no session + live device: %d %s, want 401 pin_required", w.Code, w.Body.String())
	}
	if n := len(w.Header().Values("Set-Cookie")); n != 0 {
		t.Fatalf("Set-Cookie = %v, want none", w.Header().Values("Set-Cookie"))
	}
	if next.calls != 0 {
		t.Fatal("next ran")
	}
}

func TestRequireSession_ExpiredDevice(t *testing.T) {
	m, _, devs, c := newDeviceTest(t, Options{})
	h := m.RequireSession(&spy{})
	dev, d, _ := m.IssueDevice(ctx, "mum", testPHC)

	c.Set(t0.Add(90*day + time.Second))
	w := doWith(h, "", dev)
	if w.Code != http.StatusUnauthorized || errCode(t, w) != "unauthorized" {
		t.Fatalf("device past 90d: %d %s, want 401 unauthorized", w.Code, w.Body.String())
	}
	dc := setCookies(w.Header(), DeviceCookieName)
	if len(dc) != 1 || dc[0].MaxAge >= 0 {
		t.Fatalf("device Set-Cookie = %v, want one clearing cookie (Max-Age<0)", dc)
	}
	if devs.has(d.ID) {
		t.Fatal("expired device row still present")
	}

	// Exactly at 90d is expired too.
	m2, _, devs2, c2 := newDeviceTest(t, Options{})
	dev2, d2, _ := m2.IssueDevice(ctx, "mum", testPHC)
	c2.Set(t0.Add(90 * day))
	if w := doWith(m2.RequireSession(&spy{}), "", dev2); errCode(t, w) != "unauthorized" || devs2.has(d2.ID) {
		t.Fatalf("device at exactly 90d: %s, row present %v; want unauthorized and deleted", w.Body.String(), devs2.has(d2.ID))
	}
}

func TestLookupDevice_ExpiredRevokesSessions(t *testing.T) {
	m, f, _, c := newDeviceTest(t, Options{})
	dev, d, _ := m.IssueDevice(ctx, "mum", testPHC)
	_, s, _ := m.IssueForDevice(ctx, "mum", d.ID)
	c.Set(t0.Add(90 * day))
	if _, err := m.LookupDevice(ctx, dev); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("LookupDevice(expired) err = %v", err)
	}
	if _, ok := f.rows[s.ID]; ok {
		t.Fatal("expired device's tagged session survived")
	}
}

func TestRequireSession_BadDeviceCookie(t *testing.T) {
	m, _, _, _ := newDeviceTest(t, Options{})
	h := m.RequireSession(&spy{})
	unknown, _, _ := newToken()
	for name, v := range map[string]string{
		"malformed": "not-a-token",
		"short":     strings.Repeat("A", 42),
		"unknown":   unknown,
	} {
		w := doWith(h, "", v)
		if w.Code != http.StatusUnauthorized || errCode(t, w) != "unauthorized" {
			t.Errorf("%s device cookie: %d %s, want 401 unauthorized", name, w.Code, w.Body.String())
		}
		dc := setCookies(w.Header(), DeviceCookieName)
		if len(dc) != 1 || dc[0].MaxAge >= 0 {
			t.Errorf("%s device cookie: Set-Cookie %v, want one clearing cookie", name, dc)
		}
	}
}

func TestRequireSession_NoDeviceSlide(t *testing.T) {
	m, _, devs, c := newDeviceTest(t, Options{})
	next := &spy{}
	h := m.RequireSession(next)
	dev, d, _ := m.IssueDevice(ctx, "mum", testPHC)
	sess, _, _ := m.IssueForDevice(ctx, "mum", d.ID)
	before := devs.expiresAt(d.ID)

	c.Set(t0.Add(10 * time.Minute))
	w := doWith(h, sess, dev)
	if w.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("live PIN session: status %d calls %d", w.Code, next.calls)
	}
	if got := devs.expiresAt(d.ID); !got.Equal(before) {
		t.Fatalf("device expires_at moved %v -> %v on an ordinary request", before, got)
	}
	if dc := setCookies(w.Header(), DeviceCookieName); len(dc) != 0 {
		t.Fatalf("device Set-Cookie on an ordinary request: %v", dc)
	}
}

func TestDeviceCookie_Attributes(t *testing.T) {
	for _, secure := range []bool{false, true} {
		m, _, _, _ := newDeviceTest(t, Options{Secure: secure})
		c := m.DeviceCookie("tok")
		if c.Name != DeviceCookieName || c.Name == CookieName {
			t.Fatalf("device cookie name %q", c.Name)
		}
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 7776000 || c.Secure != secure {
			t.Fatalf("Secure=%v: device cookie = %+v", secure, c)
		}
		s := c.String()
		if strings.Contains(s, "Secure") != secure || !strings.Contains(s, "Max-Age=7776000") {
			t.Fatalf("Secure=%v: header %q", secure, s)
		}
		cl := m.ClearDeviceCookie()
		if cl.MaxAge >= 0 || cl.Name != DeviceCookieName || cl.Secure != secure {
			t.Fatalf("clear cookie = %+v", cl)
		}
	}

	m, _, _, _ := newDeviceTest(t, Options{})
	w := httptest.NewRecorder()
	SetCookie(w, m.Cookie("sess"))
	SetDeviceCookie(w, m.DeviceCookie("tok"))
	m.ClearDevice(w)
	dc := setCookies(w.Header(), DeviceCookieName)
	if len(dc) != 1 || dc[0].MaxAge >= 0 {
		t.Fatalf("set-then-clear device Set-Cookie = %v, want exactly one clearing cookie", dc)
	}
	if sc := setCookies(w.Header(), CookieName); len(sc) != 1 || sc[0].Value != "sess" {
		t.Fatalf("session Set-Cookie disturbed: %v", sc)
	}
}

func TestRequireSession_DeviceStoreError(t *testing.T) {
	m, _, devs, c := newDeviceTest(t, Options{})
	h := m.RequireSession(&spy{})
	dev, d, _ := m.IssueDevice(ctx, "mum", testPHC)
	sess, _, _ := m.IssueForDevice(ctx, "mum", d.ID)
	c.Set(t0.Add(time.Hour))
	devs.err = errors.New("disk on fire")

	w := doWith(h, sess, dev)
	if w.Code != http.StatusInternalServerError || errCode(t, w) != "internal" {
		t.Fatalf("device store error: %d %s, want 500 internal", w.Code, w.Body.String())
	}
	if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Fatalf("Set-Cookie on a store error = %v, want neither cookie cleared", sc)
	}
}

func TestRunSweeper_Devices(t *testing.T) {
	m, _, devs, c := newDeviceTest(t, Options{})
	_, gone, _ := m.IssueDevice(ctx, "mum", testPHC)
	_, kept, _ := m.IssueDevice(ctx, "mum", testPHC)
	now := t0.Add(time.Hour)
	_ = devs.TouchDevice(ctx, gone.ID, now)
	_ = devs.TouchDevice(ctx, kept.ID, now.Add(time.Second))
	c.Set(now)

	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunSweeper(sctx, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for devs.has(gone.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if devs.has(gone.ID) {
		t.Fatal("sweeper kept a device with expires_at <= now")
	}
	if !devs.has(kept.ID) {
		t.Fatal("sweeper removed a device expiring at now+1s")
	}
	devs.mu.Lock()
	defer devs.mu.Unlock()
	if len(devs.sweptAt) == 0 || !devs.sweptAt[0].Equal(now) {
		t.Fatalf("DeleteExpiredDevices called with %v, want the manager's clock %v", devs.sweptAt, now)
	}
}

func TestRequireSession_NilDevices(t *testing.T) {
	m, f, c := newTest(t, Options{ErrorWriter: jsonErr})
	next := &spy{}
	h := m.RequireSession(next)
	somebody, _, _ := newToken()

	// A live session slides 30d; the device cookie beside it is ignored.
	plain, _, _ := m.Issue(ctx, "mum")
	c.Set(t0.Add(time.Hour))
	w := doWith(h, plain, somebody)
	if w.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("nil devices, live session: %d", w.Code)
	}
	if lt := f.lastTouch(); !lt.expire.Equal(t0.Add(time.Hour + 30*day)) {
		t.Fatalf("nil devices: session slid to %v, want now+30d", lt.expire)
	}
	if dc := setCookies(w.Header(), DeviceCookieName); len(dc) != 0 {
		t.Fatalf("nil devices: device Set-Cookie %v", dc)
	}

	for name, session := range map[string]string{"missing": "", "expired": "x"} {
		if name == "expired" {
			c.Set(t0.Add(31 * day))
			session = plain
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s session with nil devices panicked: %v", name, r)
				}
			}()
			w := doWith(h, session, somebody)
			if w.Code != http.StatusUnauthorized || errCode(t, w) != "unauthorized" {
				t.Errorf("%s session, nil devices: %d %s, want plain 401", name, w.Code, w.Body.String())
			}
			if dc := setCookies(w.Header(), DeviceCookieName); len(dc) != 0 {
				t.Errorf("%s session, nil devices: device cookie touched %v", name, dc)
			}
		}()
	}
	if _, err := m.LookupDevice(ctx, somebody); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("LookupDevice with nil devices err = %v", err)
	}
	if _, _, err := m.IssueDevice(ctx, "mum", testPHC); err == nil {
		t.Fatal("IssueDevice with nil devices succeeded")
	}
}
