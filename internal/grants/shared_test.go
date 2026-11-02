package grants

import (
	"reflect"
	"testing"

	"github.com/Rivil/adguard-reward/internal/store"
)

func grantOn(id int64, services []string, clients ...string) store.Grant {
	return store.Grant{ID: id, Status: "active", Services: services, Clients: clients}
}

func TestRevertPlan_SelfNotCounted(t *testing.T) {
	g := grantOn(1, []string{"youtube", "tiktok"}, "Kid phone", "Kid tablet")
	got := revertPlan(g, []store.Grant{g})
	want := map[string][]string{
		"Kid phone":  {"youtube", "tiktok"},
		"Kid tablet": {"youtube", "tiktok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertPlan = %v, want %v", got, want)
	}
}

func TestRevertPlan_OtherClient(t *testing.T) {
	g := grantOn(1, []string{"youtube"}, "Kid phone")
	ben := grantOn(2, []string{"youtube"}, "Kid tablet")
	got := revertPlan(g, []store.Grant{g, ben})
	want := map[string][]string{"Kid phone": {"youtube"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertPlan = %v, want %v (a grant on Kid tablet must not hold Kid phone open)", got, want)
	}
}

func TestRevertPlan_OtherService(t *testing.T) {
	g1 := grantOn(1, []string{"youtube", "tiktok"}, "Kid phone")
	g2 := grantOn(2, []string{"youtube"}, "Kid phone")
	got := revertPlan(g1, []store.Grant{g1, g2})
	want := map[string][]string{"Kid phone": {"tiktok"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertPlan = %v, want %v", got, want)
	}
}

func TestRevertPlan_FullyHeld(t *testing.T) {
	g1 := grantOn(1, []string{"youtube"}, "Kid phone", "Old laptop")
	g2 := grantOn(2, []string{"youtube", "tiktok"}, "Kid phone")
	got := revertPlan(g1, []store.Grant{g1, g2})
	if _, ok := got["Kid phone"]; ok {
		t.Fatalf("revertPlan = %v: Kid phone is fully held open by g2 and must be absent", got)
	}
	want := map[string][]string{"Old laptop": {"youtube"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertPlan = %v, want %v", got, want)
	}
}

// The plan takes g.Clients verbatim: there is no ownership input, so a
// client no child owns any more still gets its re-block.
func TestRevertPlan_StoredClients(t *testing.T) {
	g := grantOn(1, []string{"youtube"}, "Kid phone", "Orphan tv")
	got := revertPlan(g, nil)
	want := map[string][]string{
		"Kid phone": {"youtube"},
		"Orphan tv": {"youtube"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertPlan = %v, want %v", got, want)
	}
}

func TestHeldOpen(t *testing.T) {
	active := []store.Grant{
		grantOn(1, []string{"youtube"}, "Kid phone"),
		grantOn(2, []string{"tiktok", "roblox"}, "Kid phone", "Kid tablet"),
		grantOn(3, []string{"netflix"}, "Kid tablet"),
		grantOn(4, []string{"youtube"}, "Kid phone"),
	}
	cases := []struct {
		name    string
		exclude int64
		client  string
		want    []string
	}{
		{"all grants on Kid phone, sorted and deduped", 0, "Kid phone", []string{"roblox", "tiktok", "youtube"}},
		{"exclude drops that grant only", 2, "Kid phone", []string{"youtube"}},
		{"no union across clients", 0, "Kid tablet", []string{"netflix", "roblox", "tiktok"}},
		{"exclude the sole holder", 3, "Kid tablet", []string{"roblox", "tiktok"}},
		{"unknown client is empty", 0, "Old laptop", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := heldOpen(active, c.exclude, c.client)
			if got == nil {
				t.Fatal("heldOpen returned nil, want a non-nil slice")
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("heldOpen(%d, %q) = %v, want %v", c.exclude, c.client, got, c.want)
			}
		})
	}
	if got := heldOpen(nil, 0, "Kid phone"); got == nil || len(got) != 0 {
		t.Fatalf("heldOpen(nil) = %#v, want []string{}", got)
	}
}
