package grants

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

const setRulesPath = "/control/filtering/set_rules"

func (e *env) setRulesPosts() int { return e.fake.CountRequests("POST", setRulesPath) }

// region returns the lines between the markers in the fake's user_rules; it
// fails the test when the markers are not exactly one well-formed pair.
func (e *env) region() []string {
	e.t.Helper()
	all := e.fake.UserRules()
	b, en := slices.Index(all, rules.BeginMarker), slices.Index(all, rules.EndMarker)
	if b < 0 || en < b {
		e.t.Fatalf("no managed region in %q", all)
	}
	return all[b+1 : en]
}

// outside returns user_rules with the region (markers included) cut out.
func (e *env) outside() []string {
	e.t.Helper()
	all := e.fake.UserRules()
	b, en := slices.Index(all, rules.BeginMarker), slices.Index(all, rules.EndMarker)
	if b < 0 {
		return all
	}
	return append(slices.Clone(all[:b]), all[en+1:]...)
}

func (e *env) createService(name string, domains ...string) ServiceResult {
	e.t.Helper()
	r, err := e.eng.CreateService(ctx, name, domains)
	if err != nil {
		e.t.Fatalf("CreateService(%q): %v", name, err)
	}
	return r
}

func noLineContains(t *testing.T, lines []string, s string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, s) {
			t.Fatalf("line %q contains %q; region %q", l, s, lines)
		}
	}
}

// customFailStore fails ListCustomServices (or ListChildren) while set.
type customFailStore struct {
	*store.Store
	mu          sync.Mutex
	customErr   error
	childrenErr error
}

func (s *customFailStore) ListCustomServices(ctx context.Context) ([]store.CustomService, error) {
	s.mu.Lock()
	err := s.customErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.Store.ListCustomServices(ctx)
}

func (s *customFailStore) ListChildren(ctx context.Context) ([]store.Child, error) {
	s.mu.Lock()
	err := s.childrenErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.Store.ListChildren(ctx)
}

func TestSyncRules_ReadFailure(t *testing.T) {
	boom := errors.New("database is locked")

	t.Run("active grants", func(t *testing.T) {
		var lf *listFailStore
		e := newEnvStore(t, nil, func(st *store.Store) Store { lf = &listFailStore{Store: st}; return lf })
		e.child("Kid", "kid-phone")
		e.createService("Portal", "a.com")
		before, posts := e.fake.UserRules(), e.setRulesPosts()
		if _, err := e.st.CreateGrant(ctx, 1, []string{"custom:1"}, []string{"kid-phone"}, t0, t0.Add(d)); err != nil {
			t.Fatal(err)
		}
		lf.set(boom)
		if err := e.eng.SyncRules(ctx); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the read error", err)
		}
		if n := e.setRulesPosts() - posts; n != 0 {
			t.Fatalf("%d set_rules POSTs after a failed grants read", n)
		}
		if !reflect.DeepEqual(e.fake.UserRules(), before) {
			t.Fatal("user_rules changed after a failed read")
		}
	})

	for name, set := range map[string]func(*customFailStore){
		"custom services": func(s *customFailStore) { s.customErr = boom },
		"children":        func(s *customFailStore) { s.childrenErr = boom },
	} {
		t.Run(name, func(t *testing.T) {
			var cf *customFailStore
			e := newEnvStore(t, nil, func(st *store.Store) Store { cf = &customFailStore{Store: st}; return cf })
			e.child("Kid", "kid-phone")
			e.createService("Portal", "a.com")
			posts := e.setRulesPosts()
			cf.mu.Lock()
			set(cf)
			cf.mu.Unlock()
			if err := e.eng.SyncRules(ctx); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the read error", err)
			}
			if n := e.setRulesPosts() - posts; n != 0 {
				t.Fatalf("%d set_rules POSTs after a failed read", n)
			}
			// The region keeps the block it had: never rendered empty.
			if got := e.region(); !reflect.DeepEqual(got, []string{"||a.com^$client='kid-phone'"}) {
				t.Fatalf("region %q", got)
			}
		})
	}
}

func TestCreateService_BlocksChildren(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ann", "kid-phone")
	e.child("Ben", "kid-tablet")
	// dad-laptop is a persistent client no child maps: an adult.
	r := e.createService("Portal", "a.com")
	if !r.Applied || r.Service.Name != "Portal" || r.Service.ServiceID() != "custom:1" {
		t.Fatalf("result %+v", r)
	}
	want := []string{"||a.com^$client='kid-phone'|'kid-tablet'"}
	if got := e.region(); !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
	noLineContains(t, e.fake.UserRules(), "dad-laptop")
}

func TestUpdateService_Domains(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone", "kid-tablet")
	r := e.createService("Portal", "a.com", "b.com")
	if _, err := e.st.CreateGrant(ctx, kid.ID, []string{r.Service.ServiceID()}, []string{"kid-phone"}, t0, t0.Add(d)); err != nil {
		t.Fatal(err)
	}

	u, err := e.eng.UpdateService(ctx, r.Service.ID, "Portal", []string{"b.com", "c.com"})
	if err != nil || !u.Applied {
		t.Fatalf("update: %+v %v", u, err)
	}
	want := []string{
		"||b.com^$client='kid-phone'|'kid-tablet'",
		"||c.com^$client='kid-phone'|'kid-tablet'",
		"@@||b.com^$client='kid-phone'",
		"@@||c.com^$client='kid-phone'",
	}
	got := e.region()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q\nwant   %q", got, want)
	}
	noLineContains(t, e.fake.UserRules(), "a.com")

	if _, err := e.eng.UpdateService(ctx, 99, "x", []string{"x.com"}); !errors.Is(err, store.ErrCustomServiceNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestDeleteService(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone")
	foreignBefore := []string{"! mine", "", "||x.example^", "@@||y.example^  "}
	foreignAfter := []string{"# tail", "||z.example^"}
	e.fake.SetUserRules(append(append(slices.Clone(foreignBefore),
		rules.BeginMarker, "||stale^", rules.EndMarker), foreignAfter...))

	e.createService("Keep", "keep.com")
	gone := e.createService("Gone", "a.com", "b.com")

	t.Run("in use", func(t *testing.T) {
		g, err := e.st.CreateGrant(ctx, kid.ID, []string{gone.Service.ServiceID()}, []string{"kid-phone"}, t0, t0.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		posts := e.setRulesPosts()
		_, err = e.eng.DeleteService(ctx, gone.Service.ID)
		var inUse *store.ErrServiceInUse
		if !errors.As(err, &inUse) || inUse.GrantID != g.ID {
			t.Fatalf("err = %v, want ErrServiceInUse{%d}", err, g.ID)
		}
		if n := e.setRulesPosts() - posts; n != 0 {
			t.Fatalf("%d set_rules POSTs on a refused delete", n)
		}
		if ok, err := e.st.SetGrantStatus(ctx, g.ID, store.StatusActive, store.StatusEnded, t0); err != nil || !ok {
			t.Fatal(err)
		}
	})

	r, err := e.eng.DeleteService(ctx, gone.Service.ID)
	if err != nil || !r.Applied || r.Service.Name != "Gone" {
		t.Fatalf("delete: %+v %v", r, err)
	}
	region := e.region()
	noLineContains(t, region, "a.com")
	noLineContains(t, region, "b.com")
	if want := []string{"||keep.com^$client='kid-phone'"}; !reflect.DeepEqual(region, want) {
		t.Fatalf("region %q, want %q (keep's rule only)", region, want)
	}
	if got, want := e.outside(), append(slices.Clone(foreignBefore), foreignAfter...); !reflect.DeepEqual(got, want) {
		t.Fatalf("outside the markers %q\nwant %q", got, want)
	}
}

func TestCreateService_WriteFailure(t *testing.T) {
	t.Run("set_rules fails", func(t *testing.T) {
		e := newEnv(t, nil)
		e.child("Ann", "kid-phone")
		e.fake.SetStatus(setRulesPath, 500)
		r, err := e.eng.CreateService(ctx, "Portal", []string{"a.com"})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if r.Applied {
			t.Fatal("Applied=true after a failed write")
		}
		if got, err := e.st.GetCustomService(ctx, r.Service.ID); err != nil || got.Name != "Portal" {
			t.Fatalf("service not stored: %+v %v", got, err)
		}
		if !strings.Contains(e.logs.String(), "reconciler will retry") {
			t.Errorf("failure not logged: %s", e.logs.String())
		}
	})
	t.Run("malformed region", func(t *testing.T) {
		e := newEnv(t, nil)
		e.child("Ann", "kid-phone")
		seed := []string{"||x^", rules.BeginMarker, "||y^"}
		e.fake.SetUserRules(seed)
		r, err := e.eng.CreateService(ctx, "Portal", []string{"a.com"})
		if err != nil || r.Applied {
			t.Fatalf("result %+v err %v, want Applied=false nil", r, err)
		}
		if n := e.setRulesPosts(); n != 0 {
			t.Fatalf("%d set_rules POSTs into a malformed region", n)
		}
		if !reflect.DeepEqual(e.fake.UserRules(), seed) {
			t.Fatalf("user_rules changed: %q", e.fake.UserRules())
		}
		if err := e.eng.SyncRules(ctx); !errors.Is(err, rules.ErrMalformedRegion) {
			t.Fatalf("SyncRules err = %v, want ErrMalformedRegion", err)
		}
	})
	t.Run("store conflict writes nothing", func(t *testing.T) {
		e := newEnv(t, nil)
		e.createService("Portal", "a.com")
		posts := e.setRulesPosts()
		if _, err := e.eng.CreateService(ctx, "portal", []string{"b.com"}); !errors.Is(err, store.ErrServiceNameTaken) {
			t.Fatalf("err = %v", err)
		}
		if n := e.setRulesPosts() - posts; n != 0 {
			t.Fatalf("%d POSTs after a store conflict", n)
		}
	})
}

func TestSyncRules_Exclude(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone", "tv")
	r := e.createService("Portal", "a.com")
	svc := r.Service.ServiceID()
	ga, err := e.st.CreateGrant(ctx, kid.ID, []string{"youtube", svc}, []string{"kid-phone", "tv"}, t0, t0.Add(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := e.region(), []string{
		"||a.com^$client='kid-phone'|'tv'",
		"@@||a.com^$client='kid-phone'|'tv'",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with grant: %q, want %q", got, want)
	}
	noLineContains(t, e.region(), "youtube")

	e.eng.mu.Lock()
	changed, err := e.eng.syncRules(ctx, ctx, ga.ID)
	e.eng.mu.Unlock()
	if err != nil || !changed {
		t.Fatalf("exclude: changed=%v err=%v", changed, err)
	}
	if got, want := e.region(), []string{"||a.com^$client='kid-phone'|'tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("excluded: %q, want %q", got, want)
	}

	// Converged: a second pass writes nothing.
	posts := e.setRulesPosts()
	e.eng.mu.Lock()
	changed, err = e.eng.syncRules(ctx, ctx, ga.ID)
	e.eng.mu.Unlock()
	if err != nil || changed || e.setRulesPosts() != posts {
		t.Fatalf("idempotent pass: changed=%v err=%v posts=%d", changed, err, e.setRulesPosts()-posts)
	}
}

func TestSyncRules_PermanentCover(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("Ann", "kid-phone", "tv")
	b := e.child("Ben", "b-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.seedToggle(a.ID, svc, store.ToggleUnblocked)
	e.seedToggle(b.ID, svc, store.ToggleBlocked)         // a block adds nothing
	e.seedToggle(a.ID, "youtube", store.ToggleUnblocked) // a built-in is never a cover
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"||a.com^$client='b-phone'|'kid-phone'|'tv'",
		"@@||a.com^$client='kid-phone'|'tv'",
	}
	if got := e.region(); !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
	noLineContains(t, e.region(), "youtube")

	// Ann's grant on the same service comes and goes without touching it.
	before, posts := e.fake.UserRules(), e.setRulesPosts()
	res := e.create(a.ID, []string{svc}, []string{"kid-phone", "tv"})
	e.clk.Advance(d + time.Second)
	e.wantStatus(res.Grant.ID, store.StatusExpired)
	if got := e.fake.UserRules(); !reflect.DeepEqual(got, before) {
		t.Fatalf("user_rules after the grant expired %q, want byte-identical %q", got, before)
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("%d set_rules POSTs across the grant, want 0", n)
	}
}

func TestSyncRules_ToggleReadFailure(t *testing.T) {
	boom := errors.New("database is locked")
	var tf *toggleFailStore
	e := newEnvStore(t, nil, func(st *store.Store) Store { tf = &toggleFailStore{Store: st}; return tf })
	a := e.child("Ann", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.seedToggle(a.ID, svc, store.ToggleUnblocked)
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	before, posts := e.fake.UserRules(), e.setRulesPosts()

	tf.set(boom)
	if err := e.eng.SyncRules(ctx); !errors.Is(err, boom) {
		t.Fatalf("SyncRules err = %v, want the read error", err)
	}
	if _, err := e.eng.Reconcile(ctx); !errors.Is(err, boom) {
		t.Fatalf("Reconcile err = %v, want the read error", err)
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("%d set_rules POSTs after a failed toggles read, want 0", n)
	}
	if !reflect.DeepEqual(e.fake.UserRules(), before) {
		t.Fatalf("user_rules changed after a failed read: %q", e.fake.UserRules())
	}
}

func TestSyncRules_ToggleSurvivesEdit(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("Ann", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	e.seedToggle(a.ID, svc, store.ToggleUnblocked)
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"@@||a.com^$client='kid-phone'"}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions %q, want %q", got, want)
	}

	edited := slices.DeleteFunc(e.fake.UserRules(), func(l string) bool { return strings.HasPrefix(l, "@@") })
	e.fake.SetUserRules(edited)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("setup: exceptions %q after the edit, want none", got)
	}

	if st := e.reconcile(); st.RulesWritten != 1 {
		t.Errorf("RulesWritten = %d, want 1", st.RulesWritten)
	}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions after reconcile %q, want %q", got, want)
	}
}
