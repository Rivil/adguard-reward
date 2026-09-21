package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func buttonItem(label string, childID int64, services []string, duration int) map[string]any {
	return map[string]any{"label": label, "child_id": childID, "services": services, "duration": duration}
}

func buttonsReq(items ...map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"buttons": items}
}

func (h *harness) putButtons(cookie string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPut, "/api/v1/buttons", body, withCookie(cookie))
}

func (h *harness) getButtons(cookie string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, "/api/v1/buttons", nil, withCookie(cookie))
}

func decodeButtons(t *testing.T, w *httptest.ResponseRecorder) []buttonView {
	t.Helper()
	var body buttonsBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not a buttons body: %v", w.Body.String(), err)
	}
	return body.Buttons
}

func (h *harness) putOK(cookie string, items ...map[string]any) []buttonView {
	h.t.Helper()
	w := h.putButtons(cookie, buttonsReq(items...))
	if w.Code != http.StatusOK {
		h.t.Fatalf("PUT /api/v1/buttons: %d %s", w.Code, w.Body.String())
	}
	return decodeButtons(h.t, w)
}

func (h *harness) catalogueHits() int {
	return h.fake.CountRequests("GET", "/control/blocked_services/all")
}

func TestButtons_Gated(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	before := h.putOK(cookie, buttonItem("TikTok", ada.ID, []string{"tiktok"}, 1800))

	body := buttonsReq(buttonItem("YouTube", ada.ID, []string{"youtube"}, 3600))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if w := h.do(method, "/api/v1/buttons", body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a cookie: %d, want 401", method, w.Code)
		}
	}
	if w := h.do(http.MethodPut, "/api/v1/buttons", body, withCookie(cookie), noCSRF()); w.Code != http.StatusForbidden {
		t.Errorf("PUT without CSRF: %d, want 403", w.Code)
	}
	if got := decodeButtons(t, h.getButtons(cookie)); !reflect.DeepEqual(got, before) {
		t.Errorf("list after gated requests = %+v, want %+v", got, before)
	}
}

func TestButtons_RoundTrip(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	w := h.putButtons(cookie, buttonsReq(
		buttonItem("TikTok 30m", ada.ID, []string{"tiktok"}, 1800),
		buttonItem("YouTube 1h", ada.ID, []string{"youtube", "tiktok"}, 3600),
	))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := decodeButtons(t, w)
	if len(got) != 2 || got[0].ID <= 0 || got[1].ID <= got[0].ID {
		t.Fatalf("PUT body = %+v, want two buttons with ascending ids > 0", got)
	}
	if got[0].Label != "TikTok 30m" || got[1].Label != "YouTube 1h" {
		t.Errorf("labels = %q, %q, want PUT order", got[0].Label, got[1].Label)
	}
	if !reflect.DeepEqual(got[1].Services, []string{"youtube", "tiktok"}) {
		t.Errorf("services = %v, want given order [youtube tiktok]", got[1].Services)
	}
	if got[0].Duration != 1800 || got[1].Duration != 3600 || got[0].ChildID != ada.ID {
		t.Errorf("fields = %+v", got)
	}
	if strings.Contains(w.Body.String(), `"duration":1800.`) || !strings.Contains(w.Body.String(), `"duration":1800`) {
		t.Errorf("duration is not a plain integer: %s", w.Body.String())
	}
	if list := h.getButtons(cookie); list.Code != http.StatusOK || list.Body.String() != w.Body.String() {
		t.Errorf("GET = %d %s, want byte-equal to the PUT response %s", list.Code, list.Body.String(), w.Body.String())
	}

	w = h.putButtons(cookie, buttonsReq())
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"buttons":[]}` {
		t.Fatalf("PUT [] = %d %s, want 200 {\"buttons\":[]}", w.Code, w.Body.String())
	}
	if list := h.getButtons(cookie); strings.TrimSpace(list.Body.String()) != `{"buttons":[]}` {
		t.Errorf("GET after PUT [] = %s", list.Body.String())
	}
	for _, body := range []string{w.Body.String(), h.getButtons(cookie).Body.String()} {
		if strings.Contains(body, ":null") {
			t.Errorf("body carries null: %s", body)
		}
	}
}

func TestButtons_Validation(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	baseline := h.putOK(cookie, buttonItem("Base", ada.ID, []string{"tiktok"}, 1800))

	check := func(name string, body any, want int, contains ...string) {
		t.Helper()
		w := h.putButtons(cookie, body)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d (body %s)", name, w.Code, want, w.Body.String())
		}
		for _, c := range contains {
			if !strings.Contains(w.Body.String(), c) {
				t.Errorf("%s: body %s, want it to contain %q", name, w.Body.String(), c)
			}
		}
		if want != http.StatusOK {
			if got := decodeButtons(t, h.getButtons(cookie)); !reflect.DeepEqual(got, baseline) {
				t.Errorf("%s: list changed after a %d: %+v", name, w.Code, got)
			}
		}
	}
	item := func(mut func(m map[string]any)) map[string]any {
		m := buttonItem("Ok", ada.ID, []string{"tiktok"}, 1800)
		mut(m)
		return m
	}

	check("duration 59", buttonsReq(item(func(m map[string]any) { m["duration"] = 59 })), 422, "buttons[0].duration")
	check("duration 86401", buttonsReq(item(func(m map[string]any) { m["duration"] = 86401 })), 422, "buttons[0].duration")
	check("duration 0", buttonsReq(item(func(m map[string]any) { m["duration"] = 0 })), 422, "buttons[0].duration")

	hits := h.catalogueHits()
	check("child 999", buttonsReq(item(func(m map[string]any) { m["child_id"] = 999 })), 422, "buttons[0].child_id", "999")
	if n := h.catalogueHits() - hits; n != 0 {
		t.Errorf("unknown child hit the catalogue %d times; the child check must precede any AdGuard call", n)
	}

	check("service nope", buttonsReq(item(func(m map[string]any) { m["services"] = []string{"nope"} })), 422, "buttons[0].services", "nope")
	check("services []", buttonsReq(item(func(m map[string]any) { m["services"] = []string{} })), 422, "buttons[0].services")
	check("services [\"\"]", buttonsReq(item(func(m map[string]any) { m["services"] = []string{""} })), 422, "buttons[0].services")
	check("label empty", buttonsReq(item(func(m map[string]any) { m["label"] = "" })), 422, "buttons[0].label")
	check("label blank", buttonsReq(item(func(m map[string]any) { m["label"] = "   " })), 422, "buttons[0].label")
	check("label 65 runes", buttonsReq(item(func(m map[string]any) { m["label"] = strings.Repeat("é", 65) })), 422, "buttons[0].label")
	check("second item bad", buttonsReq(item(func(map[string]any) {}), item(func(m map[string]any) { m["duration"] = 5 })), 422, "buttons[1].duration")
	check("unknown child and service", buttonsReq(item(func(m map[string]any) {
		m["child_id"] = 999
		m["services"] = []string{"nope"}
	})), 422, "buttons[0].child_id")

	check("stray id", `{"buttons":[{"label":"x","id":1,"child_id":`+itoa(ada.ID)+`,"services":["tiktok"],"duration":1800}]}`, 400)
	check("buttons null", `{"buttons":null}`, 400)
	check("empty object", `{}`, 400)
	check("bare array", `[]`, 400)
	check("20 KiB body", `{"buttons":[{"label":"`+strings.Repeat("a", 20<<10)+`","child_id":1,"services":["tiktok"],"duration":1800}]}`, 400)

	check("label 64 runes", buttonsReq(item(func(m map[string]any) { m["label"] = strings.Repeat("é", 64) })), 200)
	check("duration 60", buttonsReq(item(func(m map[string]any) { m["duration"] = 60 })), 200)
	check("duration 86400", buttonsReq(item(func(m map[string]any) { m["duration"] = 86400 })), 200)
	if got := h.putOK(cookie, item(func(m map[string]any) { m["services"] = []string{" tiktok ", "tiktok", "youtube"} })); !reflect.DeepEqual(got[0].Services, []string{"tiktok", "youtube"}) {
		t.Errorf("services normalised = %v, want [tiktok youtube]", got[0].Services)
	}
}

func TestButtons_CatalogueDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	before := h.putOK(cookie, buttonItem("TikTok", ada.ID, []string{"tiktok"}, 1800))
	h.fake.SetStatus("/control/blocked_services/all", 500)

	w := h.putButtons(cookie, buttonsReq(buttonItem("YouTube", ada.ID, []string{"youtube"}, 3600)))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("PUT with catalogue down = %d %s, want 502", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Error != CodeAdGuardUnavailable {
		t.Errorf("envelope = %+v", e)
	}
	if got := decodeButtons(t, h.getButtons(cookie)); !reflect.DeepEqual(got, before) {
		t.Errorf("list changed behind an unreadable catalogue: %+v", got)
	}

	hits := h.catalogueHits()
	if w := h.putButtons(cookie, buttonsReq()); w.Code != http.StatusOK {
		t.Errorf("PUT [] with catalogue down = %d, want 200", w.Code)
	}
	if w := h.getButtons(cookie); w.Code != http.StatusOK {
		t.Errorf("GET with catalogue down = %d", w.Code)
	}
	if n := h.catalogueHits() - hits; n != 0 {
		t.Errorf("PUT [] and GET issued %d catalogue requests, want 0", n)
	}
}

func TestButtons_IDsReassigned(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	item := buttonItem("TikTok", ada.ID, []string{"tiktok"}, 1800)
	first := h.putOK(cookie, item)
	second := h.putOK(cookie, item)
	if second[0].ID <= first[0].ID {
		t.Errorf("second PUT id = %d, want > %d", second[0].ID, first[0].ID)
	}
}

func TestButtons_ChildDeleteCascades(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben", "Kid tablet")
	stored := h.putOK(cookie,
		buttonItem("Ada TikTok", ada.ID, []string{"tiktok"}, 1800),
		buttonItem("Ben YouTube", ben.ID, []string{"youtube"}, 3600),
	)
	if w := h.do(http.MethodDelete, childPath(ada.ID), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE child: %d %s", w.Code, w.Body.String())
	}
	got := decodeButtons(t, h.getButtons(cookie))
	if len(got) != 1 || got[0].ID != stored[1].ID || got[0].ChildID != ben.ID {
		t.Errorf("after deleting Ada = %+v, want only Ben's button id %d", got, stored[1].ID)
	}
}

func TestButtons_ConcurrentPut(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")

	const n = 8
	bodies := make([]map[string]any, n)
	for i := range bodies {
		bodies[i] = buttonsReq(
			buttonItem("A"+itoa(int64(i)), ada.ID, []string{"tiktok"}, 60*(i+1)),
			buttonItem("B"+itoa(int64(i)), ada.ID, []string{"youtube", "roblox"}, 120*(i+1)),
		)
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.putButtons(cookie, bodies[i]).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("PUT %d = %d, want 200", i, c)
		}
	}

	final := decodeButtons(t, h.getButtons(cookie))
	matched := false
	for _, b := range bodies {
		items := b["buttons"].([]map[string]any)
		if len(final) != len(items) {
			continue
		}
		same := true
		for i, it := range items {
			if final[i].Label != it["label"] || final[i].Duration != it["duration"].(int) ||
				!reflect.DeepEqual(final[i].Services, it["services"].([]string)) {
				same = false
				break
			}
		}
		if same {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("final list %+v matches none of the submitted bodies", final)
	}
}
