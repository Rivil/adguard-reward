package adguard

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
)

const svcUser, svcPass = "svc", "pw-CANARY-9f3a"

func newFake(t *testing.T) *adguardtest.Server {
	t.Helper()
	return adguardtest.New(t, adguardtest.Options{User: svcUser, Pass: svcPass})
}

func newClient(t *testing.T, s *adguardtest.Server, opts ...Option) *Client {
	t.Helper()
	c, err := New(s.URL(), svcUser, svcPass, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func lastRequest(t *testing.T, s *adguardtest.Server, path string) adguardtest.Request {
	t.Helper()
	reqs := s.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Path == path {
			return reqs[i]
		}
	}
	t.Fatalf("no recorded request for %s", path)
	return adguardtest.Request{}
}

func TestLogin_BadCredentials(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	if err := c.Login(ctx, svcUser, svcPass); err != nil {
		t.Fatalf("correct creds: %v", err)
	}
	if err := c.Login(ctx, svcUser, "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("403 (default): err = %v, want ErrBadCredentials", err)
	}
	for _, code := range []int{400, 401} {
		s.SetStatus("/control/login", code)
		if err := c.Login(ctx, svcUser, "wrong"); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%d: err = %v, want ErrBadCredentials", code, err)
		}
	}
}

func TestLogin_ServerError(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	for _, code := range []int{502, 503} {
		s.SetStatus("/control/login", code)
		err := c.Login(ctx, svcUser, svcPass)
		if errors.Is(err, ErrBadCredentials) {
			t.Errorf("%d: must not be ErrBadCredentials: %v", code, err)
		}
		var se *StatusError
		if !errors.As(err, &se) || se.Code != code || se.Path != "/control/login" {
			t.Errorf("%d: err = %v, want *StatusError{Code:%d, Path:/control/login}", code, err, code)
		}
	}

	s.Close()
	err := c.Login(ctx, svcUser, svcPass)
	if errors.Is(err, ErrBadCredentials) {
		t.Errorf("closed server: must not be ErrBadCredentials: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Errorf("closed server: err = %v, want *url.Error", err)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetStatus("/control/login", 429)
	err := c.Login(context.Background(), svcUser, svcPass)
	if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrBadCredentials) {
		t.Errorf("429: err = %v, want ErrRateLimited and not ErrBadCredentials", err)
	}
}

func TestLogin_NoBasicAuth(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	_ = c.Login(context.Background(), "u", "p")
	req := lastRequest(t, s, "/control/login")
	if req.HasAuth {
		t.Error("login must not carry the service basic-auth header")
	}
	if req.Method != "POST" || string(req.Body) != `{"name":"u","password":"p"}` {
		t.Errorf("login request = %s %s, want POST {\"name\":\"u\",\"password\":\"p\"}", req.Method, req.Body)
	}
}

func TestStatus_BasicAuth(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := lastRequest(t, s, "/control/status")
	if !req.HasAuth {
		t.Fatal("status must carry basic auth")
	}
	// The fake only answers 200 when the header decodes to user:pass, so a
	// 200 above already proves the token; assert the fake saw it too.
	if req.Method != "GET" {
		t.Errorf("method = %s, want GET", req.Method)
	}
}

func TestStatus(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v0.107.52" || !st.Running || !st.ProtectionEnabled {
		t.Errorf("Status() = %+v, want fixture values", st)
	}

	s.SetStatus("/control/status", 401)
	if _, err := c.Status(ctx); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("401: err = %v, want ErrBadCredentials", err)
	}
	s.SetStatus("/control/status", 403)
	if _, err := c.Status(ctx); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("403: err = %v, want ErrBadCredentials", err)
	}
	s.SetStatus("/control/status", 503)
	_, err = c.Status(ctx)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 || errors.Is(err, ErrBadCredentials) {
		t.Errorf("503: err = %v, want plain *StatusError 503", err)
	}
	s.SetStatus("/control/status", 400)
	_, err = c.Status(ctx)
	if !errors.As(err, &se) || se.Code != 400 || errors.Is(err, ErrBadCredentials) {
		t.Errorf("400 on a service call: err = %v, want plain *StatusError 400, not ErrBadCredentials", err)
	}
}

func TestStatus_SnippetBound(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetResponse("/control/status", 503, bytes.Repeat([]byte("x"), 10<<10))
	_, err := c.Status(context.Background())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if len(se.Snippet) > 1024 {
		t.Errorf("Snippet is %d bytes, want <= 1024", len(se.Snippet))
	}
	if len(se.Snippet) == 0 {
		t.Error("Snippet is empty; body should be captured")
	}
}

func TestDo_NoRedirect(t *testing.T) {
	s := newFake(t)
	s.SetStatus("/control/status", 302)
	for name, c := range map[string]*Client{
		"default":  newClient(t, s),
		"injected": newClient(t, s, WithHTTPClient(&http.Client{})),
	} {
		_, err := c.Status(context.Background())
		var se *StatusError
		if !errors.As(err, &se) || se.Code != 302 {
			t.Errorf("%s client: err = %v, want *StatusError 302 (redirect not followed)", name, err)
		}
	}
}

func TestDo_Deadline(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.Hang("/control/status", 200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Status(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestLog_NoSecrets(t *testing.T) {
	s := newFake(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := newClient(t, s, WithLogger(log))
	ctx := context.Background()

	var errs []string
	record := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	_, err := c.Status(ctx)
	record(err)
	s.SetStatus("/control/status", 500)
	_, err = c.Status(ctx)
	record(err)
	s.SetStatus("/control/status", 401)
	_, err = c.Status(ctx)
	record(err)
	s.Close()
	_, err = c.Status(ctx)
	record(err)

	out := buf.String() + "\n" + strings.Join(errs, "\n")
	if !strings.Contains(out, "GET") || !strings.Contains(out, "/control/status") {
		t.Fatalf("positive control failed: log lacks GET /control/status:\n%s", out)
	}
	token := base64.StdEncoding.EncodeToString([]byte(svcUser + ":" + svcPass))
	if strings.Contains(out, svcPass) {
		t.Error("log or error text contains the password")
	}
	if strings.Contains(out, token) {
		t.Error("log or error text contains the basic-auth token")
	}
	if len(errs) != 3 {
		t.Errorf("expected 3 errors, got %d", len(errs))
	}
}

func TestNew_BadURL(t *testing.T) {
	for _, raw := range []string{"://bad", "127.0.0.1:80", "http://u:p@host"} {
		_, err := New(raw, svcUser, svcPass)
		if err == nil || !strings.Contains(err.Error(), "adguard.url") {
			t.Errorf("New(%q): err = %v, want error naming adguard.url", raw, err)
		}
	}
	c, err := New("http://127.0.0.1:1/", svcUser, svcPass)
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout <= 0 {
		t.Error("default http client has no timeout")
	}
	if strings.HasSuffix(c.baseURL, "/") {
		t.Errorf("baseURL %q keeps its trailing slash", c.baseURL)
	}
}
