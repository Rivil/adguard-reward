package blocked

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
)

// fixtureClients decodes the embedded clients.json into the exported
// PersistentClient fields: Kid phone own [youtube tiktok]; Kid tablet on the
// global list; Old laptop with blocked_services null.
func fixtureClients(t *testing.T) []adguard.PersistentClient {
	t.Helper()
	var doc struct {
		Clients []struct {
			Name                     string   `json:"name"`
			IDs                      []string `json:"ids"`
			BlockedServices          []string `json:"blocked_services"`
			UseGlobalBlockedServices bool     `json:"use_global_blocked_services"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(adguardtest.Fixture("clients.json"), &doc); err != nil {
		t.Fatal(err)
	}
	out := make([]adguard.PersistentClient, 0, len(doc.Clients))
	for _, c := range doc.Clients {
		out = append(out, adguard.PersistentClient{
			Name: c.Name, IDs: c.IDs, BlockedServices: c.BlockedServices,
			UseGlobalBlockedServices: c.UseGlobalBlockedServices,
		})
	}
	return out
}

func fixtureServices(t *testing.T) []adguard.Service {
	t.Helper()
	var doc struct {
		BlockedServices []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Icon string `json:"icon_svg"`
		} `json:"blocked_services"`
	}
	if err := json.Unmarshal(adguardtest.Fixture("blocked_services_all.json"), &doc); err != nil {
		t.Fatal(err)
	}
	out := []adguard.Service{}
	for _, s := range doc.BlockedServices {
		out = append(out, adguard.Service{ID: s.ID, Name: s.Name, Icon: s.Icon})
	}
	return out
}

var fixtureGlobal = []string{"tiktok", "roblox"}

func fixtureInput(t *testing.T, mapped ...string) Input {
	t.Helper()
	return Input{
		Clients:  fixtureClients(t),
		Global:   fixtureGlobal,
		Services: fixtureServices(t),
		Mapped:   mapped,
	}
}

func client(name string, global bool, own ...string) adguard.PersistentClient {
	return adguard.PersistentClient{Name: name, BlockedServices: own, UseGlobalBlockedServices: global}
}

// states summarises a view as id → state and id → differs.
func states(v View) (map[string]string, map[string][]string) {
	st := map[string]string{}
	df := map[string][]string{}
	for _, s := range v.Services {
		st[s.ID] = s.State
		df[s.ID] = s.Differs
	}
	return st, df
}

func serviceIDs(v View) []string {
	out := []string{}
	for _, s := range v.Services {
		out = append(out, s.ID)
	}
	return out
}

func TestCompute_Fixture(t *testing.T) {
	v := Compute(fixtureInput(t, "Kid phone", "Kid tablet"))

	wantClients := []ClientState{{Name: "Kid phone"}, {Name: "Kid tablet", UsesGlobal: true}}
	if !reflect.DeepEqual(v.Clients, wantClients) {
		t.Errorf("Clients = %+v, want %+v", v.Clients, wantClients)
	}
	if got := serviceIDs(v); !reflect.DeepEqual(got, []string{"youtube", "tiktok", "roblox"}) {
		t.Errorf("service order = %v, want catalogue order", got)
	}
	want := []ServiceState{
		{ID: "youtube", Name: "YouTube", Icon: "PHN2Zy8+", State: StatePartial, Differs: []string{"Kid tablet"}},
		{ID: "tiktok", Name: "TikTok", Icon: "PHN2Zy8+", State: StateBlocked, Differs: []string{}},
		{ID: "roblox", Name: "Roblox", Icon: "PHN2Zy8+", State: StatePartial, Differs: []string{"Kid phone"}},
	}
	if !reflect.DeepEqual(v.Services, want) {
		t.Errorf("Services = %+v\nwant %+v", v.Services, want)
	}
}

func TestCompute_UsesGlobal(t *testing.T) {
	st, _ := states(Compute(fixtureInput(t, "Kid tablet")))
	if st["tiktok"] != StateBlocked || st["roblox"] != StateBlocked || st["youtube"] != StateUnblocked {
		t.Errorf("Kid tablet on global [tiktok roblox]: states = %v", st)
	}

	in := fixtureInput(t, "Kid tablet")
	in.Global = []string{}
	st, _ = states(Compute(in))
	for id, s := range st {
		if s != StateUnblocked {
			t.Errorf("Kid tablet on empty global list: %s = %s, want unblocked", id, s)
		}
	}
}

func TestCompute_MissingExcluded(t *testing.T) {
	v := Compute(fixtureInput(t, "Kid phone", "Ghost"))
	st, df := states(v)
	if st["youtube"] != StateBlocked || st["tiktok"] != StateBlocked {
		t.Errorf("a missing client must not make Kid phone's services partial: %v", st)
	}
	if st["roblox"] != StateUnblocked {
		t.Errorf("roblox = %s, want unblocked", st["roblox"])
	}
	wantClients := []ClientState{{Name: "Kid phone"}, {Name: "Ghost", Missing: true}}
	if !reflect.DeepEqual(v.Clients, wantClients) {
		t.Errorf("Clients = %+v, want %+v", v.Clients, wantClients)
	}
	for id, d := range df {
		for _, n := range d {
			if n == "Ghost" {
				t.Errorf("%s Differs names the missing client Ghost", id)
			}
		}
	}
}

func TestCompute_NoPresentClients(t *testing.T) {
	for _, mapped := range [][]string{{}, {"Ghost"}} {
		v := Compute(fixtureInput(t, mapped...))
		if len(v.Services) != 3 {
			t.Fatalf("Mapped %v: %d services, want 3", mapped, len(v.Services))
		}
		for _, s := range v.Services {
			if s.State != StateUnblocked {
				t.Errorf("Mapped %v: %s = %s, want unblocked (no vacuous truth)", mapped, s.ID, s.State)
			}
			if s.Differs == nil || len(s.Differs) != 0 {
				t.Errorf("Mapped %v: %s Differs = %#v, want []string{}", mapped, s.ID, s.Differs)
			}
		}
	}
	v := Compute(fixtureInput(t))
	if v.Clients == nil {
		t.Error("Clients is nil for an empty mapping, want []")
	}
}

func TestCompute_NullList(t *testing.T) {
	in := fixtureInput(t, "Old laptop")
	if in.Clients[2].Name != "Old laptop" || in.Clients[2].BlockedServices != nil {
		t.Fatalf("fixture drift: Old laptop should decode with a nil blocked_services, got %+v", in.Clients[2])
	}
	st, _ := states(Compute(in))
	for id, s := range st {
		if s != StateUnblocked {
			t.Errorf("%s = %s, want unblocked for a null list", id, s)
		}
	}
}

func TestCompute_Order(t *testing.T) {
	services := fixtureServices(t)
	clients := []adguard.PersistentClient{
		client("Zed", false, "roblox", "youtube"),
		client("Amy", false, "youtube"),
		client("Mia", false),
	}
	for _, mapped := range [][]string{{"Zed", "Mia", "Amy"}, {"Amy", "Zed", "Mia"}} {
		v := Compute(Input{Clients: clients, Services: services, Mapped: mapped})
		if got := serviceIDs(v); !reflect.DeepEqual(got, []string{"youtube", "tiktok", "roblox"}) {
			t.Errorf("Mapped %v: order = %v, want catalogue order", mapped, got)
		}
		_, df := states(v)
		if !reflect.DeepEqual(df["youtube"], []string{"Mia"}) {
			t.Errorf("Mapped %v: youtube Differs = %v, want [Mia]", mapped, df["youtube"])
		}
		if !reflect.DeepEqual(df["roblox"], []string{"Amy", "Mia"}) {
			t.Errorf("Mapped %v: roblox Differs = %v, want [Amy Mia] sorted", mapped, df["roblox"])
		}
	}
}

func TestCompute_UnknownID(t *testing.T) {
	v := Compute(Input{
		Clients:  []adguard.PersistentClient{client("Kid phone", false, "zzz-new", "youtube")},
		Services: fixtureServices(t),
		Mapped:   []string{"Kid phone"},
	})
	if got := serviceIDs(v); !reflect.DeepEqual(got, []string{"youtube", "tiktok", "roblox"}) {
		t.Errorf("services = %v, want exactly the catalogue", got)
	}
}

func TestOffer_Scope(t *testing.T) {
	clients := fixtureClients(t)
	steps := Offer(clients, fixtureGlobal, []string{"Kid tablet", "Kid phone", "Ghost"})
	want := []Step{{Client: "Kid tablet", Gains: []string{"roblox", "tiktok"}, Target: []string{"roblox", "tiktok"}}}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("steps = %+v, want %+v", steps, want)
	}

	if got := Offer(clients, []string{}, []string{"Kid tablet"}); got == nil || len(got) != 0 {
		t.Errorf("empty global list: steps = %#v, want []", got)
	}
	if got := Offer(clients, fixtureGlobal, []string{"Kid phone"}); len(got) != 0 {
		t.Errorf("Kid tablet unmapped: steps = %+v, want none", got)
	}
	if got := Offer(clients, fixtureGlobal, nil); got == nil || len(got) != 0 {
		t.Errorf("nil mapping: steps = %#v, want []", got)
	}
}

func TestOffer_Union(t *testing.T) {
	clients := []adguard.PersistentClient{
		client("Tab", true, "youtube"),
		client("Full", true, "tiktok", "roblox"),
	}
	steps := Offer(clients, []string{"tiktok"}, []string{"Tab"})
	want := []Step{{Client: "Tab", Gains: []string{"tiktok"}, Target: []string{"tiktok", "youtube"}}}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("Tab: steps = %+v, want %+v", steps, want)
	}

	steps = Offer(clients, []string{"tiktok", "roblox"}, []string{"Full"})
	want = []Step{{Client: "Full", Gains: []string{}, Target: []string{"roblox", "tiktok"}}}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("Full: steps = %+v, want %+v (a step with nothing to gain still flips the flag)", steps, want)
	}
}
