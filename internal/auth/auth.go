// Package auth issues and checks session tokens.
//
// A session is 256 random bits handed to the browser in an HttpOnly cookie;
// only its SHA-256 is stored, so a copy of the database cannot be replayed.
// Expiry slides: every authenticated request pushes it out by Lifetime. The
// raw token is never logged — Session carries no field that holds it and
// renders as id + username under slog.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

var (
	// ErrNotFound is returned by a SessionStore when no row matches.
	ErrNotFound = errors.New("session not found")
	// ErrNoSession is returned by Authenticate for any token that does not
	// name a live session — malformed, unknown, revoked or expired alike.
	ErrNoSession = errors.New("no session")
)

// Session is one logged-in browser.
type Session struct {
	ID         int64
	Username   string
	TokenHash  [32]byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// LogValue implements slog.LogValuer: only the id and username are logged.
func (s Session) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("id", s.ID),
		slog.String("username", s.Username),
	)
}

// SessionStore persists sessions. internal/store implements it over SQLite.
type SessionStore interface {
	// Insert stores s (ID ignored) and returns the assigned id.
	Insert(ctx context.Context, s Session) (id int64, err error)
	// ByTokenHash returns the session with this hash or ErrNotFound.
	ByTokenHash(ctx context.Context, hash [32]byte) (Session, error)
	// Touch updates last_seen_at and expires_at.
	Touch(ctx context.Context, id int64, lastSeen, expires time.Time) error
	// Delete removes one session by id.
	Delete(ctx context.Context, id int64) error
	// ListByUser lists a user's sessions, oldest first.
	ListByUser(ctx context.Context, username string) ([]Session, error)
	// DeleteByUser removes id only if it belongs to username; reports
	// whether a row went.
	DeleteByUser(ctx context.Context, username string, id int64) (bool, error)
	// DeleteOthers removes every session of username except keepID and
	// returns how many went.
	DeleteOthers(ctx context.Context, username string, keepID int64) (int, error)
	// DeleteExpired removes sessions with expires_at <= now.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// ErrorWriter renders an error response; the JSON envelope lives in
// internal/api, so the middleware is handed one rather than owning it.
type ErrorWriter func(w http.ResponseWriter, status int, code, message string)

// Options configures a Manager. Zero values: Lifetime 30 days, Now
// time.Now, Log discarded, ErrorWriter a plain-text http.Error.
type Options struct {
	// Secure sets the cookie's Secure attribute; true whenever the app is
	// reached over TLS (its own or a proxy's).
	Secure bool
	// Lifetime is the sliding idle expiry.
	Lifetime time.Duration
	Now      func() time.Time
	Log      *slog.Logger
	// ErrorWriter renders RequireSession's 401.
	ErrorWriter ErrorWriter
}

// DefaultLifetime is the locked 30-day sliding expiry.
const DefaultLifetime = 30 * 24 * time.Hour

// Manager issues, checks and revokes sessions.
type Manager struct {
	store    SessionStore
	secure   bool
	lifetime time.Duration
	now      func() time.Time
	log      *slog.Logger
	writeErr ErrorWriter
}

// New returns a Manager over store.
func New(store SessionStore, o Options) *Manager {
	if o.Lifetime == 0 {
		o.Lifetime = DefaultLifetime
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.ErrorWriter == nil {
		o.ErrorWriter = func(w http.ResponseWriter, status int, _, message string) {
			http.Error(w, message, status)
		}
	}
	return &Manager{
		store:    store,
		secure:   o.Secure,
		lifetime: o.Lifetime,
		now:      o.Now,
		log:      o.Log,
		writeErr: o.ErrorWriter,
	}
}

// Issue creates a session for username and returns the raw token to hand
// to the browser. The token exists only in the return value.
func (m *Manager) Issue(ctx context.Context, username string) (string, Session, error) {
	raw, hash, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	now := m.now()
	s := Session{
		Username:   username,
		TokenHash:  hash,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(m.lifetime),
	}
	id, err := m.store.Insert(ctx, s)
	if err != nil {
		return "", Session{}, err
	}
	s.ID = id
	m.log.Debug("session issued", "session", s)
	return raw, s, nil
}

// Authenticate resolves a presented token to its session, sliding the expiry
// forward. Every failure is ErrNoSession; an expired row is deleted on sight.
func (m *Manager) Authenticate(ctx context.Context, raw string) (Session, error) {
	hash, ok := hashToken(raw)
	if !ok {
		m.log.Debug("session rejected", "reason", "malformed token")
		return Session{}, ErrNoSession
	}
	s, err := m.store.ByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			m.log.Debug("session rejected", "reason", "unknown token")
			return Session{}, ErrNoSession
		}
		return Session{}, err
	}
	now := m.now()
	if !s.ExpiresAt.After(now) {
		if err := m.store.Delete(ctx, s.ID); err != nil {
			return Session{}, err
		}
		m.log.Debug("session rejected", "reason", "expired", "session", s)
		return Session{}, ErrNoSession
	}
	s.LastSeenAt, s.ExpiresAt = now, now.Add(m.lifetime)
	if err := m.store.Touch(ctx, s.ID, s.LastSeenAt, s.ExpiresAt); err != nil {
		return Session{}, err
	}
	return s, nil
}

// Cookie is the session cookie carrying raw.
func (m *Manager) Cookie(raw string) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    raw,
		Path:     "/",
		MaxAge:   int(m.lifetime / time.Second),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// ClearCookie is the cookie that deletes the session cookie.
func (m *Manager) ClearCookie() *http.Cookie {
	c := m.Cookie("")
	c.MaxAge = -1
	return c
}

// SetCookie writes c, replacing any Set-Cookie for the session cookie
// already on w so a response never carries two.
func SetCookie(w http.ResponseWriter, c *http.Cookie) {
	dropSessionCookie(w.Header())
	http.SetCookie(w, c)
}

// Clear writes the clearing cookie, replacing any refresh already on w.
func (m *Manager) Clear(w http.ResponseWriter) {
	SetCookie(w, m.ClearCookie())
}

func dropSessionCookie(h http.Header) {
	existing := h.Values("Set-Cookie")
	if len(existing) == 0 {
		return
	}
	kept := existing[:0:0]
	for _, v := range existing {
		if len(v) > len(CookieName) && v[:len(CookieName)+1] == CookieName+"=" {
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		h.Del("Set-Cookie")
		return
	}
	h["Set-Cookie"] = kept
}

// RunSweeper deletes expired sessions every interval until ctx is done.
func (m *Manager) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := m.store.DeleteExpired(ctx, m.now())
			switch {
			case err != nil && ctx.Err() == nil:
				m.log.Error("session sweep failed", "err", err)
			case n > 0:
				m.log.Info("expired sessions removed", "count", n)
			}
		}
	}
}
