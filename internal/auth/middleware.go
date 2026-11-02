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

// RequireSession gates next behind a valid session cookie. A missing or
// invalid cookie gets a 401 through the ErrorWriter, plus a clearing cookie
// when one was presented so the browser stops replaying it. A valid cookie
// is re-issued with a fresh Max-Age — the browser's expiry slides with the
// server's — and the session is placed in the request context.
func (m *Manager) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			m.writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		s, err := m.Authenticate(r.Context(), c.Value)
		switch {
		case errors.Is(err, ErrNoSession):
			m.Clear(w)
			m.writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
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
