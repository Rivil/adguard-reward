package grants

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

func (e *env) rename(from, to string) {
	e.t.Helper()
	if err := e.eng.RenameClient(ctx, from, to); err != nil {
		e.t.Fatalf("RenameClient(%s, %s): %v", from, to, err)
	}
}

// has reports whether the fake holds a persistent client called name.
func (e *env) has(name string) bool {
	_, ok := e.fake.BlockedServices(name)
	return ok
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRename_GrantExpiryReverts(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	res := e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})

	e.rename("Kid phone", "Ada phone")
	mark := len(e.updatesFor())
	e.clk.Advance(d + time.Second)

	if s := e.status(res.Grant.ID); s != store.StatusExpired {
		t.Fatalf("grant status = %s, want expired", s)
	}
	if got := e.blocked("Ada phone"); !slices.Contains(got, "youtube") {
		t.Errorf("Ada phone after expiry = %v, want youtube re-blocked", got)
	}
	after := e.updatesFor()[mark:]
	if slices.Contains(after, "Kid phone") {
		t.Errorf("revert update POSTs %v name the old \"Kid phone\"", after)
	}
	if !slices.Contains(after, "Ada phone") {
		t.Errorf("revert update POSTs %v, want one naming \"Ada phone\"", after)
	}
	if g, _ := e.st.GetGrant(ctx, res.Grant.ID); !reflect.DeepEqual(g.Clients, []string{"Ada phone"}) {
		t.Errorf("grant clients = %v, want [Ada phone]", g.Clients)
	}
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Ada phone"}) {
		t.Errorf("Ada clients = %v, want [Ada phone]", got)
	}
}

func TestRename_EndReverts(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	res := e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})

	e.rename("Kid phone", "Ada phone")
	if err := e.eng.End(ctx, res.Grant.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	if got := e.blocked("Ada phone"); !slices.Contains(got, "youtube") {
		t.Errorf("Ada phone after End = %v, want youtube re-blocked", got)
	}
	if s := e.status(res.Grant.ID); s != store.StatusEnded {
		t.Errorf("grant status = %s, want ended", s)
	}
}

// TestRename_TimerSerialised fires the grant's expiry while RenameClient is
// mid-write in AdGuard. Under the engine mutex the expiry waits, reads the
// renamed grant and re-blocks "Ada phone"; a rename outside the mutex lets
// it revert "Kid phone" — gone by then — and log "client gone".
func TestRename_TimerSerialised(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	res := e.create(ada.ID, []string{"youtube"}, []string{"Kid phone"})
	e.fake.Hang("/control/clients/update", 100*time.Millisecond)

	before := e.posts()
	done := make(chan error, 1)
	go func() { done <- e.eng.RenameClient(ctx, "Kid phone", "Ada phone") }()
	waitFor(t, "the rename's update POST", func() bool { return e.posts() > before })

	expired := make(chan struct{})
	go func() { e.clk.Advance(d + time.Second); close(expired) }()
	if err := <-done; err != nil {
		t.Fatalf("RenameClient: %v", err)
	}
	<-expired
	e.fake.Hang("/control/clients/update", 0)

	if s := e.status(res.Grant.ID); s != store.StatusExpired {
		t.Fatalf("grant status = %s, want expired", s)
	}
	if got := e.blocked("Ada phone"); !slices.Contains(got, "youtube") {
		t.Errorf("Ada phone after expiry = %v, want youtube re-blocked", got)
	}
	if logs := e.logs.String(); strings.Contains(logs, "client gone") {
		t.Errorf("expiry logged \"client gone\":\n%s", logs)
	}
}

// renameFailStore fails the store rename after AdGuard has been written.
type renameFailStore struct {
	*store.Store
	err error
}

func (f renameFailStore) RenameClient(context.Context, string, string) error { return f.err }

func (e *env) engineWith(st Store) *Engine {
	e.t.Helper()
	ag, err := adguard.New(e.fake.URL(), svcUser, svcPass)
	if err != nil {
		e.t.Fatal(err)
	}
	eng := New(st, ag, Options{Now: e.clk.Now, AfterFunc: e.clk.AfterFunc})
	e.t.Cleanup(eng.Close)
	return eng
}

func TestRename_StoreFailCompensates(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	boom := errors.New("disk full")
	eng := e.engineWith(renameFailStore{Store: e.st, err: boom})

	mark := len(e.updatesFor())
	err := eng.RenameClient(ctx, "Kid phone", "Ada phone")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"Kid phone"`) || !strings.Contains(msg, `"Ada phone"`) {
		t.Errorf("err %q does not name both clients", msg)
	}
	if got, want := e.updatesFor()[mark:], []string{"Kid phone", "Ada phone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("update POSTs = %v, want %v (rename, then rename back)", got, want)
	}
	if !e.has("Kid phone") || e.has("Ada phone") {
		t.Errorf("fake has Kid phone=%v Ada phone=%v, want Kid phone back and no Ada phone",
			e.has("Kid phone"), e.has("Ada phone"))
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("Kid phone blocks %v after the rename back, want [tiktok youtube]", got)
	}
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want unchanged [Kid phone]", got)
	}
}

func TestRename_CompensationFails(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ada", "Kid phone")
	boom := errors.New("disk full")
	eng := e.engineWith(renameFailStore{Store: e.st, err: boom})
	// Only the rename back is addressed to "Ada phone".
	e.fake.SetUpdateStatus("Ada phone", 500)

	err := eng.RenameClient(ctx, "Kid phone", "Ada phone")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `AdGuard still has "Ada phone"`) {
		t.Errorf("err %q does not say AdGuard kept the new name", msg)
	}
	if !e.has("Ada phone") {
		t.Error("fake lost Ada phone")
	}
}

func TestRename_StaleConflict(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ada", "Kid phone")
	bob := e.child("Bob", "Ghost")

	for _, from := range []string{"Kid phone", "Old laptop"} {
		before := len(e.fake.Requests())
		err := e.eng.RenameClient(ctx, from, "Ghost")
		var taken *store.ErrClientTaken
		if !errors.As(err, &taken) || taken.ChildID != bob.ID || taken.Client != "Ghost" {
			t.Errorf("rename %s -> Ghost: err = %v, want *store.ErrClientTaken naming Bob", from, err)
		}
		if n := len(e.fake.Requests()) - before; n != 0 {
			t.Errorf("rename %s -> Ghost made %d AdGuard requests, want 0", from, n)
		}
	}
	if !e.has("Kid phone") {
		t.Error("fake lost Kid phone")
	}
}

func TestRename_Noop(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	before := len(e.fake.Requests())
	e.rename("Kid phone", "Kid phone")
	if n := len(e.fake.Requests()) - before; n != 0 {
		t.Errorf("same-name rename made %d AdGuard requests, want 0", n)
	}
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want [Kid phone]", got)
	}
}

func TestRename_SelfStale(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone", "TV")

	mark := len(e.updatesFor())
	e.rename("Kid phone", "TV")
	if got := e.updatesFor()[mark:]; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("update POSTs = %v, want exactly one (no rename back)", got)
	}
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"TV"}) {
		t.Errorf("Ada clients = %v, want [TV]", got)
	}
	if !e.has("TV") || e.has("Kid phone") {
		t.Errorf("fake has TV=%v Kid phone=%v, want TV only", e.has("TV"), e.has("Kid phone"))
	}
}

func TestRename_AdGuardRefuses(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")

	if err := e.eng.RenameClient(ctx, "Kid phone", "Kid tablet"); !errors.Is(err, adguard.ErrClientExists) {
		t.Errorf("rename onto Kid tablet: err = %v, want adguard.ErrClientExists", err)
	}
	if err := e.eng.RenameClient(ctx, "Nobody", "Ada phone"); !errors.Is(err, adguard.ErrClientNotFound) {
		t.Errorf("rename unknown: err = %v, want adguard.ErrClientNotFound", err)
	}
	if n := e.posts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0", n)
	}
	if got := mustChild(t, e, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients = %v, want unchanged [Kid phone]", got)
	}
}

func TestEngineRename_SharedStale(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone", "TV")
	ben := e.child("Ben", "Kid phone")

	before := e.posts()
	if err := e.eng.RenameClient(ctx, "Kid phone", "TV"); err != nil {
		t.Fatalf("RenameClient: %v", err)
	}
	if n := e.posts() - before; n != 1 {
		t.Errorf("update POSTs = %d, want exactly 1", n)
	}
	for _, c := range []store.Child{ada, ben} {
		if got := mustChild(t, e, c.ID).Clients; !reflect.DeepEqual(got, []string{"TV"}) {
			t.Errorf("%s clients = %v, want [TV]", c.Name, got)
		}
	}
}

func TestEngineRename_Shared(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	ben := e.child("Ben", "Kid phone")
	res := e.create(ben.ID, []string{"youtube"}, []string{"Kid phone"})

	mark := len(e.updatesFor())
	e.rename("Kid phone", "Family TV")
	if got := e.updatesFor()[mark:]; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("update POSTs = %v, want exactly one rename", got)
	}
	for _, c := range []store.Child{ada, ben} {
		if got := mustChild(t, e, c.ID).Clients; !reflect.DeepEqual(got, []string{"Family TV"}) {
			t.Errorf("%s clients = %v, want [Family TV]", c.Name, got)
		}
	}

	e.clk.Advance(d + time.Second)
	e.wantStatus(res.Grant.ID, store.StatusExpired)
	if got := e.blocked("Family TV"); !slices.Contains(got, "youtube") {
		t.Errorf("Family TV after Ben's expiry = %v, want youtube re-blocked", got)
	}
}

func TestEngineRename_Outsider(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ada", "Kid phone")
	e.child("Ben", "Kid phone")
	cleo := e.child("Cleo", "TV")

	before := len(e.fake.Requests())
	err := e.eng.RenameClient(ctx, "Kid phone", "TV")
	var taken *store.ErrClientTaken
	if !errors.As(err, &taken) || taken.ChildID != cleo.ID || taken.Client != "TV" {
		t.Errorf("err = %v, want *store.ErrClientTaken naming Cleo", err)
	}
	if n := len(e.fake.Requests()) - before; n != 0 {
		t.Errorf("AdGuard requests = %d, want 0", n)
	}
	if !e.has("Kid phone") || e.has("TV") {
		t.Error("AdGuard was renamed despite the conflict")
	}
}

func TestRenameClient_ResyncsRules(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.child("Ada", "Kid phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.create(ada.ID, []string{svc}, []string{"Kid phone"})
	if got := e.region(); !reflect.DeepEqual(got, []string{
		"||a.com^$client='Kid phone'", "@@||a.com^$client='Kid phone'",
	}) {
		t.Fatalf("setup region %q", got)
	}

	// The old name is a prefix of the new one: a stale rule would still
	// contain "Kid phone", so match the quoted value exactly.
	e.rename("Kid phone", "Kid phone 2")
	want := []string{"||a.com^$client='Kid phone 2'", "@@||a.com^$client='Kid phone 2'"}
	if got := e.region(); !reflect.DeepEqual(got, want) {
		t.Fatalf("region after rename %q, want %q", got, want)
	}
	noLineContains(t, e.fake.UserRules(), "'Kid phone'")
}

func TestRenameClient_StoreFailure(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ada", "Kid phone")
	e.createService("Portal", "a.com")
	// Make the region stale (a manual edit inside the markers) so that any
	// resync on the rollback path would show up as a rewrite.
	e.fake.SetUserRules([]string{"! mine", rules.BeginMarker, "||tampered^", rules.EndMarker})
	before, posts := e.fake.UserRules(), e.setRulesPosts()

	eng := e.engineWith(renameFailStore{Store: e.st, err: errors.New("disk full")})
	if err := eng.RenameClient(ctx, "Kid phone", "Ada phone"); err == nil {
		t.Fatal("rename succeeded despite the store failure")
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("%d set_rules POSTs after a rolled-back rename", n)
	}
	if !reflect.DeepEqual(e.fake.UserRules(), before) {
		t.Fatalf("user_rules changed: %q", e.fake.UserRules())
	}
}
