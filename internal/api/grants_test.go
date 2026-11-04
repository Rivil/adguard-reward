package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/grants"
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
	ada := h.seedChild("Ada", "Kid phone")

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
	for _, want := range []string{`"services":["tiktok"]`, `"clients":["Kid phone"]`, `"shared":[]`, `"started_at":"`, `"ends_at":"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("GET body lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, ":null") {
		t.Errorf("GET body serialises a null: %s", raw)
	}
	var rows struct {
		Grants []map[string]json.RawMessage `json:"grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows.Grants) != 1 {
		t.Fatalf("GET body %s: %v", raw, err)
	}
	keys := slices.Sorted(maps.Keys(rows.Grants[0]))
	if want := []string{"child_id", "clients", "ends_at", "id", "services", "shared", "started_at"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("grant keys = %v, want exactly %v", keys, want)
	}
}

// grantShared is the 201 with its shared list.
type grantShared struct {
	ID     int64       `json:"id"`
	Shared []sharedRef `json:"shared"`
}

func decodeShared(t *testing.T, w *httptest.ResponseRecorder) grantShared {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/grants: %d %s, want 201", w.Code, w.Body.String())
	}
	var out grantShared
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("201 body %q: %v", w.Body.String(), err)
	}
	return out
}

func TestGrants_CreateShared(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone", "Kid tablet")
	ben := h.createOK(cookie, "Ben", "Kid phone")

	w := h.postGrant(cookie, grantBody(ada.ID, []string{"youtube"}, 1800))
	got := decodeShared(t, w)
	want := []sharedRef{{Client: "Kid phone", Others: []childRef{{ben.ID, "Ben"}}}}
	if !reflect.DeepEqual(got.Shared, want) {
		t.Errorf("201 shared = %+v, want exactly %+v", got.Shared, want)
	}
	if !strings.Contains(w.Body.String(), `"shared":[{"client":"Kid phone","others":[{"id":`+itoa(ben.ID)+`,"name":"Ben"}]}]`) {
		t.Errorf("raw 201 = %s", w.Body.String())
	}
}

func TestGrants_SharedApplied(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone")
	if got := h.blocked("Kid phone"); !slices.Contains(got, "youtube") {
		t.Fatalf("precondition: Kid phone = %v, want youtube blocked", got)
	}

	h.grantOK(cookie, ben.ID, []string{"youtube"}, 1800)
	if got := h.blocked("Kid phone"); slices.Contains(got, "youtube") {
		t.Errorf("Kid phone = %v after Ben's youtube grant, want youtube unblocked", got)
	}
}

func TestGrants_SharedThreeOwners(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone")
	cleo := h.createOK(cookie, "Cleo", "Kid phone")

	got := decodeShared(t, h.postGrant(cookie, grantBody(ben.ID, []string{"youtube"}, 1800)))
	want := []sharedRef{{Client: "Kid phone", Others: []childRef{{ada.ID, "Ada"}, {cleo.ID, "Cleo"}}}}
	if !reflect.DeepEqual(got.Shared, want) {
		t.Errorf("Ben's 201 shared = %+v, want %+v", got.Shared, want)
	}
}

func TestGrantCreate_SecondOwnerSameService(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone")

	first := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	w := h.postGrant(cookie, grantBody(ben.ID, []string{"youtube"}, 1800))
	if w.Code != http.StatusCreated {
		t.Fatalf("Ben's youtube grant while Ada's covers Kid phone: %d %s, want 201", w.Code, w.Body.String())
	}
	if got := decodeShared(t, w); got.ID == first.ID {
		t.Errorf("Ben's grant reused id %d", first.ID)
	}
}

func TestGrants_UnsharedEmpty(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	w := h.postGrant(cookie, grantBody(ada.ID, []string{"youtube"}, 1800))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"shared":[]`) {
		t.Errorf("201 = %d %s, want \"shared\":[]", w.Code, w.Body.String())
	}
	lw, _ := h.listGrants(cookie)
	if !strings.Contains(lw.Body.String(), `"shared":[]`) {
		t.Errorf("GET = %s, want the row's \"shared\":[]", lw.Body.String())
	}
}

func TestGrants_ListSharedLive(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	ben := h.createOK(cookie, "Ben", "Kid phone")

	_, body := h.listGrants(cookie)
	want := []sharedRef{{Client: "Kid phone", Others: []childRef{{ben.ID, "Ben"}}}}
	if len(body.Grants) != 1 || !reflect.DeepEqual(body.Grants[0].Shared, want) {
		t.Errorf("GET /grants = %+v, want Ada's grant shared %+v", body.Grants, want)
	}
}

// grantsDirect wires the grant handlers over a fresh fake, store and engine
// with every children read failing. Handlers are called directly, so the
// session and CSRF middleware are not under test here.
type grantsDirect struct {
	a     *API
	fake  *adguardtest.Server
	store *store.Store
	eng   *grants.Engine
}

func newGrantsDirect(t *testing.T) grantsDirect {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := adguardtest.New(t, adguardtest.Options{User: fakeUser, Pass: fakePass})
	client, err := adguard.New(fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := grants.New(st, client, grants.Options{Now: (&clock{t: t0}).Now, ApplyTimeout: harnessApplyTimeout, Log: log})
	t.Cleanup(eng.Close)
	a := New(Deps{AdGuard: client, Log: log, Children: failingChildren{}, Grants: eng})
	return grantsDirect{a: a, fake: fake, store: st, eng: eng}
}

func TestGrants_ChildrenReadFails(t *testing.T) {
	d := newGrantsDirect(t)
	ctx := context.Background()
	ada, err := d.store.CreateChild(ctx, "Ada", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(grantBody(ada.ID, []string{"youtube"}, 1800))
	w := httptest.NewRecorder()
	d.a.handleGrantCreate(w, httptest.NewRequest(http.MethodPost, "/api/v1/grants", bytes.NewReader(body)))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("%d %s, want 500", w.Code, w.Body.String())
	}
	if n := d.fake.CountRequests("POST", "/control/clients/update"); n != 0 {
		t.Errorf("fake saw %d update POSTs, want 0", n)
	}
	if rows, err := d.eng.List(ctx); err != nil || len(rows) != 0 {
		t.Errorf("active grants = %v, %v; want none", rows, err)
	}
}

func TestGrants_ListChildrenReadFails(t *testing.T) {
	d := newGrantsDirect(t)
	ctx := context.Background()
	ada, err := d.store.CreateChild(ctx, "Ada", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	ben, err := d.store.CreateChild(ctx, "Ben", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []store.Child{ada, ben} {
		if _, err := d.eng.Create(ctx, c.ID, []string{"youtube"}, c.Clients, 30*time.Minute); err != nil {
			t.Fatalf("grant for %s: %v", c.Name, err)
		}
	}
	want, err := d.eng.List(ctx)
	if err != nil || len(want) != 2 {
		t.Fatalf("engine List = %v, %v; want 2 grants", want, err)
	}

	w := httptest.NewRecorder()
	d.a.handleGrantsList(w, httptest.NewRequest(http.MethodGet, "/api/v1/grants", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", w.Code, w.Body.String())
	}
	var body grantsBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Grants) != 2 {
		t.Fatalf("GET lists %d grants, want 2: %s", len(body.Grants), w.Body.String())
	}
	for i, g := range body.Grants {
		if g.ID != want[i].ID || g.EndsAt != rfc3339(want[i].EndsAt) {
			t.Errorf("row %d = id %d ends_at %s, want id %d ends_at %s", i, g.ID, g.EndsAt, want[i].ID, rfc3339(want[i].EndsAt))
		}
		if g.Shared == nil || len(g.Shared) != 0 {
			t.Errorf("row %d shared = %v, want []", i, g.Shared)
		}
	}
	if strings.Contains(w.Body.String(), ":null") {
		t.Errorf("degraded body serialises a null: %s", w.Body.String())
	}
}

// TestGrants_Custom grants a custom service through the real engine and
// fake: the catalogue check accepts its "custom:<n>" id, the region gains one
// @@ exception per domain for the child's clients, and no custom id leaks
// into a client's blocked_services.
func TestGrants_Custom(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	cs := createCustom(t, h, cookie, "Portal", "a.com", "b.com")
	if cs.ServiceID != "custom:1" {
		t.Fatalf("precondition: service id %q, want custom:1", cs.ServiceID)
	}
	before := h.sortedBlocked("Kid phone")

	created := h.grantOK(cookie, ada.ID, []string{"custom:1"}, 1800)
	if !created.Applied || len(created.Failed) != 0 {
		t.Fatalf("201 = %+v, want applied true, failed []", created)
	}
	want := []string{
		"||a.com^$client='Kid phone'",
		"||b.com^$client='Kid phone'",
		"@@||a.com^$client='Kid phone'",
		"@@||b.com^$client='Kid phone'",
	}
	if got := managedRegion(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
	if got := h.sortedBlocked("Kid phone"); !reflect.DeepEqual(got, before) {
		t.Errorf("Kid phone blocked_services = %v, want unchanged %v", got, before)
	}
	_, list := h.listGrants(cookie)
	if len(list.Grants) != 1 || !reflect.DeepEqual(list.Grants[0].Services, []string{"custom:1"}) {
		t.Errorf("GET grants = %+v, want one grant on [custom:1]", list.Grants)
	}
}

// TestGrants_UnknownCustom refuses a custom id with no stored service: the
// prefix alone is not a pass. Nothing is stored and the region is untouched.
func TestGrants_UnknownCustom(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	createCustom(t, h, cookie, "Portal", "a.com")
	sets := h.fake.CountRequests("POST", setRulesPath)

	for _, services := range [][]string{{"custom:99"}, {"custom:1", "custom:99"}, {"tiktok", "custom:99"}} {
		w := h.postGrant(cookie, grantBody(ada.ID, services, 1800))
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%v: %d %s, want 422", services, w.Code, w.Body.String())
		}
		if e := decodeErr(t, w); e.Error != CodeUnprocessable || !strings.Contains(e.Message, `"custom:99"`) {
			t.Errorf("%v: envelope = %+v, want unprocessable naming custom:99", services, e)
		}
	}
	for _, id := range []string{"custom:", "custom:abc", "custom:01"} {
		if w := h.postGrant(cookie, grantBody(ada.ID, []string{id}, 1800)); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: %d %s, want 422", id, w.Code, w.Body.String())
		}
	}
	if n := h.grantRows(); n != 0 {
		t.Errorf("%d grant rows after rejected creates", n)
	}
	if n := h.fake.CountRequests("POST", setRulesPath) - sets; n != 0 {
		t.Errorf("%d set_rules POSTs after rejected creates", n)
	}
}

// snapshotNames is the grant's stored name snapshot, service id to name.
func (h *harness) snapshotNames(grantID int64) map[string]string {
	h.t.Helper()
	rows, err := h.store.DB().Query(`SELECT service_id, name FROM grant_service_names WHERE grant_id = ?`, grantID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			h.t.Fatal(err)
		}
		out[id] = name
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) nameRows() int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM grant_service_names`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// TestGrantCreate_SnapshotsNames grants a built-in and a custom service:
// both names are snapshotted from the catalogue the handler already read,
// so the create costs exactly one catalogue request.
func TestGrantCreate_SnapshotsNames(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	cs := createCustom(t, h, cookie, "Portal", "a.com")
	hits := h.fake.CountRequests("GET", "/control/blocked_services/all")

	created := h.grantOK(cookie, ada.ID, []string{"tiktok", cs.ServiceID}, 1800)
	want := map[string]string{"tiktok": "TikTok", cs.ServiceID: "Portal"}
	if got := h.snapshotNames(created.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	if n := h.fake.CountRequests("GET", "/control/blocked_services/all") - hits; n != 1 {
		t.Errorf("create read the catalogue %d times, want 1 (names come from the same read)", n)
	}
}

// TestGrantCreate_SnapshotTrimmedID sends a padded id: the snapshot is keyed
// by the trimmed id the grant stores.
func TestGrantCreate_SnapshotTrimmedID(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")

	created := h.grantOK(cookie, ada.ID, []string{"  tiktok\t"}, 1800)
	if got, want := h.snapshotNames(created.ID), map[string]string{"tiktok": "TikTok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

// TestGrantCreate_RejectNoNames: a 409 overlap and a 422 unknown service
// write no name rows.
func TestGrantCreate_RejectNoNames(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	if n := h.nameRows(); n != 1 {
		t.Fatalf("precondition: %d name rows, want 1", n)
	}

	for _, tc := range []struct {
		name     string
		services []string
		want     int
	}{
		{"overlap", []string{"roblox", "tiktok"}, http.StatusConflict},
		{"unknown service", []string{"roblox", "nope"}, http.StatusUnprocessableEntity},
	} {
		if w := h.postGrant(cookie, grantBody(ada.ID, tc.services, 1800)); w.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body.String())
		}
		if n := h.nameRows(); n != 1 {
			t.Errorf("%s: %d name rows after a rejected create, want 1", tc.name, n)
		}
	}
}
