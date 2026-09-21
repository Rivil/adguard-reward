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

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

// AdGuard is the slice of *adguard.Client the API needs.
type AdGuard interface {
	Login(ctx context.Context, user, pass string) error
	Clients(ctx context.Context) (adguard.ClientsResult, error)
	Services(ctx context.Context) ([]adguard.Service, error)
	MigrateFromGlobal(ctx context.Context, clientName string, ids []string) error
}

// ChildStore is the slice of *store.Store the children handlers need. It
// speaks store.Child and the store's sentinels (ErrChildNotFound,
// ErrNameTaken, *ErrClientTaken).
type ChildStore interface {
	ListChildren(ctx context.Context) ([]store.Child, error)
	GetChild(ctx context.Context, id int64) (store.Child, error)
	CreateChild(ctx context.Context, name string, clients []string) (store.Child, error)
	UpdateChild(ctx context.Context, id int64, name string, clients []string) (store.Child, error)
	DeleteChild(ctx context.Context, id int64) (bool, error)
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
	Children ChildStore
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
	a.mux.Handle("GET /api/v1/sessions", a.requireSession(http.HandlerFunc(a.handleSessionsList)))
	a.mux.Handle("DELETE /api/v1/sessions/{id}", a.requireSession(http.HandlerFunc(a.handleSessionDelete)))
	a.mux.Handle("POST /api/v1/sessions/revoke-all", a.requireSession(http.HandlerFunc(a.handleSessionsRevokeAll)))
	a.mux.Handle("GET /api/v1/children", a.requireSession(http.HandlerFunc(a.handleChildrenList)))
	a.mux.Handle("POST /api/v1/children", a.requireSession(http.HandlerFunc(a.handleChildCreate)))
	a.mux.Handle("GET /api/v1/children/{id}", a.requireSession(http.HandlerFunc(a.handleChildGet)))
	a.mux.Handle("PUT /api/v1/children/{id}", a.requireSession(http.HandlerFunc(a.handleChildUpdate)))
	a.mux.Handle("DELETE /api/v1/children/{id}", a.requireSession(http.HandlerFunc(a.handleChildDelete)))
	a.mux.Handle("GET /api/v1/children/{id}/blocked", a.requireSession(http.HandlerFunc(a.handleBlocked)))
	a.mux.Handle("GET /api/v1/clients", a.requireSession(http.HandlerFunc(a.handleClients)))
	a.mux.Handle("GET /api/v1/services", a.requireSession(http.HandlerFunc(a.handleServices)))
	a.mux.Handle("GET /api/v1/migration", a.requireSession(http.HandlerFunc(a.handleMigrationOffer)))
	a.mux.Handle("POST /api/v1/migration", a.requireSession(http.HandlerFunc(a.handleMigrationApply)))
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
