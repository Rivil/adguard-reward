package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
)

type clientsBody struct {
	Clients    []clientView     `json:"clients"`
	Unlabelled []unlabelledView `json:"unlabelled"`
}

func (h *harness) getClients(cookie string) (*httptest.ResponseRecorder, clientsBody) {
	h.t.Helper()
	w := h.do(http.MethodGet, "/api/v1/clients", nil, withCookie(cookie))
	var body clientsBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("clients body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

// hits counts fake requests by "METHOD path".
func (h *harness) hits(method, path string) int {
	n := 0
	for _, r := range h.fake.Requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func findClient(cs []clientView, name string) *clientView {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func TestClients_LiveEveryCall(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	for i := 0; i < 3; i++ {
		if w, _ := h.getClients(cookie); w.Code != http.StatusOK {
			t.Fatalf("call %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if n := h.hits(http.MethodGet, "/control/clients"); n != 3 {
		t.Errorf("GET /control/clients hit %d times, want 3", n)
	}
	if n := h.hits(http.MethodGet, "/control/blocked_services/get"); n != 3 {
		t.Errorf("GET /control/blocked_services/get hit %d times, want 3", n)
	}
	h.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["ids"] = json.RawMessage(`["10.0.0.9"]`)
	})
	_, body := h.getClients(cookie)
	if kid := findClient(body.Clients, "Kid phone"); kid == nil || !reflect.DeepEqual(kid.IDs, []string{"10.0.0.9"}) {
		t.Errorf("after MutateClient Kid phone = %+v, want ids [10.0.0.9]", kid)
	}
}

func TestClients_ChildAssignment(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	w, body := h.getClients(cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	kid := findClient(body.Clients, "Kid phone")
	if kid == nil || kid.Child == nil || kid.Child.ID != ada.ID || kid.Child.Name != "Ada" {
		t.Errorf("Kid phone = %+v, want child {%d Ada}", kid, ada.ID)
	}
	if !reflect.DeepEqual(kid.IDs, []string{"aa:bb:cc:dd:ee:01", "192.168.1.50"}) {
		t.Errorf("Kid phone ids = %v", kid.IDs)
	}
	tab := findClient(body.Clients, "Kid tablet")
	if tab == nil || tab.Child != nil {
		t.Errorf("Kid tablet = %+v, want child null", tab)
	}
	if !strings.Contains(w.Body.String(), `"child":null`) {
		t.Errorf("raw body lacks \"child\":null: %s", w.Body.String())
	}
	for _, c := range body.Clients {
		if c.UseGlobalBlockedServices != (c.Name == "Kid tablet") {
			t.Errorf("%s use_global_blocked_services = %v", c.Name, c.UseGlobalBlockedServices)
		}
	}
	if findClient(body.Clients, "printer") != nil {
		t.Error("auto_client printer appeared in /clients")
	}
	if len(body.Clients) != 3 {
		t.Errorf("%d clients, want 3", len(body.Clients))
	}
}

func TestServices_Live(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	var body struct {
		Services []serviceView `json:"services"`
	}
	for i := 0; i < 2; i++ {
		w := h.do(http.MethodGet, "/api/v1/services", nil, withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.hits(http.MethodGet, "/control/blocked_services/all"); n != 2 {
		t.Errorf("GET /control/blocked_services/all hit %d times, want 2", n)
	}
	want := []serviceView{{"youtube", "YouTube", "PHN2Zy8+"}, {"tiktok", "TikTok", "PHN2Zy8+"}, {"roblox", "Roblox", "PHN2Zy8+"}}
	if !reflect.DeepEqual(body.Services, want) {
		t.Errorf("services = %+v, want %+v", body.Services, want)
	}

	h.fake.SetResponse("/control/blocked_services/all", 200,
		[]byte(`{"blocked_services":[{"id":"only","name":"Only","icon_svg":"x","rules":[],"group_id":"g"}],"groups":[]}`))
	w := h.do(http.MethodGet, "/api/v1/services", nil, withCookie(cookie))
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Services) != 1 || body.Services[0].ID != "only" {
		t.Errorf("after catalogue change services = %+v, want [only]", body.Services)
	}
}

func findDevice(ds []unlabelledView, ip string) *unlabelledView {
	for i := range ds {
		if ds[i].IP == ip {
			return &ds[i]
		}
	}
	return nil
}

func TestClients_Unlabelled(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()

	w, body := h.getClients(cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	p := findDevice(body.Unlabelled, "192.168.1.9")
	if p == nil || p.Hostname != "printer" || p.Source != "ARP" {
		t.Errorf("printer = %+v, want {192.168.1.9 printer ARP}", p)
	}
	if p != nil && !reflect.DeepEqual(p.SuggestedIDs, []string{"192.168.1.9", "aa:bb:cc:dd:ee:09"}) {
		t.Errorf("printer suggested_ids = %v, want [ip, leased mac]", p.SuggestedIDs)
	}
	if findDevice(body.Unlabelled, "192.168.1.50") != nil {
		t.Error("192.168.1.50 (Kid phone's id) listed as unlabelled")
	}

	h.fake.SetAutoClients([]adguardtest.AutoClient{
		{Name: "printer", IP: "192.168.1.9", Source: "ARP"},
		{Name: "speaker", IP: "192.168.1.60", Source: "rDNS"},
	})
	_, body = h.getClients(cookie)
	if s := findDevice(body.Unlabelled, "192.168.1.60"); s == nil || s.Hostname != "speaker" {
		t.Errorf("second GET unlabelled = %+v, want the new speaker row", body.Unlabelled)
	}
	if n := h.hits(http.MethodGet, "/control/clients"); n != 2 {
		t.Errorf("GET /control/clients hit %d times over two GETs, want 2", n)
	}

	h.fake.SetAutoClients(nil)
	w, _ = h.getClients(cookie)
	if !strings.Contains(w.Body.String(), `"unlabelled":[]`) {
		t.Errorf("no auto clients: body %s lacks \"unlabelled\":[]", w.Body.String())
	}
}

func TestClients_LeaseDegrade(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetStatus("/control/dhcp/status", 500)

	w, body := h.getClients(cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("lease read down: %d %s, want 200", w.Code, w.Body.String())
	}
	p := findDevice(body.Unlabelled, "192.168.1.9")
	if p == nil || !reflect.DeepEqual(p.SuggestedIDs, []string{"192.168.1.9"}) {
		t.Errorf("printer = %+v, want suggested_ids [192.168.1.9]", p)
	}
	if !strings.Contains(h.logs.String(), "dhcp leases unavailable") {
		t.Error("lease failure not logged")
	}
}

func TestClients_Unavailable(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetStatus("/control/clients", 500)
	w, _ := h.getClients(cookie)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("%d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
}

func (h *harness) postClient(cookie string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/clients", body, append([]reqOpt{withCookie(cookie)}, opts...)...)
}

func (h *harness) adds() int { return h.fake.CountRequests("POST", "/control/clients/add") }

var tvBody = map[string]any{"name": "TV", "ids": []string{"192.168.1.30"}}

func withChild(body map[string]any, id any) map[string]any {
	out := map[string]any{"child_id": id}
	for k, v := range body {
		out[k] = v
	}
	return out
}

func TestCreateClient_BadInput(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	before := len(h.fake.Requests())
	for _, body := range []any{
		map[string]any{"name": "  ", "ids": []string{"192.168.1.30"}},
		map[string]any{"name": "TV", "ids": []string{}},
		map[string]any{"name": "TV", "ids": []string{" ", ""}},
		map[string]any{"name": "TV"},
		map[string]any{"name": strings.Repeat("x", 257), "ids": []string{"192.168.1.30"}},
		map[string]any{"name": "TV", "ids": []string{"192.168.1.30"}, "extra": 1},
		`{"name":"TV","ids":["192.168.1.30"]}{}`,
	} {
		w := h.postClient(cookie, body)
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Error != CodeBadRequest {
			t.Errorf("%v: %d %s, want 400 bad_request", body, w.Code, w.Body.String())
		}
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("bad input made %d AdGuard requests, want 0", n)
	}
}

func TestCreateClient_Conflict(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()

	w := h.postClient(cookie, map[string]any{"name": "Kid phone", "ids": []string{"192.168.1.30"}})
	if w.Code != http.StatusConflict || decodeErr(t, w).Error != CodeConflict {
		t.Errorf("duplicate name: %d %s, want 409", w.Code, w.Body.String())
	}
	w = h.postClient(cookie, map[string]any{"name": "TV", "ids": []string{"192.168.1.50"}})
	if e := decodeErr(t, w); w.Code != http.StatusConflict || !strings.Contains(e.Message, "Kid phone") {
		t.Errorf("taken id: %d %s, want 409 naming Kid phone", w.Code, w.Body.String())
	}
	if n := h.adds(); n != 0 {
		t.Errorf("conflicts made %d adds, want 0", n)
	}
}

func TestCreateClient_Timeout(t *testing.T) {
	old := harnessApplyTimeout
	harnessApplyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { harnessApplyTimeout = old })

	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.Hang("/control/clients/add", 2*time.Second)

	start := time.Now()
	w := h.postClient(cookie, tvBody)
	if took := time.Since(start); took > time.Second {
		t.Errorf("POST took %v, want < 1 s", took)
	}
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("%d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
}

func TestCreateClient_ChildChecks(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.createOK(cookie, "Bob", "Ghost")

	w := h.postClient(cookie, withChild(tvBody, 999))
	if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Errorf("child 999: %d %s, want 404", w.Code, w.Body.String())
	}
	ghost := map[string]any{"name": "Ghost", "ids": []string{"192.168.1.30"}}
	for _, body := range []map[string]any{withChild(ghost, ada.ID), ghost} {
		w = h.postClient(cookie, body)
		if e := decodeErr(t, w); w.Code != http.StatusConflict || !strings.Contains(e.Message, "Bob") {
			t.Errorf("Ghost (Bob's stale mapping) %v: %d %s, want 409 naming Bob", body, w.Code, w.Body.String())
		}
	}
	if n := h.adds(); n != 0 {
		t.Errorf("child checks made %d adds, want 0", n)
	}
}

func TestCreateClient_WithChild(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	w := h.postClient(cookie, withChild(tvBody, ada.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s, want 201", w.Code, w.Body.String())
	}
	var created clientView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "TV" || created.Child == nil || created.Child.ID != ada.ID || created.UseGlobalBlockedServices {
		t.Errorf("201 body = %+v, want TV for Ada with use_global false", created)
	}

	gw := h.do(http.MethodGet, "/api/v1/children/"+itoa(ada.ID), nil, withCookie(cookie))
	if c := h.decodeChild(gw); !slices.Contains(c.Clients, "TV") {
		t.Errorf("Ada clients = %v, want TV", c.Clients)
	}
	_, body := h.getClients(cookie)
	tv := findClient(body.Clients, "TV")
	if tv == nil || tv.Child == nil || tv.Child.ID != ada.ID {
		t.Errorf("GET /clients TV = %+v, want child Ada", tv)
	}
	if tv != nil && tv.UseGlobalBlockedServices {
		t.Error("fake's TV use_global_blocked_services = true, want false")
	}
	if findDevice(body.Unlabelled, "192.168.1.30") != nil {
		t.Error("TV's ip still listed as unlabelled")
	}
	if got := h.blocked("TV"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("TV blocks %v, want Ada's baseline [tiktok youtube]", got)
	}
}

func TestCreateClient_NoChild(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone")

	bodies := []any{tvBody, withChild(map[string]any{"name": "Den", "ids": []string{"192.168.1.31"}}, nil)}
	for _, b := range bodies {
		if w := h.postClient(cookie, b); w.Code != http.StatusCreated {
			t.Fatalf("%v: %d %s, want 201", b, w.Code, w.Body.String())
		}
	}
	_, list := h.listChildren(cookie)
	for _, c := range list.Children {
		if slices.Contains(c.Clients, "TV") || slices.Contains(c.Clients, "Den") {
			t.Errorf("child %s maps an unassigned client: %v", c.Name, c.Clients)
		}
	}
	_, body := h.getClients(cookie)
	for _, name := range []string{"TV", "Den"} {
		if c := findClient(body.Clients, name); c == nil || c.Child != nil || !c.UseGlobalBlockedServices {
			t.Errorf("%s = %+v, want unassigned on the global list", name, c)
		}
	}
}

func TestCreateClient_AdGuardRejects(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetResponse("/control/clients/add", 400, []byte("invalid client id: 192.168.1.30"))

	w := h.postClient(cookie, tvBody)
	e := decodeErr(t, w)
	if w.Code != http.StatusBadRequest || e.Error != CodeBadRequest || !strings.Contains(e.Message, "invalid client id") {
		t.Errorf("%d %s, want 400 bad_request carrying AdGuard's message", w.Code, w.Body.String())
	}
}

func TestCreateClient_CSRF(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	if w := h.postClient(cookie, tvBody, noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF header: %d, want 403", w.Code)
	}
	if n := h.adds(); n != 0 {
		t.Errorf("adds = %d, want 0", n)
	}
}

func TestCreateClient_ReadUnavailable(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.fake.SetStatus("/control/clients", 500)

	for _, b := range []any{tvBody, withChild(tvBody, ada.ID)} {
		w := h.postClient(cookie, b)
		if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
			t.Errorf("%v: %d %s, want 502 adguard_unavailable", b, w.Code, w.Body.String())
		}
	}
	if n := h.adds(); n != 0 {
		t.Errorf("adds = %d, want 0", n)
	}
}

func TestCreateClient_SelfStale(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "TV")

	w := h.postClient(cookie, withChild(tvBody, ada.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s, want 201", w.Code, w.Body.String())
	}
	if _, ok := h.fake.BlockedServices("TV"); !ok {
		t.Error("fake has no TV")
	}
	gw := h.do(http.MethodGet, "/api/v1/children/"+itoa(ada.ID), nil, withCookie(cookie))
	if c := h.decodeChild(gw); !reflect.DeepEqual(c.Clients, []string{"Kid phone", "TV"}) {
		t.Errorf("Ada clients = %v, want [Kid phone TV] with one TV", c.Clients)
	}
}
