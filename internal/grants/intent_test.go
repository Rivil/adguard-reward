package grants

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Rivil/adguard-reward/internal/store"
)

func unblock(child int64, svc string) toggle {
	return toggle{childID: child, serviceID: svc, state: store.ToggleUnblocked}
}

func block(child int64, svc string) toggle {
	return toggle{childID: child, serviceID: svc, state: store.ToggleBlocked}
}

// sharedTV is A (a-phone, tv), B (tv) and C (tv).
func sharedTV() []store.Child {
	return []store.Child{
		{ID: 1, Name: "A", Clients: []string{"a-phone", "tv"}},
		{ID: 2, Name: "B", Clients: []string{"tv"}},
		{ID: 3, Name: "C", Clients: []string{"tv"}},
	}
}

func TestPermanentOpen(t *testing.T) {
	toggles := []toggle{
		unblock(2, "youtube"), unblock(1, "tiktok"), block(1, "netflix"),
		unblock(9, "hulu"), // a child owning nothing opens nothing
		unblock(3, "youtube"),
	}
	got := permanentOpen(sharedTV(), toggles)
	want := map[string][]string{
		"a-phone": {"tiktok"},
		"tv":      {"tiktok", "youtube"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permanentOpen = %v, want %v", got, want)
	}
}

func TestTogglePlan_OpenWins(t *testing.T) {
	got := togglePlan(sharedTV(), []toggle{block(1, "youtube"), unblock(2, "youtube")}, nil)
	tv := got["tv"]
	if !slices.Contains(tv.remove, "youtube") || slices.Contains(tv.add, "youtube") {
		t.Fatalf("tv = %+v, want youtube removed and not added", tv)
	}
	if phone := got["a-phone"]; !slices.Contains(phone.add, "youtube") || slices.Contains(phone.remove, "youtube") {
		t.Fatalf("a-phone = %+v, want youtube added", phone)
	}
}

func TestTogglePlan_GrantHolds(t *testing.T) {
	cGrant := grantOn(7, []string{"youtube"}, "tv")
	got := togglePlan(sharedTV(), []toggle{block(1, "youtube")}, []store.Grant{cGrant})
	if slices.Contains(got["tv"].add, "youtube") {
		t.Fatalf("tv = %+v, want youtube not added under C's live grant", got["tv"])
	}
	if !slices.Contains(got["a-phone"].add, "youtube") {
		t.Fatalf("a-phone = %+v, want youtube added", got["a-phone"])
	}
}

func TestTogglePlan_BuiltinOnly(t *testing.T) {
	toggles := []toggle{unblock(1, "custom:1"), block(2, "custom:2"), block(1, "youtube")}
	got := togglePlan(sharedTV(), toggles, nil)
	for client, p := range got {
		for _, s := range append(slices.Clone(p.remove), p.add...) {
			if s != "youtube" {
				t.Fatalf("%s plan %+v carries %q", client, p, s)
			}
		}
	}
	if got := togglePlan(sharedTV(), toggles[:2], nil); len(got) != 0 {
		t.Fatalf("custom-only toggles plan = %+v, want empty", got)
	}
}

func TestTogglePlan_AdGuardOwned(t *testing.T) {
	active := []store.Grant{grantOn(1, []string{"youtube"}, "tv")}
	if got := togglePlan(sharedTV(), nil, active); len(got) != 0 {
		t.Fatalf("plan with no toggles = %+v, want empty", got)
	}
}

func TestTogglePlan_Sorted(t *testing.T) {
	toggles := []toggle{
		unblock(1, "youtube"), unblock(1, "netflix"),
		block(1, "tiktok"), block(1, "hulu"),
	}
	got := togglePlan(sharedTV(), toggles, nil)
	want := clientPlan{remove: []string{"netflix", "youtube"}, add: []string{"hulu", "tiktok"}}
	for _, client := range []string{"a-phone", "tv"} {
		if !reflect.DeepEqual(got[client], want) {
			t.Fatalf("%s = %+v, want %+v", client, got[client], want)
		}
	}
}
