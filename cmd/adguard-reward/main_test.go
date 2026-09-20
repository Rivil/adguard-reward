package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/store"
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
// appended verbatim, and returns its path. data_dir is a fresh temp dir so
// no test writes ./data.
func writeConfig(t *testing.T, fakeURL, extra string) string {
	t.Helper()
	return writeConfigIn(t, t.TempDir(), fakeURL, extra)
}

// writeConfigIn is writeConfig with the data_dir chosen by the caller, for
// tests that restart on the same database.
func writeConfigIn(t *testing.T, dataDir, fakeURL, extra string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("listen: \"127.0.0.1:0\"\ndata_dir: %q\nadguard:\n  url: %q\n  username: %q\n  password: %q\n%s", dataDir, fakeURL, svcUser, svcPass, extra)
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

// ---- phase 02: API wiring ----------------------------------------------

const csrfHeader, csrfValue = "X-Requested-With", "adguard-reward"

// apiClient never follows redirects and keeps no cookie jar, so every
// cookie a test sends is explicit.
func apiClient(insecureTLS bool) *http.Client {
	c := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if insecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test-only self-signed cert
	}
	return c
}

type reqOpt func(*http.Request)

func withCookie(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: v}) }
}
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noCSRF() reqOpt                { return func(r *http.Request) { r.Header.Del(csrfHeader) } }

// call sends one API request with the CSRF header to base (scheme://host).
func call(t *testing.T, c *http.Client, method, base, path string, body any, opts ...reqOpt) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(csrfHeader, csrfValue)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func login(t *testing.T, c *http.Client, base, user, pass string, opts ...reqOpt) *http.Response {
	t.Helper()
	return call(t, c, http.MethodPost, base, "/api/v1/login", map[string]string{"username": user, "password": pass}, opts...)
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			if found != nil {
				t.Fatalf("two Set-Cookie headers for %s", auth.CookieName)
			}
			found = c
		}
	}
	return found
}

// loginOK logs in as the service user and returns the session cookie.
func loginOK(t *testing.T, c *http.Client, base string) *http.Cookie {
	t.Helper()
	resp := login(t, c, base, svcUser, svcPass)
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status %d body %s", resp.StatusCode, b)
	}
	ck := sessionCookie(t, resp)
	if ck == nil {
		t.Fatal("login set no session cookie")
	}
	return ck
}

func loginHits(fake *adguardtest.Server) int {
	n := 0
	for _, r := range fake.Requests() {
		if r.Path == "/control/login" {
			n++
		}
	}
	return n
}

func TestRun_SessionSurvivesRestart(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	p := writeConfigIn(t, dataDir, fake.URL(), "")
	c := apiClient(false)

	r := start(t, []string{"--config", p}, nil)
	base := "http://" + r.addr
	ck := loginOK(t, c, base)
	if resp := call(t, c, http.MethodGet, base, "/api/v1/me", nil, withCookie(ck.Value)); resp.StatusCode != http.StatusOK {
		t.Fatalf("/me before restart: %d", resp.StatusCode)
	}
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d; stderr:\n%s", code, r.stderr.String())
	}

	// Same data_dir and the same port.
	p2 := writeConfigIn(t, dataDir, fake.URL(), "")
	r2 := start(t, []string{"--config", p2}, map[string]string{"ADGUARD_REWARD_LISTEN": r.addr})
	if r2.addr != r.addr {
		t.Fatalf("second run bound %s, want %s", r2.addr, r.addr)
	}
	resp := call(t, c, http.MethodGet, base, "/api/v1/me", nil, withCookie(ck.Value))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/me after restart: %d (session did not survive)", resp.StatusCode)
	}
	var me map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil || me["username"] != svcUser {
		t.Fatalf("/me body %v err %v", me, err)
	}
}

func TestRun_CookieSecure(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	c := apiClient(false)

	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	if ck := loginOK(t, c, "http://"+r.addr); ck.Secure {
		t.Fatal("plain config: cookie is Secure")
	}

	r2 := start(t, []string{"--config", writeConfig(t, fake.URL(), "base_url: \"https://reward.example\"\n")}, nil)
	if ck := loginOK(t, c, "http://"+r2.addr); !ck.Secure {
		t.Fatal("base_url https: cookie lacks Secure")
	}
}

// selfSigned writes a throwaway cert/key pair for 127.0.0.1 into dir.
func selfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "adguard-reward test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestRun_TLS(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	certPath, keyPath := selfSigned(t, t.TempDir())

	r := start(t, []string{"--config", writeConfig(t, fake.URL(), fmt.Sprintf("tls:\n  cert: %q\n  key: %q\n", certPath, keyPath))}, nil)
	ck := loginOK(t, apiClient(true), "https://"+r.addr)
	if !ck.Secure {
		t.Fatal("TLS listener: cookie lacks Secure")
	}
	if !strings.Contains(r.stderr.String(), "tls=true") {
		t.Fatalf("listening line lacks tls=true:\n%s", r.stderr.String())
	}

	plain := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	if ck := loginOK(t, apiClient(false), "http://"+plain.addr); ck.Secure {
		t.Fatal("plain listener: cookie is Secure")
	}

	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", writeConfig(t, fake.URL(), fmt.Sprintf("tls:\n  cert: %q\n", certPath))}, envOf(nil), &stderr, nil)
	if code != 1 || !strings.Contains(stderr.String(), "tls.key") {
		t.Fatalf("cert without key: exit %d stderr:\n%s", code, stderr.String())
	}
}

func TestRun_ApiChain(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)

	if resp := login(t, c, base, svcUser, svcPass, noCSRF()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without CSRF header: %d, want 403", resp.StatusCode)
	}
	resp := call(t, c, http.MethodGet, base, "/api/v1/me", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me without cookie: %d, want 401", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("/me 401 Cache-Control = %q, want no-store", cc)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("/me 401 Content-Type = %q, want application/json (envelope)", ct)
	}
	healthz(t, r.addr) // fatals unless 200, no cookie, no header
}

func TestRun_StoreOpenFails(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", writeConfigIn(t, file, fake.URL(), "")}, envOf(nil), &stderr, nil)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "data_dir") {
		t.Fatalf("stderr lacks data_dir:\n%s", stderr.String())
	}
}

func TestRun_RateLimitWired(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)
	for i := 0; i < 5; i++ {
		if resp := login(t, c, base, svcUser, "nope"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, resp.StatusCode)
		}
	}
	resp := login(t, c, base, svcUser, "nope")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("6th: status %d Retry-After %q, want 429 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if n := loginHits(fake); n != 5 {
		t.Fatalf("fake recorded %d /control/login hits, want 5", n)
	}
}

func TestRun_TrustedProxies(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	c := apiClient(false)
	xff := withHeader("X-Forwarded-For", "203.0.113.7")

	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "trusted_proxies: [\"127.0.0.0/8\"]\n")}, nil)
	base := "http://" + r.addr
	for i := 0; i < 5; i++ {
		login(t, c, base, svcUser, "nope", xff)
	}
	if resp := login(t, c, base, svcUser, "nope", xff); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("trusted: 6th via XFF = %d, want 429", resp.StatusCode)
	}
	if out := r.stderr.String(); !strings.Contains(out, "ip=203.0.113.7") {
		t.Fatalf("trusted: login lines lack ip=203.0.113.7:\n%s", out)
	}

	r2 := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	login(t, c, "http://"+r2.addr, svcUser, "nope", xff)
	out := r2.stderr.String()
	if !strings.Contains(out, "ip=127.0.0.1") || strings.Contains(out, "ip=203.0.113.7") {
		t.Fatalf("untrusted: login line should carry the peer ip=127.0.0.1:\n%s", out)
	}
}

func TestRun_NoSecretsInLogs_Login(t *testing.T) {
	const loginCanary = "pw-LOGIN-CANARY-1d7f"
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	p := writeConfig(t, fake.URL(), fmt.Sprintf("ai:\n  api_key: %q\n", aiKey))
	r := start(t, []string{"--config", p}, map[string]string{"ADGUARD_REWARD_LOG_LEVEL": "debug"})
	base := "http://" + r.addr
	c := apiClient(false)

	healthz(t, r.addr)
	if resp := login(t, c, base, svcUser, loginCanary); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("canary login: %d", resp.StatusCode)
	}
	ck := loginOK(t, c, base)
	call(t, c, http.MethodGet, base, "/api/v1/me", nil, withCookie(ck.Value))
	call(t, c, http.MethodPost, base, "/api/v1/logout", nil, withCookie(ck.Value))
	r.stop(t)

	out := r.stderr.String()
	for _, want := range []string{"listening", "/control/status", "/control/login", "event=login"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q (positive control):\n%s", want, out)
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(svcUser + ":" + svcPass))
	for _, secret := range []string{svcPass, aiKey, basic, loginCanary, ck.Value} {
		if strings.Contains(out, secret) {
			t.Errorf("stderr contains secret %q:\n%s", secret, out)
		}
	}
}

func TestRun_Sweeper(t *testing.T) {
	old := sweepInterval
	sweepInterval = 50 * time.Millisecond
	t.Cleanup(func() { sweepInterval = old })

	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()

	st, err := store.Open(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	var hash [32]byte
	hash[0] = 7
	id, err := st.Insert(context.Background(), auth.Session{Username: svcUser, TokenHash: hash, CreatedAt: past, LastSeenAt: past, ExpiresAt: past})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)

	st, err = store.Open(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := st.ByTokenHash(context.Background(), hash); err != nil {
			return // gone
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expired session %d still present after 2s", id)
}
