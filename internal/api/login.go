package api

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

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

// handleLogin is POST /api/v1/login. The AdGuard round-trip and its rate
// limiting live in recheckPassword, shared with PIN change and remove.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := decodeBody(w, r, maxLoginBody, &in); err != nil || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {username, password}")
		return
	}

	res := a.recheckPassword(r, in.Username, in.Password)
	switch res.outcome {
	case outcomeOK:
		deviceID, ok := a.loginDevice(w, r, in.Username)
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal", "could not create session")
			return
		}
		var (
			raw string
			err error
		)
		if deviceID != 0 {
			raw, _, err = a.deps.Auth.IssueForDevice(r.Context(), in.Username, deviceID)
		} else {
			raw, _, err = a.deps.Auth.Issue(r.Context(), in.Username)
		}
		if err != nil {
			a.log.Error("issue session failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not create session")
			return
		}
		auth.SetCookie(w, a.deps.Auth.Cookie(raw))
		a.logLogin(in.Username, res.ip, outcomeOK)
		w.WriteHeader(http.StatusNoContent)
	case outcomeRateLimited:
		a.rejectLimited(w, in.Username, res.ip, res.retry)
	case outcomeBadCreds:
		a.logLogin(in.Username, res.ip, outcomeBadCreds)
		writeError(w, http.StatusUnauthorized, CodeBadCredentials, badCredentialsMsg)
	case outcomeUnreachable:
		a.logLogin(in.Username, res.ip, outcomeUnreachable)
		writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable, adguardUnavailable)
	}
	// outcomeCanceled: the client is gone; nothing to write.
}

// loginDevice decides whether a full login lands on a PIN device: a live
// device cookie of the same user tags the new session (the PIN lock keeps
// applying — a password login must not lift it); a live device of another
// user, or a dead cookie, is cleared. ok is false when the device store
// failed: the login fails closed rather than silently skipping the lock.
func (a *API) loginDevice(w http.ResponseWriter, r *http.Request, username string) (deviceID int64, ok bool) {
	if a.deps.Devices == nil {
		return 0, true
	}
	c, err := r.Cookie(auth.DeviceCookieName)
	if err != nil {
		return 0, true
	}
	dev, err := a.deps.Auth.LookupDevice(r.Context(), c.Value)
	switch {
	case err == nil && dev.Username == username:
		return dev.ID, true
	case err == nil, errors.Is(err, auth.ErrDeviceNotFound):
		a.deps.Auth.ClearDevice(w)
		return 0, true
	default:
		a.log.Error("login: device lookup failed", "err", err)
		return 0, false
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
// is cleared (replacing the refresh RequireSession just wrote). On a PIN
// session logout keeps meaning logout (locked logout_semantics): the device
// and its PIN go too, every session tagged with it included, and the device
// cookie is cleared, so the next visit is the full login page.
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	if sess.DeviceID != 0 && a.deps.Devices != nil {
		if err := a.deps.Devices.RevokeDevice(r.Context(), sess.DeviceID); err != nil {
			a.log.Error("logout: revoke device failed", "err", err, "session", sess)
			writeError(w, http.StatusInternalServerError, "internal", "could not end session")
			return
		}
		a.deps.Auth.Clear(w)
		a.deps.Auth.ClearDevice(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
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
