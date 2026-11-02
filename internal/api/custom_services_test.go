package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

const (
	customPath   = "/api/v1/custom-services"
	setRulesPath = "/control/filtering/set_rules"
)

// newCustomHarness is newHarness with a real engine as Grants and the store
// as CustomServices, over the same store, fake and clock.
func newCustomHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil, nil)
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := adguard.New(h.fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	eng := grants.New(h.store, client, grants.Options{Now: h.clock.Now, ApplyTimeout: harnessApplyTimeout, Log: log})
	t.Cleanup(eng.Close)
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
		Grants:         eng,
		Buttons:        h.store,
		CustomServices: h.store,
	}).Handler()
	return h
}

type customBody struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

func decodeCustom(t *testing.T, b []byte) customServiceView {
	t.Helper()
	var v customServiceView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func listCustom(t *testing.T, h *harness, cookie string) []customServiceView {
	t.Helper()
	w := h.do(http.MethodGet, customPath, nil, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", customPath, w.Code, w.Body)
	}
	var body struct {
		CustomServices []customServiceView `json:"custom_services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.CustomServices
}

func createCustom(t *testing.T, h *harness, cookie, name string, domains ...string) customServiceView {
	t.Helper()
	w := h.do(http.MethodPost, customPath, customBody{name, domains}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST %s %q = %d %s", customPath, name, w.Code, w.Body)
	}
	return decodeCustom(t, w.Body.Bytes())
}

// managedRegion is the fake's lines between the markers (nil: no region).
func managedRegion(h *harness) []string {
	all := h.fake.UserRules()
	b, e := slices.Index(all, rules.BeginMarker), slices.Index(all, rules.EndMarker)
	if b < 0 || e < b {
		return nil
	}
	return all[b+1 : e]
}

func noRegionLine(t *testing.T, h *harness, s string) {
	t.Helper()
	for _, l := range h.fake.UserRules() {
		if strings.Contains(l, s) {
			t.Fatalf("user_rules line %q contains %q", l, s)
		}
	}
}

func message(t *testing.T, b []byte) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("decode error %s: %v", b, err)
	}
	return e.Message
}

func TestCustomServices_BadDomain(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	bad := []string{
		"https://a.com", "a.com/x", "a.com:443", "*.a.com", "a .com", "a.com\n",
		"||a.com", "a.com^", "a.com$important", `/a\.com/`, "@@a.com", "com", "",
	}
	for _, d := range bad {
		for _, domains := range [][]string{{d}, {"ok.com", d}} {
			w := h.do(http.MethodPost, customPath, customBody{"Portal", domains}, withCookie(cookie))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%q in %q: %d %s, want 400", d, domains, w.Code, w.Body)
			}
			msg := message(t, w.Body.Bytes())
			if !strings.Contains(msg, fmt.Sprintf("%q", d)) || !strings.Contains(msg, fmt.Sprintf("domains[%d]", len(domains)-1)) {
				t.Errorf("%q: message %q does not name the domain and its index", d, msg)
			}
		}
	}
	if got := listCustom(t, h, cookie); len(got) != 0 {
		t.Fatalf("stored after rejections: %+v", got)
	}
	if n := h.fake.CountRequests("POST", setRulesPath); n != 0 {
		t.Fatalf("%d set_rules POSTs after rejections", n)
	}

	cs := createCustom(t, h, cookie, "Portal", "a.com")
	w := h.do(http.MethodPut, customPath+"/"+strconv.FormatInt(cs.ID, 10), customBody{"Portal", []string{"b.com", "b.com/x"}}, withCookie(cookie))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT bad domain = %d %s", w.Code, w.Body)
	}
	if got := listCustom(t, h, cookie); len(got) != 1 || !reflect.DeepEqual(got[0].Domains, []string{"a.com"}) {
		t.Fatalf("domains after a refused PUT: %+v", got)
	}

	for name, body := range map[string]customBody{
		"no domains": {"Portal", nil},
		"51 domains": {"Portal", func() []string {
			var d []string
			for i := range 51 {
				d = append(d, fmt.Sprintf("d%d.com", i))
			}
			return d
		}()},
	} {
		if w := h.do(http.MethodPost, customPath, body, withCookie(cookie)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	for _, name := range []string{"", "   ", strings.Repeat("é", 65)} {
		if w := h.do(http.MethodPost, customPath, customBody{name, []string{"x.com"}}, withCookie(cookie)); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("name %q: %d, want 422", name, w.Code)
		}
	}
	if w := h.do(http.MethodPost, customPath, `{"name":"P","domains":["x.com"],"extra":1}`, withCookie(cookie)); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", w.Code)
	}
}

func TestCustomServices_Create(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	if _, err := h.store.CreateChild(context.Background(), "Ada", []string{"kid-phone", "kid-tablet"}); err != nil {
		t.Fatal(err)
	}
	w := h.do(http.MethodPost, customPath, customBody{"  Portal ", []string{"Portal.Example.COM."}}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": float64(1), "service_id": "custom:1", "name": "Portal",
		"domains": []any{"portal.example.com"}, "applied": true,
	}
	if !reflect.DeepEqual(raw, want) {
		t.Fatalf("body %v, want %v", raw, want)
	}
	if got, want := managedRegion(h), []string{"||portal.example.com^$client='kid-phone'|'kid-tablet'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
	for _, unmapped := range []string{"Kid phone", "Old laptop", "printer", "Zed-TV"} {
		noRegionLine(t, h, "'"+unmapped+"'")
	}
	if got := listCustom(t, h, cookie); len(got) != 1 || got[0].ServiceID != "custom:1" {
		t.Fatalf("list %+v", got)
	}
}

func TestCustomServices_Conflicts(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()

	w := h.do(http.MethodPost, customPath, customBody{"youtube", []string{"yt.example"}}, withCookie(cookie))
	if w.Code != http.StatusConflict || !strings.Contains(message(t, w.Body.Bytes()), "YouTube") {
		t.Fatalf("built-in clash = %d %s", w.Code, w.Body)
	}

	h.fake.SetStatus("/control/blocked_services/all", http.StatusBadGateway)
	w = h.do(http.MethodPost, customPath, customBody{"Portal", []string{"a.com"}}, withCookie(cookie))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("catalogue down = %d %s, want 502", w.Code, w.Body)
	}
	h.fake.SetResponse("/control/blocked_services/all", 0, nil)
	if got := listCustom(t, h, cookie); len(got) != 0 {
		t.Fatalf("stored while the catalogue was down: %+v", got)
	}

	createCustom(t, h, cookie, "Portal", "a.com")
	w = h.do(http.MethodPost, customPath, customBody{"PORTAL", []string{"b.com"}}, withCookie(cookie))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate custom name = %d %s", w.Code, w.Body)
	}
	w = h.do(http.MethodPost, customPath, customBody{"Other", []string{"b.com", "A.com"}}, withCookie(cookie))
	if w.Code != http.StatusConflict || !strings.Contains(message(t, w.Body.Bytes()), "Portal") {
		t.Fatalf("domain owned elsewhere = %d %s, want 409 naming Portal", w.Code, w.Body)
	}
	if got := listCustom(t, h, cookie); len(got) != 1 {
		t.Fatalf("list after conflicts %+v", got)
	}
}

func TestCustomServices_Update(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	if _, err := h.store.CreateChild(context.Background(), "Ada", []string{"kid-phone"}); err != nil {
		t.Fatal(err)
	}
	cs := createCustom(t, h, cookie, "Portal", "a.com", "b.com")
	w := h.do(http.MethodPut, customPath+"/"+strconv.FormatInt(cs.ID, 10), customBody{"Portal", []string{"b.com", "c.com"}}, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if got := decodeCustom(t, w.Body.Bytes()); !reflect.DeepEqual(got.Domains, []string{"b.com", "c.com"}) || !got.Applied {
		t.Fatalf("body %+v", got)
	}
	noRegionLine(t, h, "a.com")
	if got := managedRegion(h); len(got) != 2 {
		t.Fatalf("region %q, want b.com and c.com block lines", got)
	}
	for _, path := range []string{customPath + "/99", customPath + "/abc"} {
		if w := h.do(http.MethodPut, path, customBody{"X", []string{"x.com"}}, withCookie(cookie)); w.Code != http.StatusNotFound {
			t.Errorf("PUT %s = %d, want 404", path, w.Code)
		}
	}
}

func TestCustomServices_Delete(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ctx := context.Background()
	ada, err := h.store.CreateChild(ctx, "Ada", []string{"kid-phone"})
	if err != nil {
		t.Fatal(err)
	}
	cs := createCustom(t, h, cookie, "Portal", "a.com")
	g, err := h.store.CreateGrant(ctx, ada.ID, []string{cs.ServiceID}, []string{"kid-phone"}, t0, t0.Add(3600e9))
	if err != nil {
		t.Fatal(err)
	}
	path := customPath + "/" + strconv.FormatInt(cs.ID, 10)

	w := h.do(http.MethodDelete, path, nil, withCookie(cookie))
	if w.Code != http.StatusConflict {
		t.Fatalf("DELETE in use = %d %s", w.Code, w.Body)
	}
	var body struct {
		Message string `json:"message"`
		GrantID int64  `json:"grant_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.GrantID != g.ID || !strings.Contains(body.Message, strconv.FormatInt(g.ID, 10)) || !strings.Contains(body.Message, "Portal") {
		t.Fatalf("409 body %+v, want grant_id %d named in the message", body, g.ID)
	}
	if got := listCustom(t, h, cookie); len(got) != 1 {
		t.Fatalf("list after refused delete %+v", got)
	}

	if ok, err := h.store.SetGrantStatus(ctx, g.ID, store.StatusActive, store.StatusEnded, t0); err != nil || !ok {
		t.Fatal(err)
	}
	if w := h.do(http.MethodDelete, path, nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", w.Code, w.Body)
	}
	noRegionLine(t, h, "a.com")
	if got := listCustom(t, h, cookie); len(got) != 0 {
		t.Fatalf("list after delete %+v", got)
	}
	if w := h.do(http.MethodDelete, path, nil, withCookie(cookie)); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", w.Code)
	}
}

func TestCustomServices_Edges(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()

	h.fake.SetStatus(setRulesPath, http.StatusInternalServerError)
	w := h.do(http.MethodPost, customPath, customBody{"Portal", []string{"a.com"}}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST under a set_rules fault = %d %s, want 201", w.Code, w.Body)
	}
	if got := decodeCustom(t, w.Body.Bytes()); got.Applied {
		t.Fatalf("applied = true under a set_rules fault: %+v", got)
	}
	h.fake.SetResponse(setRulesPath, 0, nil)

	if w := h.do(http.MethodGet, customPath, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d, want 401", w.Code)
	}
	if w := h.do(http.MethodPost, customPath, customBody{"X", []string{"x.com"}}, withCookie(cookie), noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("missing CSRF: %d, want 403", w.Code)
	}
}

func TestCustomServices_Rename(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	portal := createCustom(t, h, cookie, "portal", "a.com")
	createCustom(t, h, cookie, "School", "s.com")
	path := customPath + "/" + strconv.FormatInt(portal.ID, 10)

	for _, name := range []string{"YouTube", "SCHOOL"} {
		if w := h.do(http.MethodPut, path, customBody{name, []string{"a.com"}}, withCookie(cookie)); w.Code != http.StatusConflict {
			t.Errorf("rename to %q = %d %s, want 409", name, w.Code, w.Body)
		}
	}
	w := h.do(http.MethodPut, path, customBody{"Portal", []string{"a.com"}}, withCookie(cookie))
	if w.Code != http.StatusOK || decodeCustom(t, w.Body.Bytes()).Name != "Portal" {
		t.Fatalf("own case change = %d %s, want 200", w.Code, w.Body)
	}
}

func TestCustomServices_DedupeDomains(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	if _, err := h.store.CreateChild(context.Background(), "Ada", []string{"kid-phone"}); err != nil {
		t.Fatal(err)
	}
	cs := createCustom(t, h, cookie, "Portal", "a.com", "A.com.", "b.com")
	if !reflect.DeepEqual(cs.Domains, []string{"a.com", "b.com"}) {
		t.Fatalf("domains %v, want [a.com b.com]", cs.Domains)
	}
	n := 0
	for _, l := range managedRegion(h) {
		if strings.HasPrefix(l, "||a.com^") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d ||a.com^ block lines, want 1: %q", n, managedRegion(h))
	}
}
