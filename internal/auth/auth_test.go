package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory SessionStore that records the calls the
// contract cares about.
type fakeStore struct {
	mu       sync.Mutex
	nextID   int64
	rows     map[int64]Session
	touches  []touch
	deletes  []int64
	expireds int
}

type touch struct {
	id               int64
	lastSeen, expire time.Time
}

func newFake() *fakeStore { return &fakeStore{rows: map[int64]Session{}} }

func (f *fakeStore) Insert(_ context.Context, s Session) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	s.ID = f.nextID
	f.rows[s.ID] = s
	return s.ID, nil
}

func (f *fakeStore) ByTokenHash(_ context.Context, h [32]byte) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.rows {
		if s.TokenHash == h {
			return s, nil
		}
	}
	return Session{}, ErrNotFound
}

func (f *fakeStore) Touch(_ context.Context, id int64, lastSeen, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rows[id]
	if !ok {
		return ErrNotFound
	}
	s.LastSeenAt, s.ExpiresAt = lastSeen, expires
	f.rows[id] = s
	f.touches = append(f.touches, touch{id, lastSeen, expires})
	return nil
}

func (f *fakeStore) Delete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	f.deletes = append(f.deletes, id)
	return nil
}

func (f *fakeStore) ListByUser(_ context.Context, u string) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Session
	for _, s := range f.rows {
		if s.Username == u {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteByUser(_ context.Context, u string, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rows[id]
	if !ok || s.Username != u {
		return false, nil
	}
	delete(f.rows, id)
	return true, nil
}

func (f *fakeStore) DeleteOthers(_ context.Context, u string, keep int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, s := range f.rows {
		if s.Username == u && id != keep {
			delete(f.rows, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireds++
	n := 0
	for id, s := range f.rows {
		if !s.ExpiresAt.After(now) {
			delete(f.rows, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) touchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touches)
}

func (f *fakeStore) lastTouch() touch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.touches[len(f.touches)-1]
}

func (f *fakeStore) expiredCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expireds
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var t0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func newTest(t *testing.T, opts Options) (*Manager, *fakeStore, *clock) {
	t.Helper()
	f := newFake()
	c := &clock{t: t0}
	if opts.Now == nil {
		opts.Now = c.Now
	}
	return New(f, opts), f, c
}

var ctx = context.Background()

func TestIssue_Token(t *testing.T) {
	m, f, _ := newTest(t, Options{})
	raw1, s1, err := m.Issue(ctx, "mum")
	if err != nil {
		t.Fatal(err)
	}
	raw2, s2, err := m.Issue(ctx, "mum")
	if err != nil {
		t.Fatal(err)
	}
	if raw1 == raw2 {
		t.Fatal("two Issue calls returned the same token")
	}
	for _, raw := range []string{raw1, raw2} {
		if len(raw) != 43 {
			t.Errorf("token length %d, want 43", len(raw))
		}
		if _, err := base64.RawURLEncoding.Strict().DecodeString(raw); err != nil {
			t.Errorf("token is not strict base64url: %v", err)
		}
	}
	if s1.ID == s2.ID || s1.ID == 0 {
		t.Fatalf("ids %d, %d: want distinct, store-assigned", s1.ID, s2.ID)
	}

	b, _ := base64.RawURLEncoding.DecodeString(raw1)
	if f.rows[s1.ID].TokenHash != sha256.Sum256(b) {
		t.Fatal("stored TokenHash != sha256(raw)")
	}
	stored, _ := json.Marshal(struct {
		S Session
	}{f.rows[s1.ID]})
	if strings.Contains(string(stored), raw1) {
		t.Fatal("raw token found in a stored field")
	}
}

func TestCookie_Attrs(t *testing.T) {
	m, _, _ := newTest(t, Options{})
	c := m.Cookie("tok")
	if c.Name != CookieName || c.Value != "tok" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode ||
		c.Path != "/" || c.Secure || c.MaxAge != 30*24*3600 {
		t.Fatalf("insecure cookie attrs: %+v", c)
	}
	ms, _, _ := newTest(t, Options{Secure: true})
	if cs := ms.Cookie("tok"); !cs.Secure || !cs.HttpOnly {
		t.Fatalf("Secure=true cookie attrs: %+v", cs)
	}
	cl := m.ClearCookie()
	if cl.MaxAge != -1 || cl.Name != c.Name || cl.Path != c.Path || !cl.HttpOnly || cl.SameSite != c.SameSite {
		t.Fatalf("clear cookie attrs: %+v", cl)
	}
}

func sessionSetCookies(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Set-Cookie") {
		if strings.HasPrefix(v, CookieName+"=") {
			out = append(out, v)
		}
	}
	return out
}

func TestSetCookie_Single(t *testing.T) {
	m, _, _ := newTest(t, Options{})
	w := httptest.NewRecorder()
	w.Header().Add("Set-Cookie", "other=1; Path=/")
	SetCookie(w, m.Cookie("tok"))
	m.Clear(w)

	got := sessionSetCookies(w.Header())
	if len(got) != 1 {
		t.Fatalf("Set-Cookie for %s = %v, want exactly one", CookieName, got)
	}
	if !strings.Contains(got[0], "Max-Age=0") {
		t.Fatalf("clearing cookie %q lacks Max-Age=0", got[0])
	}
	all := w.Header().Values("Set-Cookie")
	if len(all) != 2 || all[0] != "other=1; Path=/" {
		t.Fatalf("unrelated Set-Cookie disturbed: %v", all)
	}
}

func TestAuthenticate_Sliding(t *testing.T) {
	m, f, c := newTest(t, Options{})
	raw, s, err := m.Issue(ctx, "mum")
	if err != nil {
		t.Fatal(err)
	}

	c.Set(t0.Add(29 * day))
	if _, err := m.Authenticate(ctx, raw); err != nil {
		t.Fatalf("at +29d: %v", err)
	}
	if got := f.lastTouch(); got.id != s.ID || !got.expire.Equal(t0.Add(59*day)) || !got.lastSeen.Equal(t0.Add(29*day)) {
		t.Fatalf("touch at +29d = %+v, want expires t0+59d", got)
	}

	c.Set(t0.Add(58*day + 23*time.Hour))
	if _, err := m.Authenticate(ctx, raw); err != nil {
		t.Fatalf("at +58d23h: %v", err)
	}
	if got := f.lastTouch(); !got.expire.Equal(t0.Add(88*day + 23*time.Hour)) {
		t.Fatalf("touch at +58d23h expires = %v, want t0+88d23h", got.expire)
	}

	c.Set(t0.Add(58*day + 23*time.Hour + 31*day))
	if _, err := m.Authenticate(ctx, raw); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after a 31-day gap: err = %v, want ErrNoSession", err)
	}
	if len(f.deletes) != 1 || f.deletes[0] != s.ID {
		t.Fatalf("deletes = %v, want [%d]", f.deletes, s.ID)
	}
}

func TestAuthenticate_Expired(t *testing.T) {
	m, _, c := newTest(t, Options{})
	raw, _, _ := m.Issue(ctx, "mum")
	c.Set(t0.Add(30*day + time.Second))
	if _, err := m.Authenticate(ctx, raw); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
	// Exactly at expiry is expired too (expires_at <= now).
	m2, _, c2 := newTest(t, Options{})
	raw2, _, _ := m2.Issue(ctx, "mum")
	c2.Set(t0.Add(30 * day))
	if _, err := m2.Authenticate(ctx, raw2); !errors.Is(err, ErrNoSession) {
		t.Fatalf("at exact expiry: err = %v, want ErrNoSession", err)
	}
}

func TestAuthenticate_Revoked(t *testing.T) {
	m, f, _ := newTest(t, Options{})
	raw, s, _ := m.Issue(ctx, "mum")
	if err := f.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, raw); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestAuthenticate_Garbage(t *testing.T) {
	m, f, _ := newTest(t, Options{})
	raw, _, _ := m.Issue(ctx, "mum")
	other, _, _ := newTest(t, Options{})
	foreign, _, _ := other.Issue(ctx, "mum")

	flipped := []byte(raw)
	if flipped[10] == 'A' {
		flipped[10] = 'B'
	} else {
		flipped[10] = 'A'
	}

	// Every canonical final character has an alphabet index divisible by 4
	// (its low two bits are padding); index+1 sets a padding bit.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, raw[42])
	if last%4 != 0 {
		t.Fatalf("final char %q has non-zero padding bits", raw[42])
	}
	padded := raw[:42] + string(alphabet[last+1])
	if _, err := base64.RawURLEncoding.DecodeString(padded); err != nil {
		t.Fatalf("non-strict decode should accept %q: %v", padded, err)
	}

	cases := map[string]string{
		"empty":        "",
		"not base64":   "notbase64!",
		"44 chars":     raw + "A",
		"foreign":      foreign,
		"flipped":      string(flipped),
		"padding bits": padded,
	}
	for name, tok := range cases {
		before := f.touchCount()
		_, err := m.Authenticate(ctx, tok)
		if !errors.Is(err, ErrNoSession) {
			t.Errorf("%s: err = %v, want ErrNoSession", name, err)
		}
		if f.touchCount() != before {
			t.Errorf("%s: Touch was called for a rejected token", name)
		}
	}
	// Positive control.
	if _, err := m.Authenticate(ctx, raw); err != nil {
		t.Fatalf("genuine token rejected: %v", err)
	}
}

type spy struct {
	calls int
	user  string
	ok    bool
}

func (s *spy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls++
	sess, ok := FromContext(r.Context())
	s.user, s.ok = sess.Username, ok
	w.WriteHeader(http.StatusOK)
}

func jsonErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

func do(h http.Handler, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRequireSession(t *testing.T) {
	m, f, c := newTest(t, Options{ErrorWriter: jsonErr})
	next := &spy{}
	h := m.RequireSession(next)

	raw, s, _ := m.Issue(ctx, "mum")
	deletedRaw, deleted, _ := m.Issue(ctx, "mum")
	_ = f.Delete(ctx, deleted.ID)
	expiredRaw, _, _ := m.Issue(ctx, "mum")

	random, _, _ := newToken()
	for name, tok := range map[string]string{"no cookie": "", "random": random, "deleted": deletedRaw} {
		w := do(h, tok)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: status %d ct %q, want 401 application/json", name, w.Code, w.Header().Get("Content-Type"))
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != "unauthorized" {
			t.Errorf("%s: body %s, want {error: unauthorized}", name, w.Body.String())
		}
		if name == "deleted" {
			sc := sessionSetCookies(w.Header())
			if len(sc) != 1 || !strings.Contains(sc[0], "Max-Age=0") {
				t.Errorf("deleted session response Set-Cookie = %v, want one clearing cookie", sc)
			}
		}
	}
	if next.calls != 0 {
		t.Fatalf("next ran %d times for rejected requests", next.calls)
	}

	c.Set(t0.Add(31 * day))
	if w := do(h, expiredRaw); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired: status %d, want 401", w.Code)
	}
	c.Set(t0.Add(day))

	w := do(h, raw)
	if w.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("valid: status %d calls %d, want 200 and one call", w.Code, next.calls)
	}
	if !next.ok || next.user != "mum" {
		t.Fatalf("FromContext = (%q, %v), want (mum, true)", next.user, next.ok)
	}
	sc := sessionSetCookies(w.Header())
	if len(sc) != 1 || !strings.Contains(sc[0], "Max-Age=2592000") || !strings.Contains(sc[0], raw) {
		t.Fatalf("refresh Set-Cookie = %v, want one with Max-Age=2592000", sc)
	}
	if !strings.Contains(sc[0], "HttpOnly") || !strings.Contains(sc[0], "SameSite=Strict") || !strings.Contains(sc[0], "Path=/") {
		t.Fatalf("refresh Set-Cookie %q lacks attrs", sc[0])
	}
	_ = s
}

func TestNoTokenInLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, _, _ := newTest(t, Options{Log: log, ErrorWriter: jsonErr})

	raw, sess, err := m.Issue(ctx, "mum")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	do(m.RequireSession(&spy{}), raw)
	do(m.RequireSession(&spy{}), "garbage")
	log.Info("s", "session", sess)

	out := buf.String()
	if !strings.Contains(out, `"id":`+strconv.FormatInt(sess.ID, 10)) {
		t.Fatalf("positive control: session id missing from logs:\n%s", out)
	}
	if strings.Contains(out, raw) {
		t.Fatalf("raw token in logs:\n%s", out)
	}
	h := sess.TokenHash[:]
	for _, enc := range []string{
		base64.RawURLEncoding.EncodeToString(h),
		base64.StdEncoding.EncodeToString(h),
		hex.EncodeToString(h),
	} {
		if strings.Contains(out, enc) {
			t.Fatalf("token hash %q in logs:\n%s", enc, out)
		}
	}
}

func TestRunSweeper(t *testing.T) {
	m, f, _ := newTest(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunSweeper(ctx, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(100 * time.Millisecond)
	for f.expiredCalls() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := f.expiredCalls(); n < 2 {
		t.Fatalf("DeleteExpired called %d times within 100ms, want >= 2", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSweeper did not return after cancel")
	}
}
