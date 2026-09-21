package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
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

const (
	fakeUser = "mum"
	fakePass = "pw"
)

var t0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// harness is one API over a real store, the AdGuard fake, a shared fake
// clock and a debug-level JSON log buffer.
type harness struct {
	t     *testing.T
	fake  *adguardtest.Server
	store *store.Store
	clock *clock
	logs  *lockedBuffer
	h     http.Handler
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T, trusted []*net.IPNet, clientIP func(*http.Request) netip.Addr) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

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
	mgr := auth.New(st, auth.Options{Now: c.Now, Log: log, ErrorWriter: UnauthorizedWriter})
	if clientIP == nil {
		clientIP = ClientIP(trusted)
	}
	a := New(Deps{
		AdGuard:  client,
		ClientIP: clientIP,
		Log:      log,
		Auth:     mgr,
		Limiter:  ratelimit.New(cfg),
		Sessions: st,
		Children: st,
	})
	return &harness{t: t, fake: fake, store: st, clock: c, logs: logs, h: a.Handler()}
}

type reqOpt func(*http.Request)

func from(remote string) reqOpt { return func(r *http.Request) { r.RemoteAddr = remote } }
func withCookie(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: v}) }
}
func noCSRF() reqOpt         { return func(r *http.Request) { r.Header.Del(CSRFHeader) } }
func hdr(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// do sends a request with the CSRF header and a default peer of 203.0.113.1.
func (h *harness) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd *strings.Reader
	if s, ok := body.(string); ok {
		rd = strings.NewReader(s)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "203.0.113.1:4444"
	r.Header.Set(CSRFHeader, CSRFValue)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func creds(u, p string) map[string]string { return map[string]string{"username": u, "password": p} }

func (h *harness) login(u, p string, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/login", creds(u, p), opts...)
}

// loginOK logs in as the fake user and returns the session cookie value.
func (h *harness) loginOK(opts ...reqOpt) string {
	h.t.Helper()
	w := h.login(fakeUser, fakePass, opts...)
	if w.Code != http.StatusNoContent {
		h.t.Fatalf("login: status %d body %s", w.Code, w.Body.String())
	}
	c := sessionCookie(h.t, w)
	if c == nil {
		h.t.Fatal("login: no session cookie")
	}
	return c.Value
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			if found != nil {
				t.Fatalf("two Set-Cookie headers for %s", auth.CookieName)
			}
			found = c
		}
	}
	return found
}

func (h *harness) loginHits() int {
	n := 0
	for _, r := range h.fake.Requests() {
		if r.Path == "/control/login" {
			n++
		}
	}
	return n
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", w.Body.String(), err)
	}
	return e
}

// loginEvents returns the event=login log lines, parsed.
func (h *harness) loginEvents() []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["event"] == "login" {
			out = append(out, m)
		}
	}
	return out
}

func TestLogin_OK(t *testing.T) {
	h := newHarness(t, nil, nil)
	w := h.login(fakeUser, fakePass)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	c := sessionCookie(t, w)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("session cookie attrs: %+v", c)
	}
	if len(c.Value) != 43 {
		t.Fatalf("token length %d, want 43", len(c.Value))
	}

	var reqs []adguardtest.Request
	for _, r := range h.fake.Requests() {
		if r.Path == "/control/login" {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 1 {
		t.Fatalf("fake saw %d /control/login calls, want 1", len(reqs))
	}
	var sent map[string]string
	if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] != fakeUser || sent["password"] != fakePass || len(sent) != 2 {
		t.Fatalf("fake received %v, want {name: mum, password: pw}", sent)
	}
	if reqs[0].HasAuth {
		t.Fatal("/control/login carried an Authorization header")
	}
}

func TestLogin_Outcomes(t *testing.T) {
	h := newHarness(t, nil, nil)

	if w := h.login(fakeUser, fakePass); w.Code != http.StatusNoContent || sessionCookie(t, w) == nil {
		t.Fatalf("right creds: status %d, cookie %v", w.Code, sessionCookie(t, w))
	}

	w := h.login(fakeUser, "nope")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", w.Code)
	}
	if e := decodeErr(t, w); e != (errorBody{Error: CodeBadCredentials, Message: badCredentialsMsg}) {
		t.Fatalf("wrong password body = %+v", e)
	}
	if sessionCookie(t, w) != nil {
		t.Fatal("wrong password set a cookie")
	}

	h.fake.SetStatus("/control/login", http.StatusServiceUnavailable)
	w = h.login(fakeUser, fakePass)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Fatalf("503: status %d body %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.ToLower(decodeErr(t, w).Message), "unreachable") {
		t.Fatalf("502 message %q should be neutral, not claim unreachable", decodeErr(t, w).Message)
	}

	h.fake.SetStatus("/control/login", http.StatusTooManyRequests)
	w = h.login(fakeUser, fakePass)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Fatalf("AdGuard 429: status %d body %s, want 502 adguard_unavailable (not 401)", w.Code, w.Body.String())
	}

	h.fake.Close()
	w = h.login(fakeUser, fakePass)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Fatalf("closed fake: status %d body %s", w.Code, w.Body.String())
	}
}

func TestLogin_RateLimited(t *testing.T) {
	h := newHarness(t, nil, nil)
	for i := 0; i < 5; i++ {
		if w := h.login(fakeUser, "nope"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d", i+1, w.Code)
		}
	}
	hits := h.loginHits()
	for i := 0; i < 3; i++ {
		w := h.login(fakeUser, "nope")
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("post %d while limited: status %d", i+1, w.Code)
		}
		ra, err := strconv.Atoi(w.Header().Get("Retry-After"))
		if err != nil || ra < 1 {
			t.Fatalf("Retry-After = %q, want numeric >= 1", w.Header().Get("Retry-After"))
		}
		if decodeErr(t, w).Error != CodeRateLimited {
			t.Fatalf("body %s", w.Body.String())
		}
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("fake /control/login hits went %d -> %d while limited", hits, got)
	}
	if w := h.login(fakeUser, fakePass, from("203.0.113.2:1")); w.Code != http.StatusNoContent {
		t.Fatalf("other IP: status %d", w.Code)
	}
	if got := h.loginHits(); got != hits+1 {
		t.Fatalf("other IP did not reach the fake: hits %d", got)
	}
}

func TestLogin_BudgetOnlyOnFailure(t *testing.T) {
	h := newHarness(t, nil, nil)
	for i := 0; i < 4; i++ {
		h.login(fakeUser, "nope")
	}
	for i := 0; i < 10; i++ {
		if w := h.login(fakeUser, fakePass); w.Code != http.StatusNoContent {
			t.Fatalf("success %d: status %d", i+1, w.Code)
		}
	}
	h.fake.SetStatus("/control/login", http.StatusServiceUnavailable)
	for i := 0; i < 3; i++ {
		if w := h.login(fakeUser, fakePass); w.Code != http.StatusBadGateway {
			t.Fatalf("503 %d: status %d", i+1, w.Code)
		}
	}
	h.fake.SetResponse("/control/login", 0, nil)
	hits := h.loginHits()
	w := h.login(fakeUser, "nope")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("5th failure after successes/502s: status %d, want 401 (not 429)", w.Code)
	}
	if h.loginHits() != hits+1 {
		t.Fatal("5th failure did not reach the fake")
	}
}

func TestLogin_Burst(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.fake.Hang("/control/login", 30*time.Millisecond)
	const n = 12
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = h.login(fakeUser, "nope").Code
		}(i)
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
	if hits := h.loginHits(); hits != 5 || limited != 7 {
		t.Fatalf("burst: %d fake hits, %d 429s; want 5 and 7", hits, limited)
	}
}

func TestLogin_Global(t *testing.T) {
	h := newHarness(t, nil, func(r *http.Request) netip.Addr {
		return netip.MustParseAddr(r.Header.Get("X-Test-IP"))
	})
	for i := 0; i < 20; i++ {
		if w := h.login(fakeUser, "nope", hdr("X-Test-IP", fmt.Sprintf("198.51.100.%d", i+1))); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d", i+1, w.Code)
		}
	}
	hits := h.loginHits()
	w := h.login(fakeUser, fakePass, hdr("X-Test-IP", "192.0.2.50"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("21st IP: status %d, want 429", w.Code)
	}
	if h.loginHits() != hits {
		t.Fatal("globally limited request reached the fake")
	}
}

func TestLogin_RetryAfterEscalates(t *testing.T) {
	h := newHarness(t, nil, nil)
	trip := func() string {
		t.Helper()
		for i := 0; i < 5; i++ {
			if w := h.login(fakeUser, "nope"); w.Code != http.StatusUnauthorized {
				t.Fatalf("failure %d: status %d", i+1, w.Code)
			}
		}
		w := h.login(fakeUser, "nope")
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("after trip: status %d", w.Code)
		}
		return w.Header().Get("Retry-After")
	}
	if got := trip(); got != "60" {
		t.Fatalf("trip #1 Retry-After = %s, want 60", got)
	}
	h.clock.Advance(61 * time.Second)
	if got := trip(); got != "120" {
		t.Fatalf("trip #2 Retry-After = %s, want 120", got)
	}
	h.clock.Advance(30 * time.Second)
	w := h.login(fakeUser, "nope")
	if got := w.Header().Get("Retry-After"); w.Code != http.StatusTooManyRequests || got != "90" {
		t.Fatalf("30s into trip #2: status %d Retry-After %s, want 429 / 90", w.Code, got)
	}
}

func TestLogin_ProxiedIP(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	h := newHarness(t, []*net.IPNet{loopback}, nil)
	for i := 0; i < 5; i++ {
		h.login(fakeUser, "nope", from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.7"))
	}
	if w := h.login(fakeUser, "nope", from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.7")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked XFF ip: status %d, want 429", w.Code)
	}
	if w := h.login(fakeUser, "nope", from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.8")); w.Code != http.StatusUnauthorized {
		t.Fatalf("other XFF ip from the same peer: status %d, want 401", w.Code)
	}

	h2 := newHarness(t, nil, nil)
	for i := 0; i < 5; i++ {
		h2.login(fakeUser, "nope", from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.7"))
	}
	if w := h2.login(fakeUser, "nope", from("127.0.0.1:1"), hdr("X-Forwarded-For", "198.51.100.8")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted: XFF values should share the peer's budget, got %d", w.Code)
	}
}

func TestLogin_EventLog(t *testing.T) {
	const canary = "pw-CANARY-4c1e"
	h := newHarness(t, nil, nil)
	h.login(fakeUser, fakePass)
	h.login(fakeUser, canary)
	h.fake.SetStatus("/control/login", http.StatusServiceUnavailable)
	h.login(fakeUser, fakePass)
	h.fake.SetResponse("/control/login", 0, nil)
	for i := 0; i < 4; i++ {
		h.login(fakeUser, canary)
	}
	h.login(fakeUser, canary) // 6th failure: rate limited

	got := map[string]int{}
	for _, ev := range h.loginEvents() {
		o, _ := ev["outcome"].(string)
		got[o]++
		if ev["username"] != fakeUser || ev["ip"] != "203.0.113.1" {
			t.Errorf("event %v lacks username/ip attrs", ev)
		}
	}
	for _, o := range []string{outcomeOK, outcomeBadCreds, outcomeUnreachable, outcomeRateLimited} {
		if got[o] == 0 {
			t.Errorf("no event=login line with outcome %s (got %v)", o, got)
		}
	}
	if strings.Contains(h.logs.String(), canary) {
		t.Fatal("submitted password found in the debug log buffer")
	}
	if strings.Contains(h.logs.String(), fakePass) {
		t.Fatal("real password found in the debug log buffer")
	}
}

func TestLogin_NoTokenInLogs(t *testing.T) {
	h := newHarness(t, nil, nil)
	tok := h.loginOK()
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(tok)); w.Code != http.StatusOK {
		t.Fatalf("me: %d", w.Code)
	}
	if w := h.do(http.MethodPost, "/api/v1/logout", nil, withCookie(tok)); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	out := h.logs.String()
	if !strings.Contains(out, "/control/login") || !strings.Contains(out, `"event":"login"`) {
		t.Fatalf("positive controls missing from logs:\n%s", out)
	}
	if strings.Contains(out, tok) {
		t.Fatalf("session token found in logs:\n%s", out)
	}
}

func TestLogout_Replay(t *testing.T) {
	h := newHarness(t, nil, nil)
	tok := h.loginOK()
	w := h.do(http.MethodPost, "/api/v1/logout", nil, withCookie(tok))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d body %s", w.Code, w.Body.String())
	}
	c := sessionCookie(t, w)
	if c == nil || c.MaxAge != -1 {
		t.Fatalf("logout Set-Cookie = %+v, want exactly one with Max-Age=0", c)
	}
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(tok)); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie after logout: status %d, want 401", w.Code)
	}
}

func TestMe(t *testing.T) {
	h := newHarness(t, nil, nil)
	tok := h.loginOK()
	w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(tok))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var me struct {
		Username  string `json:"username"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, me.ExpiresAt)
	if err != nil || me.Username != fakeUser {
		t.Fatalf("me = %+v (parse err %v)", me, err)
	}
	if want := t0.Add(30 * 24 * time.Hour); !exp.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", exp, want)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("200 lacks Cache-Control: no-store")
	}

	w = h.do(http.MethodGet, "/api/v1/me", nil)
	if w.Code != http.StatusUnauthorized || decodeErr(t, w).Error != CodeUnauthorized {
		t.Fatalf("no cookie: status %d body %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("401 lacks Cache-Control: no-store")
	}
}

func TestLogin_BadBody(t *testing.T) {
	h := newHarness(t, nil, nil)
	bodies := map[string]string{
		"truncated":   "{",
		"missing":     `{"username":"a"}`,
		"empty":       `{"username":"","password":""}`,
		"oversize":    `{"username":"a","password":"` + strings.Repeat("x", 5<<10) + `"}`,
		"unknown key": `{"username":"a","password":"b","extra":1}`,
	}
	for name, b := range bodies {
		w := h.do(http.MethodPost, "/api/v1/login", b)
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Error != CodeBadRequest {
			t.Errorf("%s: status %d body %s, want 400 bad_request", name, w.Code, w.Body.String())
		}
	}
	if n := h.loginHits(); n != 0 {
		t.Fatalf("bad bodies reached the fake %d times", n)
	}
	if ev := h.loginEvents(); len(ev) != 0 {
		t.Fatalf("bad bodies produced login events: %v", ev)
	}
}

func TestLogin_CSRF(t *testing.T) {
	h := newHarness(t, nil, nil)
	if w := h.login(fakeUser, fakePass, noCSRF()); w.Code != http.StatusForbidden {
		t.Fatalf("without header: status %d, want 403", w.Code)
	}
	if h.loginHits() != 0 {
		t.Fatal("CSRF-rejected login reached the fake")
	}
	if w := h.login(fakeUser, fakePass); w.Code != http.StatusNoContent {
		t.Fatalf("with header: status %d, want 204", w.Code)
	}
}

func TestRouter_Fallthrough(t *testing.T) {
	h := newHarness(t, nil, nil)
	if w := h.do(http.MethodGet, "/api/v1/does-not-exist", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown path without cookie: status %d, want 401", w.Code)
	}
	tok := h.loginOK()
	if w := h.do(http.MethodGet, "/api/v1/does-not-exist", nil, withCookie(tok)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown path with cookie: status %d, want 404", w.Code)
	}
	if w := h.do(http.MethodGet, "/healthz", nil); w.Code != http.StatusNotFound {
		t.Fatalf("/healthz through api.Handler: status %d, want 404", w.Code)
	}
}
