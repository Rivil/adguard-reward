package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/store"
)

type childrenBody struct {
	Children []childView `json:"children"`
}

func (h *harness) decodeChild(w *httptest.ResponseRecorder) childView {
	h.t.Helper()
	var c childView
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		h.t.Fatalf("child body %q: %v", w.Body.String(), err)
	}
	return c
}

func (h *harness) listChildren(cookie string) (*httptest.ResponseRecorder, childrenBody) {
	h.t.Helper()
	w := h.do(http.MethodGet, "/api/v1/children", nil, withCookie(cookie))
	var body childrenBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("children body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

// createOK POSTs a child and returns the 201 view.
func (h *harness) createOK(cookie, name string, clients ...string) childView {
	h.t.Helper()
	body := map[string]any{"name": name}
	if clients != nil {
		body["clients"] = clients
	}
	w := h.do(http.MethodPost, "/api/v1/children", body, withCookie(cookie))
	if w.Code != http.StatusCreated {
		h.t.Fatalf("POST %q: status %d body %s", name, w.Code, w.Body.String())
	}
	return h.decodeChild(w)
}

// seedChild creates a child with its clients straight in the store,
// bypassing POST's AdGuard adopt, for tests that need the fake's fixture
// lists untouched.
func (h *harness) seedChild(name string, clients ...string) childView {
	h.t.Helper()
	c, err := h.store.CreateChild(context.Background(), name, clients)
	if err != nil {
		h.t.Fatalf("seed child %q: %v", name, err)
	}
	return viewOf(c)
}

// storeNames is the store's view, bypassing the API.
func (h *harness) storeNames() []string {
	h.t.Helper()
	rows, err := h.store.ListChildren(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	out := []string{}
	for _, c := range rows {
		out = append(out, c.Name)
	}
	return out
}

func childPath(id int64) string { return "/api/v1/children/" + itoa(id) }

func TestChildren_Gated(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/children"},
		{http.MethodPost, "/api/v1/children"},
		{http.MethodGet, childPath(ada.ID)},
		{http.MethodPut, childPath(ada.ID)},
		{http.MethodDelete, childPath(ada.ID)},
	} {
		w := h.do(tc.method, tc.path, map[string]any{"name": "X"})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a cookie: %d, want 401", tc.method, tc.path, w.Code)
		}
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/children"},
		{http.MethodPut, childPath(ada.ID)},
		{http.MethodDelete, childPath(ada.ID)},
	} {
		w := h.do(tc.method, tc.path, map[string]any{"name": "Zed", "clients": []string{}}, withCookie(cookie), noCSRF())
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF: %d, want 403", tc.method, tc.path, w.Code)
		}
	}
	rows, _ := h.store.ListChildren(context.Background())
	if len(rows) != 1 || rows[0].Name != "Ada" || !reflect.DeepEqual(rows[0].Clients, []string{"Kid phone"}) {
		t.Fatalf("store changed by a gated request: %+v", rows)
	}
}

func TestChildren_CRUD(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()

	if w := h.do(http.MethodGet, "/api/v1/children", nil, withCookie(cookie)); strings.TrimSpace(w.Body.String()) != `{"children":[]}` {
		t.Fatalf("fresh list body = %q, want {\"children\":[]}", w.Body.String())
	}

	ada := h.createOK(cookie, "Ada", "Kid phone")
	if ada.ID <= 0 || ada.Name != "Ada" || !reflect.DeepEqual(ada.Clients, []string{"Kid phone"}) {
		t.Fatalf("POST Ada = %+v", ada)
	}
	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Ben"}, withCookie(cookie))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"clients":[]`) {
		t.Fatalf("POST Ben (clients omitted): %d %s, want 201 with clients []", w.Code, w.Body.String())
	}
	ben := h.decodeChild(w)

	_, list := h.listChildren(cookie)
	if len(list.Children) != 2 || list.Children[0].ID != ada.ID || list.Children[1].ID != ben.ID {
		t.Fatalf("list = %+v, want [Ada Ben] oldest first", list.Children)
	}

	w = h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Ada M", "clients": []string{}}, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := h.decodeChild(w); got.Name != "Ada M" || !reflect.DeepEqual(got.Clients, []string{}) {
		t.Fatalf("PUT body = %+v", got)
	}
	if !strings.Contains(w.Body.String(), `"clients":[]`) {
		t.Fatalf("PUT body %s must carry clients [] not null", w.Body.String())
	}
	w = h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie))
	if got := h.decodeChild(w); w.Code != http.StatusOK || got.Name != "Ada M" || len(got.Clients) != 0 {
		t.Fatalf("GET after PUT: %d %+v", w.Code, got)
	}

	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	w = h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie))
	if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
		t.Fatalf("GET after DELETE: %d %s", w.Code, w.Body.String())
	}
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: %d, want 404", w.Code)
	}
}

func TestChildren_Conflicts(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben")
	before, _ := h.store.ListChildren(context.Background())

	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Ada"}, withCookie(cookie))
	if e := decodeErr(t, w); w.Code != http.StatusConflict || e.Error != CodeConflict || !strings.Contains(e.Message, "Ada") {
		t.Errorf("duplicate name: %d %s, want 409 conflict naming Ada", w.Code, w.Body.String())
	}

	w = h.do(http.MethodPut, childPath(ben.ID), map[string]any{"name": "Ada", "clients": []string{}}, withCookie(cookie))
	if w.Code != http.StatusConflict {
		t.Errorf("PUT ben as Ada: %d, want 409", w.Code)
	}
	_ = ada

	after, _ := h.store.ListChildren(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("store changed by conflicting requests:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestChildren_CreateShared(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.createOK(cookie, "Ada", "Kid phone")

	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Cy", "clients": []string{"Kid phone"}}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST Cy with Ada's client: %d %s, want 201", w.Code, w.Body.String())
	}
	if cy := h.decodeChild(w); cy.Name != "Cy" || !reflect.DeepEqual(cy.Clients, []string{"Kid phone"}) {
		t.Fatalf("POST body = %+v, want Cy [Kid phone]", cy)
	}

	_, list := h.listChildren(cookie)
	got := map[string][]string{}
	for _, c := range list.Children {
		got[c.Name] = c.Clients
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Cy": {"Kid phone"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GET /children = %v, want %v", got, want)
	}
}

func TestChildren_PutAtomic(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.fake.SetStatus("/control/clients/update", 500)

	w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Alicia", "clients": []string{"Kid phone", "Old laptop"}}, withCookie(cookie))
	if w.Code >= 200 && w.Code < 300 {
		t.Fatalf("PUT with AdGuard failing the adopt: %d, want non-2xx", w.Code)
	}
	w = h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie))
	if got := h.decodeChild(w); got.Name != "Ada" || !reflect.DeepEqual(got.Clients, []string{"Kid phone"}) {
		t.Fatalf("PUT half-applied: %+v", got)
	}
}

func TestChildren_Validation(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()

	bad := []struct {
		name string
		body any
	}{
		{"blank name", map[string]any{"name": "  "}},
		{"65-rune name", map[string]any{"name": strings.Repeat("é", 65)}},
		{"unknown field", map[string]any{"name": "A", "extra": 1}},
		{"20 KiB body", `{"name":"A","clients":["` + strings.Repeat("x", 20<<10) + `"]}`},
		{"non-object", `["A"]`},
		{"two objects", `{"name":"A"}{"name":"B"}`},
	}
	for _, tc := range bad {
		w := h.do(http.MethodPost, "/api/v1/children", tc.body, withCookie(cookie))
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Error != CodeBadRequest {
			t.Errorf("%s: %d %s, want 400 bad_request", tc.name, w.Code, w.Body.String())
		}
	}
	if names := h.storeNames(); len(names) != 0 {
		t.Fatalf("a 400 created rows: %v", names)
	}

	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "A", "clients": []string{""}}, withCookie(cookie))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"clients":[]`) {
		t.Errorf("clients [\"\"]: %d %s, want 201 with clients []", w.Code, w.Body.String())
	}
	a := h.decodeChild(w)
	w = h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Ada", "clients": []string{"Kid phone", " Kid phone "}}, withCookie(cookie))
	if got := h.decodeChild(w); w.Code != http.StatusCreated || !reflect.DeepEqual(got.Clients, []string{"Kid phone"}) {
		t.Errorf("duplicate/padded clients: %d %+v, want 201 [Kid phone]", w.Code, got)
	}
	w = h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": strings.Repeat("é", 64)}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Errorf("64-rune name: %d, want 201", w.Code)
	}

	for _, id := range []string{"abc", "999"} {
		if w := h.do(http.MethodGet, "/api/v1/children/"+id, nil, withCookie(cookie)); w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
			t.Errorf("GET /children/%s: %d, want 404 not_found", id, w.Code)
		}
		if w := h.do(http.MethodPut, "/api/v1/children/"+id, map[string]any{"name": "Z"}, withCookie(cookie)); w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
			t.Errorf("PUT /children/%s: %d, want 404 not_found", id, w.Code)
		}
		if w := h.do(http.MethodDelete, "/api/v1/children/"+id, nil, withCookie(cookie)); w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
			t.Errorf("DELETE /children/%s: %d, want 404 not_found", id, w.Code)
		}
	}
	// PUT validation runs against a real row too.
	w = h.do(http.MethodPut, childPath(a.ID), map[string]any{"name": ""}, withCookie(cookie))
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT blank name: %d, want 400", w.Code)
	}
}

func TestChildren_NoAdGuard(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetStatus("/control/clients", 500)

	ada := h.createOK(cookie, "Ada") // a POST without clients never needs AdGuard
	if w, list := h.listChildren(cookie); w.Code != http.StatusOK || len(list.Children) != 1 {
		t.Fatalf("list: %d %+v", w.Code, list)
	}
	// A PUT that adds no client never needs AdGuard; one that does is
	// covered by TestChildUpdate_*.
	if w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Ada M", "clients": []string{}}, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if w := h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", w.Code)
	}
	for _, r := range h.fake.Requests() {
		if r.Path != "/control/login" && !isRulesRead(r) {
			t.Errorf("CRUD hit AdGuard: %s %s", r.Method, r.Path)
		}
	}
}

func (h *harness) putChild(cookie string, id int64, name string, clients ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	if clients == nil {
		clients = []string{}
	}
	return h.do(http.MethodPut, childPath(id), map[string]any{"name": name, "clients": clients}, withCookie(cookie))
}

// wantOwn checks the fake's own list for name (sorted) and that it is off
// the global list.
func (h *harness) wantOwn(cookie, name string, want ...string) {
	h.t.Helper()
	got := slices.Clone(h.blocked(name))
	slices.Sort(got)
	if !reflect.DeepEqual(got, want) {
		h.t.Errorf("%s blocks %v, want %v", name, got, want)
	}
	_, body := h.getClients(cookie)
	if c := findClient(body.Clients, name); c == nil || c.UseGlobalBlockedServices {
		h.t.Errorf("%s = %+v, want use_global_blocked_services false", name, c)
	}
}

func (h *harness) childClients(cookie string, id int64) []string {
	h.t.Helper()
	return h.decodeChild(h.do(http.MethodGet, childPath(id), nil, withCookie(cookie))).Clients
}

func ownList(h *harness, name string, ids ...string) {
	list, _ := json.Marshal(ids)
	h.fake.MutateClient(name, func(m map[string]json.RawMessage) {
		m["use_global_blocked_services"] = json.RawMessage("false")
		m["blocked_services"] = list
	})
}

func TestChildUpdate_AddBaseline(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)

	if w := h.putChild(cookie, ada.ID, "Ada", "Kid phone", "Old laptop"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	h.wantOwn(cookie, "Old laptop", "tiktok", "youtube")
	if got := h.childClients(cookie, ada.ID); !reflect.DeepEqual(got, []string{"Kid phone", "Old laptop"}) {
		t.Errorf("Ada clients = %v", got)
	}
}

func TestChildUpdate_AddInherited(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")

	if w := h.putChild(cookie, ada.ID, "Ada", "Kid phone", "Kid tablet"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	h.wantOwn(cookie, "Kid tablet", "roblox", "tiktok", "youtube")
}

func TestChildUpdate_KeepsOwn(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ownList(h, "Old laptop", "steam")
	ada := h.seedChild("Ada", "Kid phone")

	if w := h.putChild(cookie, ada.ID, "Ada", "Kid phone", "Old laptop"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	h.wantOwn(cookie, "Old laptop", "steam", "tiktok", "youtube")
}

func TestChildUpdate_FullSwap(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ownList(h, "Kid phone", "steam", "youtube")
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)

	// Kid phone leaves, so its steam is not part of the baseline: no
	// sibling stays, the global list stands in.
	if w := h.putChild(cookie, ada.ID, "Ada", "Old laptop"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	h.wantOwn(cookie, "Old laptop", "roblox", "tiktok", "youtube")
}

func TestChildUpdate_RemoveOnly(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone", "Old laptop")

	if w := h.putChild(cookie, ada.ID, "Ada", "Old laptop"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("remove-only PUT made %d update POSTs, want 0", n)
	}
	for _, r := range h.fake.Requests() {
		if r.Path != "/control/login" && !isRulesRead(r) {
			t.Errorf("remove-only PUT hit AdGuard: %s %s", r.Method, r.Path)
		}
	}
}

func TestChildUpdate_RenameOnlyOffline(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.fake.SetAuth(false)

	w := h.putChild(cookie, ada.ID, "Ada M", "Kid phone")
	if w.Code != http.StatusOK || h.decodeChild(w).Name != "Ada M" {
		t.Errorf("rename-only PUT with AdGuard refusing: %d %s, want 200", w.Code, w.Body.String())
	}
}

func TestChildUpdate_Conflicts(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.seedChild("Ben", "Old laptop")
	before := len(h.fake.Requests())

	w := h.putChild(cookie, ada.ID, "ben", "Kid phone", "Kid tablet")
	if w.Code != http.StatusConflict || decodeErr(t, w).Error != CodeConflict {
		t.Errorf("renaming Ada to ben while adding: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("conflicting PUT made %d AdGuard requests, want 0", n)
	}
	if got := h.childClients(cookie, ada.ID); !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want unchanged [Kid phone]", got)
	}
}

func TestChildUpdate_AdGuardFails(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.fake.SetUpdateStatus("Old laptop", 500)

	w := h.putChild(cookie, ada.ID, "Ada M", "Kid phone", "Old laptop")
	e := decodeErr(t, w)
	if w.Code != http.StatusBadGateway || e.Error != CodeAdGuardUnavailable || !strings.Contains(e.Message, "Old laptop") {
		t.Errorf("%d %s, want 502 naming Old laptop", w.Code, w.Body.String())
	}
	got := h.decodeChild(h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie)))
	if got.Name != "Ada" || !reflect.DeepEqual(got.Clients, []string{"Kid phone"}) {
		t.Errorf("child after failed PUT = %+v, want Ada [Kid phone] unchanged", got)
	}

	h.fake.SetUpdateStatus("Old laptop", 0)
	h.fake.SetStatus("/control/clients", 500)
	if w := h.putChild(cookie, ada.ID, "Ada", "Kid phone", "Old laptop"); w.Code != http.StatusBadGateway {
		t.Errorf("AdGuard read down: %d %s, want 502", w.Code, w.Body.String())
	}
}

func TestChildUpdate_Ghost(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")

	if w := h.putChild(cookie, ada.ID, "Ada", "Kid phone", "Ghost"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := h.childClients(cookie, ada.ID); !reflect.DeepEqual(got, []string{"Ghost", "Kid phone"}) {
		t.Errorf("Ada clients = %v, want [Ghost Kid phone]", got)
	}
	if n := h.updatePosts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
}

func TestChildUpdate_UnknownChild(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	before := len(h.fake.Requests())
	if w := h.putChild(cookie, 999, "Zed", "Kid tablet"); w.Code != http.StatusNotFound {
		t.Errorf("PUT 999: %d, want 404", w.Code)
	}
	if n := len(h.fake.Requests()) - before; n != 0 {
		t.Errorf("unknown child made %d AdGuard requests, want 0", n)
	}
}

// updatesNaming returns the blocked_services of every update POST the fake
// saw for name, in order.
func (h *harness) updatesNaming(name string) [][]string {
	var out [][]string
	for _, r := range h.fake.Requests() {
		if r.Method != http.MethodPost || r.Path != "/control/clients/update" {
			continue
		}
		var u struct {
			Name string `json:"name"`
			Data struct {
				BlockedServices []string `json:"blocked_services"`
			} `json:"data"`
		}
		_ = json.Unmarshal(r.Body, &u)
		if u.Name == name {
			out = append(out, u.Data.BlockedServices)
		}
	}
	return out
}

// adguardSince counts the fake's requests after mark, login excluded.
func (h *harness) adguardSince(mark int) int {
	n := 0
	for _, r := range h.fake.Requests()[mark:] {
		if r.Path != "/control/login" && !isRulesRead(r) {
			n++
		}
	}
	return n
}

// isRulesRead is the managed-rules resync's user_rules read, which every
// child write now makes; with no custom services it never leads to a write,
// so the "child write makes no client writes" checks ignore it.
func isRulesRead(r adguardtest.Request) bool {
	return r.Method == http.MethodGet && r.Path == "/control/filtering/status"
}

// owners maps each child name to its clients from GET /children.
func (h *harness) owners(cookie string) map[string][]string {
	h.t.Helper()
	_, list := h.listChildren(cookie)
	got := map[string][]string{}
	for _, c := range list.Children {
		got[c.Name] = c.Clients
	}
	return got
}

func TestChildUpdate_ShareNo409(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	ben := h.seedChild("Ben")

	if w := h.putChild(cookie, ben.ID, "Ben", "Kid phone"); w.Code != http.StatusOK {
		t.Fatalf("PUT Ben [Kid phone] while Ada holds it: %d %s, want 200", w.Code, w.Body.String())
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Ben": {"Kid phone"}}
	if got := h.owners(cookie); !reflect.DeepEqual(got, want) {
		t.Errorf("GET /children = %v, want %v", got, want)
	}
}

func TestChildUpdate_ShareAdopts(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	ben := h.seedChild("Ben")

	if w := h.putChild(cookie, ben.ID, "Ben", "Kid phone"); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := h.updatesNaming("Kid phone"); len(got) != 1 {
		t.Errorf("update POSTs for Kid phone = %v, want exactly 1", got)
	}
	if n := h.updatePosts(); n != 1 {
		t.Errorf("update POSTs = %d, want 1", n)
	}
}

func TestChildUpdate_RemoveSharedNoAdGuard(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	ben := h.seedChild("Ben", "Kid phone")
	mark := len(h.fake.Requests())

	if w := h.putChild(cookie, ben.ID, "Ben"); w.Code != http.StatusOK {
		t.Fatalf("PUT Ben without Kid phone: %d %s", w.Code, w.Body.String())
	}
	if n := h.adguardSince(mark); n != 0 {
		t.Errorf("removal made %d AdGuard requests, want 0", n)
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Ben": {}}
	if got := h.owners(cookie); !reflect.DeepEqual(got, want) {
		t.Errorf("GET /children = %v, want %v", got, want)
	}
}

func TestChildDelete_SharedNoAdGuard(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	ben := h.seedChild("Ben", "Kid phone")
	mark := len(h.fake.Requests())

	if w := h.do(http.MethodDelete, childPath(ben.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE Ben: %d %s, want 204", w.Code, w.Body.String())
	}
	if n := h.adguardSince(mark); n != 0 {
		t.Errorf("DELETE made %d AdGuard requests, want 0", n)
	}
	want := map[string][]string{"Ada": {"Kid phone"}}
	if got := h.owners(cookie); !reflect.DeepEqual(got, want) {
		t.Errorf("GET /children = %v, want %v", got, want)
	}
}

func TestChildUpdate_NameStill409(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	ben := h.seedChild("Ben")
	mark := len(h.fake.Requests())

	w := h.putChild(cookie, ben.ID, "ada", "Kid phone")
	if w.Code != http.StatusConflict || decodeErr(t, w).Error != CodeConflict {
		t.Errorf("PUT Ben as ada: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := h.adguardSince(mark); n != 0 {
		t.Errorf("name conflict made %d AdGuard requests, want 0", n)
	}
}

func TestChildCreate_SharedAdopts(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")

	h.createOK(cookie, "Cy", "Kid phone")
	got := h.updatesNaming("Kid phone")
	if len(got) != 1 {
		t.Fatalf("update POSTs for Kid phone = %v, want exactly 1", got)
	}
	list := slices.Clone(got[0])
	slices.Sort(list)
	if want := []string{"roblox", "tiktok", "youtube"}; !reflect.DeepEqual(list, want) {
		t.Errorf("Kid phone blocked_services = %v, want %v (global list unioned in)", list, want)
	}

	// No adoption for a child without clients: the only AdGuard call is the
	// managed-rules resync's read, and with nothing to manage it writes nothing.
	mark := len(h.fake.Requests())
	h.createOK(cookie, "Dee")
	for _, req := range h.fake.Requests()[mark:] {
		if req.Method != http.MethodGet || req.Path != "/control/filtering/status" {
			t.Errorf("POST without clients made %s %s, want only the user_rules read", req.Method, req.Path)
		}
	}
}

func TestChildCreate_AdoptFailNoChild(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetStatus("/control/clients/update", 500)

	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Cy", "clients": []string{"Kid phone"}}, withCookie(cookie))
	e := decodeErr(t, w)
	if w.Code != http.StatusBadGateway || !strings.Contains(e.Message, "Kid phone") {
		t.Errorf("POST Cy with adopt failing: %d %s, want 502 naming Kid phone", w.Code, w.Body.String())
	}
	if got := h.owners(cookie); len(got) != 0 {
		t.Errorf("GET /children = %v, want no Cy", got)
	}
}

func TestChildCreate_NameTakenNoAdGuard(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	mark := len(h.fake.Requests())

	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "ada", "clients": []string{"Kid phone"}}, withCookie(cookie))
	if w.Code != http.StatusConflict || decodeErr(t, w).Error != CodeConflict {
		t.Errorf("POST ada: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := h.adguardSince(mark); n != 0 {
		t.Errorf("name conflict made %d AdGuard requests, want 0", n)
	}
}

// activeReadFails is the real store with ListActiveGrants failing: the
// engine's baseline read during adopt errors after AdGuard answered.
type activeReadFails struct{ *store.Store }

func (activeReadFails) ListActiveGrants(context.Context) ([]store.Grant, error) {
	return nil, errors.New("database is locked")
}

// A store read failing inside adopt is neither *ErrAdoptFailed nor an
// AdGuard outage: writeAdoptError answers 500 internal, logs it, and the
// share is not saved.
func TestChildUpdate_AdoptStoreReadFails(t *testing.T) {
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
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
	eng := grants.New(activeReadFails{st}, client, grants.Options{Now: (&clock{t: t0}).Now, ApplyTimeout: harnessApplyTimeout, Log: log})
	t.Cleanup(eng.Close)
	a := New(Deps{AdGuard: client, Log: log, Children: st, Grants: eng})

	ctx := context.Background()
	if _, err := st.CreateChild(ctx, "Ada", []string{"Kid phone"}); err != nil {
		t.Fatal(err)
	}
	ben, err := st.CreateChild(ctx, "Ben", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "Ben", "clients": []string{"Kid phone"}})
	r := httptest.NewRequest(http.MethodPut, childPath(ben.ID), bytes.NewReader(body))
	r.SetPathValue("id", strconv.FormatInt(ben.ID, 10))
	w := httptest.NewRecorder()
	a.handleChildUpdate(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT Ben [Kid phone]: %d %s, want 500", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Error != "internal" || e.Message != "could not update child" {
		t.Errorf("error = %+v, want internal / could not update child", e)
	}
	if !strings.Contains(logs.String(), "update child: baseline failed") {
		t.Errorf("log %q lacks the baseline failure", logs.String())
	}
	if n := fake.CountRequests("POST", "/control/clients/update"); n != 0 {
		t.Errorf("fake saw %d update POSTs, want 0", n)
	}
	got, err := st.GetChild(ctx, ben.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 0 {
		t.Errorf("Ben clients = %v, want [] (nothing saved)", got.Clients)
	}
}

// blockLine returns the managed region's ||domain^ block line, or "".
func blockLine(h *harness, domain string) string {
	for _, l := range managedRegion(h) {
		if strings.HasPrefix(l, "||"+domain+"^") {
			return l
		}
	}
	return ""
}

func TestChildUpdate_ResyncsRules(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "kid-phone")
	createCustom(t, h, cookie, "Portal", "a.com")
	if got := blockLine(h, "a.com"); got != "||a.com^$client='kid-phone'" {
		t.Fatalf("setup block line %q", got)
	}

	w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Ada", "clients": []string{"kid-phone", "kid-tablet"}}, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if got, want := blockLine(h, "a.com"), "||a.com^$client='kid-phone'|'kid-tablet'"; got != want {
		t.Fatalf("block line after adding kid-tablet %q, want %q", got, want)
	}
}

func TestChildUpdate_RemovalResyncs(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "kid-phone", "kid-tablet")
	createCustom(t, h, cookie, "Portal", "a.com")
	if got := blockLine(h, "a.com"); !strings.Contains(got, "'kid-tablet'") {
		t.Fatalf("setup block line %q", got)
	}

	w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Ada", "clients": []string{"kid-phone"}}, withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if got, want := blockLine(h, "a.com"), "||a.com^$client='kid-phone'"; got != want {
		t.Fatalf("block line after removing kid-tablet %q, want %q", got, want)
	}
}

func TestChildDelete_ResyncsRules(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "kid-phone", "kid-tablet")
	h.createOK(cookie, "Ben", "Zed-TV")
	createCustom(t, h, cookie, "Portal", "a.com")

	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", w.Code, w.Body)
	}
	for _, c := range []string{"'kid-phone'", "'kid-tablet'"} {
		noRegionLine(t, h, c)
	}
	if got, want := blockLine(h, "a.com"), "||a.com^$client='Zed-TV'"; got != want {
		t.Fatalf("block line after delete %q, want %q", got, want)
	}
}

func TestChildCreate_SyncFailure(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	createCustom(t, h, cookie, "Portal", "a.com")
	h.fake.SetStatus(setRulesPath, http.StatusInternalServerError)

	posts := h.fake.CountRequests("POST", setRulesPath)
	w := h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Ada", "clients": []string{"kid-phone"}}, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /children under a set_rules fault = %d %s, want 201", w.Code, w.Body)
	}
	if n := h.fake.CountRequests("POST", setRulesPath) - posts; n != 1 {
		t.Fatalf("%d set_rules attempts, want the one failed resync", n)
	}
	kids, err := h.store.ListChildren(context.Background())
	if err != nil || len(kids) != 1 || kids[0].Name != "Ada" {
		t.Fatalf("child not stored: %+v %v", kids, err)
	}
	if !strings.Contains(h.logs.String(), "reconciler will retry") {
		t.Errorf("sync failure not logged")
	}
}
