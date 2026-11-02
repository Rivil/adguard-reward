package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
)

// stub is a StatusClient whose answer can be swapped between calls.
type stub struct {
	mu    sync.Mutex
	st    adguard.Status
	err   error
	calls atomic.Int64
	block chan struct{} // when non-nil, Status blocks until it is closed
}

func (s *stub) set(st adguard.Status, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st, s.err = st, err
}

func (s *stub) Status(ctx context.Context) (adguard.Status, error) {
	s.calls.Add(1)
	s.mu.Lock()
	block := s.block
	st, err := s.st, s.err
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return adguard.Status{}, ctx.Err()
		}
	}
	return st, err
}

var (
	okStatus   = adguard.Status{Version: "v0.107.52", Running: true, ProtectionEnabled: true}
	badCreds   = fmt.Errorf("%w: %w", adguard.ErrBadCredentials, &adguard.StatusError{Code: 401, Path: "/control/status"})
	status503  = error(&adguard.StatusError{Method: "GET", Path: "/control/status", Code: 503, Snippet: "upstream down"})
	dialFailed = error(&url.Error{Op: "Get", URL: "http://127.0.0.1:1/control/status", Err: errors.New("connection refused")})
)

func get(t *testing.T, h http.Handler) (*http.Response, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	resp := rec.Result()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("healthz body is not JSON: %v", err)
	}
	return resp, body
}

// waitFor polls cond until it holds or the deadline passes. Tests wait on
// state, never on a wall-clock bound, so a loaded -race run cannot flake.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHandler_OK(t *testing.T) {
	s := &stub{st: okStatus}
	p := New(s, "test-1", slog.New(slog.DiscardHandler))
	p.Probe(context.Background())

	resp, body := get(t, p.Handler())
	if resp.StatusCode != 200 {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	want := map[string]any{"status": "ok", "adguard": "ok", "adguard_version": "v0.107.52", "version": "test-1"}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
}

func TestHandler_Classify(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"bad credentials", badCreds, "unauthorized"},
		{"503", status503, "unreachable"},
		{"dial", dialFailed, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &stub{err: tc.err}
			p := New(s, "test-1", slog.New(slog.DiscardHandler))
			r := p.Probe(context.Background())
			if string(r.State) != tc.want || r.Err == nil {
				t.Errorf("Probe() = %+v, want state %s with err", r, tc.want)
			}
			resp, body := get(t, p.Handler())
			if resp.StatusCode != 200 {
				t.Errorf("status %d, want 200 (healthz never reports AdGuard via the status code)", resp.StatusCode)
			}
			if body["status"] != "ok" || body["adguard"] != tc.want {
				t.Errorf("body = %v, want status ok / adguard %s", body, tc.want)
			}
			if v, ok := body["adguard_version"].(string); !ok || v != "" {
				t.Errorf("adguard_version = %#v, want \"\" (a string, not null)", body["adguard_version"])
			}
		})
	}
}

func TestHandler_BeforeProbe(t *testing.T) {
	p := New(&stub{st: okStatus}, "test-1", slog.New(slog.DiscardHandler))
	resp, body := get(t, p.Handler())
	if resp.StatusCode != 200 || body["adguard"] != "unreachable" {
		t.Errorf("before any probe: status %d body %v, want 200 / adguard unreachable", resp.StatusCode, body)
	}
}

func TestRun_Transitions(t *testing.T) {
	s := &stub{st: okStatus}
	p := New(s, "test-1", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx, 10*time.Millisecond); close(done) }()

	state := func() State { return p.Last().State }
	waitFor(t, "ok", func() bool { return state() == StateOK })
	s.set(adguard.Status{}, status503)
	waitFor(t, "unreachable", func() bool { return state() == StateUnreachable })
	s.set(okStatus, nil)
	waitFor(t, "ok again", func() bool { return state() == StateOK })
	s.set(adguard.Status{}, badCreds)
	waitFor(t, "unauthorized", func() bool { return state() == StateUnauthorized })

	select {
	case <-done:
		t.Fatal("Run exited on unauthorized; it must keep serving")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestHandler_NonBlocking(t *testing.T) {
	s := &stub{st: okStatus, block: make(chan struct{})}
	p := New(s, "test-1", slog.New(slog.DiscardHandler))
	probing := make(chan struct{})
	go func() { close(probing); p.Probe(context.Background()) }()
	<-probing
	waitFor(t, "probe in flight", func() bool { return s.calls.Load() == 1 })

	served := make(chan struct{})
	go func() { get(t, p.Handler()); close(served) }()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("handler blocked behind an in-flight probe")
	}
	close(s.block)
}

func TestHandler_Race(t *testing.T) {
	s := &stub{st: okStatus}
	p := New(s, "test-1", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx, time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rec := httptest.NewRecorder()
				p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
				if j%5 == 0 {
					s.set(okStatus, status503)
				} else {
					s.set(okStatus, nil)
				}
			}
		}()
	}
	wg.Wait()
	cancel()
}

func TestRun_Ticker(t *testing.T) {
	s := &stub{st: okStatus}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	p := New(s, "test-1", log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx, 5*time.Millisecond); close(done) }()

	waitFor(t, "two probes", func() bool { return s.calls.Load() >= 2 })
	s.set(adguard.Status{}, status503)
	waitFor(t, "an error probe", func() bool { return p.Last().State == StateUnreachable })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("v0.107.52")) {
		t.Errorf("Info log lacks the AdGuard version:\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte("upstream down")) {
		t.Errorf("Error log lacks the error text:\n%s", out)
	}
}
