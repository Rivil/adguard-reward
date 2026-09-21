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

type grantCreated struct {
	ID      int64    `json:"id"`
	EndsAt  string   `json:"ends_at"`
	Applied bool     `json:"applied"`
	Failed  []string `json:"failed"`
}

type grantsBody struct {
	Grants []grantView `json:"grants"`
}

func grantBody(childID int64, services []string, duration int) map[string]any {
	return map[string]any{"child_id": childID, "services": services, "duration": duration}
}

func (h *harness) postGrant(cookie string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/grants", body, withCookie(cookie))
}

// grantOK POSTs a grant and returns the decoded 201.
func (h *harness) grantOK(cookie string, childID int64, services []string, duration int) grantCreated {
	h.t.Helper()
	w := h.postGrant(cookie, grantBody(childID, services, duration))
	if w.Code != http.StatusCreated {
		h.t.Fatalf("POST /api/v1/grants: status %d body %s", w.Code, w.Body.String())
	}
	var out grantCreated
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("201 body %q: %v", w.Body.String(), err)
	}
	return out
}

func (h *harness) listGrants(cookie string) (*httptest.ResponseRecorder, grantsBody) {
	h.t.Helper()
	w := h.do(http.MethodGet, "/api/v1/grants", nil, withCookie(cookie))
	var body grantsBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("grants body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

func (h *harness) grantRows() int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM grants`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) updatePosts() int { return h.fake.CountRequests("POST", "/control/clients/update") }

func (h *harness) blocked(name string) []string {
	h.t.Helper()
	ids, ok := h.fake.BlockedServices(name)
	if !ok {
		h.t.Fatalf("fake has no client %q", name)
	}
	return ids
}

// offGlobal takes Kid tablet off the global list with its own services.
func (h *harness) offGlobal(own ...string) {
	list, _ := json.Marshal(own)
	h.fake.MutateClient("Kid tablet", func(m map[string]json.RawMessage) {
		m["use_global_blocked_services"] = json.RawMessage("false")
		m["blocked_services"] = list
	})
}

func grantPath(id int64, action string) string {
	return "/api/v1/grants/" + strconv.FormatInt(id, 10) + "/" + action
}

func TestGrants_Gated(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	body := grantBody(ada.ID, []string{"tiktok"}, 1800)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/grants"},
		{http.MethodPost, "/api/v1/grants"},
		{http.MethodPost, grantPath(1, "extend")},
		{http.MethodPost, grantPath(1, "end")},
	} {
		w := h.do(tc.method, tc.path, body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a cookie: %d, want 401", tc.method, tc.path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/grants", grantPath(1, "extend"), grantPath(1, "end")} {
		w := h.do(http.MethodPost, path, body, withCookie(cookie), noCSRF())
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF: %d, want 403", path, w.Code)
		}
	}
	if n := h.grantRows(); n != 0 {
		t.Errorf("grants table has %d rows after gated requests", n)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("fake saw %d update POSTs after gated requests", n)
	}
}

func TestGrants_CreateAndList(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	if created.ID <= 0 || !created.Applied || created.Failed == nil || len(created.Failed) != 0 {
		t.Fatalf("201 = %+v, want id > 0, applied true, failed []", created)
	}
	ends, err := time.Parse(time.RFC3339, created.EndsAt)
	if err != nil {
		t.Fatalf("ends_at %q is not RFC3339: %v", created.EndsAt, err)
	}
	if !ends.Equal(t0.Add(30 * time.Minute)) {
		t.Errorf("ends_at = %v, want started_at + 30m = %v", ends, t0.Add(30*time.Minute))
	}
	if got := h.blocked("Kid phone"); slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok unblocked", got)
	}

	w, body := h.listGrants(cookie)
	if w.Code != http.StatusOK || len(body.Grants) != 1 {
		t.Fatalf("GET /api/v1/grants: %d %s", w.Code, w.Body.String())
	}
	g := body.Grants[0]
	if g.ID != created.ID || g.ChildID != ada.ID || !reflect.DeepEqual(g.Services, []string{"tiktok"}) ||
		!reflect.DeepEqual(g.Clients, ada.Clients) || g.EndsAt != created.EndsAt || g.StartedAt != rfc3339(t0) {
		t.Errorf("listed grant = %+v, want id %d child %d services [tiktok] clients %v ends_at %q started_at %q",
			g, created.ID, ada.ID, ada.Clients, created.EndsAt, rfc3339(t0))
	}

	if ok, err := h.store.SetGrantStatus(context.Background(), created.ID, store.StatusActive, store.StatusExpired, t0); err != nil || !ok {
		t.Fatalf("SetGrantStatus = %v, %v", ok, err)
	}
	w, _ = h.listGrants(cookie)
	if got := strings.TrimSpace(w.Body.String()); got != `{"grants":[]}` {
		t.Errorf("GET after expiry = %s, want {\"grants\":[]}", got)
	}
}

func TestGrants_Validation(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid tablet")
	cy := h.createOK(cookie, "Cy")

	catalogueHits := func() int { return h.fake.CountRequests("GET", "/control/blocked_services/all") }
	check := func(name string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		posts := h.updatePosts()
		w := h.postGrant(cookie, body)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d (body %s)", name, w.Code, want, w.Body.String())
		}
		if n := h.grantRows(); n != 0 {
			t.Errorf("%s: %d grant rows after a rejected create", name, n)
		}
		if n := h.updatePosts() - posts; n != 0 {
			t.Errorf("%s: %d update POSTs after a rejected create", name, n)
		}
		return w
	}

	check("duration 59", grantBody(ada.ID, []string{"tiktok"}, 59), http.StatusUnprocessableEntity)
	check("duration 86401", grantBody(ada.ID, []string{"tiktok"}, 86401), http.StatusUnprocessableEntity)
	check("duration 0", grantBody(ada.ID, []string{"tiktok"}, 0), http.StatusUnprocessableEntity)
	w := check("unknown service", grantBody(ada.ID, []string{"nope"}, 1800), http.StatusUnprocessableEntity)
	if e := decodeErr(t, w); e.Error != CodeUnprocessable || !strings.Contains(e.Message, "nope") {
		t.Errorf("unknown service envelope = %+v, want unprocessable naming nope", e)
	}
	check("services []", grantBody(ada.ID, []string{}, 1800), http.StatusUnprocessableEntity)
	check("services [\"\"]", grantBody(ada.ID, []string{""}, 1800), http.StatusUnprocessableEntity)
	check("child with no clients", grantBody(cy.ID, []string{"tiktok"}, 1800), http.StatusUnprocessableEntity)

	hits := catalogueHits()
	w = check("unknown child", grantBody(999, []string{"tiktok"}, 1800), http.StatusNotFound)
	if e := decodeErr(t, w); e.Error != CodeNotFound {
		t.Errorf("unknown child envelope = %+v", e)
	}
	if n := catalogueHits() - hits; n != 0 {
		t.Errorf("unknown child hit the catalogue %d times; the 404 must precede any AdGuard call", n)
	}

	check("unknown field", `{"child_id":1,"services":["tiktok"],"duration":1800,"extra":1}`, http.StatusBadRequest)
	check("non-object", `[1,2]`, http.StatusBadRequest)
	check("20 KiB body", `{"child_id":1,"services":["`+strings.Repeat("a", 20<<10)+`"],"duration":1800}`, http.StatusBadRequest)

	if got := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 60); got.ID == 0 {
		t.Error("duration 60 rejected")
	}
	if got := h.grantOK(cookie, ben.ID, []string{"tiktok"}, 86400); got.ID == 0 {
		t.Error("duration 86400 rejected")
	}
}

func TestGrants_Overlap409(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid tablet")

	first := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	w := h.postGrant(cookie, grantBody(ada.ID, []string{"tiktok", "roblox"}, 1800))
	if w.Code != http.StatusConflict {
		t.Fatalf("overlapping POST: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		GrantID int64  `json:"grant_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != CodeConflict || body.GrantID != first.ID || body.Message == "" {
		t.Errorf("409 body = %+v, want error conflict, grant_id %d", body, first.ID)
	}
	if _, list := h.listGrants(cookie); len(list.Grants) != 1 {
		t.Errorf("GET lists %d grants, want 1", len(list.Grants))
	}
	if got := h.grantOK(cookie, ben.ID, []string{"tiktok"}, 1800); got.ID == first.ID {
		t.Error("another child's grant for the same service must not collide")
	}
}

func TestGrants_PartialApplyBody(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	h.offGlobal("youtube")
	h.fake.SetUpdateStatus("Kid tablet", 500)

	created := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	if created.Applied || !reflect.DeepEqual(created.Failed, []string{"Kid tablet"}) {
		t.Errorf("201 = %+v, want applied false failed [Kid tablet]", created)
	}
	if _, list := h.listGrants(cookie); len(list.Grants) != 1 || list.Grants[0].ID != created.ID {
		t.Errorf("GET = %+v, want the partially applied grant (never rolled back)", list.Grants)
	}
	if got := h.blocked("Kid phone"); slices.Contains(got, "youtube") {
		t.Errorf("Kid phone = %v, want youtube unblocked", got)
	}
}

func TestGrants_ApplyDeadline(t *testing.T) {
	old := harnessApplyTimeout
	harnessApplyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { harnessApplyTimeout = old })

	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.fake.Hang("/control/clients/update", 2*time.Second)

	start := time.Now()
	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	if took := time.Since(start); took > time.Second {
		t.Errorf("POST took %v, want < 1 s", took)
	}
	if created.Applied || !reflect.DeepEqual(created.Failed, []string{"Kid phone"}) {
		t.Errorf("201 = %+v, want applied false failed [Kid phone]", created)
	}
}

func TestGrants_CatalogueDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.fake.SetStatus("/control/blocked_services/all", 500)

	w := h.postGrant(cookie, grantBody(ada.ID, []string{"tiktok"}, 1800))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Error != CodeAdGuardUnavailable {
		t.Errorf("envelope = %+v", e)
	}
	if n := h.grantRows(); n != 0 {
		t.Errorf("%d grant rows created behind an unreadable catalogue", n)
	}
}

func TestGrants_ExtendEnd(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	h.clock.Advance(5 * time.Minute) // extend is from ends_at, not from now

	w := h.do(http.MethodPost, grantPath(created.ID, "extend"), map[string]any{"duration": 600}, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("extend: %d %s", w.Code, w.Body.String())
	}
	var ext struct {
		ID     int64  `json:"id"`
		EndsAt string `json:"ends_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ext); err != nil {
		t.Fatal(err)
	}
	prev, _ := time.Parse(time.RFC3339, created.EndsAt)
	if ext.ID != created.ID || ext.EndsAt != rfc3339(prev.Add(10*time.Minute)) {
		t.Errorf("extend body = %+v, want ends_at %s", ext, rfc3339(prev.Add(10*time.Minute)))
	}
	if _, list := h.listGrants(cookie); len(list.Grants) != 1 || list.Grants[0].EndsAt != ext.EndsAt {
		t.Errorf("GET after extend = %+v, want ends_at %s", list.Grants, ext.EndsAt)
	}
	for _, bad := range []int{30, 86401} {
		w := h.do(http.MethodPost, grantPath(created.ID, "extend"), map[string]any{"duration": bad}, withCookie(cookie))
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("extend %d: status %d, want 422", bad, w.Code)
		}
	}
	if _, list := h.listGrants(cookie); list.Grants[0].EndsAt != ext.EndsAt {
		t.Errorf("ends_at changed by a rejected extend: %s", list.Grants[0].EndsAt)
	}

	w = h.do(http.MethodPost, grantPath(created.ID, "end"), nil, withCookie(cookie))
	if w.Code != http.StatusNoContent {
		t.Fatalf("end: %d %s", w.Code, w.Body.String())
	}
	if got := h.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone after end = %v, want tiktok re-blocked before the response", got)
	}
	for _, tc := range []struct {
		path string
		body any
	}{
		{grantPath(created.ID, "extend"), map[string]any{"duration": 600}},
		{grantPath(created.ID, "end"), nil},
		{"/api/v1/grants/abc/end", nil},
		{grantPath(999, "extend"), map[string]any{"duration": 600}},
	} {
		w := h.do(http.MethodPost, tc.path, tc.body, withCookie(cookie))
		if w.Code != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404 (%s)", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestGrants_EndRevertFails(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	h.fake.SetUpdateStatus("Kid phone", 500)

	w := h.do(http.MethodPost, grantPath(created.ID, "end"), nil, withCookie(cookie))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("end with a failing revert: %d %s", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Error != CodeAdGuardUnavailable || !strings.Contains(e.Message, "Kid phone") {
		t.Errorf("envelope = %+v, want adguard_unavailable naming Kid phone", e)
	}
	if _, list := h.listGrants(cookie); len(list.Grants) != 1 {
		t.Errorf("GET lists %d grants after a failed end, want 1 (still active)", len(list.Grants))
	}
}

func TestGrants_ViewShape(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	w, _ := h.listGrants(cookie)
	raw := w.Body.String()
	for _, want := range []string{`"services":["tiktok"]`, `"clients":["Kid phone"]`, `"started_at":"`, `"ends_at":"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("GET body lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, ":null") {
		t.Errorf("GET body serialises a null: %s", raw)
	}
}
