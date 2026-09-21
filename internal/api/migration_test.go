package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func (h *harness) getOffer(cookie string) (*httptest.ResponseRecorder, migrationOffer) {
	h.t.Helper()
	w := h.do(http.MethodGet, "/api/v1/migration", nil, withCookie(cookie))
	var body migrationOffer
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("offer body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

func (h *harness) apply(cookie string, opts ...reqOpt) (*httptest.ResponseRecorder, []string) {
	h.t.Helper()
	w := h.do(http.MethodPost, "/api/v1/migration", nil, append([]reqOpt{withCookie(cookie)}, opts...)...)
	var body struct {
		Migrated []string `json:"migrated"`
	}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("apply body %q: %v", w.Body.String(), err)
		}
	}
	return w, body.Migrated
}

func setGlobal(h *harness, name string, on bool) {
	h.fake.MutateClient(name, func(m map[string]json.RawMessage) {
		if on {
			m["use_global_blocked_services"] = json.RawMessage("true")
		} else {
			m["use_global_blocked_services"] = json.RawMessage("false")
		}
	})
}

// usesGlobal re-reads the fake through /api/v1/clients.
func usesGlobal(h *harness, cookie, name string) bool {
	h.t.Helper()
	_, body := h.getClients(cookie)
	c := findClient(body.Clients, name)
	if c == nil {
		h.t.Fatalf("client %s missing", name)
	}
	return c.UseGlobalBlockedServices
}

func TestMigration_OfferScope(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	setGlobal(h, "Old laptop", true) // unmapped: must not appear

	w, offer := h.getOffer(cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	want := migrationOffer{
		Global:  []string{"tiktok", "roblox"},
		Clients: []migrationClient{{Name: "Kid tablet", Child: childRef{ID: ada.ID, Name: "Ada"}, Gains: []string{"roblox", "tiktok"}}},
	}
	if !reflect.DeepEqual(offer, want) {
		t.Errorf("offer = %+v, want %+v", offer, want)
	}

	// A tablet that already blocks roblox on its own only gains tiktok.
	h.fake.MutateClient("Kid tablet", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["roblox"]`)
	})
	_, offer = h.getOffer(cookie)
	if len(offer.Clients) != 1 || !reflect.DeepEqual(offer.Clients[0].Gains, []string{"tiktok"}) {
		t.Errorf("own list [roblox]: offer = %+v, want gains [tiktok]", offer.Clients)
	}

	// An empty global list means nothing to migrate from, whatever the flag.
	h.fake.SetResponse("/control/blocked_services/get", 200, []byte(`{"schedule":{"time_zone":"Local"},"ids":[]}`))
	w, offer = h.getOffer(cookie)
	if w.Code != http.StatusOK || len(offer.Clients) != 0 || offer.Clients == nil {
		t.Errorf("empty global: %d %s, want 200 with clients []", w.Code, w.Body.String())
	}
	h.fake.SetResponse("/control/blocked_services/get", 0, nil)

	// No children → nothing mapped → empty.
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	w, offer = h.getOffer(cookie)
	if len(offer.Clients) != 0 || !strings.Contains(w.Body.String(), `"clients":[]`) {
		t.Errorf("no children: %s, want clients []", w.Body.String())
	}
}

func TestMigration_OfferIsReadOnly(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	w, offer := h.getOffer(cookie)
	if w.Code != http.StatusOK || len(offer.Clients) != 1 || offer.Clients[0].Name != "Kid tablet" || offer.Clients[0].Child.ID != ada.ID {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if h.fake.LastUpdate() != nil {
		t.Error("GET /migration wrote a client")
	}
	for _, r := range h.fake.Requests() {
		if r.Method == http.MethodPost && r.Path != "/control/login" {
			t.Errorf("GET /migration issued %s %s", r.Method, r.Path)
		}
	}
}

func TestMigration_Apply(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	setGlobal(h, "Old laptop", true)

	w, migrated := h.apply(cookie)
	if w.Code != http.StatusOK || !reflect.DeepEqual(migrated, []string{"Kid tablet"}) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var updates [][]byte
	for _, r := range h.fake.Requests() {
		if r.Path == "/control/clients/update" {
			updates = append(updates, r.Body)
		}
		if strings.HasPrefix(r.Path, "/control/blocked_services/set") {
			t.Errorf("apply wrote the global list: %s %s", r.Method, r.Path)
		}
	}
	if len(updates) != 1 {
		t.Fatalf("%d client updates, want 1", len(updates))
	}
	var u struct {
		Name string `json:"name"`
		Data struct {
			BlockedServices []string `json:"blocked_services"`
			UseGlobal       bool     `json:"use_global_blocked_services"`
		} `json:"data"`
	}
	if err := json.Unmarshal(updates[0], &u); err != nil {
		t.Fatal(err)
	}
	if u.Name != "Kid tablet" || !reflect.DeepEqual(u.Data.BlockedServices, []string{"roblox", "tiktok"}) || u.Data.UseGlobal {
		t.Errorf("update = %+v, want Kid tablet [roblox tiktok] use_global false", u)
	}

	_, body := h.getClients(cookie)
	if findClient(body.Clients, "Kid tablet").UseGlobalBlockedServices {
		t.Error("Kid tablet still uses the global list after apply")
	}
	if !findClient(body.Clients, "Old laptop").UseGlobalBlockedServices {
		t.Error("unmapped Old laptop was migrated")
	}
	_, offer := h.getOffer(cookie)
	if !reflect.DeepEqual(offer.Global, []string{"tiktok", "roblox"}) {
		t.Errorf("global list changed to %v", offer.Global)
	}
	if len(offer.Clients) != 0 {
		t.Errorf("second offer = %+v, want empty", offer.Clients)
	}
}

func TestMigration_ApplyEmpty(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone")
	w, migrated := h.apply(cookie)
	if w.Code != http.StatusOK || migrated == nil || len(migrated) != 0 {
		t.Fatalf("%d %s, want 200 {migrated:[]}", w.Code, w.Body.String())
	}
	if h.hits(http.MethodPost, "/control/clients/update") != 0 {
		t.Error("empty apply wrote a client")
	}

	h.createOK(cookie, "Ben", "Kid tablet")
	for i := 0; i < 2; i++ {
		if w, _ := h.apply(cookie); w.Code != http.StatusOK {
			t.Fatalf("apply %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if n := h.hits(http.MethodPost, "/control/clients/update"); n != 1 {
		t.Errorf("two applies of one offer wrote %d updates, want 1", n)
	}
}

func TestMigration_PartialFailure(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	setGlobal(h, "Kid phone", true)
	h.fake.SetUpdateStatus("Kid tablet", 500)

	w, _ := h.apply(cookie)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("%d %s, want 502", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Error != CodeAdGuardUnavailable || !strings.Contains(e.Message, "Kid tablet") {
		t.Errorf("envelope = %+v, want adguard_unavailable naming Kid tablet", e)
	}
	if usesGlobal(h, cookie, "Kid phone") {
		t.Error("Kid phone (first step) was not migrated")
	}
	if !usesGlobal(h, cookie, "Kid tablet") {
		t.Error("Kid tablet (failed step) shows as migrated")
	}
	_, offer := h.getOffer(cookie)
	if len(offer.Clients) != 1 || offer.Clients[0].Name != "Kid tablet" {
		t.Errorf("offer after partial failure = %+v, want only Kid tablet", offer.Clients)
	}
}

func TestMigration_AdGuardDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid tablet")

	h.fake.SetStatus("/control/clients/update", 500)
	w, _ := h.apply(cookie)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("update 500: POST = %d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
	h.fake.SetResponse("/control/clients/update", 0, nil)

	h.fake.SetStatus("/control/clients", 500)
	w, _ = h.getOffer(cookie)
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("clients 500: GET = %d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
	w, _ = h.apply(cookie)
	if w.Code != http.StatusBadGateway {
		t.Errorf("clients 500: POST = %d, want 502", w.Code)
	}
}

func TestMigration_CSRF(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid tablet")

	if w, _ := h.apply(cookie, noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF header: %d, want 403", w.Code)
	}
	if h.fake.LastUpdate() != nil {
		t.Error("a 403 POST wrote a client")
	}
	if w := h.do(http.MethodGet, "/api/v1/migration", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("GET without cookie: %d, want 401", w.Code)
	}
	if w := h.do(http.MethodPost, "/api/v1/migration", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("POST without cookie: %d, want 401", w.Code)
	}
}
