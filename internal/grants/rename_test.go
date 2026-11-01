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
