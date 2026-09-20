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
}

// API is the router plus its dependencies.
type API struct {
	deps Deps
	mux  *http.ServeMux
	log  *slog.Logger
}

// New builds the router. Routes are registered by the handler files.
func New(d Deps) *API {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	a := &API{deps: d, mux: http.NewServeMux(), log: d.Log}
	a.routes()
	return a
}

// routes mounts every /api/v1 handler. Kept in one place so the order of
// registration — and the catch-all — is visible.
func (a *API) routes() {}

// Handler is the API mounted under /api/v1/ with the no-store and CSRF
// layers wrapping the mux, 404s and 405s included.
func (a *API) Handler() http.Handler {
	return noStore(csrf(a.mux))
}
