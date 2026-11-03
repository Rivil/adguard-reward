package grants

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

// blockLines returns the region's block (non-@@) lines.
func (e *env) blockLines() []string {
	e.t.Helper()
	return slices.DeleteFunc(slices.Clone(e.region()), func(l string) bool { return strings.HasPrefix(l, "@@") })
}

func TestToggle_CustomUnblock(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("A", "kid-phone", "tv")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	blockBefore := e.blockLines()
	if len(e.exceptions()) != 0 {
		t.Fatalf("precondition: exceptions %q", e.exceptions())
	}

	res := e.toggle(a.ID, svc, store.ToggleUnblocked)
	if want := []string{"@@||a.com^$client='kid-phone'|'tv'"}; !reflect.DeepEqual(e.exceptions(), want) {
		t.Fatalf("exceptions = %q, want %q", e.exceptions(), want)
	}
	if got := e.blockLines(); !reflect.DeepEqual(got, blockBefore) {
		t.Fatalf("block lines = %q, want unchanged %q", got, blockBefore)
	}
	if !res.Applied || len(res.Failed) != 0 || !reflect.DeepEqual(res.Clients, []string{"kid-phone", "tv"}) {
		t.Fatalf("result = %+v, want applied over both clients", res)
	}
}

func TestToggle_CustomBlockUnderGrant(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("A", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	g := e.create(a.ID, []string{svc}, a.Clients).Grant
	if len(e.exceptions()) != 1 {
		t.Fatalf("precondition: grant exception missing: %q", e.region())
	}

	res := e.toggle(a.ID, svc, store.ToggleBlocked)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after block = %q, want none", got)
	}
	if res.Grant == nil || res.Grant.ID != g.ID || !res.Grant.Ended || !res.Applied {
		t.Fatalf("result = %+v, want applied and grant %d ended", res, g.ID)
	}
	if e.status(g.ID) != store.StatusEnded {
		t.Fatalf("grant status = %s, want ended", e.status(g.ID))
	}
	if e.clk.pending() != 0 {
		t.Fatalf("pending timers = %d, want 0", e.clk.pending())
	}
}

func TestToggle_CustomRegionFailure(t *testing.T) {
	for name, fault := range map[string]func(e *env){
		"set_rules 500": func(e *env) { e.fake.SetStatus(setRulesPath, 500) },
		"malformed region": func(e *env) {
			e.fake.SetUserRules([]string{"||x.example^", rules.BeginMarker, "||a.com^"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			a := e.child("A", "tv", "kid-phone")
			svc := e.createService("Portal", "a.com").Service.ServiceID()
			fault(e)
			res, err := e.eng.Toggle(ctx, a.ID, svc, store.ToggleUnblocked)
			if err != nil {
				t.Fatalf("Toggle err = %v, want nil", err)
			}
			if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-phone", "tv"}) {
				t.Fatalf("result = %+v, want Applied false, Failed = the child's clients", res)
			}
			if got := e.storedToggles(); len(got) != 1 || got[0].ServiceID != svc || got[0].State != store.ToggleUnblocked {
				t.Fatalf("toggles = %+v, want the row stored", got)
			}
		})
	}
}

func TestToggle_CustomDeadline(t *testing.T) {
	e := newEnv(t, nil) // default ApplyTimeout (4 s)
	a := e.child("A", "kid-phone", "tv")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.fake.Hang(setRulesPath, 10*time.Second)

	start := time.Now()
	res, err := e.eng.Toggle(ctx, a.ID, svc, store.ToggleUnblocked)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 4500*time.Millisecond {
		t.Fatalf("Toggle took %v, want < 4.5 s", took)
	}
	if res.Applied || !reflect.DeepEqual(res.Failed, []string{"kid-phone", "tv"}) {
		t.Fatalf("result = %+v, want Applied false, Failed = the child's clients", res)
	}
	if len(e.storedToggles()) != 1 {
		t.Fatal("toggle not stored")
	}
}

func TestToggle_CustomClientsGoverned(t *testing.T) {
	t.Run("co-owner unblock holds tv", func(t *testing.T) {
		e := newEnv(t, nil)
		a := e.child("A", "a-phone", "tv")
		b := e.child("B", "tv")
		svc := e.createService("Portal", "a.com").Service.ServiceID()
		e.seedToggle(b.ID, svc, store.ToggleUnblocked)

		res := e.toggle(a.ID, svc, store.ToggleBlocked)
		if !reflect.DeepEqual(res.Clients, []string{"a-phone"}) {
			t.Fatalf("Clients = %v, want [a-phone]", res.Clients)
		}
		if want := []string{"@@||a.com^$client='tv'"}; !reflect.DeepEqual(e.exceptions(), want) {
			t.Fatalf("exceptions = %q, want tv's kept: %q", e.exceptions(), want)
		}

		res = e.toggle(a.ID, svc, store.ToggleUnblocked)
		if !reflect.DeepEqual(res.Clients, []string{"a-phone", "tv"}) {
			t.Fatalf("unblock Clients = %v, want [a-phone tv]", res.Clients)
		}
	})
	t.Run("live grant holds tv", func(t *testing.T) {
		e := newEnv(t, nil)
		a := e.child("A", "a-phone", "tv")
		c := e.child("C", "tv")
		svc := e.createService("Portal", "a.com").Service.ServiceID()
		e.create(c.ID, []string{svc}, []string{"tv"})

		res := e.toggle(a.ID, svc, store.ToggleBlocked)
		if !reflect.DeepEqual(res.Clients, []string{"a-phone"}) {
			t.Fatalf("Clients = %v, want [a-phone] (tv held by C's grant)", res.Clients)
		}
	})
}
