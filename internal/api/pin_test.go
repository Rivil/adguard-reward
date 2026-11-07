package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
)

// faultSessions is a SessionStore whose SetDevice and Delete fail with the
// set error; every other call, and those two when unset, reach the store.
type faultSessions struct {
	auth.SessionStore
	setDeviceErr, deleteErr error
}

func (f faultSessions) SetDevice(ctx context.Context, id, deviceID int64, expires time.Time) error {
	if f.setDeviceErr != nil {
		return f.setDeviceErr
	}
	return f.SessionStore.SetDevice(ctx, id, deviceID, expires)
}

func (f faultSessions) Delete(ctx context.Context, id int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.SessionStore.Delete(ctx, id)
}

// faultDevices is a DeviceStore whose TouchDevice and DeleteDevice fail
// with the set error; every other call, and those two when unset, reach
// the store.
type faultDevices struct {
	auth.DeviceStore
	touchErr, deleteErr error
}

func (f faultDevices) TouchDevice(ctx context.Context, id int64, expires time.Time) error {
	if f.touchErr != nil {
		return f.touchErr
	}
	return f.DeviceStore.TouchDevice(ctx, id, expires)
}

func (f faultDevices) DeleteDevice(ctx context.Context, id int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.DeviceStore.DeleteDevice(ctx, id)
}

// rewire rebuilds the handler over the same store, fake, clock and log
// buffer, with the manager and Deps reaching sessions and devices through
// the given wrappers. Cookies issued before the rewire stay valid; Grants
// is left nil — nothing here reaches it.
func (h *harness) rewire(sessions auth.SessionStore, devices auth.DeviceStore) {
	h.t.Helper()
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := adguard.New(h.fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		h.t.Fatal(err)
	}
	cfg := ratelimit.Defaults()
	cfg.Now = h.clock.Now
	h.h = New(Deps{
		AdGuard:  client,
		ClientIP: ClientIP(nil),
		Log:      log,
		Auth:     auth.New(sessions, auth.Options{Now: h.clock.Now, Log: log, ErrorWriter: UnauthorizedWriter, Devices: devices}),
		Limiter:  ratelimit.New(cfg),
		Sessions: sessions,
		Children: h.store,
		Buttons:  h.store,
		Devices:  devices,
	}).Handler()
}

// logsWithMsg returns the parsed log lines whose msg is msg.
func (h *harness) logsWithMsg(msg string) []map[string]any {
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
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func withDevice(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.DeviceCookieName, Value: v}) }
}

func deviceCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.DeviceCookieName {
			if found != nil {
				t.Fatalf("two Set-Cookie headers for %s", auth.DeviceCookieName)
			}
			found = c
		}
	}
	return found
}

// setPIN sets a first PIN on a fresh login and returns the session and
// device cookie values.
func (h *harness) setPIN(pin string) (session, device string) {
	h.t.Helper()
	session = h.loginOK()
	w := h.do(http.MethodPost, "/api/v1/pin", map[string]string{"pin": pin}, withCookie(session))
	if w.Code != http.StatusNoContent {
		h.t.Fatalf("set PIN: status %d body %s", w.Code, w.Body.String())
	}
	dc := deviceCookie(h.t, w)
	if dc == nil || dc.Value == "" {
		h.t.Fatal("set PIN: no device cookie")
	}
	return session, dc.Value
}

func (h *harness) pinHashes() []string {
	h.t.Helper()
	rows, err := h.store.DB().Query(`SELECT pin_hash FROM devices ORDER BY id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (h *harness) deviceCount() int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM devices`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

var phcPattern = regexp.MustCompile(`^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)

func TestSetPIN_First(t *testing.T) {
	h := newHarness(t, nil, nil)
	session := h.loginOK()
	hits := h.loginHits()

	w := h.do(http.MethodPost, "/api/v1/pin", map[string]string{"pin": "583920"}, withCookie(session))
	if w.Code != http.StatusNoContent {
		t.Fatalf("first set: status %d body %s", w.Code, w.Body.String())
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("first set hit /control/login: %d -> %d", hits, got)
	}
	dc := deviceCookie(t, w)
	if dc == nil || !dc.HttpOnly || dc.SameSite != http.SameSiteStrictMode || dc.Path != "/" {
		t.Fatalf("device cookie = %+v, want HttpOnly SameSite=Strict Path=/", dc)
	}

	var pinHash string
	var tokenHash []byte
	if err := h.store.DB().QueryRow(`SELECT pin_hash, token_hash FROM devices`).Scan(&pinHash, &tokenHash); err != nil {
		t.Fatal(err)
	}
	if !phcPattern.MatchString(pinHash) {
		t.Fatalf("pin_hash %q is not Argon2id PHC", pinHash)
	}
	raw, err := base64.RawURLEncoding.DecodeString(dc.Value)
	if err != nil || len(raw) != 32 {
		t.Fatalf("device cookie %q: %d bytes, %v", dc.Value, len(raw), err)
	}
	if sum := sha256.Sum256(raw); !bytes.Equal(tokenHash, sum[:]) {
		t.Fatal("token_hash is not sha256 of the decoded device cookie")
	}
	if ok, err := auth.VerifyPIN("583920", pinHash); !ok || err != nil {
		t.Fatalf("stored hash does not verify the PIN: %v %v", ok, err)
	}
}

func TestSetPIN_NoPlaintext(t *testing.T) {
	h := newHarness(t, nil, nil)
	_, device := h.setPIN("583920")
	secrets := []string{"583920", device}

	// Every row of every table.
	var tables []string
	rows, err := h.store.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	var dump strings.Builder
	for _, tbl := range tables {
		r, err := h.store.DB().Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					dump.Write(x)
				case string:
					dump.WriteString(x)
				default:
					dump.WriteString(strings.TrimSpace(strings.ReplaceAll(fmtAny(x), "\n", " ")))
				}
				dump.WriteByte('|')
			}
		}
		r.Close()
	}

	// Every file in the data dir (.db, -wal, -shm).
	entries, err := os.ReadDir(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var files [][]byte
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(h.dataDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, b)
	}
	if len(files) == 0 {
		t.Fatal("data dir is empty")
	}

	// The debug log, each line's time attr removed (a timestamp could
	// contain six digits by chance).
	var logText strings.Builder
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		delete(m, "time")
		b, _ := json.Marshal(m)
		logText.Write(b)
		logText.WriteByte('\n')
	}
	if !strings.Contains(logText.String(), `"pin set"`) {
		t.Fatal("positive control: the debug log has no pin set line")
	}

	for _, s := range secrets {
		if strings.Contains(dump.String(), s) {
			t.Errorf("%q appears in the table dump", s)
		}
		for i, b := range files {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%q appears in data dir file %s", s, entries[i].Name())
			}
		}
		if strings.Contains(logText.String(), s) {
			t.Errorf("%q appears in the debug log", s)
		}
	}
}

func fmtAny(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSetPIN_BadInput(t *testing.T) {
	h := newHarness(t, nil, nil)
	session := h.loginOK()
	for name, body := range map[string]string{
		"short":         `{"pin":"123"}`,
		"long":          `{"pin":"1234567"}`,
		"letter":        `{"pin":"12a4"}`,
		"arabic-indic":  `{"pin":"١٢٣٤"}`,
		"number":        `{"pin":1234}`,
		"missing":       `{}`,
		"unknown key":   `{"pin":"1234","nope":1}`,
		"oversize":      `{"pin":"1234","password":"` + strings.Repeat("x", 5000) + `"}`,
		"two objects":   `{"pin":"1234"}{"pin":"5678"}`,
		"not json":      `pin=1234`,
		"stray brace":   `{"pin":"1234"}}`,
		"padded":        `{"pin":"1234"}` + strings.Repeat(" ", 5000),
		"trailing junk": `{"pin":"1234"} x`,
	} {
		w := h.do(http.MethodPost, "/api/v1/pin", body, withCookie(session))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s, want 400", name, w.Code, w.Body.String())
		}
	}
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("bad input created %d device rows", n)
	}
	if w := h.do(http.MethodPost, "/api/v1/pin", `{"pin":"1234"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", w.Code)
	}
	if n := h.deviceCount(); n != 0 {
		t.Fatalf("unauthenticated set created %d device rows", n)
	}
}

func TestSetPIN_IdleLock(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, device := h.setPIN("583920")
	const justUnder = 14*time.Minute + 59*time.Second

	h.clock.Advance(justUnder)
	if w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session), withDevice(device)); w.Code != http.StatusOK {
		t.Fatalf("GET /me at +14m59s: %d %s", w.Code, w.Body.String())
	}
	h.clock.Advance(15 * time.Minute)
	w := h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session), withDevice(device))
	if w.Code != http.StatusUnauthorized || decodeErr(t, w).Error != auth.CodePINRequired {
		t.Fatalf("GET /me after 15m idle: %d %s, want 401 pin_required", w.Code, w.Body.String())
	}
	w = h.do(http.MethodGet, "/api/v1/me", nil, withCookie(session))
	if w.Code != http.StatusUnauthorized || decodeErr(t, w).Error != CodeUnauthorized {
		t.Fatalf("same request without the device cookie: %d %s, want 401 unauthorized", w.Code, w.Body.String())
	}

	// Set at T with no further request: T+15m01s is locked.
	h2 := newHarness(t, nil, nil)
	s2, d2 := h2.setPIN("583920")
	h2.clock.Advance(15*time.Minute + time.Second)
	w = h2.do(http.MethodGet, "/api/v1/me", nil, withCookie(s2), withDevice(d2))
	if w.Code != http.StatusUnauthorized || decodeErr(t, w).Error != auth.CodePINRequired {
		t.Fatalf("T+15m01s after set: %d %s, want 401 pin_required", w.Code, w.Body.String())
	}
}

func TestSetPIN_TaggedWithoutCookie(t *testing.T) {
	h := newHarness(t, nil, nil)
	session, _ := h.setPIN("583920")
	before := h.pinHashes()
	hits := h.loginHits()

	w := h.do(http.MethodPost, "/api/v1/pin", map[string]string{"pin": "111111"}, withCookie(session))
	if w.Code != http.StatusForbidden || decodeErr(t, w).Error != CodeReauthRequired {
		t.Fatalf("tagged session, no device cookie, no password: %d %s, want 403 reauth_required", w.Code, w.Body.String())
	}
	if after := h.pinHashes(); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("pin_hash changed: %v -> %v", before, after)
	}
	if got := h.loginHits(); got != hits {
		t.Fatalf("refused change reached AdGuard: hits %d -> %d", hits, got)
	}
	if dc := deviceCookie(t, w); dc != nil {
		t.Fatalf("refused change set a device cookie: %+v", dc)
	}
}

func TestPinStatus(t *testing.T) {
	h := newHarness(t, nil, nil)
	session := h.loginOK()
	status := func() bool {
		t.Helper()
		w := h.do(http.MethodGet, "/api/v1/pin", nil, withCookie(session))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/pin: %d %s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		v, ok := body["has_pin"].(bool)
		if !ok || len(body) != 1 {
			t.Fatalf("body %s, want exactly {has_pin: bool}", w.Body.String())
		}
		return v
	}
	if status() {
		t.Fatal("has_pin true before set")
	}
	if w := h.do(http.MethodPost, "/api/v1/pin", map[string]string{"pin": "583920"}, withCookie(session)); w.Code != http.StatusNoContent {
		t.Fatalf("set: %d", w.Code)
	}
	if !status() {
		t.Fatal("has_pin false after set")
	}
	if w := h.do(http.MethodGet, "/api/v1/pin", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/pin without a session: %d, want 401", w.Code)
	}
}

func TestSetPIN_AttachFailureCleanupLog(t *testing.T) {
	attachErr := errors.New("attach refused")
	for _, tc := range []struct {
		name      string
		deleteErr error
		// devices is the device rows left behind: 0 when the cleanup
		// delete ran, 1 when it failed.
		devices int
	}{
		{name: "cleanup succeeds", devices: 0},
		{name: "cleanup fails", deleteErr: errors.New("delete refused"), devices: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			session := h.loginOK()
			h.rewire(
				faultSessions{SessionStore: h.store, setDeviceErr: attachErr},
				faultDevices{DeviceStore: h.store, deleteErr: tc.deleteErr},
			)

			w := h.do(http.MethodPost, "/api/v1/pin", map[string]string{"pin": "583920"}, withCookie(session))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("attach failing: %d %s, want 500", w.Code, w.Body.String())
			}
			if dc := deviceCookie(t, w); dc != nil {
				t.Fatalf("attach failing set a device cookie: %+v", dc)
			}
			if n := len(h.logsWithMsg("attach device failed")); n != 1 {
				t.Fatalf("positive control: %d attach device failed lines, want 1", n)
			}
			if n := h.deviceCount(); n != tc.devices {
				t.Fatalf("device rows after the failed attach = %d, want %d", n, tc.devices)
			}

			drops := h.logsWithMsg("drop unattached device failed")
			if tc.deleteErr == nil {
				if len(drops) != 0 {
					t.Fatalf("cleanup delete succeeded but logged %v", drops)
				}
				return
			}
			if len(drops) != 1 || drops[0]["level"] != "ERROR" || drops[0]["err"] != tc.deleteErr.Error() {
				t.Fatalf("cleanup delete failed: lines %v, want one ERROR with err %q", drops, tc.deleteErr)
			}
		})
	}
}
