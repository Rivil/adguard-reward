package grants

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

var tvIDs = []string{"192.168.1.30"}

func ptr(id int64) *int64 { return &id }

func (e *env) child(name string, clients ...string) store.Child {
	e.t.Helper()
	c, err := e.st.CreateChild(ctx, name, clients)
	if err != nil {
		e.t.Fatalf("CreateChild(%s): %v", name, err)
	}
	return c
}

// usesGlobal reads the fake's use_global_blocked_services for name.
func (e *env) usesGlobal(name string) bool {
	e.t.Helper()
	ag, err := adguard.New(e.fake.URL(), svcUser, svcPass)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := ag.Clients(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, pc := range res.Persistent {
		if pc.Name == name {
			return pc.UseGlobalBlockedServices
		}
	}
	e.t.Fatalf("fake has no client %q", name)
	return false
}

func (e *env) adds() int { return e.fake.CountRequests("POST", "/control/clients/add") }

// ownList sets name's own blocked list on the fake and takes it off the
// global list.
func (e *env) ownList(name string, ids ...string) {
	list, _ := json.Marshal(ids)
	e.fake.MutateClient(name, func(m map[string]json.RawMessage) {
		m["use_global_blocked_services"] = json.RawMessage("false")
		m["blocked_services"] = list
	})
}

func (e *env) createClient(name string, child *int64) {
	e.t.Helper()
	if err := e.eng.CreateClient(ctx, name, tvIDs, child); err != nil {
		e.t.Fatalf("CreateClient(%s): %v", name, err)
	}
}

func (e *env) wantOwn(name string, want ...string) {
	e.t.Helper()
	if got := e.blocked(name); !reflect.DeepEqual(got, want) {
		e.t.Errorf("%s blocks %v, want %v", name, got, want)
	}
	if e.usesGlobal(name) {
		e.t.Errorf("%s use_global_blocked_services = true, want false", name)
	}
}

func TestCreateClient_Baseline(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Fatalf("setup: Kid phone = %v, want [tiktok]", got)
	}

	e.createClient("TV", ptr(ada.ID))
	e.wantOwn("TV", "tiktok", "youtube")
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone", "TV"}) {
		t.Errorf("Ada clients = %v, want [Kid phone TV]", got)
	}
}

func mustChild(t *testing.T, e *env, id int64) store.Child {
	t.Helper()
	c, err := e.st.GetChild(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateClient_SurvivesExpiry(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	res := e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	e.createClient("TV", ptr(ada.ID))

	e.clk.Advance(d + time.Second)

	if e.status(res.Grant.ID) != store.StatusExpired {
		t.Fatalf("grant status = %s, want expired", e.status(res.Grant.ID))
	}
	e.wantOwn("TV", "tiktok", "youtube")
	if slices.Contains(e.updatesFor(), "TV") {
		t.Errorf("an update POST named TV: %v", e.updatesFor())
	}
	g, _ := e.st.GetGrant(ctx, res.Grant.ID)
	if !reflect.DeepEqual(g.Clients, []string{"Kid phone"}) {
		t.Errorf("grant clients = %v, want [Kid phone] unchanged", g.Clients)
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("Kid phone after expiry = %v", got)
	}
}

func TestCreateClient_OtherChildGrant(t *testing.T) {
	e := newEnv(t, nil)
	e.ownList("Kid phone", "youtube")
	e.ownList("Kid tablet", "tiktok")
	ada := e.child("Ada", "Kid phone")
	ben := e.child("Ben", "Kid tablet")
	e.create(ben.ID, []string{"tiktok"}, []string{"Kid tablet"})

	e.createClient("TV", ptr(ada.ID))
	e.wantOwn("TV", "youtube")
}

func TestBaseline_GlobalSibling(t *testing.T) {
	e := newEnv(t, nil)
	e.ownList("Kid phone", "steam")
	ada := e.child("Ada", "Kid tablet", "Kid phone")
	e.createClient("TV", ptr(ada.ID))
	e.wantOwn("TV", "roblox", "steam", "tiktok")
}

func TestCreateClient_FirstDevice(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada")
	e.createClient("TV", ptr(ada.ID))
	e.wantOwn("TV", "roblox", "tiktok")

	// A stale sibling AdGuard does not know counts as no sibling.
	ben := e.child("Ben", "Ghost")
	if err := e.eng.CreateClient(ctx, "Den", []string{"192.168.1.31"}, ptr(ben.ID)); err != nil {
		t.Fatal(err)
	}
	e.wantOwn("Den", "roblox", "tiktok")
}

func TestCreateClient_NoChild(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ada", "Kid phone")
	e.createClient("TV", nil)
	if !e.usesGlobal("TV") {
		t.Error("unassigned TV use_global_blocked_services = false, want true")
	}
	all, _ := e.st.ListChildren(ctx)
	for _, c := range all {
		if slices.Contains(c.Clients, "TV") {
			t.Errorf("child %s maps the unassigned TV", c.Name)
		}
	}
	if err := e.eng.CreateClient(ctx, "Den", []string{"192.168.1.31"}, ptr(999)); !errors.Is(err, store.ErrChildNotFound) {
		t.Errorf("unknown child: err = %v, want ErrChildNotFound", err)
	}
	if n := e.adds(); n != 1 {
		t.Errorf("adds = %d, want 1 (unknown child adds nothing)", n)
	}
}

func TestAdoptClients_Inherited(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	if err := e.eng.AdoptClients(ctx, ada.ID, []string{"Kid phone"}, []string{"Kid tablet"}); err != nil {
		t.Fatal(err)
	}
	e.wantOwn("Kid tablet", "roblox", "tiktok", "youtube")
}

func TestAdoptClients_GrantServices(t *testing.T) {
	e := newEnv(t, nil)
	e.ownList("Old laptop", "steam")
	ada := e.child("Ada", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	if err := e.eng.AdoptClients(ctx, ada.ID, []string{"Kid phone"}, []string{"Old laptop"}); err != nil {
		t.Fatal(err)
	}
	e.wantOwn("Old laptop", "steam", "tiktok", "youtube")
}

func TestAdoptClients_Ghost(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.eng.AdoptClients(ctx, 1, []string{"Kid phone"}, []string{"Ghost"}); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if n := e.posts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
}

func TestAdoptClients_Empty(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.eng.AdoptClients(ctx, 1, []string{"Kid phone"}, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(e.fake.Requests()); n != 0 {
		t.Errorf("AdGuard requests = %d, want 0", n)
	}
}

func TestAdoptClients_Fails(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.SetUpdateStatus("Kid tablet", 500)
	err := e.eng.AdoptClients(ctx, 1, nil, []string{"Kid tablet"})
	var af *ErrAdoptFailed
	if !errors.As(err, &af) || af.Client != "Kid tablet" {
		t.Errorf("err = %v, want *ErrAdoptFailed naming Kid tablet", err)
	}
}

func TestAdoptClients_ClientsReadFails(t *testing.T) {
	e := newEnv(t, nil)
	// A 200 cut short: no status or transport error to know the failure by.
	e.fake.SetResponse("/control/clients", 200, []byte(`{"clients":[`))
	err := e.eng.AdoptClients(ctx, 1, nil, []string{"Kid tablet"})
	var read *ErrClientsRead
	if !errors.As(err, &read) {
		t.Fatalf("err = %v, want *ErrClientsRead", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want it to reach the decode failure", err)
	}
	if n := e.posts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
}

// TestCreateClient_Serialised holds the engine mutex itself: a CreateClient
// that takes e.mu reaches AdGuard only after the Unlock, so no grant can
// apply or revert between its baseline read and its add.
func TestCreateClient_Serialised(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})

	e.eng.mu.Lock()
	before := len(e.fake.Requests())
	done := make(chan error, 1)
	go func() { done <- e.eng.CreateClient(ctx, "TV", tvIDs, ptr(ada.ID)) }()
	select {
	case err := <-done:
		e.eng.mu.Unlock()
		t.Fatalf("CreateClient returned (err %v) while the test held the engine mutex", err)
	case <-time.After(100 * time.Millisecond):
	}
	n := len(e.fake.Requests()) - before
	e.eng.mu.Unlock()
	if n != 0 {
		t.Errorf("CreateClient made %d AdGuard requests while the engine mutex was held, want 0", n)
	}
	if err := <-done; err != nil {
		t.Fatalf("CreateClient after Unlock: %v", err)
	}
	e.wantOwn("TV", "tiktok", "youtube")
}

// failingStore fails AddChildClient after AdGuard has been written.
type failingStore struct {
	*store.Store
	addErr error
}

func (f failingStore) AddChildClient(context.Context, int64, string) error { return f.addErr }

func TestCreateClient_StoreFails(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	ag, err := adguard.New(e.fake.URL(), svcUser, svcPass)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	eng := New(failingStore{Store: e.st, addErr: boom}, ag, Options{Now: e.clk.Now, AfterFunc: e.clk.AfterFunc})
	t.Cleanup(eng.Close)

	err = eng.CreateClient(ctx, "TV", tvIDs, ptr(ada.ID))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
	e.wantOwn("TV", "tiktok", "youtube")
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want unchanged [Kid phone]", got)
	}
}

func TestCreateClient_ReadFails(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	// Fail the baseline read only: AddClient's own /control/clients pre-read
	// still answers, so an engine that ignored the error would reach the add.
	e.fake.SetStatus("/control/blocked_services/get", 500)
	if err := e.eng.CreateClient(ctx, "TV", tvIDs, ptr(ada.ID)); err == nil {
		t.Fatal("err = nil, want the read error")
	}
	if n := e.adds(); n != 0 {
		t.Errorf("adds = %d, want 0", n)
	}
}

// adopt runs AdoptClients and fails the test on error.
func (e *env) adopt(childID int64, siblings, added []string) {
	e.t.Helper()
	if err := e.eng.AdoptClients(ctx, childID, siblings, added); err != nil {
		e.t.Fatalf("AdoptClients(%d, %v, %v): %v", childID, siblings, added, err)
	}
}

func TestAdopt_SharedKeepsOtherGrant(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	g := e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Fatalf("setup: Kid phone = %v, want [tiktok]", got)
	}
	e.offGlobal("youtube", "roblox")
	ben := e.child("Ben", "Kid tablet")

	e.adopt(ben.ID, []string{"Kid tablet"}, []string{"Kid phone"})
	e.wantOwn("Kid phone", "roblox", "tiktok")

	// Ada's revert still owns the re-block.
	e.clk.Advance(d + time.Second)
	e.wantStatus(g.Grant.ID, store.StatusExpired)
	e.wantOwn("Kid phone", "roblox", "tiktok", "youtube")
}

func TestAdopt_CoverIsPerClient(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone", "Old laptop")
	e.create(ada.ID, []string{"youtube"}, []string{"Old laptop"})
	e.ownList("Kid phone", "tiktok")
	e.offGlobal("youtube")
	ben := e.child("Ben", "Kid tablet")

	e.adopt(ben.ID, []string{"Kid tablet"}, []string{"Kid phone"})
	e.wantOwn("Kid phone", "tiktok", "youtube")
}

func TestAdopt_TwoAdded(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	e.offGlobal("youtube")
	ben := e.child("Ben", "Kid tablet")

	e.adopt(ben.ID, []string{"Kid tablet"}, []string{"Kid phone", "Old laptop"})
	e.wantOwn("Old laptop", "youtube")
	e.wantOwn("Kid phone", "tiktok")
}

func TestBaseline_SharedSiblingGrant(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	ben := e.child("Ben", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Fatalf("setup: Kid phone = %v, want [tiktok]", got)
	}

	e.createClient("Ben tv", ptr(ben.ID))
	e.wantOwn("Ben tv", "tiktok", "youtube")
}

func TestAdopt_AllCovered(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	e.offGlobal("youtube", "tiktok")
	ben := e.child("Ben", "Kid tablet")
	before := e.posts()

	e.adopt(ben.ID, []string{"Kid tablet"}, []string{"Kid phone"})
	if n := e.posts() - before; n != 0 {
		t.Errorf("update POSTs = %d, want 0 (baseline within current plus held)", n)
	}
	e.wantOwn("Kid phone", "tiktok")
}

func TestAdoptClients_NoCustomLeak(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.create(ada.ID, []string{"youtube", svc}, []string{"Kid phone"})

	// Kid tablet inherits the global list, so adoption writes its own list:
	// the baseline must not carry the grant's custom id.
	e.adopt(ada.ID, []string{"Kid phone"}, []string{"Kid tablet"})
	if e.usesGlobal("Kid tablet") {
		t.Fatal("setup: Kid tablet still on the global list, adoption wrote nothing")
	}
	if err := e.eng.CreateClient(ctx, "TV", tvIDs, ptr(ada.ID)); err != nil {
		t.Fatal(err)
	}
	if e.adds() != 1 {
		t.Fatalf("adds = %d, want 1", e.adds())
	}
	e.noCustomInClientWrites()
	for _, name := range []string{"Kid tablet", "TV"} {
		for _, id := range e.blocked(name) {
			if strings.HasPrefix(id, "custom:") {
				t.Fatalf("%s blocked_services holds %s", name, id)
			}
		}
	}
}

func TestCreateClient_ResyncsRules(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	e.createService("Portal", "a.com")
	if got, want := e.region(), []string{"||a.com^$client='Kid phone'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("setup region %q", got)
	}
	if err := e.eng.CreateClient(ctx, "TV", tvIDs, ptr(ada.ID)); err != nil {
		t.Fatal(err)
	}
	if got, want := e.region(), []string{"||a.com^$client='Kid phone'|'TV'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("region after CreateClient %q, want %q", got, want)
	}

	// An unassigned client is not a child's: no rule names it.
	if err := e.eng.CreateClient(ctx, "Dad laptop", []string{"192.168.1.40"}, nil); err != nil {
		t.Fatal(err)
	}
	noLineContains(t, e.fake.UserRules(), "Dad laptop")
}

// Permanent toggles in the baseline (locked new_client_blocked_set_v2).

func TestCreateClient_BaselineToggles(t *testing.T) {
	e := newEnv(t, nil)
	// kid-phone has not converged yet: it still blocks youtube.
	e.fakeClient("kid-phone", "youtube")
	e.fakeClient("den")
	ann := e.child("Ann", "kid-phone")
	e.seedToggle(ann.ID, "youtube", store.ToggleUnblocked)
	e.seedToggle(ann.ID, "tiktok", store.ToggleBlocked)

	e.createClient("TV", ptr(ann.ID))
	e.wantOwn("TV", "tiktok")

	e.adopt(ann.ID, []string{"kid-phone"}, []string{"den"})
	e.wantOwn("den", "tiktok")
}

func TestCreateClient_BaselineGrantAndUnblock(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "tiktok", "youtube")
	ann := e.child("Ann", "kid-phone")
	e.create(ann.ID, []string{"tiktok", "youtube"}, []string{"kid-phone"})
	e.seedToggle(ann.ID, "youtube", store.ToggleUnblocked)

	e.createClient("TV", ptr(ann.ID))
	e.wantOwn("TV", "tiktok")
}

func TestAdoptClients_SharedPermanentOpen(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("a-phone", "tiktok", "youtube")
	e.fakeClient("tv", "roblox") // youtube open: Ben unblocked it
	ann := e.child("Ann", "a-phone")
	ben := e.child("Ben", "tv")
	e.seedToggle(ben.ID, "youtube", store.ToggleUnblocked)

	e.adopt(ann.ID, []string{"a-phone"}, []string{"tv"})
	e.wantOwn("tv", "roblox", "tiktok")
}

func TestBaseline_SharedSiblingPermanent(t *testing.T) {
	e := newEnv(t, nil)
	e.setBlocked("Kid phone", "tiktok") // youtube open: Ada unblocked it
	ada := e.child("Ada", "Kid phone")
	ben := e.child("Ben", "Kid phone")
	e.seedToggle(ada.ID, "youtube", store.ToggleUnblocked)

	// Kid phone is open for Ada's reason, not Ben's.
	e.createClient("Ben tv", ptr(ben.ID))
	e.wantOwn("Ben tv", "tiktok", "youtube")

	if err := e.eng.CreateClient(ctx, "Ada tv", []string{"192.168.1.31"}, ptr(ada.ID)); err != nil {
		t.Fatal(err)
	}
	e.wantOwn("Ada tv", "tiktok")
}

func TestCreateClient_ToggleReadFails(t *testing.T) {
	var fs *toggleFailStore
	e := newEnvStore(t, nil, func(st *store.Store) Store {
		fs = &toggleFailStore{Store: st}
		return fs
	})
	ann := e.child("Ann", "Kid phone")
	boom := errors.New("database is locked")
	fs.set(boom)

	if err := e.eng.CreateClient(ctx, "TV", tvIDs, ptr(ann.ID)); !errors.Is(err, boom) {
		t.Fatalf("CreateClient err = %v, want the toggle read error", err)
	}
	if n := e.adds(); n != 0 {
		t.Errorf("adds = %d, want 0 (no baseline without the toggles)", n)
	}
	if err := e.eng.AdoptClients(ctx, ann.ID, []string{"Kid phone"}, []string{"Old laptop"}); !errors.Is(err, boom) {
		t.Fatalf("AdoptClients err = %v, want the toggle read error", err)
	}
	if n := e.posts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
}
