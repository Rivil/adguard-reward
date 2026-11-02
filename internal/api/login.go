package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
)

// maxLoginBody bounds the login request body; a credential pair is a few
// hundred bytes at most.
const maxLoginBody = 4 << 10

const (
	outcomeOK          = "ok"
	outcomeBadCreds    = "bad_credentials"
	outcomeUnreachable = "adguard_unreachable"
	outcomeRateLimited = "rate_limited"
	badCredentialsMsg  = "invalid username or password"
	adguardUnavailable = "AdGuard Home did not accept the login attempt — try again shortly"
	rateLimitedMsg     = "too many failed login attempts — try again later"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin is POST /api/v1/login. The limiter is consulted twice: once
// before queueing for the in-flight slot (fast 429, no AdGuard call) and
// again while holding it, so a burst that arrived together cannot all pass
// the first check before any failure is counted. The slot is released only
// after Fail or Issue has run.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {username, password}")
		return
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected a single JSON object")
		return
	}
	// Drain so the body limit is what rejects an oversize request, not the
	// decoder's early stop.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "request body too large")
		return
	}

	ip := a.deps.ClientIP(r)
	if ok, retry := a.deps.Limiter.Check(ip); !ok {
		a.rejectLimited(w, in.Username, ip, retry)
		return
	}

	select {
	case a.loginSlot <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	defer func() { <-a.loginSlot }()

	if ok, retry := a.deps.Limiter.Check(ip); !ok {
		a.rejectLimited(w, in.Username, ip, retry)
		return
	}

	err := a.deps.AdGuard.Login(r.Context(), in.Username, in.Password)
	switch {
	case err == nil:
		raw, _, err := a.deps.Auth.Issue(r.Context(), in.Username)
		if err != nil {
			a.log.Error("issue session failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not create session")
			return
		}
		auth.SetCookie(w, a.deps.Auth.Cookie(raw))
		a.logLogin(in.Username, ip, outcomeOK)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, adguard.ErrBadCredentials):
		a.deps.Limiter.Fail(ip)
		a.logLogin(in.Username, ip, outcomeBadCreds)
		writeError(w, http.StatusUnauthorized, CodeBadCredentials, badCredentialsMsg)
	default:
		// 5xx, transport failure, or AdGuard's own 429: neutral message, no
		// budget consumed — the parent did nothing wrong.
		a.log.Warn("adguard login unavailable", "err", err)
		a.logLogin(in.Username, ip, outcomeUnreachable)
		writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable, adguardUnavailable)
	}
}

func (a *API) rejectLimited(w http.ResponseWriter, username string, ip netip.Addr, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(retry/time.Second)))
	a.logLogin(username, ip, outcomeRateLimited)
	writeError(w, http.StatusTooManyRequests, CodeRateLimited, rateLimitedMsg)
}

// logLogin is the one structured line per attempt. The password is never
// an attribute.
func (a *API) logLogin(username string, ip netip.Addr, outcome string) {
	a.log.Info("login", "event", "login", "username", username, "ip", ip.String(), "outcome", outcome)
}

// handleLogout is POST /api/v1/logout: the session row goes and the cookie
// is cleared (replacing the refresh RequireSession just wrote).
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	if err := a.deps.Sessions.Delete(r.Context(), sess.ID); err != nil {
		a.log.Error("logout failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not end session")
		return
	}
	a.deps.Auth.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe is GET /api/v1/me.
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, struct {
		Username  string `json:"username"`
		ExpiresAt string `json:"expires_at"`
	}{sess.Username, sess.ExpiresAt.UTC().Format(time.RFC3339)})
}
