package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

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

// changePIN replaces a tagged session's PIN after the password re-check.
// It lands with PIN change and remove; until then it refuses.
func (a *API) changePIN(w http.ResponseWriter, _ *http.Request, _ auth.Session, _, _ string) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "changing a PIN is not available yet")
}
