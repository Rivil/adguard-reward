package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type clientsBody struct {
	Clients []clientView `json:"clients"`
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
