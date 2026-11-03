package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

const svcUser, svcPass = "svc", "pw"

var (
	t0   = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	d    = 30 * time.Minute
	both = []string{"Kid phone", "Kid tablet"}
	ctx  = context.Background()
)

// fakeClock drives Now and AfterFunc. Advance fires every timer due by the
// new time synchronously on the calling goroutine, earliest first, without
// holding the clock lock — a callback may arm a new timer.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	fn      func()
	stopped bool
	fired   bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		if next.at.After(c.now) {
			c.now = next.at
		}
		next.fired = true
		c.mu.Unlock()
		next.fn()
	}
}

// pending reports how many timers are armed and unfired.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t    *testing.T
	fake *adguardtest.Server
	st   *store.Store
	clk  *fakeClock
	logs *lockedBuffer
	eng  *Engine
}

func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	return newEnvStore(t, mod, nil)
}

// newEnvStore is newEnv with the engine's Store wrapped by wrap (nil: the
// real store as is); env.st stays the real store underneath.
func newEnvStore(t *testing.T, mod func(*Options), wrap func(*store.Store) Store) *env {
	t.Helper()
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	ag, err := adguard.New(fake.URL(), svcUser, svcPass)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := &fakeClock{now: t0}
	logs := &lockedBuffer{}
	opts := Options{
		Now:       clk.Now,
		AfterFunc: clk.AfterFunc,
		Log:       slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mod != nil {
		mod(&opts)
	}
	var engStore Store = st
	if wrap != nil {
		engStore = wrap(st)
	}
	eng := New(engStore, ag, opts)
	t.Cleanup(eng.Close)
	return &env{t: t, fake: fake, st: st, clk: clk, logs: logs, eng: eng}
}

// offGlobal takes Kid tablet off the global blocked-services list with its
// own list — the fixture ships it inheriting the global list, under which
// AdGuard ignores the per-client list.
func (e *env) offGlobal(own ...string) {
	list, _ := json.Marshal(own)
	e.fake.MutateClient("Kid tablet", func(m map[string]json.RawMessage) {
		m["use_global_blocked_services"] = json.RawMessage("false")
		m["blocked_services"] = list
	})
}

// blocked returns the fake's stored list for name, sorted.
func (e *env) blocked(name string) []string {
	e.t.Helper()
	ids, ok := e.fake.BlockedServices(name)
	if !ok {
		e.t.Fatalf("fake has no client %q", name)
	}
	slices.Sort(ids)
	return ids
}

func (e *env) posts() int { return e.fake.CountRequests("POST", "/control/clients/update") }

func (e *env) create(childID int64, services, clients []string) Result {
	e.t.Helper()
	res, err := e.eng.Create(ctx, childID, services, clients, d)
	if err != nil {
		e.t.Fatalf("Create(%d, %v, %v): %v", childID, services, clients, err)
	}
	return res
}

func (e *env) status(id int64) string {
	e.t.Helper()
	g, err := e.st.GetGrant(ctx, id)
	if err != nil {
		e.t.Fatalf("GetGrant(%d): %v", id, err)
	}
	return g.Status
}

// updatesFor lists the client names of every recorded update POST.
func (e *env) updatesFor() []string {
	var names []string
	for _, r := range e.fake.Requests() {
		if r.Method != "POST" || r.Path != "/control/clients/update" {
			continue
		}
		var u struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(r.Body, &u)
		names = append(names, u.Name)
	}
	return names
}

func TestEngine_CreateUnblocks(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube")
	res := e.create(1, []string{"youtube"}, both)

	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Errorf("Kid phone = %v, want [tiktok]", got)
	}
	if got := e.blocked("Kid tablet"); len(got) != 0 {
		t.Errorf("Kid tablet = %v, want []", got)
	}
	if !res.Applied || res.Failed == nil || len(res.Failed) != 0 {
		t.Errorf("Applied=%v Failed=%#v, want true / []string{}", res.Applied, res.Failed)
	}
	if !res.Grant.EndsAt.Equal(t0.Add(d)) {
		t.Errorf("EndsAt = %v, want %v", res.Grant.EndsAt, t0.Add(d))
	}
	var phoneBody []byte
	for _, r := range e.fake.Requests() {
		if r.Method == "POST" && r.Path == "/control/clients/update" && bytes.Contains(r.Body, []byte(`"name":"Kid phone"`)) {
			phoneBody = r.Body
		}
	}
	if phoneBody == nil || !bytes.Contains(phoneBody, []byte(`"future_field":42`)) {
		t.Errorf("Kid phone update must carry future_field 42 verbatim, got %s", phoneBody)
	}
}

func TestEngine_CreateNoOpWrite(t *testing.T) {
	e := newEnv(t, nil)
	res := e.create(1, []string{"roblox"}, []string{"Kid phone"})
	if n := e.posts(); n != 0 {
		t.Errorf("update POSTs = %d, want 0 (roblox was not blocked)", n)
	}
	if !res.Applied {
		t.Error("Applied = false, want true")
	}
}

func TestEngine_CreateFailedClients(t *testing.T) {
	t.Run("global list", func(t *testing.T) {
		e := newEnv(t, nil)
		res := e.create(1, []string{"youtube"}, []string{"Kid tablet"})
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"Kid tablet"}) {
			t.Errorf("Applied=%v Failed=%v, want false / [Kid tablet]", res.Applied, res.Failed)
		}
		if n := e.posts(); n != 0 {
			t.Errorf("update POSTs = %d, want 0 for a global-list client", n)
		}
		if e.status(res.Grant.ID) != store.StatusActive {
			t.Error("row not active")
		}
	})
	t.Run("missing client", func(t *testing.T) {
		e := newEnv(t, nil)
		res := e.create(1, []string{"youtube"}, []string{"Ghost"})
		if res.Applied || !reflect.DeepEqual(res.Failed, []string{"Ghost"}) {
			t.Errorf("Applied=%v Failed=%v, want false / [Ghost]", res.Applied, res.Failed)
		}
		if e.status(res.Grant.ID) != store.StatusActive {
			t.Error("row not active")
		}
	})
	t.Run("read failed", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fake.SetStatus("/control/clients", 500)
		res, err := e.eng.Create(ctx, 1, []string{"youtube"}, both, d)
		if err != nil {
			t.Fatalf("Create err = %v, want nil (row is the intent)", err)
		}
		if res.Applied || !reflect.DeepEqual(res.Failed, both) {
			t.Errorf("Applied=%v Failed=%v, want false / %v", res.Applied, res.Failed, both)
		}
		if n := e.posts(); n != 0 {
			t.Errorf("update POSTs = %d, want 0 after a failed read (no blind writes)", n)
		}
		if e.status(res.Grant.ID) != store.StatusActive {
			t.Error("row not active")
		}
	})
}

func TestEngine_PartialApply(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube")
	e.fake.SetUpdateStatus("Kid tablet", 500)
	res, err := e.eng.Create(ctx, 1, []string{"youtube"}, both, d)
	if err != nil {
		t.Fatalf("Create err = %v, want nil", err)
	}
	if res.Applied || !reflect.DeepEqual(res.Failed, []string{"Kid tablet"}) {
		t.Errorf("Applied=%v Failed=%v, want false / [Kid tablet]", res.Applied, res.Failed)
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"tiktok"}) {
		t.Errorf("Kid phone = %v, want [tiktok] (unblocked despite the tablet failing)", got)
	}
	all, err := e.eng.List(ctx)
	if err != nil || len(all) != 1 || all[0].ID != res.Grant.ID {
		t.Errorf("List = %v, %v; want the grant (locked partial_apply: never rolled back)", all, err)
	}
	if !slices.Contains(e.updatesFor(), "Kid tablet") {
		t.Errorf("fake saw updates for %v; want one for Kid tablet (the 500 path, not the global-list path)", e.updatesFor())
	}
}

func TestEngine_CreateDeadline(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.ApplyTimeout = time.Second })
	e.offGlobal("youtube")
	e.fake.Hang("/control/clients/update", 8*time.Second)

	start := time.Now()
	res, err := e.eng.Create(ctx, 1, []string{"youtube"}, both, d)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("Create took %v, want < 1.5 s — ApplyTimeout must bound the whole operation, not each call", took)
	}
	if res.Applied || !reflect.DeepEqual(res.Failed, both) {
		t.Errorf("Applied=%v Failed=%v, want false / %v", res.Applied, res.Failed, both)
	}
	if e.status(res.Grant.ID) != store.StatusActive {
		t.Error("row missing or not active")
	}
}

func TestEngine_ListUnblocked(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.ApplyTimeout = 200 * time.Millisecond })
	e.offGlobal("youtube", "tiktok")
	e.create(1, []string{"tiktok"}, both)
	e.fake.Hang("/control/clients/update", 5*time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.clk.Advance(d) // the revert hangs on the first Add
	}()
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	if _, err := e.eng.List(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("List took %v while a revert hung; must not take mu", took)
	}

	start = time.Now()
	if _, err := e.eng.Create(ctx, 2, []string{"roblox"}, []string{"Kid phone"}, d); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Create took %v; a hung revert may hold mu for at most ApplyTimeout", took)
	}
	<-done
}

func TestEngine_TimerReverts(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	e.fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","roblox"]`)
	})

	e.clk.Advance(d)
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"roblox", "tiktok", "youtube"}) {
		t.Errorf("Kid phone = %v, want [roblox tiktok youtube] (set-union keeps the parent's edit)", got)
	}
	if got := e.blocked("Kid tablet"); !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("Kid tablet = %v, want [tiktok youtube]", got)
	}
	if e.status(res.Grant.ID) != store.StatusExpired {
		t.Errorf("status = %q, want expired", e.status(res.Grant.ID))
	}

	posts := e.posts()
	e.eng.expire(res.Grant.ID)
	if n := e.posts(); n != posts {
		t.Errorf("second expire issued %d more update POSTs, want 0", n-posts)
	}
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, []string{"roblox", "tiktok", "youtube"}) {
		t.Errorf("Kid phone after second expire = %v", got)
	}
}

func TestEngine_RevertFailureStaysActive(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	e.fake.SetUpdateStatus("Kid phone", 500)

	e.clk.Advance(d)
	if e.status(res.Grant.ID) != store.StatusActive {
		t.Fatalf("status = %q, want active after a failed revert", e.status(res.Grant.ID))
	}
	if got := e.blocked("Kid tablet"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid tablet = %v, want tiktok re-blocked", got)
	}
	if got := e.blocked("Kid phone"); slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok still absent (its write failed)", got)
	}
	if logs := e.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "Kid phone") {
		t.Errorf("expected a warn line naming Kid phone, got:\n%s", logs)
	}

	e.fake.SetUpdateStatus("Kid phone", 0)
	e.eng.expire(res.Grant.ID)
	if got := e.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone after retry = %v, want tiktok re-blocked", got)
	}
	if e.status(res.Grant.ID) != store.StatusExpired {
		t.Errorf("status = %q, want expired", e.status(res.Grant.ID))
	}
}

func TestEngine_MissingClientExpires(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	e.fake.RemoveClient("Kid tablet")

	e.clk.Advance(d)
	if got := e.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok re-blocked", got)
	}
	if e.status(res.Grant.ID) != store.StatusExpired {
		t.Errorf("status = %q, want expired (a deleted device must not wedge the grant)", e.status(res.Grant.ID))
	}
}

func TestEngine_ExtendReschedules(t *testing.T) {
	e := newEnv(t, nil)
	res := e.create(1, []string{"tiktok"}, []string{"Kid phone"})
	id := res.Grant.ID

	g, err := e.eng.Extend(ctx, id, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(d + 10*time.Minute); !g.EndsAt.Equal(want) {
		t.Errorf("EndsAt = %v, want %v", g.EndsAt, want)
	}
	posts := e.posts()
	e.clk.Advance(d)
	if e.status(id) != store.StatusActive {
		t.Errorf("status after the original ends_at = %q, want active", e.status(id))
	}
	if n := e.posts(); n != posts {
		t.Errorf("stale timer wrote %d updates, want 0", n-posts)
	}
	e.clk.Advance(10 * time.Minute)
	if e.status(id) != store.StatusExpired {
		t.Errorf("status after the extension = %q, want expired", e.status(id))
	}
	if got := e.blocked("Kid phone"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid phone = %v, want tiktok re-blocked", got)
	}
	if _, err := e.eng.Extend(ctx, id, time.Minute); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("Extend(expired) err = %v, want ErrGrantNotFound", err)
	}
	if _, err := e.eng.Extend(ctx, 999, time.Minute); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("Extend(999) err = %v, want ErrGrantNotFound", err)
	}
}

func TestEngine_EndVsTimerRace(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	id := res.Grant.ID
	e.fake.Hang("/control/clients/update", 20*time.Millisecond)

	var wg sync.WaitGroup
	var endErr error
	wg.Add(2)
	go func() { defer wg.Done(); endErr = e.eng.End(ctx, id) }()
	go func() { defer wg.Done(); e.clk.Advance(d) }()
	wg.Wait()

	status := e.status(id)
	switch {
	case endErr == nil && status != store.StatusEnded:
		t.Errorf("End succeeded but status = %q", status)
	case errors.Is(endErr, ErrGrantNotFound) && status != store.StatusExpired:
		t.Errorf("End lost the race but status = %q", status)
	case endErr != nil && !errors.Is(endErr, ErrGrantNotFound):
		t.Errorf("End err = %v", endErr)
	}
	for _, name := range both {
		got := e.blocked(name)
		if n := countOf(got, "tiktok"); n != 1 {
			t.Errorf("%s = %v, want tiktok exactly once", name, got)
		}
	}
}

func countOf(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}

func TestEngine_EndRevertFailure(t *testing.T) {
	e := newEnv(t, nil)
	e.offGlobal("youtube", "tiktok")
	res := e.create(1, []string{"tiktok"}, both)
	id := res.Grant.ID

	e.fake.SetUpdateStatus("Kid phone", 500)
	err := e.eng.End(ctx, id)
	var rf *ErrRevertFailed
	if !errors.As(err, &rf) || !reflect.DeepEqual(rf.Clients, []string{"Kid phone"}) {
		t.Fatalf("End err = %v, want *ErrRevertFailed{Kid phone}", err)
	}
	if e.status(id) != store.StatusActive {
		t.Errorf("status = %q, want active", e.status(id))
	}
	if got := e.blocked("Kid tablet"); !slices.Contains(got, "tiktok") {
		t.Errorf("Kid tablet = %v, want tiktok re-blocked", got)
	}

	e.fake.SetUpdateStatus("Kid phone", 0)
	if err := e.eng.End(ctx, id); err != nil {
		t.Fatalf("End after clearing the fault: %v", err)
	}
	if e.status(id) != store.StatusEnded {
		t.Fatalf("status = %q, want ended", e.status(id))
	}
	posts := e.posts()
	if err := e.eng.End(ctx, id); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("End(ended) err = %v, want ErrGrantNotFound", err)
	}
	if err := e.eng.End(ctx, 999); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("End(999) err = %v, want ErrGrantNotFound", err)
	}
	if n := e.posts(); n != posts {
		t.Errorf("End on a non-active grant issued %d update POSTs", n-posts)
	}
	e.clk.Advance(d)
	if e.status(id) != store.StatusEnded {
		t.Errorf("status after old ends_at = %q, want ended", e.status(id))
	}
}

func TestEngine_Serialised(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.Hang("/control/clients/update", 30*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(child int64) {
			defer wg.Done()
			res, err := e.eng.Create(ctx, child, []string{"tiktok"}, []string{"Kid phone"}, d)
			if err != nil {
				t.Error(err)
				return
			}
			if err := e.eng.End(ctx, res.Grant.ID); err != nil {
				t.Error(err)
			}
		}(int64(i + 1))
	}
	wg.Wait()
	if n := e.fake.MaxInFlightUpdates(); n != 1 {
		t.Errorf("MaxInFlightUpdates = %d, want 1", n)
	}
	if n := e.clk.pending(); n != 0 {
		t.Errorf("%d timers still armed after every grant ended", n)
	}
}

// Shared clients (locked: shared_device_rule). Children 1 and 2 stand for
// Ada and Ben; the grant rows need no children rows (no FK, locked
// client_set), so only the ownership tests create them.

// setBlocked replaces name's own blocked_services in the fake.
func (e *env) setBlocked(name string, ids ...string) {
	list, _ := json.Marshal(ids)
	e.fake.MutateClient(name, func(m map[string]json.RawMessage) {
		m["blocked_services"] = list
	})
}

func (e *env) end(id int64) {
	e.t.Helper()
	if err := e.eng.End(ctx, id); err != nil {
		e.t.Fatalf("End(%d): %v", id, err)
	}
}

func (e *env) wantStatus(id int64, want string) {
	e.t.Helper()
	if got := e.status(id); got != want {
		e.t.Errorf("grant %d status = %q, want %q", id, got, want)
	}
}

func (e *env) wantBlocks(name, svc string, want bool) {
	e.t.Helper()
	if got := e.blocked(name); slices.Contains(got, svc) != want {
		verb := "lack"
		if want {
			verb = "block"
		}
		e.t.Errorf("%s = %v, want it to %s %s", name, got, verb, svc)
	}
}

var (
	youtube   = []string{"youtube"}
	kidPhone  = []string{"Kid phone"}
	phoneLapt = []string{"Kid phone", "Old laptop"}
)

func TestEngine_SharedExpiryKeepsOther(t *testing.T) {
	e := newEnv(t, nil)
	e.setBlocked("Old laptop", "youtube")
	g1 := e.create(1, youtube, phoneLapt).Grant // ends t0+30m
	e.clk.Advance(10 * time.Minute)
	g2 := e.create(2, youtube, kidPhone).Grant // ends t0+40m

	e.clk.Advance(20 * time.Minute)
	e.wantStatus(g1.ID, store.StatusExpired)
	e.wantStatus(g2.ID, store.StatusActive)
	e.wantBlocks("Old laptop", "youtube", true)
	e.wantBlocks("Kid phone", "youtube", false)

	e.clk.Advance(10 * time.Minute)
	e.wantStatus(g2.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestEngine_SharedEnd(t *testing.T) {
	e := newEnv(t, nil)
	g1 := e.create(1, youtube, kidPhone).Grant
	g2 := e.create(2, youtube, kidPhone).Grant
	posts := e.posts()

	if err := e.eng.End(ctx, g1.ID); err != nil {
		t.Fatalf("End(G1) = %v, want nil", err)
	}
	e.wantStatus(g1.ID, store.StatusEnded)
	e.wantBlocks("Kid phone", "youtube", false)
	if n := e.posts() - posts; n != 0 {
		t.Errorf("End(G1) issued %d update POSTs, want 0 (G2 holds youtube open)", n)
	}

	e.end(g2.ID)
	e.wantStatus(g2.ID, store.StatusEnded)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestEngine_EndSharedOtherService(t *testing.T) {
	e := newEnv(t, nil)
	e.create(1, []string{"tiktok"}, kidPhone)
	ben := e.create(2, youtube, kidPhone).Grant
	if got := e.blocked("Kid phone"); len(got) != 0 {
		t.Fatalf("Kid phone = %v, want [] with both grants applied", got)
	}

	e.end(ben.ID)
	if got := e.blocked("Kid phone"); !reflect.DeepEqual(got, youtube) {
		t.Errorf("Kid phone = %v, want [youtube] (Ada's tiktok grant must not spare youtube)", got)
	}
}

func TestEngine_ExpiredGrantNoCover(t *testing.T) {
	e := newEnv(t, nil)
	g0 := e.create(1, youtube, kidPhone).Grant   // ended below
	gExp := e.create(3, youtube, kidPhone).Grant // expires at t0+30m
	e.clk.Advance(10 * time.Minute)
	g1 := e.create(2, youtube, kidPhone).Grant // expires at t0+40m

	e.end(g0.ID)
	e.clk.Advance(20 * time.Minute)
	e.wantStatus(gExp.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", false) // G1 still live

	e.clk.Advance(10 * time.Minute)
	e.wantStatus(g1.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestEngine_CoveredNoWrite(t *testing.T) {
	e := newEnv(t, nil)
	g1 := e.create(1, youtube, kidPhone).Grant
	e.clk.Advance(10 * time.Minute)
	e.create(2, youtube, kidPhone)
	posts := e.posts()

	e.clk.Advance(20 * time.Minute)
	e.wantStatus(g1.ID, store.StatusExpired)
	if n := e.posts() - posts; n != 0 {
		t.Errorf("G1's expiry issued %d update POSTs, want 0 (Kid phone fully held open by G2)", n)
	}
	e.wantBlocks("Kid phone", "youtube", false)
}

// listFailStore fails ListActiveGrants while err is set.
type listFailStore struct {
	*store.Store
	mu  sync.Mutex
	err error
}

func (s *listFailStore) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *listFailStore) ListActiveGrants(ctx context.Context) ([]store.Grant, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.Store.ListActiveGrants(ctx)
}

func TestEnd_ActiveReadFails(t *testing.T) {
	var fs *listFailStore
	e := newEnvStore(t, nil, func(st *store.Store) Store {
		fs = &listFailStore{Store: st}
		return fs
	})
	e.setBlocked("Old laptop", "youtube")
	g := e.create(1, youtube, phoneLapt).Grant
	posts := e.posts()
	fs.set(errors.New("disk on fire"))

	err := e.eng.End(ctx, g.ID)
	var rf *ErrRevertFailed
	if !errors.As(err, &rf) || !reflect.DeepEqual(rf.Clients, g.Clients) {
		t.Fatalf("End err = %v, want *ErrRevertFailed naming every client %v", err, g.Clients)
	}
	if n := e.posts() - posts; n != 0 {
		t.Errorf("End issued %d update POSTs after a failed read, want 0 (never re-block blind)", n)
	}
	e.wantStatus(g.ID, store.StatusActive)
	if n := e.clk.pending(); n != 1 {
		t.Errorf("%d timers armed, want the grant's timer kept", n)
	}

	// The timer path fails the same way and leaves the grant for a retry.
	e.clk.Advance(d)
	e.wantStatus(g.ID, store.StatusActive)
	if n := e.posts() - posts; n != 0 {
		t.Errorf("expiry issued %d update POSTs after a failed read, want 0", n)
	}
	e.wantBlocks("Kid phone", "youtube", false)

	fs.set(nil)
	e.end(g.ID)
	e.wantStatus(g.ID, store.StatusEnded)
	e.wantBlocks("Kid phone", "youtube", true)
	e.wantBlocks("Old laptop", "youtube", true)
}

func TestExpire_ClientUnassigned(t *testing.T) {
	e := newEnv(t, nil)
	ada, err := e.st.CreateChild(ctx, "Ada", kidPhone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateChild(ctx, "Ben", kidPhone); err != nil {
		t.Fatal(err)
	}
	g := e.create(ada.ID, youtube, kidPhone).Grant
	if _, err := e.st.UpdateChild(ctx, ada.ID, "Ada", nil); err != nil {
		t.Fatal(err)
	}

	e.clk.Advance(d)
	e.wantStatus(g.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestExpire_HolderUnassigned(t *testing.T) {
	e := newEnv(t, nil)
	ada, err := e.st.CreateChild(ctx, "Ada", kidPhone)
	if err != nil {
		t.Fatal(err)
	}
	ben, err := e.st.CreateChild(ctx, "Ben", kidPhone)
	if err != nil {
		t.Fatal(err)
	}
	gBen := e.create(ben.ID, youtube, kidPhone).Grant // ends t0+30m
	e.clk.Advance(10 * time.Minute)
	gAda := e.create(ada.ID, youtube, kidPhone).Grant // ends t0+40m
	if ok, err := e.st.DeleteChild(ctx, ada.ID); err != nil || !ok {
		t.Fatalf("DeleteChild(Ada) = %v, %v", ok, err)
	}

	e.clk.Advance(20 * time.Minute)
	e.wantStatus(gBen.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", false) // Ada's stored grant still holds it

	e.clk.Advance(10 * time.Minute)
	e.wantStatus(gAda.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
}

func TestExpire_SharedTimersSerial(t *testing.T) {
	e := newEnv(t, withNoTimers)
	e.setBlocked("Old laptop", "youtube")
	g1 := e.create(1, youtube, phoneLapt).Grant
	g2 := e.create(2, youtube, kidPhone).Grant
	e.clk.Advance(d) // both overdue; no timer fires
	e.fake.Hang("/control/clients/update", 30*time.Millisecond)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range []int64{g1.ID, g2.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			e.eng.expire(id)
		}()
	}
	close(start)
	wg.Wait()

	e.wantStatus(g1.ID, store.StatusExpired)
	e.wantStatus(g2.ID, store.StatusExpired)
	e.wantBlocks("Kid phone", "youtube", true)
	e.wantBlocks("Old laptop", "youtube", true)
	if n := e.fake.MaxInFlightUpdates(); n != 1 {
		t.Errorf("MaxInFlightUpdates = %d, want 1", n)
	}
}

// exceptions returns the @@ lines of the managed region.
func (e *env) exceptions() []string {
	e.t.Helper()
	var out []string
	for _, l := range e.region() {
		if strings.HasPrefix(l, "@@") {
			out = append(out, l)
		}
	}
	return out
}

// noCustomInClientWrites fails if any clients/update or clients/add body
// names a custom service.
func (e *env) noCustomInClientWrites() {
	e.t.Helper()
	for _, r := range e.fake.Requests() {
		if r.Method == "POST" && (r.Path == "/control/clients/update" || r.Path == "/control/clients/add") &&
			strings.Contains(string(r.Body), "custom:") {
			e.t.Fatalf("%s body leaks a custom id: %s", r.Path, r.Body)
		}
	}
}

func TestGrant_CustomNoLeak(t *testing.T) {
	e := newEnv(t, nil)
	// Real fixture clients, each on its own list: a custom id leaking into
	// the blocked_services path would show up as a clients/update POST.
	e.offGlobal("youtube")
	kid := e.child("Ann", "Kid phone", "Kid tablet")
	svc := e.createService("Portal", "a.com", "b.com").Service.ServiceID()

	res := e.create(kid.ID, []string{svc}, both)
	if !res.Applied || len(res.Failed) != 0 {
		t.Fatalf("create: %+v", res)
	}
	want := []string{"@@||a.com^$client='Kid phone'|'Kid tablet'", "@@||b.com^$client='Kid phone'|'Kid tablet'"}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions %q, want %q", got, want)
	}
	if n := e.posts(); n != 0 {
		t.Fatalf("%d clients/update POSTs on create", n)
	}

	e.end(res.Grant.ID)
	e.wantStatus(res.Grant.ID, store.StatusEnded)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after End: %q", got)
	}

	res2 := e.create(kid.ID, []string{svc}, both)
	e.clk.Advance(d)
	e.wantStatus(res2.Grant.ID, store.StatusExpired)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after expiry: %q", got)
	}
	if n := e.posts(); n != 0 {
		t.Fatalf("%d clients/update POSTs across create, end and expiry", n)
	}
	e.noCustomInClientWrites()
}

func TestGrant_Mixed(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "Kid phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()

	res := e.create(kid.ID, []string{"youtube", svc}, []string{"Kid phone"})
	if !res.Applied {
		t.Fatalf("create: %+v", res)
	}
	e.wantBlocks("Kid phone", "youtube", false)
	if got, want := e.exceptions(), []string{"@@||a.com^$client='Kid phone'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions %q, want %q", got, want)
	}

	e.end(res.Grant.ID)
	e.wantBlocks("Kid phone", "youtube", true)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after End: %q", got)
	}
	e.noCustomInClientWrites()
}

func TestGrant_CustomShared(t *testing.T) {
	e := newEnv(t, nil)
	a := e.child("Ann", "a-phone", "tv")
	b := e.child("Ben", "b-phone", "tv")
	svc := e.createService("Portal", "a.com").Service.ServiceID()

	ga := e.create(a.ID, []string{svc}, []string{"a-phone", "tv"})
	gb := e.create(b.ID, []string{svc}, []string{"b-phone", "tv"})
	if got, want := e.exceptions(), []string{"@@||a.com^$client='a-phone'|'b-phone'|'tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("both: %q, want %q", got, want)
	}

	e.end(ga.Grant.ID)
	if got, want := e.exceptions(), []string{"@@||a.com^$client='b-phone'|'tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after End A: %q, want %q", got, want)
	}

	e.clk.Advance(d)
	e.wantStatus(gb.Grant.ID, store.StatusExpired)
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("after B expired: %q", got)
	}
	if n := e.posts(); n != 0 {
		t.Fatalf("%d clients/update POSTs", n)
	}
}

func TestGrant_CustomPreservesOutside(t *testing.T) {
	e := newEnv(t, nil)
	seed := []string{"! mine", "||x^", "@@||y^"}
	e.fake.SetUserRules(seed)
	kid := e.child("Ann", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	check := func(step string) {
		t.Helper()
		if got := e.outside(); !reflect.DeepEqual(got, seed) {
			t.Fatalf("after %s, outside the markers %q, want %q", step, got, seed)
		}
	}
	check("service create")

	res := e.create(kid.ID, []string{svc}, []string{"kid-phone"})
	check("grant create")

	posts := e.setRulesPosts()
	if _, err := e.eng.Extend(ctx, res.Grant.ID, d); err != nil {
		t.Fatal(err)
	}
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("Extend issued %d set_rules POSTs", n)
	}
	check("extend")

	e.end(res.Grant.ID)
	check("end")

	res2 := e.create(kid.ID, []string{svc}, []string{"kid-phone"})
	e.clk.Advance(2 * d)
	e.wantStatus(res2.Grant.ID, store.StatusExpired)
	check("expiry")
}

func TestGrant_CustomWriteFailure(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone", "tv")
	svc := e.createService("Portal", "a.com").Service.ServiceID()

	e.fake.SetStatus(setRulesPath, 500)
	res := e.create(kid.ID, []string{svc}, []string{"tv", "kid-phone"})
	if res.Applied || !reflect.DeepEqual(res.Failed, res.Grant.Clients) {
		t.Fatalf("create under a set_rules fault: %+v, want Failed == %v", res, res.Grant.Clients)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)

	e.fake.SetResponse(setRulesPath, 0, nil)
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"@@||a.com^$client='kid-phone'|'tv'"}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions %q", got)
	}

	e.fake.SetStatus(setRulesPath, 500)
	err := e.eng.End(ctx, res.Grant.ID)
	var rf *ErrRevertFailed
	if !errors.As(err, &rf) || !reflect.DeepEqual(rf.Clients, res.Grant.Clients) {
		t.Fatalf("End: %v, want *ErrRevertFailed over every client", err)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions after a failed End: %q, want %q", got, want)
	}
}

func TestGrant_UnknownCustom(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone")
	e.createService("Portal", "a.com")
	for _, id := range []string{"custom:99", "custom:01", "custom:abc", "custom:"} {
		_, err := e.eng.Create(ctx, kid.ID, []string{"youtube", id}, []string{"kid-phone"}, d)
		var unknown *ErrUnknownService
		if !errors.As(err, &unknown) || unknown.ID != id {
			t.Fatalf("%s: err = %v, want *ErrUnknownService{%s}", id, err, id)
		}
	}
	if got := builtin([]string{"youtube", "custom:1", "custom:01", "custom:abc", "tiktok"}); !reflect.DeepEqual(got, []string{"youtube", "tiktok"}) {
		t.Fatalf("builtin kept a custom-namespace id: %q", got)
	}
	if gs, err := e.st.ListActiveGrants(ctx); err != nil || len(gs) != 0 {
		t.Fatalf("grants stored: %+v %v", gs, err)
	}
	if n := e.posts() + e.setRulesPosts(); n != 1 { // the one set_rules from createService
		t.Fatalf("AdGuard writes after a refused create: %d", n)
	}
}

func TestDeleteService_RaceGrant(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone")
	for i := range 20 {
		svc := e.createService(fmt.Sprintf("S%d", i), fmt.Sprintf("s%d.example", i)).Service
		var (
			wg      sync.WaitGroup
			delErr  error
			grantEr error
		)
		wg.Add(2)
		go func() { defer wg.Done(); _, delErr = e.eng.DeleteService(ctx, svc.ID) }()
		go func() {
			defer wg.Done()
			_, grantEr = e.eng.Create(ctx, kid.ID, []string{svc.ServiceID()}, []string{"kid-phone"}, d)
		}()
		wg.Wait()

		var inUse *store.ErrServiceInUse
		var unknown *ErrUnknownService
		_, getErr := e.st.GetCustomService(ctx, svc.ID)
		switch {
		case grantEr == nil && errors.As(delErr, &inUse) && getErr == nil:
		case delErr == nil && errors.As(grantEr, &unknown) && errors.Is(getErr, store.ErrCustomServiceNotFound):
		default:
			t.Fatalf("round %d: delete=%v grant=%v service-read=%v", i, delErr, grantEr, getErr)
		}
	}
	active, err := e.st.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range active {
		for _, id := range g.Services {
			n, _ := store.ParseCustomID(id)
			if _, err := e.st.GetCustomService(ctx, n); err != nil {
				t.Fatalf("grant %d covers deleted service %s", g.ID, id)
			}
		}
	}
}

func TestGrant_MixedEndPartialFailure(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "Kid phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	res := e.create(kid.ID, []string{"youtube", svc}, []string{"Kid phone"})
	want := []string{"@@||a.com^$client='Kid phone'"}

	// The region write fails first: nothing else is attempted.
	e.fake.SetStatus(setRulesPath, 500)
	posts := e.posts()
	var rf *ErrRevertFailed
	if err := e.eng.End(ctx, res.Grant.ID); !errors.As(err, &rf) {
		t.Fatalf("End under a set_rules fault: %v", err)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)
	e.wantBlocks("Kid phone", "youtube", false)
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions %q, want %q", got, want)
	}
	if n := e.posts() - posts; n != 0 {
		t.Fatalf("%d clients/update POSTs after the region write failed", n)
	}
	e.fake.SetResponse(setRulesPath, 0, nil)

	// The region write succeeds, the built-in revert fails.
	e.fake.SetUpdateStatus("Kid phone", 500)
	if err := e.eng.End(ctx, res.Grant.ID); !errors.As(err, &rf) {
		t.Fatalf("End under a clients/update fault: %v", err)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)
	e.fake.SetUpdateStatus("Kid phone", 0)
	// The region was rewritten without the grant before the revert failed.
	if got := e.exceptions(); len(got) != 0 {
		t.Fatalf("exceptions after the region write: %q, want none", got)
	}

	// The grant is still active, so the next convergence pass (the
	// reconciler's SyncRules) restores its exceptions from intent.
	if err := e.eng.SyncRules(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.exceptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exceptions after re-convergence %q, want %q", got, want)
	}
}

func TestGrant_EndMalformedRegion(t *testing.T) {
	e := newEnv(t, nil)
	kid := e.child("Ann", "kid-phone")
	svc := e.createService("Portal", "a.com").Service.ServiceID()
	res := e.create(kid.ID, []string{svc}, []string{"kid-phone"})

	broken := []string{"||x^", rules.BeginMarker, "@@||a.com^$client='kid-phone'"}
	e.fake.SetUserRules(broken)
	posts := e.setRulesPosts()
	err := e.eng.End(ctx, res.Grant.ID)
	var rf *ErrRevertFailed
	if !errors.As(err, &rf) {
		t.Fatalf("err = %v, want *ErrRevertFailed", err)
	}
	if !strings.Contains(err.Error(), "managed rules region is malformed") || !errors.Is(err, rules.ErrMalformedRegion) {
		t.Fatalf("message %q does not name the malformed region", err)
	}
	e.wantStatus(res.Grant.ID, store.StatusActive)
	if n := e.setRulesPosts() - posts; n != 0 {
		t.Fatalf("%d set_rules POSTs into a malformed region", n)
	}
	if !reflect.DeepEqual(e.fake.UserRules(), broken) {
		t.Fatalf("user_rules changed: %q", e.fake.UserRules())
	}
}
