package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type blockedBody struct {
	Child    childRef             `json:"child"`
	Clients  []blockedClientView  `json:"clients"`
	Services []blockedServiceView `json:"services"`
}

func (h *harness) getBlocked(cookie string, id int64) (*httptest.ResponseRecorder, blockedBody) {
	h.t.Helper()
	w := h.do(http.MethodGet, childPath(id)+"/blocked", nil, withCookie(cookie))
	var body blockedBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("blocked body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

func stateOf(b blockedBody) map[string][2]any {
	out := map[string][2]any{}
	for _, s := range b.Services {
		out[s.ID] = [2]any{s.State, s.Differs}
	}
	return out
}

func TestBlocked_View(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "Kid tablet", "Ghost")
	before := len(h.fake.Requests())

	w, body := h.getBlocked(cookie, ada.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if body.Child != (childRef{ID: ada.ID, Name: "Ada"}) {
		t.Errorf("child = %+v", body.Child)
	}
	want := map[string][2]any{
		"youtube": {"partial", []string{"Kid tablet"}},
		"tiktok":  {"blocked", []string{}},
		"roblox":  {"partial", []string{"Kid phone"}},
	}
	if got := stateOf(body); !reflect.DeepEqual(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
	if !strings.Contains(w.Body.String(), `"differs":[]`) {
		t.Errorf("raw body must carry differs [] not null: %s", w.Body.String())
	}
	wantClients := []blockedClientView{
		{Name: "Ghost", Missing: true},
		{Name: "Kid phone"},
		{Name: "Kid tablet", UsesGlobal: true},
	}
	if !reflect.DeepEqual(body.Clients, wantClients) {
		t.Errorf("clients = %+v, want %+v", body.Clients, wantClients)
	}
	reqs := h.fake.Requests()[before:]
	counts := map[string]int{}
	for _, r := range reqs {
		counts[r.Method+" "+r.Path]++
	}
	for _, p := range []string{"GET /control/clients", "GET /control/blocked_services/get", "GET /control/blocked_services/all"} {
		if counts[p] != 1 {
			t.Errorf("%s hit %d times for one view, want 1", p, counts[p])
		}
	}
}

func TestBlocked_Live(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	_, body := h.getBlocked(cookie, ada.ID)
	if stateOf(body)["roblox"][0] != "unblocked" {
		t.Fatalf("roblox before = %v", stateOf(body)["roblox"])
	}
	h.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","tiktok","roblox"]`)
	})
	_, body = h.getBlocked(cookie, ada.ID)
	if stateOf(body)["roblox"][0] != "blocked" {
		t.Errorf("roblox after MutateClient = %v, want blocked (fold must re-run per request)", stateOf(body)["roblox"])
	}
}

func TestBlocked_MissingIsNotAnError(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ghost := h.createOK(cookie, "Ghosty", "Ghost")
	w, body := h.getBlocked(cookie, ghost.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for id, st := range stateOf(body) {
		if st[0] != "unblocked" {
			t.Errorf("%s = %v, want unblocked", id, st)
		}
	}
	if len(body.Clients) != 1 || !body.Clients[0].Missing {
		t.Errorf("clients = %+v, want Ghost missing", body.Clients)
	}
	w = h.do(http.MethodGet, childPath(ghost.ID), nil, withCookie(cookie))
	if got := h.decodeChild(w); !reflect.DeepEqual(got.Clients, []string{"Ghost"}) {
		t.Errorf("Ghost was pruned: %+v", got)
	}
}

func TestBlocked_AdGuardDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	expect502 := func(label, path string) {
		t.Helper()
		w := h.do(http.MethodGet, path, nil, withCookie(cookie))
		if w.Code != http.StatusBadGateway {
			t.Errorf("%s: GET %s = %d %s, want 502", label, path, w.Code, w.Body.String())
			return
		}
		if e := decodeErr(t, w); e.Error != CodeAdGuardUnavailable {
			t.Errorf("%s: GET %s error = %q, want adguard_unavailable", label, path, e.Error)
		}
		if strings.Contains(w.Body.String(), `"services"`) || strings.Contains(w.Body.String(), `"clients"`) {
			t.Errorf("%s: GET %s carried a partial body: %s", label, path, w.Body.String())
		}
	}
	clientsPath, blockedPath, servicesPath := "/api/v1/clients", childPath(ada.ID)+"/blocked", "/api/v1/services"

	h.fake.SetStatus("/control/clients", 500)
	expect502("clients 500", clientsPath)
	expect502("clients 500", blockedPath)
	h.fake.SetResponse("/control/clients", 0, nil)

	h.fake.SetStatus("/control/blocked_services/all", 503)
	expect502("all 503", servicesPath)
	expect502("all 503", blockedPath)
	h.fake.SetResponse("/control/blocked_services/all", 0, nil)

	h.fake.SetStatus("/control/blocked_services/get", 500)
	expect502("get 500", clientsPath)
	expect502("get 500", blockedPath)
	h.fake.SetResponse("/control/blocked_services/get", 0, nil)

	h.fake.SetAuth(false)
	for _, p := range []string{clientsPath, blockedPath, servicesPath} {
		w := h.do(http.MethodGet, p, nil, withCookie(cookie))
		if w.Code == http.StatusUnauthorized {
			t.Errorf("SetAuth(false): GET %s = 401 — the service credential is not the parent's session", p)
		}
		expect502("auth off", p)
	}
	h.fake.SetAuth(true)

	h.fake.Close()
	for _, p := range []string{clientsPath, blockedPath, servicesPath} {
		expect502("transport", p)
	}
}

func TestBlocked_NotFound(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	before := len(h.fake.Requests())
	w := h.do(http.MethodGet, "/api/v1/children/999/blocked", nil, withCookie(cookie))
	if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Fatalf("%d %s, want 404 not_found", w.Code, w.Body.String())
	}
	if w := h.do(http.MethodGet, "/api/v1/children/abc/blocked", nil, withCookie(cookie)); w.Code != http.StatusNotFound {
		t.Errorf("non-numeric id: %d, want 404", w.Code)
	}
	if extra := h.fake.Requests()[before:]; len(extra) != 0 {
		t.Errorf("404 hit AdGuard: %+v", extra)
	}
	if w := h.do(http.MethodGet, "/api/v1/children/1/blocked", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d, want 401", w.Code)
	}
}
