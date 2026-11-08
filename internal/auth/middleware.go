package auth

import (
	"context"
	"errors"
	"net/http"
)

type ctxKey struct{}

// FromContext returns the session RequireSession stored for this request.
func FromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(Session)
	return s, ok
}

// RequireSession gates next behind a valid session cookie. A valid cookie
// is re-issued with a fresh Max-Age — the browser's expiry slides with the
// server's — and the session is placed in the request context.
//
// Without a live session the answer is a 401 whose code depends on the
// device cookie: a live PIN device gets pin_required (the SPA shows the PIN
// pad; the device cookie is kept), anything else gets unauthorized (the SPA
// shows the login page) and a presented device cookie is cleared. A
// presented session cookie is cleared either way so the browser stops
// replaying it. A store failure says nothing about either cookie: 500 and
// both are kept.
func (m *Manager) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			m.rejectNoSession(w, r, false)
			return
		}
		s, err := m.Authenticate(r.Context(), c.Value)
		switch {
		case errors.Is(err, ErrNoSession):
			m.rejectNoSession(w, r, true)
			return
		case err != nil:
			// The store failed, which says nothing about the cookie: keep it.
			m.log.Error("session lookup failed", "err", err)
			m.writeErr(w, http.StatusInternalServerError, "internal", "session lookup failed")
			return
		}
		SetCookie(w, m.Cookie(c.Value))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
	})
}

// rejectNoSession writes RequireSession's 401 (or a 500 when the device
// store fails). hadSession says a session cookie was presented and must be
// cleared.
func (m *Manager) rejectNoSession(w http.ResponseWriter, r *http.Request, hadSession bool) {
	if m.devices != nil {
		if dc, err := r.Cookie(DeviceCookieName); err == nil {
			_, err := m.LookupDevice(r.Context(), dc.Value)
			switch {
			case err == nil:
				if hadSession {
					m.Clear(w)
				}
				m.writeErr(w, http.StatusUnauthorized, CodePINRequired, "enter this device's PIN")
				return
			case errors.Is(err, ErrDeviceNotFound):
				m.ClearDevice(w)
			default:
				m.log.Error("device lookup failed", "err", err)
				m.writeErr(w, http.StatusInternalServerError, "internal", "device lookup failed")
				return
			}
		}
	}
	if hadSession {
		m.Clear(w)
	}
	m.writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
}
