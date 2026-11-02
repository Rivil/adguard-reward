package ratelimit

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source shared with the limiter under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var t0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func newTest(t *testing.T) (*Limiter, *clock) {
	t.Helper()
	c := &clock{t: t0}
	cfg := Defaults()
	cfg.Now = c.Now
	return New(cfg), c
}

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

var (
	ipA = ip("203.0.113.1")
	ipB = ip("203.0.113.2")
)

func fail(l *Limiter, a netip.Addr, n int) {
	for i := 0; i < n; i++ {
		l.Fail(a)
	}
}

// trip records five failures and returns the lockout Check now reports.
func trip(t *testing.T, l *Limiter, a netip.Addr) time.Duration {
	t.Helper()
	fail(l, a, 5)
	ok, ra := l.Check(a)
	if ok {
		t.Fatalf("Check(%s) allowed after 5 failures", a)
	}
	return ra
}

func wantCheck(t *testing.T, l *Limiter, a netip.Addr, wantOK bool, wantRA time.Duration) {
	t.Helper()
	ok, ra := l.Check(a)
	if ok != wantOK || ra != wantRA {
		t.Fatalf("Check(%s) = (%v, %v), want (%v, %v)", a, ok, ra, wantOK, wantRA)
	}
}

func TestPerIP_Threshold(t *testing.T) {
	l, _ := newTest(t)
	fail(l, ipA, 4)
	wantCheck(t, l, ipA, true, 0)
	l.Fail(ipA)
	wantCheck(t, l, ipA, false, 60*time.Second)
}

func TestPerIP_Doubling(t *testing.T) {
	l, c := newTest(t)
	want := []time.Duration{
		1 * time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
	}
	for i, w := range want {
		got := trip(t, l, ipA)
		if got != w {
			t.Fatalf("trip #%d lockout = %v, want %v", i+1, got, w)
		}
		c.Advance(got) // unlock exactly at the lockout's end
	}
	for i := len(want) + 1; i <= 40; i++ {
		got := trip(t, l, ipA)
		if got != time.Hour {
			t.Fatalf("trip #%d lockout = %v, want 1h (shift overflow?)", i, got)
		}
		c.Advance(got)
	}
	// lockedUntil is never in the past for a fresh trip: after unlock the IP
	// is allowed again, so no stale lockout lingers.
	wantCheck(t, l, ipA, true, 0)
}

func TestPerIP_Reset(t *testing.T) {
	l, c := newTest(t)
	for _, w := range []time.Duration{time.Minute, 2 * time.Minute} {
		if got := trip(t, l, ipA); got != w {
			t.Fatalf("lockout = %v, want %v", got, w)
		}
		c.Advance(w)
	}
	got := trip(t, l, ipA)
	if got != 4*time.Minute {
		t.Fatalf("third lockout = %v, want 4m", got)
	}
	c.Advance(4*time.Minute + time.Hour) // 1h past the lockout's end
	if got := trip(t, l, ipA); got != time.Minute {
		t.Fatalf("lockout after reset = %v, want 1m (not 8m)", got)
	}
}

func TestPerIP_RetryAfter(t *testing.T) {
	l, c := newTest(t)
	for _, w := range []time.Duration{time.Minute, 2 * time.Minute} {
		trip(t, l, ipA)
		c.Advance(w)
	}
	if got := trip(t, l, ipA); got != 4*time.Minute {
		t.Fatalf("lockout = %v, want 4m", got)
	}
	c.Advance(90 * time.Second)
	wantCheck(t, l, ipA, false, 150*time.Second)
	c.Advance(239200*time.Millisecond - 90*time.Second)
	wantCheck(t, l, ipA, false, time.Second)
}

func TestGlobal_Flat(t *testing.T) {
	l, c := newTest(t)
	for i := 0; i < 20; i++ {
		l.Fail(ip(fmt.Sprintf("198.51.100.%d", i+1)))
	}
	c.Advance(10 * time.Second)
	wantCheck(t, l, ip("192.0.2.9"), false, 50*time.Second)

	c.Advance(50 * time.Second) // window over
	wantCheck(t, l, ip("192.0.2.9"), true, 0)
	for i := 0; i < 20; i++ {
		l.Fail(ip(fmt.Sprintf("198.51.100.%d", i+1)))
	}
	ok, ra := l.Check(ip("192.0.2.10"))
	if ok {
		t.Fatal("21st IP allowed after 20 global failures")
	}
	if ra <= 0 || ra > time.Minute {
		t.Fatalf("global retryAfter = %v, want in (0, 1m] (no doubling)", ra)
	}
}

func TestPerIP_ClearOnUnlock(t *testing.T) {
	l, c := newTest(t)
	got := trip(t, l, ipA)
	c.Advance(got)
	wantCheck(t, l, ipA, true, 0)
	l.Fail(ipA)
	wantCheck(t, l, ipA, true, 0)
	fail(l, ipA, 3)
	wantCheck(t, l, ipA, true, 0)
	l.Fail(ipA)
	wantCheck(t, l, ipA, false, 2*time.Minute)
}

func TestPrune(t *testing.T) {
	l, c := newTest(t)
	for i := 0; i < 100; i++ {
		fail(l, ip(fmt.Sprintf("10.0.%d.%d", i/256, i%256)), 5)
	}
	if got := l.Len(); got != 100 {
		t.Fatalf("Len = %d, want 100", got)
	}
	c.Advance(time.Hour + 2*time.Minute)
	l.Prune()
	if got := l.Len(); got != 0 {
		t.Fatalf("Len after prune = %d, want 0", got)
	}

	// An IP inside a 1h lockout survives.
	l2, c2 := newTest(t)
	for i := 0; i < 7; i++ {
		got := trip(t, l2, ipA)
		c2.Advance(got)
	}
	if got := trip(t, l2, ipA); got != time.Hour {
		t.Fatalf("lockout = %v, want 1h", got)
	}
	c2.Advance(30 * time.Minute)
	l2.Prune()
	if got := l2.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 (locked IP pruned)", got)
	}
}

func TestLimiter_Race(t *testing.T) {
	l, _ := newTest(t)
	ips := []netip.Addr{ip("10.1.0.1"), ip("10.1.0.2"), ip("10.1.0.3"), ip("10.1.0.4"), ip("10.1.0.5")}
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			a := ips[g%len(ips)]
			for i := 0; i < 20; i++ {
				if ok, _ := l.Check(a); ok {
					l.Fail(a)
				}
				l.Len()
			}
		}(g)
	}
	wg.Wait()
}

func TestLimiter_Reset(t *testing.T) {
	t.Run("59m59s past a 4m lockout does not reset", func(t *testing.T) {
		l, c := newTest(t)
		for _, w := range []time.Duration{time.Minute, 2 * time.Minute} {
			trip(t, l, ipA)
			c.Advance(w)
		}
		if got := trip(t, l, ipA); got != 4*time.Minute {
			t.Fatalf("lockout = %v, want 4m", got)
		}
		c.Advance(4*time.Minute + 59*time.Minute + 59*time.Second)
		if got := trip(t, l, ipA); got != 8*time.Minute {
			t.Fatalf("lockout = %v, want 8m", got)
		}
	})
	t.Run("1h past a 4m lockout resets", func(t *testing.T) {
		l, c := newTest(t)
		for _, w := range []time.Duration{time.Minute, 2 * time.Minute} {
			trip(t, l, ipA)
			c.Advance(w)
		}
		if got := trip(t, l, ipA); got != 4*time.Minute {
			t.Fatalf("lockout = %v, want 4m", got)
		}
		c.Advance(4*time.Minute + time.Hour)
		if got := trip(t, l, ipA); got != time.Minute {
			t.Fatalf("lockout = %v, want 1m", got)
		}
	})
	t.Run("cap is sticky", func(t *testing.T) {
		l, c := newTest(t)
		var got time.Duration
		for i := 0; i < 7; i++ {
			got = trip(t, l, ipA)
			c.Advance(got)
		}
		if got != time.Hour {
			t.Fatalf("7th lockout = %v, want 1h", got)
		}
		c.Advance(time.Second) // E+1s
		if got := trip(t, l, ipA); got != time.Hour {
			t.Fatalf("lockout at E+1s = %v, want 1h", got)
		}
	})
}

func TestPerIP_WindowAnchor(t *testing.T) {
	l, c := newTest(t)
	fail(l, ipA, 4)
	c.Advance(59 * time.Second)
	fail(l, ipA, 4)
	wantCheck(t, l, ipA, false, time.Minute)

	l2, c2 := newTest(t)
	fail(l2, ipA, 4)
	c2.Advance(61 * time.Second)
	l2.Fail(ipA)
	wantCheck(t, l2, ipA, true, 0)
}

func TestLimiter_BudgetIsFailuresOnly(t *testing.T) {
	l, _ := newTest(t)
	for i := 0; i < 100; i++ {
		wantCheck(t, l, ipA, true, 0)
	}
	fail(l, ipA, 4)
	for i := 0; i < 50; i++ {
		wantCheck(t, l, ipA, true, 0)
	}
	// Compile-level: the only mutators are Fail and Prune.
	var _ interface {
		Check(netip.Addr) (bool, time.Duration)
		Fail(netip.Addr)
		Prune()
		Len() int
	} = l
}

func TestCeilSeconds(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                        time.Second,
		-time.Second:             time.Second,
		800 * time.Millisecond:   time.Second,
		time.Second:              time.Second,
		1001 * time.Millisecond:  2 * time.Second,
		150 * time.Second:        150 * time.Second,
		59999 * time.Millisecond: 60 * time.Second,
	}
	for in, want := range cases {
		if got := ceilSeconds(in); got != want {
			t.Errorf("ceilSeconds(%v) = %v, want %v", in, got, want)
		}
	}
}
