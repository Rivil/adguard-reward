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
	"image/png"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/store"
	"github.com/Rivil/adguard-reward/web"
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
	return startWith(t, args, env, nil)
}

// startWith is start with a callback that runs inside run's onListen — at
// the instant the listener opens, before this function returns — so a test
// can observe fake state that the startup pass must already have produced.
// The callback runs on run's goroutine: use t.Errorf, never t.Fatal.
func startWith(t *testing.T, args []string, env map[string]string, onListen func(addr string)) *runResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runResult{stderr: &syncBuffer{}, done: make(chan struct{}), cancel: cancel}
	listening := make(chan string, 1)
	go func() {
		r.code = run(ctx, args, envOf(env), r.stderr, func(addr string) {
			if onListen != nil {
				onListen(addr)
			}
			listening <- addr
		})
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
	dataDir := t.TempDir()
	// A seeded overdue grant makes the startup pass read and write AdGuard
	// before the listener opens, so its log lines are covered too.
	seedGrant(t, dataDir, []string{"Kid phone"}, []string{"tiktok"}, time.Now().Add(-time.Minute))
	p := writeConfigIn(t, dataDir, fake.URL(), fmt.Sprintf("ai:\n  api_key: %q\n", aiKey))
	env := map[string]string{"ADGUARD_REWARD_LOG_LEVEL": "debug"}

	r := start(t, []string{"--config", p}, env)
	healthz(t, r.addr)
	r.stop(t)

	out := r.stderr.String()
	for _, want := range []string{"listening", "/control/status", "startup reconcile", "/control/clients"} {
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

func TestRun_ChildrenSurviveRestart(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	p := writeConfigIn(t, dataDir, fake.URL(), "")
	c := apiClient(false)

	r := start(t, []string{"--config", p}, nil)
	base := "http://" + r.addr
	ck := loginOK(t, c, base)
	type child struct {
		ID      int64    `json:"id"`
		Name    string   `json:"name"`
		Clients []string `json:"clients"`
	}
	resp := call(t, c, http.MethodPost, base, "/api/v1/children",
		map[string]any{"name": "Ada", "clients": []string{"Kid phone", "Kid tablet"}}, withCookie(ck.Value))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST child: %d %s", resp.StatusCode, b)
	}
	var created child
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d; stderr:\n%s", code, r.stderr.String())
	}

	p2 := writeConfigIn(t, dataDir, fake.URL(), "")
	r2 := start(t, []string{"--config", p2}, map[string]string{"ADGUARD_REWARD_LISTEN": r.addr})
	if r2.addr != r.addr {
		t.Fatalf("second run bound %s, want %s", r2.addr, r.addr)
	}
	resp = call(t, c, http.MethodGet, base, "/api/v1/children", nil, withCookie(ck.Value))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /children after restart: %d", resp.StatusCode)
	}
	var list struct {
		Children []child `json:"children"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Children) != 1 || list.Children[0].ID != created.ID || list.Children[0].Name != "Ada" ||
		!reflect.DeepEqual(list.Children[0].Clients, []string{"Kid phone", "Kid tablet"}) {
		t.Fatalf("after restart children = %+v, want the created %+v", list.Children, created)
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

// seedGrant writes a child owning clients and an active grant for services
// ending at endsAt straight into the store at dataDir, as a previous process
// would have left them, and returns the grant id.
func seedGrant(t *testing.T, dataDir string, clients, services []string, endsAt time.Time) int64 {
	t.Helper()
	st, err := store.Open(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	child, err := st.CreateChild(ctx, "Ada", clients)
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.CreateGrant(ctx, child.ID, services, clients, endsAt.Add(-time.Hour), endsAt)
	if err != nil {
		t.Fatal(err)
	}
	return g.ID
}

// rewindGrant sets a stored grant's ends_at, bypassing the API, between two
// runs on the same data_dir.
func rewindGrant(t *testing.T, dataDir string, id int64, endsAt time.Time) {
	t.Helper()
	st, err := store.Open(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`UPDATE grants SET ends_at = ? WHERE id = ?`, endsAt.Unix(), id); err != nil {
		t.Fatal(err)
	}
}

// grantStatus reads a grant's status from the store at dataDir.
func grantStatus(t *testing.T, dataDir string, id int64) string {
	t.Helper()
	st, err := store.Open(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g, err := st.GetGrant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return g.Status
}

type grantCreated struct {
	ID      int64    `json:"id"`
	EndsAt  string   `json:"ends_at"`
	Applied bool     `json:"applied"`
	Failed  []string `json:"failed"`
}

type grantRow struct {
	ID        int64    `json:"id"`
	ChildID   int64    `json:"child_id"`
	Services  []string `json:"services"`
	Clients   []string `json:"clients"`
	StartedAt string   `json:"started_at"`
	EndsAt    string   `json:"ends_at"`
}

// postChildAndGrant creates a child owning Kid phone and a grant of tiktok
// for duration seconds through the API, returning the 201 body.
func postChildAndGrant(t *testing.T, c *http.Client, base string, ck *http.Cookie, duration int) grantCreated {
	t.Helper()
	resp := call(t, c, http.MethodPost, base, "/api/v1/children",
		map[string]any{"name": "Ada", "clients": []string{"Kid phone"}}, withCookie(ck.Value))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST child: %d %s", resp.StatusCode, b)
	}
	var child struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	resp = call(t, c, http.MethodPost, base, "/api/v1/grants",
		map[string]any{"child_id": child.ID, "services": []string{"tiktok"}, "duration": duration}, withCookie(ck.Value))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST grant: %d %s", resp.StatusCode, b)
	}
	var created grantCreated
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func listGrants(t *testing.T, c *http.Client, base string, ck *http.Cookie) (string, []grantRow) {
	t.Helper()
	resp := call(t, c, http.MethodGet, base, "/api/v1/grants", nil, withCookie(ck.Value))
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/grants: %d %s", resp.StatusCode, b)
	}
	var body struct {
		Grants []grantRow `json:"grants"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b)), body.Grants
}

func phoneBlocks(fake *adguardtest.Server, id string) bool {
	ids, _ := fake.BlockedServices("Kid phone")
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}

// waitFor polls cond every 20 ms until it holds or d elapses.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestRun_GrantRestoredAfterRestart(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	c := apiClient(false)

	r := start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	ck := loginOK(t, c, base)
	created := postChildAndGrant(t, c, base, ck, 60)
	if phoneBlocks(fake, "tiktok") {
		t.Fatal("Kid phone still blocks tiktok after the grant")
	}
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d; stderr:\n%s", code, r.stderr.String())
	}
	rewindGrant(t, dataDir, created.ID, time.Now().Add(-60*time.Second))

	restored := false
	r2 := startWith(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")},
		map[string]string{"ADGUARD_REWARD_LISTEN": r.addr},
		func(string) { restored = phoneBlocks(fake, "tiktok") })
	if !restored {
		t.Error("Kid phone did not block tiktok at the instant the listener opened; the startup pass must run before net.Listen")
	}
	if r2.addr != r.addr {
		t.Fatalf("second run bound %s, want %s", r2.addr, r.addr)
	}
	if raw, _ := listGrants(t, c, base, ck); raw != `{"grants":[]}` {
		t.Errorf("GET /api/v1/grants after restart = %s, want {\"grants\":[]}", raw)
	}
}

func TestRun_GrantSurvivesRestart(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	c := apiClient(false)

	r := start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	ck := loginOK(t, c, base)
	created := postChildAndGrant(t, c, base, ck, 1800)
	_, before := listGrants(t, c, base, ck)
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d; stderr:\n%s", code, r.stderr.String())
	}

	start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, map[string]string{"ADGUARD_REWARD_LISTEN": r.addr})
	_, after := listGrants(t, c, base, ck)
	if len(after) != 1 || after[0].ID != created.ID || after[0].EndsAt != created.EndsAt || !reflect.DeepEqual(after, before) {
		t.Fatalf("after restart grants = %+v, want %+v (id %d, ends_at %s)", after, before, created.ID, created.EndsAt)
	}
	if phoneBlocks(fake, "tiktok") {
		t.Error("a live grant was re-blocked by the restart")
	}
}

func TestRun_LiveGrantRearmed(t *testing.T) {
	old := reconcileInterval
	reconcileInterval = time.Hour
	t.Cleanup(func() { reconcileInterval = old })

	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	c := apiClient(false)

	r := start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	created := postChildAndGrant(t, c, base, loginOK(t, c, base), 1800)
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d", code)
	}
	rewindGrant(t, dataDir, created.ID, time.Now().Add(2*time.Second))

	start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)
	if phoneBlocks(fake, "tiktok") {
		t.Fatal("live grant re-blocked at startup; it has 2 s left")
	}
	if !waitFor(5*time.Second, func() bool { return phoneBlocks(fake, "tiktok") }) {
		t.Fatal("tiktok not re-blocked within 5 s of listening; Start must re-arm timers for live grants")
	}
	if got := grantStatus(t, dataDir, created.ID); got != store.StatusExpired {
		t.Errorf("status = %q, want expired", got)
	}
}

func TestRun_ReconcilerDrift(t *testing.T) {
	old := reconcileInterval
	reconcileInterval = 50 * time.Millisecond
	t.Cleanup(func() { reconcileInterval = old })

	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	c := apiClient(false)
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	postChildAndGrant(t, c, base, loginOK(t, c, base), 1800)

	posts := fake.CountRequests("POST", "/control/clients/update")
	time.Sleep(500 * time.Millisecond)
	if n := fake.CountRequests("POST", "/control/clients/update") - posts; n != 0 {
		t.Errorf("reconciler issued %d update POSTs with nothing drifted", n)
	}

	fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","tiktok"]`)
	})
	if !waitFor(2*time.Second, func() bool { return !phoneBlocks(fake, "tiktok") }) {
		t.Fatal("re-blocked tiktok not removed within 2 s; the reconciler is not running on reconcileInterval")
	}

	// At the production cadence the same drift is still there half a second on.
	reconcileInterval = 60 * time.Second
	fake2 := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r2 := start(t, []string{"--config", writeConfig(t, fake2.URL(), "")}, nil)
	base2 := "http://" + r2.addr
	postChildAndGrant(t, c, base2, loginOK(t, c, base2), 1800)
	fake2.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","tiktok"]`)
	})
	time.Sleep(500 * time.Millisecond)
	if !phoneBlocks(fake2, "tiktok") {
		t.Error("drift repaired within 500 ms at a 60 s interval; something other than the reconciler wrote")
	}
}

func TestRun_StartupAdGuardDown(t *testing.T) {
	old := reconcileInterval
	reconcileInterval = 50 * time.Millisecond
	t.Cleanup(func() { reconcileInterval = old })

	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	fake.SetStatus("/control/clients", http.StatusInternalServerError)
	dataDir := t.TempDir()
	fake.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube"]`)
	})
	id := seedGrant(t, dataDir, []string{"Kid phone"}, []string{"tiktok"}, time.Now().Add(-time.Minute))

	r := start(t, []string{"--config", writeConfigIn(t, dataDir, fake.URL(), "")}, nil)
	if !strings.Contains(r.stderr.String(), `msg="startup reconcile" err=`) {
		t.Errorf("stderr lacks the startup reconcile error:\n%s", r.stderr.String())
	}
	if got := grantStatus(t, dataDir, id); got != store.StatusActive {
		t.Errorf("status = %q, want active while AdGuard is unreadable", got)
	}
	if phoneBlocks(fake, "tiktok") {
		t.Fatal("tiktok re-blocked while /control/clients answers 500")
	}

	fake.SetResponse("/control/clients", 0, nil)
	if !waitFor(2*time.Second, func() bool { return phoneBlocks(fake, "tiktok") }) {
		t.Fatal("block not restored within 2 s of AdGuard recovering")
	}
	if !waitFor(2*time.Second, func() bool { return grantStatus(t, dataDir, id) == store.StatusExpired }) {
		t.Errorf("status = %q, want expired", grantStatus(t, dataDir, id))
	}
	if code := r.stop(t); code == 1 {
		t.Fatalf("exit = 1; an unreachable AdGuard at boot must not be fatal:\n%s", r.stderr.String())
	}
}

// ---- SPA, PWA and buttons over the real embed ----------------------------

// requireDist fatals when web/dist is not built. A skip here would be a
// false pass: the binary would ship a 503 shell. make test-go builds the
// frontend first; a bare go test needs `make build-web`.
func requireDist(t *testing.T) []byte {
	t.Helper()
	index, err := fs.ReadFile(web.Dist(), "index.html")
	if err != nil {
		t.Fatalf("web/dist not built — run make build-web (%v)", err)
	}
	return index
}

// get fetches a non-API path with no headers at all, like a browser navigation.
func get(t *testing.T, c *http.Client, base, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

var assetRef = regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.js`)

func TestRun_SPAFallback(t *testing.T) {
	index := requireDist(t)
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)

	for _, p := range []string{"/buttons", "/children/7"} {
		resp, body := get(t, c, base, p)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s = %d %q, want 200 text/html", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if !bytes.Equal(body, index) {
			t.Fatalf("GET %s body differs from the embedded index.html", p)
		}
		if !bytes.Contains(body, []byte(`<div id="app">`)) || !bytes.Contains(body, []byte(`rel="manifest"`)) {
			t.Fatalf("GET %s body lacks the shell markers: %s", p, body)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
	}

	js := assetRef.Find(index)
	if js == nil {
		t.Fatalf("index.html references no /assets/*.js: %s", index)
	}
	resp, _ := get(t, c, base, string(js))
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("GET %s = %d %q, want 200 immutable", js, resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if resp, body := get(t, c, base, "/assets/missing.js"); resp.StatusCode != http.StatusNotFound || bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		t.Fatalf("GET /assets/missing.js = %d %q, want a non-HTML 404", resp.StatusCode, body)
	}

	ck := loginOK(t, c, base)
	resp = call(t, c, http.MethodGet, base, "/api/v1/nothing", nil, withCookie(ck.Value))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nothing with cookie = %d, want 404", resp.StatusCode)
	}
	resp = call(t, c, http.MethodGet, base, "/api/v1/nothing", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/nothing without cookie = %d, want 401", resp.StatusCode)
	}
	resp, body := get(t, c, base, "/api/v2/x")
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/json" ||
		bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		t.Fatalf("GET /api/v2/x = %d %q %q, want 404 application/json", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	healthz(t, r.addr)
}

func TestRun_PWAServed(t *testing.T) {
	requireDist(t)
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)

	resp, body := get(t, c, base, "/manifest.webmanifest")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var manifest struct {
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
		Display  string `json:"display"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if manifest.StartURL != "/" || manifest.Scope != "/" || manifest.Display != "standalone" {
		t.Fatalf("manifest = %+v, want start_url / scope / display standalone", manifest)
	}

	for _, ic := range []struct {
		path string
		size int
	}{{"/icon-192.png", 192}, {"/icon-512.png", 512}} {
		resp, body := get(t, c, base, ic.path)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("%s = %d %q", ic.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s is not a PNG: %v", ic.path, err)
		}
		if cfg.Width != ic.size || cfg.Height != ic.size {
			t.Fatalf("%s is %dx%d, want %dx%d", ic.path, cfg.Width, cfg.Height, ic.size, ic.size)
		}
	}

	resp, body = get(t, c, base, "/sw.js")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("sw.js = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("sw.js Cache-Control = %q, want no-cache", cc)
	}
	if bytes.Contains(body, []byte("__PRECACHE__")) {
		t.Fatalf("sw.js still carries the precache token: %s", body)
	}
	// The worker's bypass rule names /api/ by design; what must never appear
	// is an API path in the precache list itself.
	if lit := precacheLiteral.Find(body); lit == nil || bytes.Contains(lit, []byte("/api/")) {
		t.Fatalf("sw.js precache literal %q is missing or names an /api/ path", lit)
	}
}

var precacheLiteral = regexp.MustCompile(`\["/"[^\]]*\]`)

func TestRun_SWPrecacheListIsServed(t *testing.T) {
	requireDist(t)
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)

	_, sw := get(t, c, base, "/sw.js")
	lit := precacheLiteral.Find(sw)
	if lit == nil {
		t.Fatalf("no precache array literal in sw.js: %s", sw)
	}
	var list []string
	if err := json.Unmarshal(lit, &list); err != nil {
		t.Fatalf("precache literal %s is not JSON: %v", lit, err)
	}
	hasRoot, hasAsset := false, false
	for _, p := range list {
		if p == "/" {
			hasRoot = true
		}
		if strings.HasPrefix(p, "/assets/") {
			hasAsset = true
		}
		if strings.HasPrefix(p, "/api/") {
			t.Fatalf("precache lists an API path %q", p)
		}
		resp, _ := get(t, c, base, p)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("precached %s = %d, want 200", p, resp.StatusCode)
		}
		if strings.HasPrefix(p, "/assets/") && !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("precached %s Cache-Control = %q, want immutable", p, resp.Header.Get("Cache-Control"))
		}
	}
	if !hasRoot || !hasAsset {
		t.Fatalf("precache %v lacks / or an /assets/ entry", list)
	}
}

func TestRun_APINoStore(t *testing.T) {
	requireDist(t)
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)
	ck := loginOK(t, c, base)

	resp := call(t, c, http.MethodGet, base, "/api/v1/buttons", nil, withCookie(ck.Value))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /api/v1/buttons = %d Cache-Control %q, want 200 no-store", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	head := call(t, c, http.MethodHead, base, "/api/v1/buttons", nil, withCookie(ck.Value))
	if strings.HasPrefix(head.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("HEAD /api/v1/buttons answered HTML")
	}
}

type buttonWire struct {
	ID       int64    `json:"id"`
	Label    string   `json:"label"`
	ChildID  int64    `json:"child_id"`
	Services []string `json:"services"`
	Duration int      `json:"duration"`
}

func putButtons(t *testing.T, c *http.Client, base, cookie string, items []map[string]any) *http.Response {
	t.Helper()
	return call(t, c, http.MethodPut, base, "/api/v1/buttons", map[string]any{"buttons": items}, withCookie(cookie))
}

func getButtons(t *testing.T, c *http.Client, base, cookie string) []byte {
	t.Helper()
	resp := call(t, c, http.MethodGet, base, "/api/v1/buttons", nil, withCookie(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/buttons: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRun_ButtonsSurviveRestart(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	dataDir := t.TempDir()
	p := writeConfigIn(t, dataDir, fake.URL(), "")
	c := apiClient(false)

	r := start(t, []string{"--config", p}, nil)
	base := "http://" + r.addr
	ck := loginOK(t, c, base)
	resp := call(t, c, http.MethodPost, base, "/api/v1/children",
		map[string]any{"name": "Ada", "clients": []string{"Kid phone"}}, withCookie(ck.Value))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST child: %d %s", resp.StatusCode, b)
	}
	var child struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	resp = putButtons(t, c, base, ck.Value, []map[string]any{
		{"label": "YouTube 1h", "child_id": child.ID, "services": []string{"youtube", "tiktok"}, "duration": 3600},
		{"label": "TikTok 30m", "child_id": child.ID, "services": []string{"tiktok"}, "duration": 1800},
	})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT buttons: %d %s", resp.StatusCode, b)
	}
	before := getButtons(t, c, base, ck.Value)
	var stored struct {
		Buttons []buttonWire `json:"buttons"`
	}
	if err := json.Unmarshal(before, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Buttons) != 2 || stored.Buttons[0].Label != "YouTube 1h" ||
		!reflect.DeepEqual(stored.Buttons[1].Services, []string{"tiktok"}) {
		t.Fatalf("stored = %+v", stored.Buttons)
	}
	if code := r.stop(t); code != 0 {
		t.Fatalf("first run exit = %d; stderr:\n%s", code, r.stderr.String())
	}

	p2 := writeConfigIn(t, dataDir, fake.URL(), "")
	r2 := start(t, []string{"--config", p2}, map[string]string{"ADGUARD_REWARD_LISTEN": r.addr})
	if r2.addr != r.addr {
		t.Fatalf("second run bound %s, want %s", r2.addr, r.addr)
	}
	after := getButtons(t, c, base, ck.Value)
	if !bytes.Equal(after, before) {
		t.Fatalf("after restart:\n%s\nbefore:\n%s", after, before)
	}
}

func TestRun_ButtonsWired(t *testing.T) {
	fake := adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
	r := start(t, []string{"--config", writeConfig(t, fake.URL(), "")}, nil)
	base := "http://" + r.addr
	c := apiClient(false)
	ck := loginOK(t, c, base)

	resp := putButtons(t, c, base, ck.Value, []map[string]any{
		{"label": "x", "child_id": 999, "services": []string{"youtube"}, "duration": 3600},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT with child 999 = %d %s, want 422", resp.StatusCode, b)
	}
	if body := getButtons(t, c, base, ck.Value); strings.TrimSpace(string(body)) != `{"buttons":[]}` {
		t.Fatalf("GET /api/v1/buttons = %s, want {\"buttons\":[]}", body)
	}
}
