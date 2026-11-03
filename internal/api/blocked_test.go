package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/blocked"
	"github.com/Rivil/adguard-reward/internal/store"
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
	ada := h.seedChild("Ada", "Kid phone", "Kid tablet", "Ghost")
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
	none := []blocked.ChildRef{}
	wantClients := []blockedClientView{
		{Name: "Ghost", Missing: true, SharedWith: none},
		{Name: "Kid phone", SharedWith: none},
		{Name: "Kid tablet", UsesGlobal: true, SharedWith: none},
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
	ada := h.seedChild("Ada", "Kid phone")
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

func serviceRow(b blockedBody, id string) *blockedServiceView {
	for i := range b.Services {
		if b.Services[i].ID == id {
			return &b.Services[i]
		}
	}
	return nil
}

func clientRow(b blockedBody, name string) *blockedClientView {
	for i := range b.Clients {
		if b.Clients[i].Name == name {
			return &b.Clients[i]
		}
	}
	return nil
}

func TestBlocked_SharedUnblockedBy(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone")
	g := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)

	w, body := h.getBlocked(cookie, ben.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	kid := clientRow(body, "Kid phone")
	if want := []blocked.ChildRef{{ID: ada.ID, Name: "Ada"}}; kid == nil || !reflect.DeepEqual(kid.SharedWith, want) {
		t.Errorf("Kid phone = %+v, want shared_with %v", kid, want)
	}
	yt := serviceRow(body, "youtube")
	want := []blocked.Unblock{{Client: "Kid phone", GrantID: g.ID, Child: blocked.ChildRef{ID: ada.ID, Name: "Ada"}}}
	if yt == nil || !reflect.DeepEqual(yt.UnblockedBy, want) {
		t.Errorf("youtube = %+v, want unblocked_by %+v", yt, want)
	}
}

func TestBlocked_OwnGrantUnnamed(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	h.createOK(cookie, "Ben", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)

	_, body := h.getBlocked(cookie, ada.ID)
	yt := serviceRow(body, "youtube")
	if yt == nil || yt.State != blocked.StateUnblocked || yt.UnblockedBy == nil || len(yt.UnblockedBy) != 0 {
		t.Errorf("Ada's youtube = %+v, want unblocked with unblocked_by []", yt)
	}
}

func TestBlocked_BothOwnersListShared(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone", "Kid tablet")

	for _, c := range []childView{ada, ben} {
		_, body := h.getBlocked(cookie, c.ID)
		if clientRow(body, "Kid phone") == nil {
			t.Errorf("%s's blocked view lacks Kid phone: %+v", c.Name, body.Clients)
		}
	}
	_, body := h.getBlocked(cookie, ben.ID)
	if tab := clientRow(body, "Kid tablet"); tab == nil || tab.SharedWith == nil || len(tab.SharedWith) != 0 {
		t.Errorf("Ben's unshared Kid tablet = %+v, want shared_with []", tab)
	}
}

func TestBlocked_AnnotationShape(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone", "Kid tablet", "Ghost")

	w, _ := h.getBlocked(cookie, ada.ID)
	var raw struct {
		Clients  []map[string]json.RawMessage `json:"clients"`
		Services []map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Clients) == 0 || len(raw.Services) == 0 {
		t.Fatalf("empty view: %s", w.Body.String())
	}
	for _, c := range raw.Clients {
		if v, ok := c["shared_with"]; !ok || !strings.HasPrefix(string(v), "[") {
			t.Errorf("client %s shared_with = %s, want an array", c["name"], v)
		}
	}
	for _, sv := range raw.Services {
		if v, ok := sv["unblocked_by"]; !ok || !strings.HasPrefix(string(v), "[") {
			t.Errorf("service %s unblocked_by = %s, want an array", sv["id"], v)
		}
	}
}

func TestBlocked_DeletedGranter(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid phone")
	g := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE Ada: %d %s", w.Code, w.Body.String())
	}

	w, body := h.getBlocked(cookie, ben.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", w.Code, w.Body.String())
	}
	yt := serviceRow(body, "youtube")
	want := []blocked.Unblock{{Client: "Kid phone", GrantID: g.ID, Child: blocked.ChildRef{ID: ada.ID, Name: ""}}}
	if yt == nil || !reflect.DeepEqual(yt.UnblockedBy, want) {
		t.Errorf("youtube = %+v, want unblocked_by %+v", yt, want)
	}
}

type listFailingGrants struct{ Grants }

func (listFailingGrants) List(context.Context) ([]store.Grant, error) {
	return nil, errors.New("database is locked")
}

// blockedVia serves GET /children/{id}/blocked on d's fake with the given
// children and grants deps, calling the handler directly.
func blockedVia(t *testing.T, d grantsDirect, children ChildStore, g Grants, id int64) blockedBody {
	t.Helper()
	deps := d.a.deps
	deps.Children, deps.Grants = children, g
	r := httptest.NewRequest(http.MethodGet, childPath(id)+"/blocked", nil)
	r.SetPathValue("id", itoa(id))
	w := httptest.NewRecorder()
	New(deps).handleBlocked(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("blocked view: %d %s, want 200", w.Code, w.Body.String())
	}
	var body blockedBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// toggle is the one field that is null by contract (never toggled).
	if strings.Contains(strings.ReplaceAll(w.Body.String(), `"toggle":null`, ""), ":null") {
		t.Errorf("degraded body serialises a null: %s", w.Body.String())
	}
	return body
}

// sharedFixture seeds Ada and Ben on Kid phone with Ada's youtube grant
// active, returning Ben.
func sharedFixture(t *testing.T, d grantsDirect) store.Child {
	t.Helper()
	ctx := context.Background()
	ada, err := d.store.CreateChild(ctx, "Ada", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	ben, err := d.store.CreateChild(ctx, "Ben", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.eng.Create(ctx, ada.ID, []string{"youtube"}, ada.Clients, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	return ben
}

func noUnblockedBy(t *testing.T, body blockedBody) {
	t.Helper()
	for _, sv := range body.Services {
		if sv.UnblockedBy == nil || len(sv.UnblockedBy) != 0 {
			t.Errorf("%s unblocked_by = %v, want []", sv.ID, sv.UnblockedBy)
		}
	}
}

func TestBlocked_GrantsReadFails(t *testing.T) {
	d := newGrantsDirect(t)
	ben := sharedFixture(t, d)
	healthy := blockedVia(t, d, d.store, d.eng, ben.ID)
	if yt := serviceRow(healthy, "youtube"); yt == nil || len(yt.UnblockedBy) != 1 {
		t.Fatalf("precondition: healthy youtube = %+v, want one unblocked_by", yt)
	}

	body := blockedVia(t, d, d.store, listFailingGrants{d.eng}, ben.ID)
	noUnblockedBy(t, body)
	if got, want := stateOf(body), stateOf(healthy); !reflect.DeepEqual(got, want) {
		t.Errorf("states = %v, want unchanged %v", got, want)
	}
}

func TestBlocked_ChildrenReadFails(t *testing.T) {
	d := newGrantsDirect(t)
	ben := sharedFixture(t, d)
	healthy := blockedVia(t, d, d.store, d.eng, ben.ID)
	if kid := clientRow(healthy, "Kid phone"); kid == nil || len(kid.SharedWith) != 1 {
		t.Fatalf("precondition: healthy Kid phone = %+v, want one shared_with", kid)
	}

	body := blockedVia(t, d, failingChildren{d.store}, d.eng, ben.ID)
	noUnblockedBy(t, body)
	for _, c := range body.Clients {
		if c.SharedWith == nil || len(c.SharedWith) != 0 {
			t.Errorf("%s shared_with = %v, want []", c.Name, c.SharedWith)
		}
	}
	if got, want := stateOf(body), stateOf(healthy); !reflect.DeepEqual(got, want) {
		t.Errorf("states = %v, want unchanged %v", got, want)
	}
}

// blockedCustomVia is blockedVia with Deps.CustomServices set to cs; it also
// returns the raw body so the wire shape can be checked key by key.
func blockedCustomVia(t *testing.T, d grantsDirect, cs CustomServiceStore, g Grants, id int64) (blockedBody, string) {
	t.Helper()
	deps := d.a.deps
	deps.Children, deps.Grants, deps.CustomServices = d.store, g, cs
	r := httptest.NewRequest(http.MethodGet, childPath(id)+"/blocked", nil)
	r.SetPathValue("id", itoa(id))
	w := httptest.NewRecorder()
	New(deps).handleBlocked(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("blocked view: %d %s, want 200", w.Code, w.Body.String())
	}
	var body blockedBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body, w.Body.String()
}

// customFixture seeds Ada on Kid phone and a custom service Portal.
func customFixture(t *testing.T, d grantsDirect) store.Child {
	t.Helper()
	ctx := context.Background()
	ada, err := d.store.CreateChild(ctx, "Ada", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.eng.CreateService(ctx, "Portal", []string{"a.com"}); err != nil {
		t.Fatal(err)
	}
	return ada
}

func TestBlocked_CustomReadFailure(t *testing.T) {
	d := newGrantsDirect(t)
	ada := customFixture(t, d)

	healthy, _ := blockedCustomVia(t, d, d.store, d.eng, ada.ID)
	if p := serviceRow(healthy, "custom:1"); p == nil || p.State != "blocked" {
		t.Fatalf("precondition: healthy custom:1 = %+v, want blocked", p)
	}

	for name, run := range map[string]func() blockedBody{
		"custom list": func() blockedBody {
			b, _ := blockedCustomVia(t, d, &countingCustom{inner: d.store, err: errors.New("database is locked")}, d.eng, ada.ID)
			return b
		},
		"grants": func() blockedBody {
			b, _ := blockedCustomVia(t, d, d.store, listFailingGrants{d.eng}, ada.ID)
			return b
		},
	} {
		body := run()
		if p := serviceRow(body, "custom:1"); p != nil {
			t.Errorf("%s read failed: custom:1 still reported as %+v", name, p)
		}
		if yt := serviceRow(body, "youtube"); yt == nil {
			t.Errorf("%s read failed: built-ins missing", name)
		}
	}
}

func TestBlocked_CustomJSON(t *testing.T) {
	d := newGrantsDirect(t)
	ada := customFixture(t, d)
	if _, err := d.eng.Create(context.Background(), ada.ID, []string{"custom:1"}, ada.Clients, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	_, raw := blockedCustomVia(t, d, d.store, d.eng, ada.ID)
	var doc struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	var custom, builtin map[string]any
	for _, s := range doc.Services {
		switch s["id"] {
		case "custom:1":
			custom = s
		case "youtube":
			builtin = s
		}
	}
	want := map[string]any{
		"id": "custom:1", "name": "Portal", "icon": "", "custom": true,
		"state": "unblocked", "toggle": nil, "differs": []any{}, "unblocked_by": []any{},
	}
	if !reflect.DeepEqual(custom, want) {
		t.Fatalf("custom entry %v, want %v", custom, want)
	}
	if builtin == nil || builtin["custom"] != false {
		t.Fatalf("built-in entry %v, want custom:false", builtin)
	}
}

// failingToggles is a ChildStore whose toggles read fails.
type failingToggles struct{ ChildStore }

func (failingToggles) ListServiceToggles(context.Context) ([]store.ServiceToggle, error) {
	return nil, errors.New("database is locked")
}

// blockedRaw serves the blocked view with children as Deps.Children and the
// real store as CustomServices; it returns the decoded body and each
// service's raw JSON object by id.
func blockedRaw(t *testing.T, d grantsDirect, children ChildStore, id int64) (blockedBody, map[string]map[string]json.RawMessage) {
	t.Helper()
	deps := d.a.deps
	deps.Children, deps.Grants, deps.CustomServices = children, d.eng, d.store
	r := httptest.NewRequest(http.MethodGet, childPath(id)+"/blocked", nil)
	r.SetPathValue("id", itoa(id))
	w := httptest.NewRecorder()
	New(deps).handleBlocked(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("blocked view: %d %s, want 200", w.Code, w.Body.String())
	}
	var body blockedBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services []map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]json.RawMessage{}
	for _, s := range doc.Services {
		var id string
		if err := json.Unmarshal(s["id"], &id); err != nil {
			t.Fatal(err)
		}
		byID[id] = s
	}
	return body, byID
}

func mustToggle(t *testing.T, d grantsDirect, childID int64, service, state string) {
	t.Helper()
	if _, err := d.store.SetServiceToggle(context.Background(), childID, service, state, t0); err != nil {
		t.Fatal(err)
	}
}

func TestBlocked_Toggle(t *testing.T) {
	d := newGrantsDirect(t)
	ada, err := d.store.CreateChild(context.Background(), "Ada", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	mustToggle(t, d, ada.ID, "youtube", "unblocked")
	// The applied unblock: youtube is gone from Kid phone's own list.
	d.fake.MutateClient("Kid phone", func(c map[string]json.RawMessage) {
		c["blocked_services"] = json.RawMessage(`["tiktok"]`)
	})

	body, raw := blockedRaw(t, d, d.store, ada.ID)
	yt := serviceRow(body, "youtube")
	if yt == nil || yt.State != "unblocked" || yt.Toggle == nil || *yt.Toggle != "unblocked" {
		t.Fatalf("youtube = %+v, want state unblocked, toggle unblocked", yt)
	}
	tk, ok := raw["tiktok"]["toggle"]
	if !ok {
		t.Fatalf("tiktok has no toggle key: %v", raw["tiktok"])
	}
	if string(tk) != "null" {
		t.Fatalf("untoggled tiktok toggle = %s, want null", tk)
	}
}

func TestBlocked_PermanentUnblockedBy(t *testing.T) {
	d := newGrantsDirect(t)
	ctx := context.Background()
	ann, err := d.store.CreateChild(ctx, "Ann", []string{"Kid phone", "Old laptop"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := d.store.CreateChild(ctx, "Bob", []string{"Kid phone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.eng.CreateService(ctx, "Portal", []string{"a.com"}); err != nil {
		t.Fatal(err)
	}
	mustToggle(t, d, bob.ID, "custom:1", "unblocked")

	body, raw := blockedRaw(t, d, d.store, ann.ID)
	p := serviceRow(body, "custom:1")
	if p == nil || p.State != "partial" || !reflect.DeepEqual(p.Differs, []string{"Kid phone"}) || p.Toggle != nil {
		t.Fatalf("Ann's custom:1 = %+v, want partial differing on Kid phone, toggle null", p)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw["custom:1"]["unblocked_by"], &entries); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{
		"client": "Kid phone", "grant_id": float64(0), "permanent": true,
		"child": map[string]any{"id": float64(bob.ID), "name": "Bob"},
	}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("unblocked_by = %v, want %v", entries, want)
	}
}

func TestBlocked_TogglesDown(t *testing.T) {
	d := newGrantsDirect(t)
	ada := customFixture(t, d)
	mustToggle(t, d, ada.ID, "youtube", "blocked")
	mustToggle(t, d, ada.ID, "custom:1", "unblocked")

	healthy, _ := blockedRaw(t, d, d.store, ada.ID)
	if yt := serviceRow(healthy, "youtube"); yt == nil || yt.Toggle == nil || *yt.Toggle != "blocked" {
		t.Fatalf("precondition: healthy youtube = %+v, want toggle blocked", yt)
	}
	if p := serviceRow(healthy, "custom:1"); p == nil {
		t.Fatal("precondition: healthy view lacks custom:1")
	}

	body, raw := blockedRaw(t, d, failingToggles{d.store}, ada.ID)
	if len(body.Services) == 0 {
		t.Fatal("no services in degraded view")
	}
	for _, s := range body.Services {
		if strings.HasPrefix(s.ID, "custom:") {
			t.Fatalf("toggles down: %s still reported as %+v", s.ID, s)
		}
		if string(raw[s.ID]["toggle"]) != "null" {
			t.Fatalf("toggles down: %s toggle = %s, want null", s.ID, raw[s.ID]["toggle"])
		}
	}
	if got, want := stateOf(body)["youtube"], stateOf(healthy)["youtube"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("youtube state = %v, want unchanged %v", got, want)
	}
}
