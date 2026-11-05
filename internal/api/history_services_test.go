package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/store"
)

const historyServicesPath = "/api/v1/history/services"

func (h *harness) historyServicesOK(cookie string) []historyServiceOptionView {
	h.t.Helper()
	w := h.do(http.MethodGet, historyServicesPath, nil, withCookie(cookie))
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET history services = %d %s", w.Code, w.Body)
	}
	var body historyServicesView
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		h.t.Fatalf("history services body %q: %v", w.Body.String(), err)
	}
	return body.Services
}

func TestHistoryServices_Names(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	h.seedLegacyGrant(ada.ID, "youtube")

	// The catalogue renames both, so only the unsnapshotted one may follow it.
	all := adguardtest.Fixture("blocked_services_all.json")
	all = bytes.ReplaceAll(all, []byte(`"TikTok"`), []byte(`"TikTok Renamed"`))
	all = bytes.ReplaceAll(all, []byte(`"YouTube"`), []byte(`"YouTube Live"`))
	h.fake.SetResponse(catalogueAllPath, http.StatusOK, all)

	want := []historyServiceOptionView{{ID: "tiktok", Name: "TikTok"}, {ID: "youtube", Name: "YouTube Live"}}
	if got := h.historyServicesOK(cookie); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want the snapshot for tiktok and the live name for youtube: %+v", got, want)
	}

	h.fake.SetStatus(catalogueAllPath, http.StatusInternalServerError)
	want = []historyServiceOptionView{{ID: "tiktok", Name: "TikTok"}, {ID: "youtube", Name: "youtube"}}
	if got := h.historyServicesOK(cookie); !reflect.DeepEqual(got, want) {
		t.Fatalf("services with AdGuard down = %+v, want %+v", got, want)
	}
}

func TestHistoryServices_CatalogueDown(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	h.seedLegacyGrant(ada.ID, "custom:7", "youtube")
	h.fake.SetStatus(catalogueAllPath, http.StatusInternalServerError)

	// By name, and capitals sort before lower case.
	want := []historyServiceOptionView{
		{ID: "tiktok", Name: "TikTok"},
		{ID: "custom:7", Name: "custom:7"},
		{ID: "youtube", Name: "youtube"},
	}
	if got := h.historyServicesOK(cookie); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want %+v", got, want)
	}
}

func TestHistoryServices_NoCatalogueRead(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	hits := h.fake.CountRequests("GET", catalogueAllPath)
	h.historyServicesOK(cookie)
	if n := h.fake.CountRequests("GET", catalogueAllPath) - hits; n != 0 {
		t.Fatalf("all-snapshotted options read the catalogue %d times, want 0", n)
	}
}

func TestHistoryServices_DeletedCustom(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	// Straight in the store: the API refuses a name the catalogue already has.
	ctx := context.Background()
	cs, err := h.store.CreateCustomService(ctx, "Roblox", []string{"roblox.example"})
	if err != nil {
		t.Fatal(err)
	}
	id := store.CustomServiceID(cs.ID)
	g, err := h.store.CreateGrantNamed(ctx, ada.ID, []string{id}, ada.Clients, t0, t0.Add(time.Hour), map[string]string{id: "Roblox"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.store.SetGrantStatus(ctx, g.ID, store.StatusActive, store.StatusEnded, t0.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("SetGrantStatus = %v, %v", ok, err)
	}
	if w := h.do(http.MethodDelete, customPath+"/"+itoa(cs.ID), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("delete custom = %d %s", w.Code, w.Body)
	}

	want := []historyServiceOptionView{{ID: id, Name: "Roblox"}}
	if got := h.historyServicesOK(cookie); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want %+v", got, want)
	}
	if got := overviewIDs(h.overviewOK(cookie, "?service="+id)); !reflect.DeepEqual(got, []int64{g.ID}) {
		t.Fatalf("service=%s = %v, want [%d]", id, got, g.ID)
	}
}

func TestHistoryServices_Shape(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()

	w := h.do(http.MethodGet, historyServicesPath, nil, withCookie(cookie))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"services":[]`)) {
		t.Fatalf("no grants = %d %s, want services []", w.Code, w.Body)
	}
	if w := h.do(http.MethodGet, historyServicesPath, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", w.Code)
	}

	ada := h.seedChild("Ada", "Kid phone")
	if _, err := h.store.CreateGrantNamed(context.Background(), ada.ID, []string{"custom:2", "custom:1", "aaa"}, ada.Clients, t0, t0.Add(time.Hour),
		map[string]string{"custom:2": "Games", "custom:1": "Games", "aaa": "Zebra"}); err != nil {
		t.Fatal(err)
	}

	w = h.do(http.MethodGet, historyServicesPath, nil, withCookie(cookie))
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	if got := keysOf(t, raw); !reflect.DeepEqual(got, []string{"services"}) {
		t.Fatalf("body keys = %v", got)
	}
	if got := keysOf(t, raw["services"].([]any)[0]); !reflect.DeepEqual(got, []string{"id", "name"}) {
		t.Fatalf("service keys = %v", got)
	}

	want := []historyServiceOptionView{
		{ID: "custom:1", Name: "Games"},
		{ID: "custom:2", Name: "Games"},
		{ID: "aaa", Name: "Zebra"},
	}
	if got := h.historyServicesOK(cookie); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want by name then id: %+v", got, want)
	}
}

func TestHistoryServices_Routing(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	g := h.seedRawGrant(ada.ID, store.StatusExpired, nil, t0, "youtube")

	if got := h.historyServicesOK(cookie); len(got) != 1 || got[0].ID != "youtube" {
		t.Fatalf("options = %+v, want youtube", got)
	}
	if got := overviewIDs(h.overviewOK(cookie, "")); !reflect.DeepEqual(got, []int64{g}) {
		t.Fatalf("page = %v, want [%d]", got, g)
	}
	if w := h.do(http.MethodGet, "/api/v1/history/other", nil, withCookie(cookie)); w.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/history/other = %d %s, want 404", w.Code, w.Body)
	}
}
