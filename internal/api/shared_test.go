package api

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/Rivil/adguard-reward/internal/blocked"
)

// sortedBlocked is the fake's blocked list for name, sorted.
func (h *harness) sortedBlocked(name string) []string {
	h.t.Helper()
	got := slices.Clone(h.blocked(name))
	slices.Sort(got)
	return got
}

func (h *harness) endGrant(cookie string, id int64) {
	h.t.Helper()
	if w := h.do(http.MethodPost, grantPath(id, "end"), nil, withCookie(cookie)); w.Code != http.StatusNoContent {
		h.t.Fatalf("end grant %d: %d %s, want 204", id, w.Code, w.Body.String())
	}
}

// TestShared_Lifecycle walks one shared device through every layer over
// HTTP — real store, engine and fake AdGuard: share by PUT, two owners'
// grants for the same service, the blocked-view attribution, and the
// ref-counted revert as each grant ends.
func TestShared_Lifecycle(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	ada := h.createOK(cookie, "Ada", "Kid phone")
	ben := h.createOK(cookie, "Ben")

	// 1. Ben shares Kid phone: no ownership 409.
	if w := h.putChild(cookie, ben.ID, "Ben", "Kid phone"); w.Code != http.StatusOK {
		t.Fatalf("PUT Ben + Kid phone: %d %s, want 200", w.Code, w.Body.String())
	}
	if !slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Fatalf("precondition: Kid phone = %v, want youtube blocked", h.blocked("Kid phone"))
	}

	// 2. Ada's grant names Ben as the other owner and opens youtube.
	ga := decodeShared(t, h.postGrant(cookie, grantBody(ada.ID, []string{"youtube"}, 1800)))
	if want := []sharedRef{{Client: "Kid phone", Others: []childRef{{ben.ID, "Ben"}}}}; !reflect.DeepEqual(ga.Shared, want) {
		t.Fatalf("Ada's 201 shared = %+v, want %+v", ga.Shared, want)
	}
	if slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Fatalf("after Ada's grant Kid phone = %v, want youtube open", h.blocked("Kid phone"))
	}

	// 3. Ben's grant for the same service is not an overlap.
	gb := decodeShared(t, h.postGrant(cookie, grantBody(ben.ID, []string{"youtube"}, 1800)))
	if want := []sharedRef{{Client: "Kid phone", Others: []childRef{{ada.ID, "Ada"}}}}; !reflect.DeepEqual(gb.Shared, want) {
		t.Fatalf("Ben's 201 shared = %+v, want %+v", gb.Shared, want)
	}

	// 4. Ben's blocked view attributes youtube on Kid phone to Ada's grant
	//    (his own grant is not named).
	w, view := h.getBlocked(cookie, ben.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("Ben's blocked view: %d %s", w.Code, w.Body.String())
	}
	yt := serviceRow(view, "youtube")
	wantBy := []blocked.Unblock{{Client: "Kid phone", GrantID: ga.ID, Child: blocked.ChildRef{ID: ada.ID, Name: "Ada"}}}
	if yt == nil || !reflect.DeepEqual(yt.UnblockedBy, wantBy) {
		t.Fatalf("Ben's youtube = %+v, want unblocked_by %+v", yt, wantBy)
	}

	// 5. Ending Ada's grant leaves youtube open: Ben's grant still covers it.
	h.endGrant(cookie, ga.ID)
	if slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Fatalf("after End Ada Kid phone = %v, want youtube still open for Ben", h.blocked("Kid phone"))
	}

	// 6. Ending Ben's grant — the last cover — re-blocks it.
	h.endGrant(cookie, gb.ID)
	if !slices.Contains(h.blocked("Kid phone"), "youtube") {
		t.Errorf("after End Ben Kid phone = %v, want youtube re-blocked", h.blocked("Kid phone"))
	}
}

// TestShared_ShareDuringGrant shares a device another child's active grant
// holds open: the adopting PUT applies Ben's baseline but must not re-block
// the covered service until that grant ends.
func TestShared_ShareDuringGrant(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	// Seeded through the store so Kid phone keeps its own [youtube tiktok]
	// (POST /children would adopt it into Ada's baseline first).
	ada := h.seedChild("Ada", "Kid phone")
	ga := h.grantOK(cookie, ada.ID, []string{"youtube"}, 1800)
	if got := h.sortedBlocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Fatalf("precondition: Kid phone = %v under Ada's grant, want [tiktok]", got)
	}
	ben := h.createOK(cookie, "Ben", "Kid tablet")
	ownList(h, "Kid tablet", "youtube", "roblox")

	if w := h.putChild(cookie, ben.ID, "Ben", "Kid tablet", "Kid phone"); w.Code != http.StatusOK {
		t.Fatalf("PUT Ben + Kid phone: %d %s, want 200", w.Code, w.Body.String())
	}
	if got := h.sortedBlocked("Kid phone"); !reflect.DeepEqual(got, []string{"roblox", "tiktok"}) {
		t.Fatalf("Kid phone after the share = %v, want exactly [roblox tiktok] while Ada's grant holds youtube", got)
	}

	h.endGrant(cookie, ga.ID)
	if got := h.sortedBlocked("Kid phone"); !slices.Contains(got, "youtube") {
		t.Errorf("Kid phone after End Ada = %v, want youtube re-blocked", got)
	}
}
