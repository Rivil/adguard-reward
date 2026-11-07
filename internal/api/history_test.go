package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/store"
)

const catalogueAllPath = "/control/blocked_services/all"

func historyPath(id int64) string { return childPath(id) + "/history" }

func (h *harness) getHistory(cookie, path string) (*httptest.ResponseRecorder, historyPageView) {
	h.t.Helper()
	w := h.do(http.MethodGet, path, nil, withCookie(cookie))
	var body historyPageView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("history body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

func (h *harness) historyOK(cookie string, id int64) historyPageView {
	h.t.Helper()
	w, body := h.getHistory(cookie, historyPath(id))
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET history %d = %d %s", id, w.Code, w.Body)
	}
	return body
}

// seedLegacyGrant writes an ended grant the way a pre-0009 binary left it:
// no end_reason and no name snapshot.
func (h *harness) seedLegacyGrant(childID int64, services ...string) int64 {
	h.t.Helper()
	res, err := h.store.DB().Exec(`INSERT INTO grants (child_id, status, started_at, ends_at, ended_at)
	    VALUES (?, 'ended', ?, ?, ?)`, childID, t0.Unix(), t0.Add(time.Hour).Unix(), t0.Add(30*time.Minute).Unix())
	if err != nil {
		h.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, s := range services {
		if _, err := h.store.DB().Exec(`INSERT INTO grant_services (grant_id, child_id, service_id, active) VALUES (?, ?, ?, 0)`,
			id, childID, s); err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

func serviceNames(g historyGrantView) map[string]string {
	out := map[string]string{}
	for _, s := range g.Services {
		out[s.ID] = s.Name
	}
	return out
}

func findGrant(t *testing.T, p historyPageView, id int64) historyGrantView {
	t.Helper()
	for _, g := range p.Grants {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("grant %d not in history %+v", id, p.Grants)
	return historyGrantView{}
}

func TestEndStatus(t *testing.T) {
	for _, tc := range []struct{ status, reason, want string }{
		{store.StatusEnded, "", "ended_unknown"},
		{store.StatusExpired, "", "expired"},
		{store.StatusExpired, store.EndReasonExpired, "expired"},
		{store.StatusEnded, store.EndReasonEndedEarly, "ended_early"},
		{store.StatusEnded, store.EndReasonPermanentBlock, "permanent_block"},
		{store.StatusActive, "", "ongoing"},
		{store.StatusActive, store.EndReasonEndedEarly, "ongoing"},
		{store.StatusActive, store.EndReasonPermanentBlock, "ongoing"},
	} {
		if got := endStatus(tc.status, tc.reason); got != tc.want {
			t.Errorf("endStatus(%q, %q) = %q, want %q", tc.status, tc.reason, got, tc.want)
		}
	}
}

func TestHistory_NameSnapshotEndToEnd(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	cs := createCustom(t, h, cookie, "Portal", "portal.example")
	created := h.grantOK(cookie, ada.ID, []string{cs.ServiceID, "tiktok"}, 1800)

	if w := h.do(http.MethodPut, customPath+"/"+itoa(cs.ID), customBody{"Games", []string{"portal.example"}}, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("rename custom = %d %s", w.Code, w.Body)
	}
	if got := serviceNames(findGrant(t, h.historyOK(cookie, ada.ID), created.ID)); got[cs.ServiceID] != "Portal" {
		t.Fatalf("after rename names = %v, want %s still Portal", got, cs.ServiceID)
	}

	if w := h.do(http.MethodPost, grantPath(created.ID, "end"), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("end = %d %s", w.Code, w.Body)
	}
	if w := h.do(http.MethodDelete, customPath+"/"+itoa(cs.ID), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("delete custom = %d %s", w.Code, w.Body)
	}
	all := adguardtest.Fixture("blocked_services_all.json")
	h.fake.SetResponse(catalogueAllPath, http.StatusOK, bytes.ReplaceAll(all, []byte(`"TikTok"`), []byte(`"TikTok Renamed"`)))
	legacy := h.seedLegacyGrant(ada.ID, "custom:7", "tiktok")

	p := h.historyOK(cookie, ada.ID)
	if got := serviceNames(findGrant(t, p, created.ID)); !reflect.DeepEqual(got, map[string]string{cs.ServiceID: "Portal", "tiktok": "TikTok"}) {
		t.Fatalf("snapshot names = %v, want Portal and TikTok as at grant start", got)
	}
	if got := serviceNames(findGrant(t, p, legacy)); !reflect.DeepEqual(got, map[string]string{"custom:7": "custom:7", "tiktok": "TikTok Renamed"}) {
		t.Fatalf("legacy names = %v, want bare custom:7 and the live tiktok name", got)
	}
}

func TestHistory_CatalogueDown(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	hits := h.fake.CountRequests("GET", catalogueAllPath)
	h.historyOK(cookie, ada.ID)
	if n := h.fake.CountRequests("GET", catalogueAllPath) - hits; n != 0 {
		t.Fatalf("all-snapshotted history read the catalogue %d times, want 0", n)
	}

	legacy := h.seedLegacyGrant(ada.ID, "youtube")
	h.fake.SetStatus(catalogueAllPath, http.StatusInternalServerError)
	p := h.historyOK(cookie, ada.ID)
	if got := serviceNames(findGrant(t, p, created.ID)); got["tiktok"] != "TikTok" {
		t.Fatalf("snapshotted name with catalogue down = %v, want TikTok", got)
	}
	if got := serviceNames(findGrant(t, p, legacy)); got["youtube"] != "youtube" {
		t.Fatalf("unsnapshotted name with catalogue down = %v, want bare youtube", got)
	}
}

func TestHistory_ExtendedAndTrimmed(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	ext := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)
	for i := 0; i < 2; i++ {
		h.clock.Advance(time.Minute)
		if w := h.do(http.MethodPost, grantPath(ext.ID, "extend"), map[string]any{"duration": 600}, withCookie(cookie)); w.Code != http.StatusOK {
			t.Fatalf("extend %d = %d %s", i, w.Code, w.Body)
		}
	}
	h.clock.Advance(time.Minute)
	if w := h.do(http.MethodPost, grantPath(ext.ID, "end"), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("end = %d %s", w.Code, w.Body)
	}

	trim, err := h.store.CreateGrant(context.Background(), ada.ID, []string{"youtube", "netflix"}, ada.Clients, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SetServiceToggle(context.Background(), ada.ID, "youtube", store.ToggleBlocked, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	p := h.historyOK(cookie, ada.ID)
	g := findGrant(t, p, ext.ID)
	if g.Status != "ended_early" || g.EndedAt == nil || len(g.Extensions) != 2 {
		t.Fatalf("extended grant = %+v, want ended_early with ended_at and two extensions", g)
	}
	if g.Extensions[0].NewEndsAt != g.Extensions[1].OldEndsAt || g.Extensions[0].At >= g.Extensions[1].At {
		t.Fatalf("extensions = %+v, want chained and ascending", g.Extensions)
	}
	tg := findGrant(t, p, trim.ID)
	var removed *historyServiceView
	for i := range tg.Services {
		if tg.Services[i].ID == "youtube" {
			removed = &tg.Services[i]
		}
	}
	if tg.Status != "ongoing" || tg.EndedAt != nil || removed == nil || removed.RemovedReason == nil ||
		*removed.RemovedReason != "permanent_block" || removed.RemovedAt == nil || *removed.RemovedAt != rfc3339(t0.Add(5*time.Minute)) {
		t.Fatalf("trimmed grant = %+v, want ongoing with youtube removed by permanent_block", tg)
	}
}

func TestHistory_Paging(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	var first int64
	for i := 0; i < 51; i++ {
		g, err := h.store.CreateGrant(context.Background(), ada.ID, []string{"svc-" + itoa(int64(i))}, ada.Clients, t0, t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = g.ID
		}
	}
	h.fake.SetStatus(catalogueAllPath, http.StatusInternalServerError) // unsnapshotted ids stand in
	p := h.historyOK(cookie, ada.ID)
	if len(p.Grants) != 50 || p.NextBefore == nil || *p.NextBefore != p.Grants[49].ID {
		t.Fatalf("page 1 = %d grants next_before %v, want 50 and the last id", len(p.Grants), p.NextBefore)
	}
	w, p2 := h.getHistory(cookie, historyPath(ada.ID)+"?before="+itoa(*p.NextBefore))
	if w.Code != http.StatusOK || len(p2.Grants) != 1 || p2.Grants[0].ID != first || p2.NextBefore != nil {
		t.Fatalf("page 2 = %d %+v, want just grant %d and next_before null", w.Code, p2, first)
	}
}

func TestHistory_Errors(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	gone := h.seedChild("Gone")
	if w := h.do(http.MethodDelete, childPath(gone.ID), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("delete child = %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/children/abc/history", http.StatusNotFound},
		{historyPath(999), http.StatusNotFound},
		{historyPath(gone.ID), http.StatusNotFound},
		{historyPath(ada.ID) + "?before=abc", http.StatusBadRequest},
		{historyPath(ada.ID) + "?before=-1", http.StatusBadRequest},
		{historyPath(ada.ID) + "?before=0", http.StatusBadRequest},
		{historyPath(ada.ID) + "?before=", http.StatusBadRequest},
		{historyPath(ada.ID) + "?before=5", http.StatusOK},
	} {
		if w, _ := h.getHistory(cookie, tc.path); w.Code != tc.want {
			t.Errorf("GET %s = %d %s, want %d", tc.path, w.Code, w.Body, tc.want)
		}
	}
	if w := h.do(http.MethodGet, historyPath(ada.ID), nil); w.Code != http.StatusUnauthorized {
		t.Errorf("GET history without a session = %d, want 401", w.Code)
	}
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is not an object", v)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestHistory_GoldenKeys(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	bare := h.seedChild("Ben")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	w := h.do(http.MethodGet, historyPath(ada.ID), nil, withCookie(cookie))
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	if got := keysOf(t, raw); !reflect.DeepEqual(got, []string{"child", "grants", "next_before"}) {
		t.Fatalf("page keys = %v", got)
	}
	if raw["next_before"] != nil {
		t.Fatalf("next_before = %v, want null", raw["next_before"])
	}
	if got := keysOf(t, raw["child"]); !reflect.DeepEqual(got, []string{"id", "name"}) {
		t.Fatalf("child keys = %v", got)
	}
	g := raw["grants"].([]any)[0]
	if got := keysOf(t, g); !reflect.DeepEqual(got, []string{"ended_at", "ends_at", "extensions", "id", "services", "started_at", "status"}) {
		t.Fatalf("grant keys = %v", got)
	}
	gm := g.(map[string]any)
	if ext, ok := gm["extensions"].([]any); !ok || len(ext) != 0 {
		t.Fatalf("extensions = %#v, want []", gm["extensions"])
	}
	if got := keysOf(t, gm["services"].([]any)[0]); !reflect.DeepEqual(got, []string{"id", "name", "removed_at", "removed_reason"}) {
		t.Fatalf("service keys = %v", got)
	}

	w = h.do(http.MethodGet, historyPath(bare.ID), nil, withCookie(cookie))
	if !bytes.Contains(w.Body.Bytes(), []byte(`"grants":[]`)) {
		t.Fatalf("empty history body = %s, want grants []", w.Body)
	}
}

func TestHistory_ChildZero(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	h.seedLegacyGrant(ada.ID, "youtube")

	if w, _ := h.getHistory(cookie, "/api/v1/children/0/history"); w.Code != http.StatusNotFound {
		t.Fatalf("GET child 0 history = %d %s, want 404 and no grants", w.Code, w.Body)
	}
}

const overviewPath = "/api/v1/history"

func (h *harness) overview(cookie, query string) (*httptest.ResponseRecorder, historyOverviewPageView) {
	h.t.Helper()
	w := h.do(http.MethodGet, overviewPath+query, nil, withCookie(cookie))
	var body historyOverviewPageView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("overview body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

func (h *harness) overviewOK(cookie, query string) historyOverviewPageView {
	h.t.Helper()
	w, body := h.overview(cookie, query)
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET overview %q = %d %s", query, w.Code, w.Body)
	}
	return body
}

func overviewIDs(p historyOverviewPageView) []int64 {
	out := []int64{}
	for _, g := range p.Grants {
		out = append(out, g.ID)
	}
	return out
}

// seedRawGrant writes a grant straight into the tables so a test can reach any
// (status, end_reason) pair; a nil reason stores NULL.
func (h *harness) seedRawGrant(childID int64, status string, reason any, started time.Time, services ...string) int64 {
	h.t.Helper()
	res, err := h.store.DB().Exec(`INSERT INTO grants (child_id, status, end_reason, started_at, ends_at) VALUES (?, ?, ?, ?, ?)`,
		childID, status, reason, started.Unix(), started.Add(time.Hour).Unix())
	if err != nil {
		h.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, s := range services {
		if _, err := h.store.DB().Exec(`INSERT INTO grant_services (grant_id, child_id, service_id, active) VALUES (?, ?, ?, 0)`,
			id, childID, s); err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

func TestHistoryOverview_GoldenKeys(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()

	w := h.do(http.MethodGet, overviewPath, nil, withCookie(cookie))
	for _, want := range []string{`"child":null`, `"grants":[]`, `"next_before":null`} {
		if !bytes.Contains(w.Body.Bytes(), []byte(want)) {
			t.Fatalf("empty overview = %d %s, want %s", w.Code, w.Body, want)
		}
	}

	ada := h.seedChild("Ada", "Kid phone")
	h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	decode := func(query string) map[string]any {
		t.Helper()
		w := h.do(http.MethodGet, overviewPath+query, nil, withCookie(cookie))
		var raw map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatalf("%d %s: %v", w.Code, w.Body, err)
		}
		return raw
	}

	raw := decode("")
	if got := keysOf(t, raw); !reflect.DeepEqual(got, []string{"child", "grants", "next_before"}) {
		t.Fatalf("page keys = %v", got)
	}
	if raw["child"] != nil {
		t.Fatalf("unfiltered page child = %v, want null", raw["child"])
	}
	g := raw["grants"].([]any)[0]
	want := []string{"child", "ended_at", "ends_at", "extensions", "id", "services", "started_at", "status"}
	if got := keysOf(t, g); !reflect.DeepEqual(got, want) {
		t.Fatalf("grant keys = %v, want %v", got, want)
	}
	child := g.(map[string]any)["child"]
	if got := keysOf(t, child); !reflect.DeepEqual(got, []string{"id", "name"}) {
		t.Fatalf("grant child keys = %v", got)
	}
	if child.(map[string]any)["name"] != "Ada" {
		t.Fatalf("grant child = %v, want Ada", child)
	}

	raw = decode("?child=" + itoa(ada.ID))
	if got := keysOf(t, raw["child"]); !reflect.DeepEqual(got, []string{"id", "name"}) {
		t.Fatalf("filtered page child keys = %v", got)
	}
	if name := raw["child"].(map[string]any)["name"]; name != "Ada" {
		t.Fatalf("filtered page child name = %v, want Ada", name)
	}
}

func TestHistoryOverview_StatusMatchesWire(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	want := map[int64]bool{}
	for _, status := range []string{store.StatusActive, store.StatusExpired, store.StatusEnded} {
		for _, reason := range []any{nil, store.EndReasonExpired, store.EndReasonEndedEarly, store.EndReasonPermanentBlock} {
			want[h.seedRawGrant(ada.ID, status, reason, t0, "youtube")] = true
		}
	}

	seen := map[int64]bool{}
	for _, status := range []string{historyOngoing, historyExpired, historyEndedEarly, historyPermanentBlock, historyEndedUnknown} {
		for _, g := range h.overviewOK(cookie, "?status="+status).Grants {
			if g.Status != status {
				t.Fatalf("?status=%s returned grant %d with status %s", status, g.ID, g.Status)
			}
			if seen[g.ID] {
				t.Fatalf("grant %d appears under two statuses", g.ID)
			}
			seen[g.ID] = true
		}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("the five statuses hold %v, want every grant %v", seen, want)
	}
}

func TestHistoryOverview_Errors(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	gone := h.seedChild("Gone")
	if w := h.do(http.MethodDelete, childPath(gone.ID), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("delete child = %d %s", w.Code, w.Body)
	}
	h.seedLegacyGrant(ada.ID, "youtube")
	const at = "2026-03-08T05:00:00Z"

	for _, query := range []string{
		"status=bogus",
		"from=notadate",
		"until=notadate",
		"until=2026-01-01",
		"before=0",
		"before=-1",
		"before=abc",
		"service=",
		"status=",
		"from=",
		"until=",
		"before=",
		"child=1&child=2",
		"service=a&service=b",
		"status=expired&status=ongoing",
		"from=" + at + "&from=" + at,
		"until=" + at + "&until=" + at,
		"before=1&before=2",
		"from=2026-03-08T05:00:00+01:00", // an unescaped + decodes to a space
		"status=%zz",
		"from=0001-01-01T00:00:00Z", // the zero time, which the store reads as unset
		"until=0001-01-01T00:00:00Z",
	} {
		if w, _ := h.overview(cookie, "?"+query); w.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d %s, want 400", query, w.Code, w.Body)
		}
	}
	for _, child := range []string{"abc", "", "0", "-1", "999", itoa(gone.ID)} {
		if w, _ := h.overview(cookie, "?child="+child); w.Code != http.StatusNotFound {
			t.Errorf("?child=%s = %d %s, want 404", child, w.Code, w.Body)
		}
	}
	if w := h.do(http.MethodGet, overviewPath, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("overview without a session = %d, want 401", w.Code)
	}

	w, p := h.overview(cookie, "?from=2026-03-09T00:00:00Z&until=2026-03-08T00:00:00Z")
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"grants":[]`)) || len(p.Grants) != 0 {
		t.Errorf("from after until = %d %s, want 200 with grants []", w.Code, w.Body)
	}
}

func TestHistoryOverview_FilteredPaging(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	want := []int64{}
	for i := 0; i < 60; i++ {
		want = append([]int64{h.seedRawGrant(ada.ID, store.StatusExpired, nil, t0, "youtube")}, want...)
		h.seedRawGrant(ada.ID, store.StatusExpired, nil, t0, "tiktok")
	}

	p1 := h.overviewOK(cookie, "?service=youtube")
	if len(p1.Grants) != 50 || p1.NextBefore == nil || *p1.NextBefore != p1.Grants[49].ID {
		t.Fatalf("page 1 = %d grants next_before %v, want 50 and the last id", len(p1.Grants), p1.NextBefore)
	}
	p2 := h.overviewOK(cookie, "?service=youtube&before="+itoa(*p1.NextBefore))
	if len(p2.Grants) != 10 || p2.NextBefore != nil {
		t.Fatalf("page 2 = %d grants next_before %v, want 10 and null", len(p2.Grants), p2.NextBefore)
	}
	if got := append(overviewIDs(p1), overviewIDs(p2)...); !reflect.DeepEqual(got, want) {
		t.Fatalf("walk = %v, want every match once, newest first: %v", got, want)
	}
}

func TestHistoryOverview_ClientURL(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	h.seedChild("Ada", "Kid phone")
	other := h.seedChild("Ben", "Ben phone")
	cleo := h.seedChild("Cleo", "Cleo phone")
	if cleo.ID != 3 {
		t.Fatalf("child id = %d, want 3 — the literal URL names it", cleo.ID)
	}
	in := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	match := h.seedRawGrant(cleo.ID, store.StatusExpired, nil, in, "custom:5")

	// One near miss per filter, then filler up to the cursor in the URL.
	h.seedRawGrant(other.ID, store.StatusExpired, nil, in, "custom:5")
	h.seedRawGrant(cleo.ID, store.StatusExpired, nil, in, "custom:6")
	h.seedRawGrant(cleo.ID, store.StatusActive, nil, in, "custom:5")
	h.seedRawGrant(cleo.ID, store.StatusExpired, nil, in.Add(-24*time.Hour), "custom:5")
	for id := int64(0); id < 39; {
		id = h.seedRawGrant(cleo.ID, store.StatusExpired, nil, in.Add(24*time.Hour), "custom:5")
	}
	if late := h.seedRawGrant(cleo.ID, store.StatusExpired, nil, in, "custom:5"); late != 40 {
		t.Fatalf("cursor grant id = %d, want 40 — the literal URL names it", late)
	}

	p := h.overviewOK(cookie, "?child=3&service=custom%3A5&status=expired&from=2026-03-08T05%3A00%3A00.000Z&until=2026-03-09T04%3A00%3A00.000Z&before=40")
	if got := overviewIDs(p); !reflect.DeepEqual(got, []int64{match}) {
		t.Fatalf("client URL = %v, want only [%d]", got, match)
	}
}

func TestHistoryOverview_EndToEnd(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	old := h.seedChild("Old", "Old phone") // the highest id, so SQLite reuses it

	trimmed := h.grantOK(cookie, ada.ID, []string{"youtube", "tiktok"}, 1800)
	h.toggleOK(cookie, ada.ID, "youtube", store.ToggleBlocked)

	p := h.overviewOK(cookie, "?service=youtube")
	if got := overviewIDs(p); !reflect.DeepEqual(got, []int64{trimmed.ID}) {
		t.Fatalf("service=youtube = %v, want the trimmed grant [%d]", got, trimmed.ID)
	}
	var removed *historyServiceView
	for i := range p.Grants[0].Services {
		if p.Grants[0].Services[i].ID == "youtube" {
			removed = &p.Grants[0].Services[i]
		}
	}
	if removed == nil || removed.RemovedReason == nil || *removed.RemovedReason != store.EndReasonPermanentBlock {
		t.Fatalf("services = %+v, want youtube marked removed by permanent_block", p.Grants[0].Services)
	}
	if p.Grants[0].Child != (historyChildView{ID: ada.ID, Name: "Ada"}) {
		t.Fatalf("grant child = %+v, want Ada", p.Grants[0].Child)
	}

	oldGrant := h.seedRawGrant(old.ID, store.StatusExpired, nil, t0, "netflix")
	if w := h.do(http.MethodDelete, childPath(old.ID), nil, withCookie(cookie)); w.Code/100 != 2 {
		t.Fatalf("delete child = %d %s", w.Code, w.Body)
	}
	if got := overviewIDs(h.overviewOK(cookie, "")); !reflect.DeepEqual(got, []int64{trimmed.ID}) {
		t.Fatalf("after delete = %v, want no grant %d", got, oldGrant)
	}

	cleo := h.seedChild("Cleo", "Cleo phone")
	if cleo.ID != old.ID {
		t.Fatalf("new child id = %d, want reused %d — the test needs the reuse", cleo.ID, old.ID)
	}
	own := h.seedRawGrant(cleo.ID, store.StatusExpired, nil, t0, "disney")

	if got := overviewIDs(h.overviewOK(cookie, "")); !reflect.DeepEqual(got, []int64{own, trimmed.ID}) {
		t.Fatalf("overview = %v, want [%d %d] without the old child's %d", got, own, trimmed.ID, oldGrant)
	}
	p = h.overviewOK(cookie, "?child="+itoa(cleo.ID))
	if got := overviewIDs(p); !reflect.DeepEqual(got, []int64{own}) || p.Child == nil || p.Child.Name != "Cleo" {
		t.Fatalf("filtered = %v child %+v, want only [%d] on Cleo's page", got, p.Child, own)
	}
}

func TestHistoryOverview_CatalogueDown(t *testing.T) {
	h := newCustomHarness(t)
	cookie := h.loginOK()
	ada := h.seedChild("Ada", "Kid phone")
	created := h.grantOK(cookie, ada.ID, []string{"tiktok"}, 1800)

	hits := h.fake.CountRequests("GET", catalogueAllPath)
	h.overviewOK(cookie, "")
	if n := h.fake.CountRequests("GET", catalogueAllPath) - hits; n != 0 {
		t.Fatalf("all-snapshotted overview read the catalogue %d times, want 0", n)
	}

	legacy := h.seedLegacyGrant(ada.ID, "youtube")
	h.fake.SetStatus(catalogueAllPath, http.StatusInternalServerError)
	p := h.overviewOK(cookie, "")
	if got := overviewIDs(p); !reflect.DeepEqual(got, []int64{legacy, created.ID}) {
		t.Fatalf("overview = %v, want [%d %d]", got, legacy, created.ID)
	}
	if got := serviceNames(p.Grants[1].historyGrantView); got["tiktok"] != "TikTok" {
		t.Fatalf("snapshotted name with catalogue down = %v, want TikTok", got)
	}
	if got := serviceNames(p.Grants[0].historyGrantView); got["youtube"] != "youtube" {
		t.Fatalf("unsnapshotted name with catalogue down = %v, want bare youtube", got)
	}
}
