package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func (h *harness) postRename(cookie, from, to string, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/clients/rename", map[string]any{"from": from, "to": to},
		append([]reqOpt{withCookie(cookie)}, opts...)...)
}

func TestRename_UpdatesMappingAndGrants(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)

	w := h.postRename(cookie, "Kid phone", "Ada phone")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", w.Code, w.Body.String())
	}
	var v clientView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "Ada phone" || v.Child == nil || v.Child.ID != ada.ID ||
		!reflect.DeepEqual(v.IDs, []string{"aa:bb:cc:dd:ee:01", "192.168.1.50"}) {
		t.Errorf("200 body = %+v, want Ada phone with Kid phone's ids, child Ada", v)
	}
	_, children := h.listChildren(cookie)
	if len(children.Children) != 1 || !reflect.DeepEqual(children.Children[0].Clients, []string{"Ada phone"}) {
		t.Errorf("GET /children = %+v, want Ada holding [Ada phone]", children.Children)
	}
	_, grants := h.listGrants(cookie)
	if len(grants.Grants) != 1 || !reflect.DeepEqual(grants.Grants[0].Clients, []string{"Ada phone"}) {
		t.Errorf("GET /grants = %+v, want the active grant on [Ada phone]", grants.Grants)
	}
}

func TestRename_Unassigned(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	if w := h.postRename(cookie, "Old laptop", "Den laptop"); w.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", w.Code, w.Body.String())
	}
	_, body := h.getClients(cookie)
	if c := findClient(body.Clients, "Den laptop"); c == nil || c.Child != nil {
		t.Errorf("Den laptop = %+v, want listed and unassigned", c)
	}
}

func TestRename_SpecialChars(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	odd := "Kid/phone 100%2F"
	if w := h.postRename(cookie, "Kid phone", odd); w.Code != http.StatusOK {
		t.Fatalf("to %q: %d %s", odd, w.Code, w.Body.String())
	}
	_, body := h.getClients(cookie)
	if findClient(body.Clients, odd) == nil {
		t.Errorf("GET /clients lacks %q", odd)
	}
	if w := h.postRename(cookie, odd, "Kid phone"); w.Code != http.StatusOK {
		t.Fatalf("back from %q: %d %s", odd, w.Code, w.Body.String())
	}
	if _, ok := h.fake.BlockedServices("Kid phone"); !ok {
		t.Error("Kid phone did not round-trip")
	}
}

func TestRename_Errors(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Bob", "Ghost")

	before := len(h.fake.Requests())
	for _, b := range []any{
		map[string]any{"from": "Kid phone", "to": ""},
		map[string]any{"from": "Kid phone", "to": "   "},
		map[string]any{"from": "", "to": "Ada phone"},
		map[string]any{"from": "Kid phone", "to": strings.Repeat("x", 257)},
		map[string]any{"from": "Kid phone", "to": "Ada phone", "extra": true},
	} {
		w := h.do(http.MethodPost, "/api/v1/clients/rename", b, withCookie(cookie))
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Error != CodeBadRequest {
			t.Errorf("%v: %d %s, want 400", b, w.Code, w.Body.String())
		}
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("bad input made %d AdGuard requests, want 0", n)
	}

	if w := h.postRename(cookie, "Nobody", "Ada phone"); w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Errorf("unknown from: %d %s, want 404", w.Code, w.Body.String())
	}
	if w := h.postRename(cookie, "Kid phone", "Kid tablet"); w.Code != http.StatusConflict || decodeErr(t, w).Error != CodeConflict {
		t.Errorf("to Kid tablet: %d %s, want 409", w.Code, w.Body.String())
	}
	w := h.postRename(cookie, "Kid phone", "Ghost")
	if e := decodeErr(t, w); w.Code != http.StatusConflict || !strings.Contains(e.Message, "Bob") {
		t.Errorf("to Bob's stale Ghost: %d %s, want 409 naming Bob", w.Code, w.Body.String())
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("refused renames made %d update POSTs, want 0", n)
	}
}

func TestRename_Unavailable(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.fake.SetStatus("/control/clients", 500)

	w := h.postRename(cookie, "Kid phone", "Ada phone")
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("%d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
	if got := h.decodeChild(h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie))).Clients; !slices.Equal(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want unchanged [Kid phone]", got)
	}
}

func TestRename_CSRF(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	if w := h.postRename(cookie, "Kid phone", "Ada phone", noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF header: %d, want 403", w.Code)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
}

func TestRename_AdGuardRejects(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetResponse("/control/clients/update", 400, []byte("invalid client name"))

	w := h.postRename(cookie, "Kid phone", "Ada phone")
	e := decodeErr(t, w)
	if w.Code != http.StatusBadRequest || e.Error != CodeBadRequest || !strings.Contains(e.Message, "invalid client name") {
		t.Errorf("%d %s, want 400 bad_request carrying AdGuard's message", w.Code, w.Body.String())
	}
}
