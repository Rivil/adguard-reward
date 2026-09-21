package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
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
	eng := New(st, ag, opts)
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
