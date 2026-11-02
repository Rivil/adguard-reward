package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

// countingCustom wraps a CustomServiceStore, counting List calls and
// failing them with err when set.
type countingCustom struct {
	inner CustomServiceStore
	err   error
	calls atomic.Int32
}

func (c *countingCustom) ListCustomServices(ctx context.Context) ([]store.CustomService, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.ListCustomServices(ctx)
}

// newCatalogueHarness is newHarness with Deps.CustomServices wired to cs
// (nil: the harness store). The handler is rebuilt over the same store,
// fake and clock, so sessions from loginOK are valid; Grants is left nil —
// nothing here reaches it.
func newCatalogueHarness(t *testing.T, cs CustomServiceStore) *harness {
	t.Helper()
	h := newHarness(t, nil, nil)
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := adguard.New(h.fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if cs == nil {
		cs = h.store
	}
	cfg := ratelimit.Defaults()
	cfg.Now = h.clock.Now
	h.h = New(Deps{
		AdGuard:        client,
		ClientIP:       ClientIP(nil),
		Log:            log,
		Auth:           auth.New(h.store, auth.Options{Now: h.clock.Now, Log: log, ErrorWriter: UnauthorizedWriter}),
		Limiter:        ratelimit.New(cfg),
		Sessions:       h.store,
		Children:       h.store,
		Buttons:        h.store,
		CustomServices: cs,
	}).Handler()
	return h
}

// getServicesRaw is GET /api/v1/services decoded as generic objects, so a
// missing or extra key fails a DeepEqual.
func getServicesRaw(t *testing.T, h *harness, cookie string) (int, []map[string]any) {
	t.Helper()
	w := h.do(http.MethodGet, "/api/v1/services", nil, withCookie(cookie))
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var body struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return w.Code, body.Services
}

func builtIn(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "icon": "PHN2Zy8+", "custom": false}
}

// TestServices_IncludesCustom: a stored custom service follows AdGuard's
// catalogue in /services with its custom: id, an empty icon and
// custom:true; the built-ins carry custom:false.
func TestServices_IncludesCustom(t *testing.T) {
	h := newCatalogueHarness(t, nil)
	cookie := h.loginOK()
	portal, err := h.store.CreateCustomService(context.Background(), "Portal", []string{"portal.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if portal.ID != 1 {
		t.Fatalf("precondition: Portal id = %d, want 1", portal.ID)
	}

	code, got := getServicesRaw(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("GET /services = %d, want 200", code)
	}
	want := []map[string]any{
		builtIn("youtube", "YouTube"),
		builtIn("tiktok", "TikTok"),
		builtIn("roblox", "Roblox"),
		{"id": "custom:1", "name": "Portal", "icon": "", "custom": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}

	// A second service lands after the first, in id order.
	if _, err := h.store.CreateCustomService(context.Background(), "Homework", []string{"hw.example.com"}); err != nil {
		t.Fatal(err)
	}
	_, got = getServicesRaw(t, h, cookie)
	if n := len(got); n != 5 || got[3]["id"] != "custom:1" || got[4]["id"] != "custom:2" || got[4]["name"] != "Homework" {
		t.Errorf("after a second service: %v, want custom:1 then custom:2 Homework last", got)
	}
}

// TestServices_CatalogueDown: an AdGuard catalogue failure is a 502 even
// with custom services stored — they never stand in for the outage — and
// the custom list is not read at all.
func TestServices_CatalogueDown(t *testing.T) {
	cs := &countingCustom{}
	h := newCatalogueHarness(t, cs)
	cs.inner = h.store // set before any request reaches it
	cookie := h.loginOK()
	if _, err := h.store.CreateCustomService(context.Background(), "Portal", []string{"portal.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.fake.SetStatus("/control/blocked_services/all", http.StatusBadGateway)

	w := h.do(http.MethodGet, "/api/v1/services", nil, withCookie(cookie))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("GET /services with AdGuard down = %d %s, want 502", w.Code, w.Body.String())
	}
	var e errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error != CodeAdGuardUnavailable {
		t.Errorf("body = %s, want error %q", w.Body.String(), CodeAdGuardUnavailable)
	}
	if n := cs.calls.Load(); n != 0 {
		t.Errorf("custom services listed %d times during an AdGuard outage, want 0", n)
	}
}

// TestServices_CustomStoreDown: a failed custom-service read is a 500, not
// a built-in-only list that would silently drop the parent's services.
func TestServices_CustomStoreDown(t *testing.T) {
	cs := &countingCustom{err: errors.New("disk on fire")}
	h := newCatalogueHarness(t, cs)
	cookie := h.loginOK()

	w := h.do(http.MethodGet, "/api/v1/services", nil, withCookie(cookie))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET /services with the store failing = %d %s, want 500", w.Code, w.Body.String())
	}
	if n := cs.calls.Load(); n != 1 {
		t.Errorf("custom services listed %d times, want 1", n)
	}
}

// TestServices_NoCustomStore: with Deps.CustomServices nil the catalogue is
// AdGuard's alone, every entry custom:false.
func TestServices_NoCustomStore(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	code, got := getServicesRaw(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("GET /services = %d, want 200", code)
	}
	want := []map[string]any{builtIn("youtube", "YouTube"), builtIn("tiktok", "TikTok"), builtIn("roblox", "Roblox")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
}
