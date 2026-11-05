package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/netip"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// Unlock outcomes, one per attempt in the event=unlock log line.
const (
	unlockOK         = "ok"
	unlockBadPIN     = "bad_pin"
	unlockRevoked    = "device_revoked"
	unlockNoDevice   = "no_device"
	unlockBadRequest = "bad_request"
	// unlockError: the server failed (store, hashing); the answer is 500.
	unlockError = "error"
)

type unlockRequest struct {
	PIN *string `json:"pin"`
}

// deviceTokenEncoding mirrors the auth token format so a malformed cookie
// can be told from a well-formed one naming no live device: the first is
// no_device (plain 401), the second device_revoked.
var deviceTokenEncoding = base64.RawURLEncoding.Strict()

func wellFormedDeviceToken(raw string) bool {
	if len(raw) != deviceTokenEncoding.EncodedLen(32) {
		return false
	}
	b, err := deviceTokenEncoding.DecodeString(raw)
	return err == nil && len(b) == 32
}

// handleUnlock is POST /api/v1/unlock {pin}: trade a live device cookie and
// its PIN for a fresh session for the device's username, without AdGuard.
//
//   - no or malformed device cookie: 401 unauthorized (no_device)
//   - well-formed cookie, no live device: 401 device_revoked, cookie cleared
//   - wrong PIN: 401 bad_pin {attempts_remaining}; the 5th in a row revokes
//     the device and every session tagged with it: 401 device_revoked
//   - right PIN: counter reset, device slid to +90 days, session issued
//     tagged with the device, both cookies set, 204
//   - malformed or oversize body: 400, no attempt consumed
//   - a store or hashing failure: 500, logged with outcome error
//
// The attempt is reserved in the store before Argon2 runs, so parallel
// guesses cannot outrun the cap. Every attempt logs one event=unlock line;
// neither the PIN nor any raw token is ever an attribute.
func (a *API) handleUnlock(w http.ResponseWriter, r *http.Request) {
	ip := a.deps.ClientIP(r)
	var in unlockRequest
	badBody := decodeBody(w, r, maxPINBody, &in) != nil || in.PIN == nil

	if a.deps.Auth == nil || a.deps.Devices == nil {
		writeError(w, http.StatusInternalServerError, "internal", "device PINs are not configured")
		return
	}

	c, err := r.Cookie(auth.DeviceCookieName)
	if err != nil || !wellFormedDeviceToken(c.Value) {
		if badBody {
			a.rejectUnlockBody(w, "", ip)
			return
		}
		a.logUnlock("", ip, unlockNoDevice)
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}

	dev, err := a.deps.Auth.LookupDevice(r.Context(), c.Value)
	switch {
	case errors.Is(err, auth.ErrDeviceNotFound):
		if badBody {
			// The cookie is dead either way: stop the browser replaying it.
			a.deps.Auth.ClearDevice(w)
			a.rejectUnlockBody(w, "", ip)
			return
		}
		a.revokedUnlock(w, "", ip)
		return
	case err != nil:
		a.failUnlock(w, "", ip, "device lookup failed", err)
		return
	}
	if badBody {
		a.rejectUnlockBody(w, dev.Username, ip)
		return
	}

	attempt, err := a.deps.Devices.ReserveAttempt(r.Context(), dev.ID)
	switch {
	case errors.Is(err, auth.ErrNoAttempts):
		// Spent. Normally the 5th strike already revoked it; if that revoke
		// failed, finish it now so its tagged sessions do not live on.
		if err := a.deps.Devices.RevokeDevice(r.Context(), dev.ID); err != nil {
			a.failUnlock(w, dev.Username, ip, "revoke spent device failed", err)
			return
		}
		a.revokedUnlock(w, dev.Username, ip)
		return
	case errors.Is(err, auth.ErrDeviceNotFound):
		// Revoked by a parallel attempt since the lookup.
		a.revokedUnlock(w, dev.Username, ip)
		return
	case err != nil:
		a.failUnlock(w, dev.Username, ip, "reserve attempt failed", err)
		return
	}

	ok, err := auth.VerifyPIN(*in.PIN, dev.PINHash)
	if err != nil {
		a.failUnlock(w, dev.Username, ip, "stored pin hash unreadable", err)
		return
	}
	if !ok {
		if attempt >= auth.MaxPINAttempts {
			if err := a.deps.Devices.RevokeDevice(r.Context(), dev.ID); err != nil {
				a.failUnlock(w, dev.Username, ip, "revoke device failed", err)
				return
			}
			a.revokedUnlock(w, dev.Username, ip)
			return
		}
		a.logUnlock(dev.Username, ip, unlockBadPIN)
		writeJSON(w, http.StatusUnauthorized, struct {
			Error             string `json:"error"`
			Message           string `json:"message"`
			AttemptsRemaining int    `json:"attempts_remaining"`
		}{CodeBadPIN, "wrong PIN", auth.MaxPINAttempts - attempt})
		return
	}

	if err := a.deps.Devices.ResetAttempts(r.Context(), dev.ID); err != nil {
		a.failUnlock(w, dev.Username, ip, "reset attempts failed", err)
		return
	}
	raw, sess, err := a.deps.Auth.IssueForDevice(r.Context(), dev.Username, dev.ID)
	if err != nil {
		a.failUnlock(w, dev.Username, ip, "issue session failed", err)
		return
	}
	// The manager's clock stamped the session; the device slides from the
	// same instant. Only a successful unlock slides the device.
	if err := a.deps.Devices.TouchDevice(r.Context(), dev.ID, sess.CreatedAt.Add(a.deps.Auth.DeviceLifetime())); err != nil {
		// The session's cookie is never sent: do not leave its row behind.
		if derr := a.deps.Sessions.Delete(r.Context(), sess.ID); derr != nil {
			a.log.Error("unlock: drop unsent session failed", "err", derr, "session", sess)
		}
		a.failUnlock(w, dev.Username, ip, "touch device failed", err)
		return
	}
	auth.SetCookie(w, a.deps.Auth.Cookie(raw))
	auth.SetDeviceCookie(w, a.deps.Auth.DeviceCookie(c.Value))
	a.logUnlock(dev.Username, ip, unlockOK)
	w.WriteHeader(http.StatusNoContent)
}

// failUnlock is the 500 for a server-side failure, still logged as one
// event=unlock line.
func (a *API) failUnlock(w http.ResponseWriter, username string, ip netip.Addr, what string, err error) {
	a.log.Error("unlock: "+what, "err", err)
	a.logUnlock(username, ip, unlockError)
	writeError(w, http.StatusInternalServerError, "internal", "could not unlock")
}

func (a *API) rejectUnlockBody(w http.ResponseWriter, username string, ip netip.Addr) {
	a.logUnlock(username, ip, unlockBadRequest)
	writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {pin}")
}

func (a *API) revokedUnlock(w http.ResponseWriter, username string, ip netip.Addr) {
	a.deps.Auth.ClearDevice(w)
	a.logUnlock(username, ip, unlockRevoked)
	writeError(w, http.StatusUnauthorized, CodeDeviceRevoked, "this device was signed out — sign in with your password")
}

// logUnlock is the one structured line per unlock attempt.
func (a *API) logUnlock(username string, ip netip.Addr, outcome string) {
	a.log.Info("unlock", "event", "unlock", "username", username, "ip", ip.String(), "outcome", outcome)
}
