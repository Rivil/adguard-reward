package grants

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/rules"
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
	// With no grants the pass still converges the managed region: one
	// user_rules read, no clients read and — nothing to manage — no write.
	reqs := empty.fake.Requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Path != "/control/filtering/status" {
		t.Errorf("no active grants: requests %+v, want only GET /control/filtering/status", reqs)
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

// Log lines Run emits. "reconcile failed" carries a space so slog quotes it;
// the trailing space on logPass therefore cannot match it, which keeps the
// two assertions independent (and "startup reconcile" out of both).
const (
	logFailed = `msg="reconcile failed"`
	logPass   = "msg=reconcile "
)

// runUntil runs the engine at a 20 ms interval until until() holds or d
// passes, then cancels and waits for Run to return. It returns the whole log
// buffer, read after Run has stopped writing to it.
func (e *env) runUntil(until func() bool, d time.Duration) string {
	e.t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.eng.Run(runCtx, 20*time.Millisecond)
	}()
	for deadline := time.Now().Add(d); time.Now().Before(deadline) && !until(); {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		e.t.Fatal("Run did not return within 1 s of cancel")
	}
	return e.logs.String()
}

// TestReconcile_RunLogs pins which log line each pass outcome produces. Run's
// switch is pure reporting, so only the log tells a failed pass from a quiet
// one — without these the conditions can be inverted or their boundaries
// widened and every other test still passes.
func TestReconcile_RunLogs(t *testing.T) {
	// A failing pass is reported, not swallowed.
	t.Run("failed", func(t *testing.T) {
		e := newEnv(t, nil)
		e.create(1, []string{"tiktok"}, []string{"Kid phone"})
		e.fake.SetStatus("/control/clients", 500)

		logs := e.runUntil(func() bool { return strings.Contains(e.logs.String(), logFailed) }, 300*time.Millisecond)
		if !strings.Contains(logs, logFailed) {
			t.Fatalf("a failing pass logged no %s line:\n%s", logFailed, logs)
		}
	})

	// A pass that fails only because the process is shutting down is not an
	// error — cancellation is the expected way Run ends.
	t.Run("cancelled", func(t *testing.T) {
		e := newEnv(t, nil)
		e.create(1, []string{"tiktok"}, []string{"Kid phone"})
		e.fake.Hang("/control/clients", 200*time.Millisecond)

		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.eng.Run(runCtx, 20*time.Millisecond)
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run did not return within 1 s of cancel")
		}
		if logs := e.logs.String(); strings.Contains(logs, logFailed) {
			t.Fatalf("cancellation logged %s:\n%s", logFailed, logs)
		}
	})

	// Nothing to report means nothing is logged — a per-interval heartbeat
	// would bury the lines that matter.
	t.Run("quiet", func(t *testing.T) {
		e := newEnv(t, nil)
		e.create(1, []string{"tiktok"}, []string{"Kid phone"})
		gets := e.gets()

		logs := e.runUntil(func() bool { return e.gets()-gets >= 3 }, time.Second)
		if n := e.gets() - gets; n < 3 {
			t.Fatalf("only %d passes ran; the quiet assertion needs at least 3", n)
		}
		if strings.Contains(logs, logFailed) || strings.Contains(logs, logPass) {
			t.Fatalf("%d consistent passes logged:\n%s", e.gets()-gets, logs)
		}
	})

	// A revert is reported once, with its own counter set and the other zero.
	t.Run("reverted", func(t *testing.T) {
		e := newEnv(t, withNoTimers)
		e.offGlobal("youtube", "tiktok")
		res := e.create(1, []string{"tiktok"}, both)
		e.clk.Advance(d)
		if e.status(res.Grant.ID) != store.StatusActive {
			t.Fatal("timer fired; this test needs a missed timer")
		}

		logs := e.runUntil(func() bool { return strings.Contains(e.logs.String(), logPass) }, time.Second)
		if n := strings.Count(logs, logPass); n != 1 {
			t.Fatalf("revert logged %d %s lines, want exactly 1:\n%s", n, logPass, logs)
		}
		if !strings.Contains(logs, "reverted=1") || !strings.Contains(logs, "repaired=0") {
			t.Fatalf("want a line with reverted=1 repaired=0:\n%s", logs)
		}
	})

	// So is a repair — the other operand of the same condition.
	t.Run("repaired", func(t *testing.T) {
		e := newEnv(t, nil)
		e.create(1, []string{"tiktok"}, []string{"Kid phone"})
		e.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
			m["blocked_services"] = json.RawMessage(`["youtube","tiktok"]`)
		})

		logs := e.runUntil(func() bool { return strings.Contains(e.logs.String(), logPass) }, time.Second)
		if n := strings.Count(logs, logPass); n != 1 {
			t.Fatalf("repair logged %d %s lines, want exactly 1:\n%s", n, logPass, logs)
		}
		if !strings.Contains(logs, "repaired=1") || !strings.Contains(logs, "reverted=0") {
			t.Fatalf("want a line with repaired=1 reverted=0:\n%s", logs)
		}
	})
}

func TestReconcile_BothOverdueShared(t *testing.T) {
	e := newEnv(t, nil)
	// Both grants applied, then the process was down past both ends_at.
	e.setBlocked("Kid phone", "tiktok")
	g1 := e.seedOverdue(1, youtube, kidPhone)
	g2 := e.seedOverdue(2, youtube, kidPhone)

	st := e.reconcile()
	if st.Reverted != 2 {
		t.Errorf("Stats.Reverted = %d, want 2", st.Reverted)
	}
	e.wantStatus(g1.ID, store.StatusExpired)
	e.wantStatus(g2.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestReconcile_OverdueSharedKeepsOther(t *testing.T) {
	e := newEnv(t, nil)
	ada := e.create(1, youtube, kidPhone).Grant
	ben := e.seedOverdue(2, youtube, kidPhone)

	st := e.reconcile()
	if st.Reverted != 1 {
		t.Errorf("Stats.Reverted = %d, want 1", st.Reverted)
	}
	e.wantStatus(ben.ID, store.StatusExpired)
	e.wantStatus(ada.ID, store.StatusActive)
	e.wantBlocks("Kid phone", "youtube", false)
}

func TestReconcile_WritesCountsActual(t *testing.T) {
	e := newEnv(t, nil)
	e.setBlocked("Old laptop", "tiktok")
	e.create(2, youtube, kidPhone) // G2, live, applied: no drift
	e.seedOverdue(1, youtube, phoneLapt)
	before := len(e.updatesFor())

	st := e.reconcile()
	if st.Writes != 1 {
		t.Errorf("Stats.Writes = %d, want 1 (Old laptop only; Kid phone is held open by G2)", st.Writes)
	}
	if got := e.updatesFor()[before:]; !reflect.DeepEqual(got, []string{"Old laptop"}) {
		t.Errorf("pass wrote %v, want [Old laptop]", got)
	}
	e.wantBlocks("Old laptop", "youtube", true)
	e.wantBlocks("Kid phone", "youtube", false)
}

func TestReconcile_RegionNoGrants(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ann", "kid-phone")
	// Stored straight into the store: nothing has written the region yet.
	if _, err := e.st.CreateCustomService(ctx, "Portal", []string{"a.com"}); err != nil {
		t.Fatal(err)
	}
	st := e.reconcile()
	if st.RulesWritten != 1 {
		t.Fatalf("Stats %+v, want RulesWritten 1", st)
	}
	want := []string{rules.BeginMarker, "||a.com^$client='kid-phone'", rules.EndMarker}
	if got := e.fake.UserRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("user_rules %q, want %q", got, want)
	}
	if n := e.gets(); n != 0 {
		t.Errorf("no grants but %d clients reads", n)
	}
}

func TestReconcile_RegionTamper(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ann", "kid-phone")
	e.createService("Portal", "a.com", "b.com")
	want := e.region()

	// A manual edit inside the markers: a block line gone, an exception
	// added; plus a parent's own line outside them.
	all := e.fake.UserRules()
	tampered := []string{"! mine"}
	for _, l := range all {
		if l == "||a.com^$client='kid-phone'" {
			continue
		}
		tampered = append(tampered, l)
		if l == rules.BeginMarker {
			tampered = append(tampered, "@@||a.com^$client='kid-phone'")
		}
	}
	tampered = append(tampered, "||outside.example^")
	e.fake.SetUserRules(tampered)

	if st := e.reconcile(); st.RulesWritten != 1 {
		t.Fatalf("Stats %+v, want RulesWritten 1", st)
	}
	if got := e.region(); !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want restored %q", got, want)
	}
	if got, w := e.outside(), []string{"! mine", "||outside.example^"}; !reflect.DeepEqual(got, w) {
		t.Fatalf("outside the markers %q, want %q", got, w)
	}

	posts := e.setRulesPosts()
	if st := e.reconcile(); st.RulesWritten != 0 {
		t.Fatalf("second pass Stats %+v, want RulesWritten 0", st)
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("converged pass issued %d set_rules POSTs", n)
	}
}

func TestReconcile_RegionClientsDown(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone")
	r := e.createService("Portal", "a.com")
	e.create(kid.ID, []string{r.Service.ServiceID()}, []string{"kid-phone"})
	want := e.region()
	e.fake.SetUserRules([]string{rules.BeginMarker, "||stale^", rules.EndMarker})

	e.fake.SetStatus("/control/clients", http.StatusBadGateway)
	st, err := e.eng.Reconcile(ctx)
	if err == nil {
		t.Fatal("Reconcile: nil error with /control/clients down")
	}
	if st.RulesWritten != 1 {
		t.Fatalf("Stats %+v, want RulesWritten 1", st)
	}
	if got := e.region(); !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q despite the clients outage", got, want)
	}
}

func TestReconcile_OverdueCustom(t *testing.T) {
	e := newEnv(t, withNoTimers)
	kid := e.child("Ann", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	g := e.seedOverdue(kid.ID, []string{svc}, []string{"kid-phone"})
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.exceptions()) != 1 {
		t.Fatalf("setup: exceptions %q", e.exceptions())
	}

	st := e.reconcile()
	if st.Reverted != 1 {
		t.Fatalf("Stats %+v, want one reverted", st)
	}
	e.wantStatus(g.ID, store.StatusExpired)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after the pass %q, want none", got)
	}
	if got, want := e.region(), []string{"||a.com^$client='kid-phone'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
}

func TestReconcile_AppliedFalse(t *testing.T) {
	e := newEnv(t, nil)
	e.child("Ann", "kid-phone")
	e.fake.SetStatus(setRulesPath, http.StatusInternalServerError)
	r, err := e.eng.CreateService(ctx, "Portal", []string{"a.com"})
	if err != nil || r.Applied {
		t.Fatalf("setup: %+v %v, want Applied=false", r, err)
	}
	e.fake.SetResponse(setRulesPath, 0, nil)

	if st := e.reconcile(); st.RulesWritten != 1 {
		t.Fatalf("Stats %+v, want RulesWritten 1", st)
	}
	if got, want := e.region(), []string{"||a.com^$client='kid-phone'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("region %q, want %q", got, want)
	}
}

func TestReconcile_Malformed(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	kid := e.child("Ann", "Kid phone", "Kid tablet")
	e.createService("Portal", "a.com")
	g := e.create(kid.ID, []string{"youtube"}, both)
	e.setBlocked("Kid tablet", "youtube", "tiktok") // drift: re-blocked in AdGuard's UI

	broken := []string{"||x^", rules.BeginMarker, "||y^"}
	e.fake.SetUserRules(broken)
	posts := e.setRulesPosts()

	st, err := e.eng.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v, want nil (malformed region is logged, not fatal)", err)
	}
	if st.Repaired != 1 || st.RulesWritten != 0 {
		t.Fatalf("Stats %+v, want the drift repaired and no rules write", st)
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("%d set_rules POSTs into a malformed region", n)
	}
	if !reflect.DeepEqual(e.fake.UserRules(), broken) {
		t.Fatalf("user_rules changed: %q", e.fake.UserRules())
	}
	if logs := e.logs.String(); !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "malformed") {
		t.Fatalf("malformed region not logged at error:\n%s", logs)
	}
	e.wantBlocks("Kid tablet", "youtube", false)
	e.wantStatus(g.Grant.ID, store.StatusActive)
}

func TestReconcile_HalfEndedGrant(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "Kid phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	res := e.create(kid.ID, []string{"youtube", svc}, []string{"Kid phone"})
	want := e.exceptions()
	if len(want) != 1 {
		t.Fatalf("setup: exceptions %q", want)
	}

	e.fake.SetUpdateStatus("Kid phone", http.StatusInternalServerError)
	var rf *ErrRevertFailed
	if err := e.eng.End(ctx, res.Grant.ID); !errors.As(err, &rf) {
		t.Fatalf("End: %v, want *ErrRevertFailed", err)
	}
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("after the half-failed End: exceptions %q, want none", got)
	}
	e.fake.SetUpdateStatus("Kid phone", 0)

	if st := e.reconcile(); st.RulesWritten != 1 {
		t.Fatalf("Stats %+v, want RulesWritten 1", st)
	}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions after one pass %q, want %q", got, want)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)
}
