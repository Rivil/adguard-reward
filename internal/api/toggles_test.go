package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

type toggleResp struct {
	ServiceID string           `json:"service_id"`
	State     string           `json:"state"`
	Applied   bool             `json:"applied"`
	Failed    []string         `json:"failed"`
	Shared    []sharedRef      `json:"shared"`
	Grant     *toggleGrantView `json:"grant"`
}

func togglePath(childID int64) string {
	return "/api/v1/children/" + strconv.FormatInt(childID, 10) + "/toggles"
}

func toggleBody(service, state string) map[string]any {
	return map[string]any{"service_id": service, "state": state}
}

func (h *harness) postToggle(cookie string, childID int64, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, togglePath(childID), body, append([]reqOpt{withCookie(cookie)}, opts...)...)
}

// toggleOK POSTs a toggle, requires 200 and returns the raw and decoded body.
func (h *harness) toggleOK(cookie string, childID int64, service, state string) (string, toggleResp) {
	h.t.Helper()
	w := h.postToggle(cookie, childID, toggleBody(service, state))
	if w.Code != http.StatusOK {
		h.t.Fatalf("toggle %s %s for child %d: %d %s, want 200", service, state, childID, w.Code, w.Body.String())
	}
	var out toggleResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("200 body %q: %v", w.Body.String(), err)
	}
	return w.Body.String(), out
}

// toggleRows is the store's toggle table, bypassing the API.
func (h *harness) toggleRows() []store.ServiceToggle {
	h.t.Helper()
	rows, err := h.store.ListServiceToggles(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// addTV creates an unassigned persistent "TV" in the fake through the API
// and takes it off the global list with own as its blocked services.
func (h *harness) addTV(cookie string, own ...string) {
	h.t.Helper()
	if w := h.postClient(cookie, tvBody); w.Code != http.StatusCreated {
		h.t.Fatalf("POST TV: %d %s, want 201", w.Code, w.Body.String())
	}
	ownList(h, "TV", own...)
}

func TestToggle_Auth(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	body := toggleBody("youtube", store.ToggleUnblocked)

	if w := h.do(http.MethodPost, togglePath(ada.ID), body); w.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d %s, want 401", w.Code, w.Body.String())
	}
	if w := h.postToggle(cookie, ada.ID, body, noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF: %d %s, want 403", w.Code, w.Body.String())
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v after gated requests, want none", rows)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("fake saw %d update POSTs after gated requests", n)
	}
}

func TestToggle_BadBody(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	before := len(h.fake.Requests())

	for name, body := range map[string]any{
		"state open":         toggleBody("youtube", "open"),
		"state missing":      map[string]any{"service_id": "youtube"},
		"service_id missing": map[string]any{"state": store.ToggleBlocked},
		"service_id blank":   toggleBody("  ", store.ToggleBlocked),
		"unknown field":      map[string]any{"service_id": "youtube", "state": store.ToggleBlocked, "child_id": ada.ID},
		"two objects":        `{"service_id":"youtube","state":"blocked"}{}`,
		"not an object":      `["youtube"]`,
	} {
		w := h.postToggle(cookie, ada.ID, body)
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Error != CodeBadRequest {
			t.Errorf("%s: %d %s, want 400 bad_request", name, w.Code, w.Body.String())
		}
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v after bad bodies, want none", rows)
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("bad bodies made %d AdGuard requests, want 0", n)
	}
}

func TestToggle_UnknownChild(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	before := len(h.fake.Requests())

	w := h.postToggle(cookie, 99, toggleBody("youtube", store.ToggleUnblocked))
	if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Errorf("child 99: %d %s, want 404 not_found", w.Code, w.Body.String())
	}
	w = h.do(http.MethodPost, "/api/v1/children/abc/toggles", toggleBody("youtube", store.ToggleUnblocked), withCookie(cookie))
	if w.Code != http.StatusNotFound {
		t.Errorf("child abc: %d %s, want 404", w.Code, w.Body.String())
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("unknown child made %d AdGuard requests, want 0", n)
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v, want none", rows)
	}
}

func TestToggle_UnknownService(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")

	for _, id := range []string{"nope", "custom:", "custom:99", "custom:01"} {
		for _, state := range []string{store.ToggleBlocked, store.ToggleUnblocked} {
			w := h.postToggle(cookie, ada.ID, toggleBody(id, state))
			if w.Code != http.StatusUnprocessableEntity || decodeErr(t, w).Error != CodeUnprocessable {
				t.Errorf("%q %s: %d %s, want 422 unprocessable", id, state, w.Code, w.Body.String())
			}
		}
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v after unknown services, want none", rows)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("unknown services made %d update POSTs, want 0", n)
	}
	if got := h.sortedBlocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("Kid phone = %v, want its fixture list untouched", got)
	}
}

func TestToggle_CatalogueDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.fake.SetStatus("/control/blocked_services/all", http.StatusBadGateway)

	w := h.postToggle(cookie, ada.ID, toggleBody("youtube", store.ToggleUnblocked))
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Fatalf("%d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v behind an unreadable catalogue, want none", rows)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("%d update POSTs behind an unreadable catalogue, want 0", n)
	}
}

func TestToggle_CatalogueSlow(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.fake.Hang("/control/blocked_services/all", 10*time.Second)

	start := time.Now()
	w := h.postToggle(cookie, ada.ID, toggleBody("youtube", store.ToggleUnblocked))
	took := time.Since(start)
	if took >= 5*time.Second {
		t.Errorf("POST took %v, want < 5 s", took)
	}
	if took >= 2*time.Second {
		t.Errorf("POST took %v, want the catalogue read cut at its own 1 s deadline", took)
	}
	if w.Code != http.StatusBadGateway || decodeErr(t, w).Error != CodeAdGuardUnavailable {
		t.Errorf("%d %s, want 502 adguard_unavailable", w.Code, w.Body.String())
	}
	if rows := h.toggleRows(); len(rows) != 0 {
		t.Errorf("toggle rows = %+v behind a hung catalogue, want none", rows)
	}
}

func TestToggle_SlowCatalogueThenHang(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.fake.Hang("/control/blocked_services/all", 800*time.Millisecond)
	h.fake.Hang("/control/clients/update", 10*time.Second)

	start := time.Now()
	w := h.postToggle(cookie, ada.ID, toggleBody("youtube", store.ToggleUnblocked))
	if took := time.Since(start); took >= 5*time.Second {
		t.Errorf("POST took %v, want < 5 s (1 s catalogue + 4 s apply)", took)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", w.Code, w.Body.String())
	}
	var got toggleResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Applied || !reflect.DeepEqual(got.Failed, []string{"Kid phone"}) {
		t.Errorf("200 = %+v, want applied false failed [Kid phone]", got)
	}
	want := []store.ServiceToggle{{ChildID: ada.ID, ServiceID: "youtube", State: store.ToggleUnblocked}}
	if rows := stripTimes(h.toggleRows()); !reflect.DeepEqual(rows, want) {
		t.Errorf("toggle rows = %+v, want %+v (intent stored despite the hang)", rows, want)
	}
}

// stripTimes zeroes UpdatedAt so rows compare by identity and state.
func stripTimes(rows []store.ServiceToggle) []store.ServiceToggle {
	out := slices.Clone(rows)
	for i := range out {
		out[i].UpdatedAt = time.Time{}
	}
	return out
}

func TestToggle_PartialIs200(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	// Seeded so Kid tablet stays on AdGuard's global list: its write fails.
	ada := h.seedChild("Ada", "Kid phone", "Kid tablet")

	raw, got := h.toggleOK(cookie, ada.ID, "youtube", store.ToggleUnblocked)
	if got.Applied || !reflect.DeepEqual(got.Failed, []string{"Kid tablet"}) {
		t.Errorf("200 = %s, want applied false failed [Kid tablet]", raw)
	}
	if got.ServiceID != "youtube" || got.State != store.ToggleUnblocked {
		t.Errorf("200 = %s, want service_id youtube state unblocked echoed", raw)
	}
	if slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Errorf("Kid phone = %v, want youtube unblocked on the client that took the write", h.blocked("Kid phone"))
	}
	want := []store.ServiceToggle{{ChildID: ada.ID, ServiceID: "youtube", State: store.ToggleUnblocked}}
	if rows := stripTimes(h.toggleRows()); !reflect.DeepEqual(rows, want) {
		t.Errorf("toggle rows = %+v, want %+v", rows, want)
	}
}

func TestToggle_GrantReport(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")

	only := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	raw, got := h.toggleOK(cookie, ada.ID, "youtube", store.ToggleBlocked)
	if want := (&toggleGrantView{ID: only.ID, Ended: true}); !reflect.DeepEqual(got.Grant, want) {
		t.Errorf("block under grant %d = %s, want grant {id %d, ended true}", only.ID, raw, only.ID)
	}
	if !slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Errorf("Kid phone = %v, want youtube blocked", h.blocked("Kid phone"))
	}
	if _, list := h.listGrants(cookie); len(list.Grants) != 0 {
		t.Errorf("active grants = %+v, want the emptied grant ended", list.Grants)
	}

	two := h.grantOK(cookie, ada.ID, []string{"tiktok", "roblox"}, 1800)
	raw, got = h.toggleOK(cookie, ada.ID, "tiktok", store.ToggleBlocked)
	if want := (&toggleGrantView{ID: two.ID, Ended: false}); !reflect.DeepEqual(got.Grant, want) {
		t.Errorf("block one of two = %s, want grant {id %d, ended false}", raw, two.ID)
	}

	raw, got = h.toggleOK(cookie, ada.ID, "youtube", store.ToggleUnblocked)
	if got.Grant != nil || !strings.Contains(raw, `"grant":null`) {
		t.Errorf("no grant involved = %s, want grant null", raw)
	}
}

func TestToggle_SharedReport(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.addTV(cookie, "youtube")
	ada := h.seedChild("Ada", "Kid phone", "TV")
	ben := h.seedChild("Ben", "TV")
	wantShared := []sharedRef{{Client: "TV", Others: []childRef{{ben.ID, "Ben"}}}}

	raw, got := h.toggleOK(cookie, ada.ID, "youtube", store.ToggleUnblocked)
	if !got.Applied || !reflect.DeepEqual(got.Shared, wantShared) {
		t.Errorf("unblock = %s, want applied true shared %+v", raw, wantShared)
	}

	// The report names the shared device even when its write failed.
	h.fake.SetUpdateStatus("TV", http.StatusInternalServerError)
	raw, got = h.toggleOK(cookie, ada.ID, "youtube", store.ToggleBlocked)
	if got.Applied || !reflect.DeepEqual(got.Failed, []string{"TV"}) || !reflect.DeepEqual(got.Shared, wantShared) {
		t.Errorf("failed block = %s, want applied false failed [TV] shared %+v", raw, wantShared)
	}
	h.fake.SetUpdateStatus("TV", 0)

	cy := h.seedChild("Cy", "Old laptop")
	raw, got = h.toggleOK(cookie, cy.ID, "youtube", store.ToggleBlocked)
	if !got.Applied || got.Shared == nil || len(got.Shared) != 0 || !strings.Contains(raw, `"shared":[]`) {
		t.Errorf("unshared = %s, want applied true shared []", raw)
	}
	if !strings.Contains(raw, `"failed":[]`) {
		t.Errorf("unshared = %s, want failed []", raw)
	}
}

func TestToggle_SharedHeldOpen(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.addTV(cookie, "youtube")
	ada := h.seedChild("Ada", "Kid phone", "TV")
	ben := h.seedChild("Ben", "TV")

	h.toggleOK(cookie, ben.ID, "youtube", store.ToggleUnblocked)
	if slices.Contains(h.blocked("TV"), "youtube") {
		t.Fatalf("precondition: TV = %v, want youtube open by Ben", h.blocked("TV"))
	}

	raw, got := h.toggleOK(cookie, ada.ID, "youtube", store.ToggleBlocked)
	if !reflect.DeepEqual(got.Shared, []sharedRef{}) {
		t.Errorf("Ada's block = %s, want shared [] (TV held open by Ben)", raw)
	}
	if slices.Contains(h.blocked("TV"), "youtube") {
		t.Errorf("TV = %v, want youtube still open for Ben", h.blocked("TV"))
	}
	if !slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Errorf("Kid phone = %v, want youtube blocked", h.blocked("Kid phone"))
	}

	raw, got = h.toggleOK(cookie, ada.ID, "youtube", store.ToggleUnblocked)
	if want := []sharedRef{{Client: "TV", Others: []childRef{{ben.ID, "Ben"}}}}; !reflect.DeepEqual(got.Shared, want) {
		t.Errorf("Ada's unblock = %s, want shared %+v", raw, want)
	}
}
