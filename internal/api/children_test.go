package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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

	w = h.do(http.MethodPost, "/api/v1/children", map[string]any{"name": "Cy", "clients": []string{"Kid phone"}}, withCookie(cookie))
	if e := decodeErr(t, w); w.Code != http.StatusConflict || e.Error != CodeConflict ||
		!strings.Contains(e.Message, "Kid phone") || !strings.Contains(e.Message, "Ada") {
		t.Errorf("taken client: %d %s, want 409 conflict naming Kid phone and Ada", w.Code, w.Body.String())
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

func TestChildren_PutAtomic(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.createOK(cookie, "Ben", "Old laptop")

	w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Alicia", "clients": []string{"Old laptop"}}, withCookie(cookie))
	if w.Code != http.StatusConflict {
		t.Fatalf("PUT with Ben's client: %d, want 409", w.Code)
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

	ada := h.createOK(cookie, "Ada", "Kid phone")
	if w, list := h.listChildren(cookie); w.Code != http.StatusOK || len(list.Children) != 1 {
		t.Fatalf("list: %d %+v", w.Code, list)
	}
	if w := h.do(http.MethodPut, childPath(ada.ID), map[string]any{"name": "Ada M", "clients": []string{"Kid tablet"}}, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if w := h.do(http.MethodGet, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", w.Code)
	}
	for _, r := range h.fake.Requests() {
		if r.Path != "/control/login" {
			t.Errorf("CRUD hit AdGuard: %s %s", r.Method, r.Path)
		}
	}
}
