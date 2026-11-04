package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// maxPINBody bounds the PIN request bodies; {pin, password} is tiny.
const maxPINBody = 4 << 10

// pinRequest is the body of POST /api/v1/pin. Pointers tell a missing key
// from an empty one; a JSON number for pin fails to decode.
type pinRequest struct {
	PIN      *string `json:"pin"`
	Password *string `json:"password"`
}

// errBadBody is decodeBody's single failure: malformed, oversize, trailing
// data or an unknown key alike.
var errBadBody = errors.New("bad request body")

// decodeBody reads exactly one JSON object with no unknown keys into v.
// The whole body, trailing whitespace included, counts against limit, and
// anything after the object but whitespace is refused.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errBadBody
	}
	// The next read must be a clean end of body: a second value, a stray
	// delimiter or a limit overrun is an error, never io.EOF.
	if _, err := dec.Token(); err != io.EOF {
		return errBadBody
	}
	return nil
}

// handlePinStatus is GET /api/v1/pin. has_pin comes from the session's
// device tag — server state — never from the device cookie.
func (a *API) handlePinStatus(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, struct {
		HasPIN bool `json:"has_pin"`
	}{sess.DeviceID != 0})
}

// handlePinSet is POST /api/v1/pin. On an untagged session it sets this
// device's first PIN: the session alone is enough (locked
// pin_change_reauth). It hashes the PIN, issues a device token in its own
// cookie and tags the session, which from now on idles out after 15
// minutes. A tagged session already has a PIN: replacing it needs the
// AdGuard password.
func (a *API) handlePinSet(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	var in pinRequest
	if err := decodeBody(w, r, maxPINBody, &in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {pin}")
		return
	}
	if in.PIN == nil || auth.ValidatePIN(*in.PIN) != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "a PIN is 4 to 6 digits")
		return
	}
	if a.deps.Devices == nil {
		writeError(w, http.StatusInternalServerError, "internal", "device PINs are not configured")
		return
	}
	if sess.DeviceID != 0 {
		if in.Password == nil || *in.Password == "" {
			writeError(w, http.StatusForbidden, CodeReauthRequired, "changing the PIN needs your AdGuard password")
			return
		}
		a.changePIN(w, r, sess, *in.PIN, *in.Password)
		return
	}

	hash, err := auth.HashPIN(*in.PIN)
	if err != nil {
		a.log.Error("hash pin failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not set PIN")
		return
	}
	raw, dev, err := a.deps.Auth.IssueDevice(r.Context(), sess.Username, hash)
	if err != nil {
		a.log.Error("issue device failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not set PIN")
		return
	}
	if err := a.deps.Auth.AttachDevice(r.Context(), sess.ID, dev.ID); err != nil {
		a.log.Error("attach device failed", "err", err, "session", sess, "device", dev)
		// Without the tag the PIN would never lock anything: drop it.
		if derr := a.deps.Devices.DeleteDevice(r.Context(), dev.ID); derr != nil {
			a.log.Error("drop unattached device failed", "err", derr, "device", dev)
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not set PIN")
		return
	}
	auth.SetDeviceCookie(w, a.deps.Auth.DeviceCookie(raw))
	a.log.Info("pin set", "session", sess, "device", dev)
	w.WriteHeader(http.StatusNoContent)
}

// changePIN replaces a tagged session's PIN after re-checking the AdGuard
// password for the session's own username (locked pin_change_reauth) under
// the login rate limit. The new hash resets the strike counter; the device
// token, and so the device cookie, is kept.
func (a *API) changePIN(w http.ResponseWriter, r *http.Request, sess auth.Session, pin, password string) {
	if !a.reauth(w, r, sess, password) {
		return
	}
	hash, err := auth.HashPIN(pin)
	if err != nil {
		a.log.Error("hash pin failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not change PIN")
		return
	}
	switch err := a.deps.Devices.ReplacePIN(r.Context(), sess.DeviceID, hash); {
	case errors.Is(err, auth.ErrDeviceNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "this device has no PIN")
		return
	case err != nil:
		a.log.Error("replace pin failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not change PIN")
		return
	}
	a.log.Info("pin changed", "session", sess)
	w.WriteHeader(http.StatusNoContent)
}

// removeRequest is the body of DELETE /api/v1/pin.
type removeRequest struct {
	Password *string `json:"password"`
}

// handlePinRemove is DELETE /api/v1/pin {password}: after the password
// re-check the device and its PIN go and the device cookie is cleared,
// while this session carries on untagged under the 30-day sliding expiry.
// Any other session still tagged with the device goes with it.
func (a *API) handlePinRemove(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	var in removeRequest
	if err := decodeBody(w, r, maxPINBody, &in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {password}")
		return
	}
	if a.deps.Devices == nil {
		writeError(w, http.StatusInternalServerError, "internal", "device PINs are not configured")
		return
	}
	if sess.DeviceID == 0 {
		writeError(w, http.StatusNotFound, CodeNotFound, "this device has no PIN")
		return
	}
	if in.Password == nil || *in.Password == "" {
		writeError(w, http.StatusForbidden, CodeReauthRequired, "removing the PIN needs your AdGuard password")
		return
	}
	if !a.reauth(w, r, sess, *in.Password) {
		return
	}
	// Detach this session first so the revoke below spares it.
	if err := a.deps.Auth.DetachDevice(r.Context(), sess.ID); err != nil {
		a.log.Error("detach device failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not remove PIN")
		return
	}
	if err := a.deps.Devices.RevokeDevice(r.Context(), sess.DeviceID); err != nil {
		a.log.Error("remove device failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not remove PIN")
		return
	}
	a.deps.Auth.ClearDevice(w)
	a.log.Info("pin removed", "session", sess)
	w.WriteHeader(http.StatusNoContent)
}

// reauth runs the shared password re-check for the session's username and
// writes the refusal itself; true means the password was right.
func (a *API) reauth(w http.ResponseWriter, r *http.Request, sess auth.Session, password string) bool {
	res := a.recheckPassword(r, sess.Username, password)
	switch res.outcome {
	case outcomeOK:
		return true
	case outcomeBadCreds:
		writeError(w, http.StatusForbidden, CodeBadCredentials, "wrong password")
	case outcomeRateLimited:
		w.Header().Set("Retry-After", strconv.Itoa(int(res.retry/time.Second)))
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, rateLimitedMsg)
	case outcomeUnreachable:
		writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable, adguardUnavailable)
	}
	// outcomeCanceled: the client is gone; nothing to write.
	return false
}
