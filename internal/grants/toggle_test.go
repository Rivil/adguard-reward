package grants

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

func (e *env) toggle(childID int64, serviceID, state string) ToggleResult {
	e.t.Helper()
	res, err := e.eng.Toggle(ctx, childID, serviceID, state)
	if err != nil {
		e.t.Fatalf("Toggle(%d, %s, %s): %v", childID, serviceID, state, err)
	}
	return res
}

func (e *env) blocks(client, service string) bool {
	e.t.Helper()
	return slices.Contains(e.blocked(client), service)
}

func (e *env) storedToggles() []store.ServiceToggle {
	e.t.Helper()
	list, err := e.st.ListServiceToggles(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func (e *env) updatesTo(name string) int {
	n := 0
	for _, c := range e.updatesFor() {
		if c == name {
			n++
		}
	}
	return n
}

func (e *env) grant(id int64) store.Grant {
	e.t.Helper()
	g, err := e.st.GetGrant(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func TestToggle_UnblockAll(t *testing.T) {
	e := newEnv(t, nil)
	for _, c := range []string{"kid-phone", "tablet", "tv"} {
		e.fakeClient(c, "youtube", "tiktok")
	}
	a := e.child("A", "kid-phone", "tablet", "tv")
	e.child("B", "tv")

	res := e.toggle(a.ID, "youtube", store.ToggleUnblocked)
	for _, c := range a.Clients {
		if e.blocks(c, "youtube") || !e.blocks(c, "tiktok") {
			t.Errorf("%s blocks %v, want tiktok only", c, e.blocked(c))
		}
	}
	if !res.Applied || len(res.Failed) != 0 || !reflect.DeepEqual(res.Clients, []string{"kid-phone", "tablet", "tv"}) || res.Grant != nil {
		t.Fatalf("result = %+v, want applied over all three clients, no grant", res)
	}
}

func TestToggle_UnblockDuringGrant(t *testing.T) {
	e := newEnv(t, nil)
	for _, c := range []string{"kid-phone", "tablet"} {
		e.fakeClient(c, "youtube")
	}
	a := e.child("A", "kid-phone", "tablet")
	g := e.create(a.ID, []string{"youtube"}, a.Clients).Grant
	before := e.posts()

	res := e.toggle(a.ID, "youtube", store.ToggleUnblocked)
	if e.posts() != before {
		t.Fatalf("unblock under a grant issued %d update POSTs, want 0", e.posts()-before)
	}
	if res.Grant != nil || !res.Applied {
		t.Fatalf("result = %+v, want applied, no grant trimmed", res)
	}
	if got := e.grant(g.ID); !reflect.DeepEqual(got.Services, []string{"youtube"}) || !got.EndsAt.Equal(g.EndsAt) {
		t.Fatalf("grant = %+v, want untouched", got)
	}
	if e.clk.pending() != 1 {
		t.Fatalf("pending timers = %d, want 1", e.clk.pending())
	}
	e.clk.Advance(d + time.Minute)
	if e.status(g.ID) != store.StatusExpired {
		t.Fatalf("grant status = %s, want expired", e.status(g.ID))
	}
	for _, c := range a.Clients {
		if e.blocks(c, "youtube") {
			t.Errorf("%s re-blocked youtube at expiry", c)
		}
	}
}

func TestToggle_BlockDuringGrant(t *testing.T) {
	e := newEnv(t, nil)
	for _, c := range []string{"kid-phone", "tablet"} {
		e.fakeClient(c, "youtube", "tiktok")
	}
	a := e.child("A", "kid-phone", "tablet")
	g := e.create(a.ID, []string{"youtube", "tiktok"}, a.Clients).Grant

	res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
	for _, c := range a.Clients {
		if !e.blocks(c, "youtube") || e.blocks(c, "tiktok") {
			t.Errorf("%s blocks %v in-call, want youtube only", c, e.blocked(c))
		}
	}
	if res.Grant == nil || res.Grant.ID != g.ID || res.Grant.Ended || !res.Applied {
		t.Fatalf("result = %+v, want grant %d trimmed, not ended", res, g.ID)
	}
	got := e.grant(g.ID)
	if got.Status != store.StatusActive || !reflect.DeepEqual(got.Services, []string{"tiktok"}) {
		t.Fatalf("grant = %+v, want active [tiktok]", got)
	}
	if e.clk.pending() != 1 {
		t.Fatalf("pending timers = %d, want 1", e.clk.pending())
	}
	e.clk.Advance(d + time.Minute)
	for _, c := range a.Clients {
		if got := e.blocked(c); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
			t.Errorf("%s after expiry blocks %v, want [tiktok youtube]", c, got)
		}
	}
}

func TestToggle_BlockEndsGrant(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "youtube")
	a := e.child("A", "kid-phone")
	g := e.create(a.ID, []string{"youtube"}, a.Clients).Grant

	res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
	if res.Grant == nil || res.Grant.ID != g.ID || !res.Grant.Ended {
		t.Fatalf("result grant = %+v, want {%d, Ended}", res.Grant, g.ID)
	}
	if e.clk.pending() != 0 {
		t.Fatalf("pending timers = %d, want 0", e.clk.pending())
	}
	before := e.posts()
	e.clk.Advance(2 * d)
	if e.posts() != before {
		t.Fatalf("an ended grant's expiry issued %d update POSTs", e.posts()-before)
	}
	if !e.blocks("kid-phone", "youtube") {
		t.Fatal("kid-phone lost youtube")
	}
}

func TestToggle_BlockOldDevice(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "youtube")
	e.fakeClient("old-tablet", "youtube")
	a := e.child("A", "kid-phone", "old-tablet")
	e.create(a.ID, []string{"youtube"}, a.Clients)
	if _, err := e.st.UpdateChild(ctx, a.ID, "A", []string{"kid-phone"}); err != nil {
		t.Fatal(err)
	}

	res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
	if !e.blocks("old-tablet", "youtube") || !e.blocks("kid-phone", "youtube") {
		t.Fatalf("kid-phone %v old-tablet %v, want youtube on both", e.blocked("kid-phone"), e.blocked("old-tablet"))
	}
	if !res.Applied || !reflect.DeepEqual(res.Clients, []string{"kid-phone", "old-tablet"}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestToggle_BlockOldDeviceFails(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "youtube", "tiktok")
	e.fakeClient("old-tablet", "youtube", "tiktok")
	a := e.child("A", "kid-phone", "old-tablet")
	g := e.create(a.ID, []string{"youtube", "tiktok"}, a.Clients).Grant
	if _, err := e.st.UpdateChild(ctx, a.ID, "A", []string{"kid-phone"}); err != nil {
		t.Fatal(err)
	}
	e.fake.SetUpdateStatus("old-tablet", 500)

	res, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleBlocked)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || !reflect.DeepEqual(res.Failed, []string{"old-tablet"}) {
		t.Fatalf("result = %+v, want Applied false, Failed [old-tablet]", res)
	}
	if got := e.grant(g.ID); !reflect.DeepEqual(got.Services, []string{"tiktok"}) {
		t.Fatalf("grant = %+v, want trimmed to [tiktok]", got)
	}

	e.fake.SetUpdateStatus("old-tablet", 0)
	before := e.updatesTo("old-tablet")
	if _, err := e.eng.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.updatesTo("old-tablet") - before; n != 0 {
		t.Fatalf("Reconcile issued %d update POSTs to old-tablet, want 0 (best-effort)", n)
	}
}

func TestToggle_BlockSharedHeldOpen(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("a-phone")
	e.fakeClient("tv")
	a := e.child("A", "a-phone", "tv")
	b := e.child("B", "tv")
	e.seedToggle(b.ID, "youtube", store.ToggleUnblocked)
	before := e.updatesTo("tv")

	res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
	if !e.blocks("a-phone", "youtube") || e.blocks("tv", "youtube") {
		t.Fatalf("a-phone %v tv %v, want youtube on a-phone only", e.blocked("a-phone"), e.blocked("tv"))
	}
	if e.updatesTo("tv") != before {
		t.Fatal("tv was written")
	}
	if !res.Applied || slices.Contains(res.Failed, "tv") {
		t.Fatalf("result = %+v, want applied, tv not failed", res)
	}
}

func TestToggle_PartialApply(t *testing.T) {
	t.Run("one client 500", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fakeClient("kid-phone", "youtube")
		e.fakeClient("tablet", "youtube")
		a := e.child("A", "kid-phone", "tablet")
		e.fake.SetUpdateStatus("tablet", 500)
		res, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleUnblocked)
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"tablet"}) {
			t.Fatalf("result = %+v, want Applied false, Failed [tablet]", res)
		}
		if got := e.storedToggles(); len(got) != 1 || got[0].State != store.ToggleUnblocked {
			t.Fatalf("toggles = %+v, want the row stored", got)
		}
	})
	t.Run("clients read 502", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fakeClient("kid-phone", "youtube")
		e.fakeClient("tablet", "youtube")
		a := e.child("A", "kid-phone", "tablet")
		e.fake.SetStatus("/control/clients", 502)
		before := e.posts()
		res, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleUnblocked)
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-phone", "tablet"}) || e.posts() != before {
			t.Fatalf("result = %+v posts %d, want every client failed and 0 POSTs", res, e.posts()-before)
		}
		if len(e.storedToggles()) != 1 {
			t.Fatal("toggle not stored")
		}
	})
	t.Run("global-list client", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fakeClient("kid-phone", "youtube")
		e.fakeGlobalClient("kid-tablet")
		a := e.child("A", "kid-phone", "kid-tablet")
		res := e.toggle(a.ID, "youtube", store.ToggleUnblocked)
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-tablet"}) {
			t.Fatalf("result = %+v, want Failed [kid-tablet]", res)
		}
		if e.globalListWrites() != 0 {
			t.Fatal("the global blocked_services list was written")
		}
	})
}

func TestToggle_UnknownCustom(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("A", "Kid phone")
	svc, err := e.eng.CreateService(ctx, "Portal", []string{"a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.eng.DeleteService(ctx, svc.Service.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{store.CustomServiceID(svc.Service.ID), "custom:99", "custom:01"} {
		_, err := e.eng.Toggle(ctx, a.ID, id, store.ToggleUnblocked)
		var unknown *ErrUnknownService
		if !errors.As(err, &unknown) || unknown.ID != id {
			t.Fatalf("Toggle(%s) err = %v, want *ErrUnknownService", id, err)
		}
	}
	if got := e.storedToggles(); len(got) != 0 {
		t.Fatalf("toggles = %+v, want none", got)
	}
}

func TestToggle_UnknownChild(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.eng.Toggle(ctx, 99, "youtube", store.ToggleBlocked); !errors.Is(err, store.ErrChildNotFound) {
		t.Fatalf("err = %v, want ErrChildNotFound", err)
	}
	if e.posts() != 0 {
		t.Fatal("an unknown child reached AdGuard")
	}
}

func TestToggle_RaceExpiry(t *testing.T) {
	for run := 0; run < 50; run++ {
		e := newEnv(t, nil)
		e.fakeClient("kid-phone", "youtube")
		e.fakeClient("tablet", "youtube")
		a := e.child("A", "kid-phone", "tablet")
		e.create(a.ID, []string{"youtube"}, a.Clients)

		done := make(chan struct{})
		go func() {
			defer close(done)
			e.clk.Advance(d + time.Minute)
		}()
		if _, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleUnblocked); err != nil {
			t.Fatal(err)
		}
		<-done
		for _, c := range a.Clients {
			if e.blocks(c, "youtube") {
				t.Fatalf("run %d: %s blocks youtube after unblock raced expiry", run, c)
			}
		}
	}
}

func TestToggle_Idempotent(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "youtube")
	a := e.child("A", "kid-phone")
	e.toggle(a.ID, "youtube", store.ToggleUnblocked)
	before := e.posts()
	if res := e.toggle(a.ID, "youtube", store.ToggleUnblocked); !res.Applied {
		t.Fatalf("second unblock = %+v", res)
	}
	if e.posts() != before {
		t.Fatalf("second identical unblock issued %d POSTs, want 0", e.posts()-before)
	}
}

func TestToggle_Deadline(t *testing.T) {
	e := newEnv(t, nil) // default ApplyTimeout (4 s)
	e.fakeClient("kid-phone", "youtube")
	e.fakeClient("tablet", "youtube")
	a := e.child("A", "kid-phone", "tablet")
	e.fake.Hang("/control/clients/update", 10*time.Second)

	start := time.Now()
	res, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleUnblocked)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 4500*time.Millisecond {
		t.Fatalf("Toggle took %v, want < 4.5 s", took)
	}
	if res.Applied {
		t.Fatalf("result = %+v, want Applied false", res)
	}
}

func TestToggle_ClientsGoverned(t *testing.T) {
	setup := func(t *testing.T) (*env, store.Child, store.Child) {
		e := newEnv(t, nil)
		for _, c := range []string{"a-phone", "tablet", "tv"} {
			e.fakeClient(c)
		}
		a := e.child("A", "a-phone", "tablet", "tv")
		b := e.child("B", "tv")
		return e, a, b
	}
	t.Run("co-owner unblock holds tv", func(t *testing.T) {
		e, a, b := setup(t)
		e.seedToggle(b.ID, "youtube", store.ToggleUnblocked)
		res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
		if !reflect.DeepEqual(res.Clients, []string{"a-phone", "tablet"}) {
			t.Fatalf("Clients = %v, want [a-phone tablet]", res.Clients)
		}
	})
	t.Run("live grant holds tv", func(t *testing.T) {
		e, a, _ := setup(t)
		c := e.child("C", "tv")
		e.create(c.ID, []string{"youtube"}, []string{"tv"})
		res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
		if slices.Contains(res.Clients, "tv") {
			t.Fatalf("Clients = %v, want tv excluded", res.Clients)
		}
	})
	t.Run("unblock governs every client", func(t *testing.T) {
		e, a, _ := setup(t)
		res := e.toggle(a.ID, "youtube", store.ToggleUnblocked)
		if !reflect.DeepEqual(res.Clients, []string{"a-phone", "tablet", "tv"}) {
			t.Fatalf("Clients = %v, want all three", res.Clients)
		}
	})
	t.Run("a failed client is still governed", func(t *testing.T) {
		e, a, _ := setup(t)
		e.fake.SetUpdateStatus("tablet", 500)
		res := e.toggle(a.ID, "youtube", store.ToggleBlocked)
		if !slices.Contains(res.Failed, "tablet") || !slices.Contains(res.Clients, "tablet") {
			t.Fatalf("result = %+v, want tablet in Failed and Clients", res)
		}
	})
}

func TestToggle_GlobalBothDirections(t *testing.T) {
	e := newEnv(t, nil)
	e.fakeClient("kid-phone", "youtube")
	e.fakeGlobalClient("kid-tablet")
	a := e.child("A", "kid-phone", "kid-tablet")
	for _, state := range []string{store.ToggleUnblocked, store.ToggleBlocked} {
		res := e.toggle(a.ID, "youtube", state)
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-tablet"}) {
			t.Fatalf("%s: result = %+v, want Applied false, Failed [kid-tablet]", state, res)
		}
	}
	if n := e.updatesTo("kid-tablet"); n != 0 {
		t.Fatalf("kid-tablet written %d times, want 0", n)
	}
	if e.globalListWrites() != 0 {
		t.Fatal("the global blocked_services list was written")
	}
}

func TestToggle_ReadFailure(t *testing.T) {
	fs := &toggleFailStore{}
	e := newEnvStore(t, nil, func(s *store.Store) Store { fs.Store = s; return fs })
	e.fakeClient("kid-phone", "youtube")
	a := e.child("A", "kid-phone")
	fs.set(errors.New("database is locked"))
	before := e.posts()
	res, err := e.eng.Toggle(ctx, a.ID, "youtube", store.ToggleUnblocked)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-phone"}) || !reflect.DeepEqual(res.Clients, res.Failed) || e.posts() != before {
		t.Fatalf("result = %+v, want kid-phone failed with no write", res)
	}
}
