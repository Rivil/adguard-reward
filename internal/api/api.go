// Package api serves /api/v1: the JSON surface the SPA talks to.
//
// Every response is Cache-Control: no-store and every state-changing
// request must carry the CSRF header; both are applied outside the mux so
// unknown paths and wrong methods get the same treatment as real routes.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
)

// AdGuard is the slice of *adguard.Client the API needs.
type AdGuard interface {
	Login(ctx context.Context, user, pass string) error
}

// Deps is everything the handlers reach for. Interfaces are api-local so
// the package compiles against fakes.
type Deps struct {
	AdGuard  AdGuard
	ClientIP func(*http.Request) netip.Addr
	Log      *slog.Logger
	Auth     *auth.Manager
	Limiter  *ratelimit.Limiter
	Sessions auth.SessionStore
}

// API is the router plus its dependencies.
type API struct {
	deps Deps
	mux  *http.ServeMux
	log  *slog.Logger

	// loginSlot serialises the AdGuard round-trip so a parallel burst is
	// bounded by the limiter: a request holds it from its second
	// Limiter.Check until Fail or Issue has run.
	loginSlot chan struct{}
}

// New builds the router. Routes are registered by the handler files.
func New(d Deps) *API {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	a := &API{deps: d, mux: http.NewServeMux(), log: d.Log, loginSlot: make(chan struct{}, 1)}
	a.routes()
	return a
}

// routes mounts every /api/v1 handler. Kept in one place so the order of
// registration — and the catch-all — is visible.
func (a *API) routes() {
	a.mux.HandleFunc("POST /api/v1/login", a.handleLogin)
	a.mux.Handle("POST /api/v1/logout", a.requireSession(http.HandlerFunc(a.handleLogout)))
	a.mux.Handle("GET /api/v1/me", a.requireSession(http.HandlerFunc(a.handleMe)))
	// Unknown paths are 401 without a session and 404 with one, so the
	// route table cannot be probed anonymously.
	a.mux.Handle("/api/v1/", a.requireSession(http.NotFoundHandler()))
}

// requireSession gates next behind auth.RequireSession. With no Manager
// wired it fails closed: nothing can authenticate, so everything is 401.
func (a *API) requireSession(next http.Handler) http.Handler {
	if a.deps.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		})
	}
	return a.deps.Auth.RequireSession(next)
}

// Handler is the API mounted under /api/v1/ with the no-store and CSRF
// layers wrapping the mux, 404s and 405s included.
func (a *API) Handler() http.Handler {
	return noStore(csrf(a.mux))
}
