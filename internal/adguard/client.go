// Package adguard is a minimal client for the AdGuard Home /control API,
// targeting the v0.107.x shape. Every call carries the configured service
// credential as HTTP basic auth; Login is the one exception, since it
// authenticates a parent, not the service.
package adguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	snippetLimit   = 1024 // bytes of an error body kept in StatusError.Snippet
)

// Client talks to one AdGuard Home instance.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
	log      *slog.Logger

	// rmw serialises SetBlockedServices' read-modify-write so two grants
	// on the same client cannot clobber each other's write.
	rmw sync.Mutex
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client. The client is copied and
// its redirect policy forced to "never follow", so an injected client cannot
// silently re-enable redirects.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		cp := *hc
		cp.CheckRedirect = noRedirect
		c.http = &cp
	}
}

// WithLogger sets the logger for per-request debug lines (method, path,
// status, duration — never headers or bodies).
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Status is the subset of GET /control/status the app uses.
type Status struct {
	Version           string `json:"version"`
	Running           bool   `json:"running"`
	ProtectionEnabled bool   `json:"protection_enabled"`
}

// New validates baseURL and returns a client using the service credential.
func New(baseURL, username, password string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("adguard.url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("adguard.url: scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("adguard.url: missing host")
	}
	if u.User != nil {
		return nil, errors.New("adguard.url: must not embed credentials")
	}
	c := &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: defaultTimeout, CheckRedirect: noRedirect},
		log:      slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Login checks a parent's credentials against POST /control/login. It sends
// no basic auth — the whole point is to have AdGuard judge user/pass — and
// discards the session cookie AdGuard sets on success.
func (c *Client) Login(ctx context.Context, user, pass string) error {
	body := struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}{user, pass}
	resp, err := c.send(ctx, http.MethodPost, "/control/login", body, false)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusBadRequest {
		// Older builds answer a wrong password with 400; only Login treats
		// that as a credential rejection.
		return fmt.Errorf("%w: %w", ErrBadCredentials, c.statusError(resp))
	}
	return c.classify(resp)
}

// Status fetches GET /control/status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "/control/status", nil, &st)
	return st, err
}

// do performs an authenticated JSON call. in (if non-nil) is encoded as the
// body; out (if non-nil) receives the decoded 2xx body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	resp, err := c.send(ctx, method, path, in, true)
	if err != nil {
		return err
	}
	defer drain(resp)
	if err := c.classify(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("adguard: %s %s: decode: %w", method, path, err)
	}
	return nil
}

// send builds and executes the request. Transport errors come back as the
// *url.Error the http client produced, wrapped with method and path.
func (c *Client) send(ctx context.Context, method, path string, in any, auth bool) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("adguard: %s %s: encode: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("adguard: %s %s: %w", method, path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.SetBasicAuth(c.username, c.password)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Debug("adguard request failed", "method", method, "path", path,
			"duration_ms", time.Since(start).Milliseconds(), "err", scrub(err, c.password))
		return nil, fmt.Errorf("adguard: %s %s: %w", method, path, err)
	}
	c.log.Debug("adguard request", "method", method, "path", path, "status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds())
	return resp, nil
}

// classify maps a response status to the package's error vocabulary.
func (c *Client) classify(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrBadCredentials, c.statusError(resp))
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrRateLimited, c.statusError(resp))
	default:
		return c.statusError(resp)
	}
}

func (c *Client) statusError(resp *http.Response) *StatusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, snippetLimit))
	return &StatusError{
		Method:  resp.Request.Method,
		Path:    resp.Request.URL.Path,
		Code:    resp.StatusCode,
		Snippet: strings.TrimSpace(string(b)),
	}
}

// scrub is belt-and-braces for transport errors: a *url.Error's text
// includes the URL, which never carries the password, but the cost of the
// check is nothing compared with a leak.
func scrub(err error, secret string) string {
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
