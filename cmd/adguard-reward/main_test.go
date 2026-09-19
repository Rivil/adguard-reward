package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
)

const (
	svcUser = "svc"
	svcPass = "pw-CANARY-9f3a"
	aiKey   = "sk-CANARY-77b1"
)

// syncBuffer is a bytes.Buffer safe for the prober goroutine and the test to
// share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// envOf builds a lookupEnv over a fixed map so tests never touch the process
// environment.
func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// writeConfig writes a minimal valid config pointing at fake, with extra
// appended verbatim, and returns its path.
func writeConfig(t *testing.T, fakeURL, extra string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("listen: \"127.0.0.1:0\"\nadguard:\n  url: %q\n  username: %q\n  password: %q\n%s", fakeURL, svcUser, svcPass, extra)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runResult is what a started run reports back.
type runResult struct {
	code   int
	addr   string
	stderr *syncBuffer
	done   chan struct{}
	cancel context.CancelFunc
}

// start runs run() in a goroutine and waits until it is listening. It fails
// the test if run returns before onListen fires.
func start(t *testing.T, args []string, env map[string]string) *runResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runResult{stderr: &syncBuffer{}, done: make(chan struct{}), cancel: cancel}
	listening := make(chan string, 1)
	go func() {
		r.code = run(ctx, args, envOf(env), r.stderr, func(addr string) { listening <- addr })
		close(r.done)
	}()
	select {
	case r.addr = <-listening:
	case <-r.done:
		t.Fatalf("run returned %d before listening; stderr:\n%s", r.code, r.stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for onListen")
	}
	t.Cleanup(func() {
		cancel()
		<-r.done
	})
	return r
}

// stop cancels the context and returns run's exit code.
func (r *runResult) stop(t *testing.T) int {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	return r.code
}

// healthz GETs /healthz on the running instance and decodes the body.
func healthz(t *testing.T, addr string) (map[string]any, http.Header) {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body, resp.Header
}

func TestRun_MissingKey(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})

	t.Run("yaml lacking password", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		body := fmt.Sprintf("adguard:\n  url: %q\n  username: %q\n", fake.URL(), svcUser)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := run(context.Background(), []string{"--config", p}, envOf(nil), &stderr, nil)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "adguard.password") {
			t.Fatalf("stderr lacks adguard.password:\n%s", stderr.String())
		}
	})

	t.Run("env-only lacking url", func(t *testing.T) {
		env := map[string]string{
			"ADGUARD_REWARD_ADGUARD_USERNAME": svcUser,
			"ADGUARD_REWARD_ADGUARD_PASSWORD": svcPass,
		}
		var stderr bytes.Buffer
		code := run(context.Background(), nil, envOf(env), &stderr, nil)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "adguard.url") {
			t.Fatalf("stderr lacks adguard.url:\n%s", stderr.String())
		}
	})
}

func TestRun_ConfigPath(t *testing.T) {
	t.Run("explicit missing path is fatal", func(t *testing.T) {
		const p = "/nonexistent/config.yaml"
		var stderr bytes.Buffer
		code := run(context.Background(), []string{"--config", p}, envOf(nil), &stderr, nil)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), p) {
			t.Fatalf("stderr lacks %q:\n%s", p, stderr.String())
		}
	})

	t.Run("default missing path runs env-only", func(t *testing.T) {
		if _, err := os.Stat(defaultConfigPath()); err == nil {
			t.Skipf("a config.yaml exists beside the test binary at %s", defaultConfigPath())
		}
		fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
		env := map[string]string{
			"ADGUARD_REWARD_LISTEN":           "127.0.0.1:0",
			"ADGUARD_REWARD_ADGUARD_URL":      fake.URL(),
			"ADGUARD_REWARD_ADGUARD_USERNAME": svcUser,
			"ADGUARD_REWARD_ADGUARD_PASSWORD": svcPass,
		}
		r := start(t, nil, env)
		r.stop(t)
		var probed bool
		for _, req := range fake.Requests() {
			if req.Path == "/control/status" {
				probed = true
			}
		}
		if !probed {
			t.Fatalf("fake recorded no /control/status probe; stderr:\n%s", r.stderr.String())
		}
	})
}

func TestRun_StartupUnauthorized(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	fake.SetStatus("/control/status", http.StatusUnauthorized)
	p := writeConfig(t, fake.URL(), "")

	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", p}, envOf(nil), &stderr, nil)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr:\n%s", code, stderr.String())
	}
	out := stderr.String()
	for _, want := range []string{"rejected", "adguard.username", fake.URL()} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, svcPass) {
		t.Errorf("stderr contains the password:\n%s", out)
	}
}

func TestRun_StartupUnreachable(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	fake.SetStatus("/control/status", http.StatusServiceUnavailable)
	p := writeConfig(t, fake.URL(), "")

	r := start(t, []string{"--config", p}, nil)
	body, _ := healthz(t, r.addr)
	if body["adguard"] != "unreachable" {
		t.Fatalf("adguard = %v, want unreachable", body["adguard"])
	}
	if code := r.stop(t); code != 0 {
		t.Fatalf("exit after cancel = %d, want 0; stderr:\n%s", code, r.stderr.String())
	}
}

func TestRun_Healthz(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	p := writeConfig(t, fake.URL(), "")

	old := version
	version = "test-1"
	t.Cleanup(func() { version = old })

	var fixture struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(adguardtest.Fixture("status.json"), &fixture); err != nil {
		t.Fatal(err)
	}

	r := start(t, []string{"--config", p}, nil)
	body, hdr := healthz(t, r.addr)
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	want := map[string]any{
		"status":          "ok",
		"adguard":         "ok",
		"adguard_version": fixture.Version,
		"version":         "test-1",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
}

func TestRun_NoSecretsInLogs(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	p := writeConfig(t, fake.URL(), fmt.Sprintf("ai:\n  api_key: %q\n", aiKey))
	env := map[string]string{"ADGUARD_REWARD_LOG_LEVEL": "debug"}

	r := start(t, []string{"--config", p}, env)
	healthz(t, r.addr)
	r.stop(t)

	out := r.stderr.String()
	for _, want := range []string{"listening", "/control/status"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q (positive control):\n%s", want, out)
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(svcUser + ":" + svcPass))
	for _, secret := range []string{svcPass, aiKey, basic} {
		if strings.Contains(out, secret) {
			t.Errorf("stderr contains secret %q:\n%s", secret, out)
		}
	}
}

// TestVersionVar pins version as an addressable package var so -ldflags -X
// keeps binding to it; a const or a removed var fails to compile here.
func TestVersionVar(t *testing.T) {
	v := &version
	if *v == "" {
		t.Fatal("version must have a non-empty default")
	}
}

func TestDockerfileCMD(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CMD") && strings.Contains(line, "--config") {
			t.Fatalf("Dockerfile CMD passes --config, so an env-only start hits the missing-explicit-path error: %s", line)
		}
	}
}

var _ io.Writer = (*syncBuffer)(nil)
