package grants

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

// noTimer is an AfterFunc that never fires — the "missed timer" case.
type noTimer struct{}

func (noTimer) Stop() bool { return false }

func withNoTimers(o *Options) {
	o.AfterFunc = func(time.Duration, func()) Timer { return noTimer{} }
}

func (e *env) gets() int { return e.fake.CountRequests("GET", "/control/clients") }

// seedOverdue writes an active grant straight into the store with ends_at in
// the past, as a grant left behind by a previous process would be.
func (e *env) seedOverdue(childID int64, services, clients []string) store.Grant {
	e.t.Helper()
	g, err := e.st.CreateGrant(ctx, childID, services, clients, t0.Add(-2*time.Hour), t0.Add(-time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func (e *env) reconcile() Stats {
	e.t.Helper()
	st, err := e.eng.Reconcile(ctx)
	if err != nil {
		e.t.Fatalf("Reconcile: %v", err)
	}
	return st
}

func TestReconcile_DriftRepaired(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	e.create(1, []string{"tiktok"}, both)
	e.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","tiktok"]`)
	})
	posts, gets := e.posts(), e.gets()

	st := e.reconcile()
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"youtube"}) {
		t.Errorf("Kid phone = %v, want [youtube]", got)
	}
	if got := e.blocked("Kid tablet"); !reflect.DeepEqual(got, []string{"youtube"}) {
		t.Errorf("Kid tablet = %v, want [youtube] untouched", got)
	}
	if st.Repaired != 1 {
		t.Errorf("Stats.Repaired = %d, want 1", st.Repaired)
	}
	if n := e.posts() - posts; n != 1 {
		t.Errorf("pass issued %d update POSTs, want 1 (Kid tablet untouched)", n)
	}
	if n := e.gets() - gets; n != 2 {
		t.Errorf("pass issued %d GET /control/clients, want 2 (pass read + one RMW re-read)", n)
	}
}

func TestReconcile_NoopNoWrites(t *testing.T) {
	e := newEnv(t, nil)
	e.create(1, []string{"tiktok"}, []string{"Kid phone"})
	posts, gets := e.posts(), e.gets()
	st := e.reconcile()
	if n := e.posts() - posts; n != 0 {
		t.Errorf("quiet pass issued %d update POSTs", n)
	}
	if n := e.gets() - gets; n != 1 {
		t.Errorf("quiet pass issued %d GET /control/clients, want exactly 1", n)
	}
	if st.Grants != 1 || st.Reverted != 0 || st.Repaired != 0 || st.Writes != 0 {
		t.Errorf("Stats = %+v", st)
	}

	empty := newEnv(t, nil)
	if st := empty.reconcile(); st != (Stats{}) {
		t.Errorf("Stats with no grants = %+v, want zero", st)
	}
	if n := len(empty.fake.Requests()); n != 0 {
		t.Errorf("no active grants but %d requests were made", n)
	}
}

func TestReconcile_RevertsOverdue(t *testing.T) {
	e := newEnv(t, withNoTimers)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	e.clk.Advance(d)
	if e.status(res.Grant.ID) != store.StatusActive {
		t.Fatal("timer fired; this test needs a missed timer")
	}

	st := e.reconcile()
	for _, name := range both {
		if got := e.blocked(name); !slices.Contains(got, "tiktok") {
			t.Errorf("%s = %v, want tiktok re-blocked", name, got)
		}
	}
	if e.status(res.Grant.ID) != store.StatusExpired {
		t.Errorf("status = %q, want expired", e.status(res.Grant.ID))
	}
	if st.Reverted != 1 {
		t.Errorf("Stats.Reverted = %d, want 1", st.Reverted)
	}
}

func TestReconcile_RevertIdempotent(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	g := e.seedOverdue(1, []string{"tiktok"}, both)
	posts, gets := e.posts(), e.gets()

	e.reconcile()
	if e.status(g.ID) != store.StatusExpired {
		t.Errorf("status = %q, want expired", e.status(g.ID))
	}
	if n := e.posts() - posts; n != 0 {
		t.Errorf("already-reverted grant was re-written %d times", n)
	}
	if n := e.gets() - gets; n != 3 {
		t.Errorf("pass issued %d GET /control/clients, want 3 (pass read + one RMW re-read per client)", n)
	}
}

func TestReconcile_ConvergesPartialApply(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	e.fake.SetUpdateStatus("Kid tablet", 500)
	res := e.create(1, []string{"tiktok"}, both)
	if res.Applied || !reflect.DeepEqual(res.Failed, []string{"Kid tablet"}) {
		t.Fatalf("Applied=%v Failed=%v", res.Applied, res.Failed)
	}
	e.fake.SetUpdateStatus("Kid tablet", 0)
	posts := e.posts()

	e.reconcile()
	if n := e.posts() - posts; n != 1 {
		t.Errorf("pass issued %d update POSTs, want 1", n)
	}
	if got := e.blocked("Kid tablet"); slices.Contains(got, "tiktok") {
		t.Errorf("Kid tablet = %v, want tiktok unblocked", got)
	}
}

func TestReconcile_PassBounded(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.ApplyTimeout = 200 * time.Millisecond })
	res := e.create(1, []string{"tiktok"}, []string{"Kid phone"})
	e.fake.Hang("/control/clients", 5*time.Second)

	done := make(chan error, 1)
	go func() {
		_, err := e.eng.Reconcile(ctx)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	if _, err := e.eng.Create(ctx, 2, []string{"youtube"}, []string{"Kid phone"}, d); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("Create during a hung pass took %v, want < 1.5 s", took)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("Reconcile returned nil against a hung AdGuard, want an error")
		}
	case <-time.After(time.Second):
		t.Fatal("Reconcile did not return within 1 s")
	}
	if e.status(res.Grant.ID) != store.StatusActive {
		t.Errorf("status = %q, want active (no change on a failed pass)", e.status(res.Grant.ID))
	}
}

func TestReconcile_AdGuardDown(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	g := e.seedOverdue(1, []string{"tiktok"}, both)
	e.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube"]`)
	})
	e.fake.SetStatus("/control/clients", 500)
	posts := e.posts()

	if _, err := e.eng.Reconcile(ctx); err == nil {
		t.Fatal("Reconcile err = nil, want an error while AdGuard is down")
	}
	if e.status(g.ID) != store.StatusActive {
		t.Errorf("status = %q, want active", e.status(g.ID))
	}
	if n := e.posts() - posts; n != 0 {
		t.Errorf("failed pass issued %d update POSTs", n)
	}

	e.fake.SetResponse("/control/clients", 0, nil)
	st := e.reconcile()
	if st.Reverted != 1 || e.status(g.ID) != store.StatusExpired {
		t.Errorf("after clearing: Stats=%+v status=%q, want Reverted 1 / expired", st, e.status(g.ID))
	}
	if got := e.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok re-blocked", got)
	}
}

func TestReconcile_EndRace(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	e.fake.Hang("/control/clients", 30*time.Millisecond)

	var wg sync.WaitGroup
	var recErr, endErr error
	wg.Add(2)
	go func() { defer wg.Done(); _, recErr = e.eng.Reconcile(ctx) }()
	go func() { defer wg.Done(); endErr = e.eng.End(ctx, res.Grant.ID) }()
	wg.Wait()
	if recErr != nil || endErr != nil {
		t.Fatalf("Reconcile err = %v, End err = %v", recErr, endErr)
	}
	if e.status(res.Grant.ID) != store.StatusEnded {
		t.Errorf("status = %q, want ended", e.status(res.Grant.ID))
	}
	for _, name := range both {
		if got := e.blocked(name); !slices.Contains(got, "tiktok") {
			t.Errorf("%s = %v, want tiktok re-blocked (pass ran wholly before or after End)", name, got)
		}
	}
}

func TestReconcile_StartArmsTimers(t *testing.T) {
	e := newEnv(t, nil)
	overdue := e.seedOverdue(1, []string{"tiktok"}, []string{"Kid phone"})
	live, err := e.st.CreateGrant(ctx, 2, []string{"youtube"}, []string{"Kid phone"}, t0, t0.Add(d))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture blocks youtube on Kid phone, so the live grant is drifted.
	if err := e.eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.status(overdue.ID) != store.StatusExpired {
		t.Errorf("overdue status = %q, want expired", e.status(overdue.ID))
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Errorf("Kid phone after Start = %v, want [tiktok] (overdue re-blocked, live repaired)", got)
	}
	if n := e.clk.pending(); n != 1 {
		t.Fatalf("%d timers armed after Start, want 1 for the live grant", n)
	}

	e.clk.Advance(d)
	if e.status(live.ID) != store.StatusExpired {
		t.Errorf("live status after its ends_at = %q, want expired via the re-armed timer", e.status(live.ID))
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("Kid phone after timer = %v, want [tiktok youtube]", got)
	}
}

func TestReconcile_MissingClient(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	g := e.seedOverdue(1, []string{"tiktok"}, both)
	e.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube"]`)
	})
	e.fake.RemoveClient("Kid tablet")

	st := e.reconcile()
	if got := e.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok re-blocked", got)
	}
	if e.status(g.ID) != store.StatusExpired || st.Reverted != 1 {
		t.Errorf("status=%q Stats=%+v, want expired / Reverted 1", e.status(g.ID), st)
	}
}

func TestReconcile_RunLoop(t *testing.T) {
	e := newEnv(t, nil)
	e.create(1, []string{"tiktok"}, []string{"Kid phone"})
	e.fake.Hang("/control/clients", 50*time.Millisecond)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.eng.Run(runCtx, 20*time.Millisecond)
	}()
	time.Sleep(300 * time.Millisecond)
	if n := e.gets(); n < 3 {
		t.Errorf("only %d passes ran in 300 ms with a 20 ms interval", n)
	}
	if n := e.fake.MaxInFlightUpdates(); n > 1 {
		t.Errorf("MaxInFlightUpdates = %d, want ≤ 1 (passes must not overlap)", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run did not return within 100 ms of cancel")
	}
}
