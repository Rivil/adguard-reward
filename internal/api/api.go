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
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

// AdGuard is the slice of *adguard.Client the API needs.
type AdGuard interface {
	Login(ctx context.Context, user, pass string) error
	Clients(ctx context.Context) (adguard.ClientsResult, error)
	Services(ctx context.Context) ([]adguard.Service, error)
	MigrateFromGlobal(ctx context.Context, clientName string, ids []string) error
	Leases(ctx context.Context) ([]adguard.Lease, error)
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
	// ListServiceToggles feeds the blocked view's permanent toggle state.
	ListServiceToggles(ctx context.Context) ([]store.ServiceToggle, error)
}

// Grants is the slice of *grants.Engine the grant and client-write handlers
// need. It speaks store.Grant and the engine's sentinels (ErrGrantNotFound,
// *ErrRevertFailed, *ErrAdoptFailed) plus the store's *ErrGrantOverlap and
// the AdGuard client's write errors.
type Grants interface {
	Create(ctx context.Context, childID int64, services, clients []string, d time.Duration) (grants.Result, error)
	List(ctx context.Context) ([]store.Grant, error)
	Extend(ctx context.Context, id int64, d time.Duration) (store.Grant, error)
	End(ctx context.Context, id int64) error
	CreateClient(ctx context.Context, name string, ids []string, childID *int64) error
	AdoptClients(ctx context.Context, childID int64, siblings, added []string) error
	RenameClient(ctx context.Context, oldName, newName string) error
	CreateService(ctx context.Context, name string, domains []string) (grants.ServiceResult, error)
	UpdateService(ctx context.Context, id int64, name string, domains []string) (grants.ServiceResult, error)
	DeleteService(ctx context.Context, id int64) (grants.ServiceResult, error)
	SyncRules(ctx context.Context) error
}

// ButtonStore is the slice of *store.Store the buttons handlers need. It
// speaks store.Button and the store's *ErrUnknownChild.
type ButtonStore interface {
	ListButtons(ctx context.Context) ([]store.Button, error)
	ReplaceButtons(ctx context.Context, in []store.Button) ([]store.Button, error)
}

// CustomServiceStore is the slice of *store.Store the catalogue needs: the
// parent-defined services, by id.
type CustomServiceStore interface {
	ListCustomServices(ctx context.Context) ([]store.CustomService, error)
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
	Grants   Grants
	Buttons  ButtonStore
	// CustomServices may be nil, and the catalogue then lists built-ins
	// only; main always wires it.
	CustomServices CustomServiceStore
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
	a.mux.Handle("POST /api/v1/clients", a.requireSession(http.HandlerFunc(a.handleClientCreate)))
	a.mux.Handle("POST /api/v1/clients/rename", a.requireSession(http.HandlerFunc(a.handleClientRename)))
	a.mux.Handle("GET /api/v1/services", a.requireSession(http.HandlerFunc(a.handleServices)))
	a.mux.Handle("GET /api/v1/migration", a.requireSession(http.HandlerFunc(a.handleMigrationOffer)))
	a.mux.Handle("POST /api/v1/migration", a.requireSession(http.HandlerFunc(a.handleMigrationApply)))
	a.mux.Handle("GET /api/v1/grants", a.requireSession(http.HandlerFunc(a.handleGrantsList)))
	a.mux.Handle("POST /api/v1/grants", a.requireSession(http.HandlerFunc(a.handleGrantCreate)))
	a.mux.Handle("POST /api/v1/grants/{id}/extend", a.requireSession(http.HandlerFunc(a.handleGrantExtend)))
	a.mux.Handle("POST /api/v1/grants/{id}/end", a.requireSession(http.HandlerFunc(a.handleGrantEnd)))
	a.mux.Handle("GET /api/v1/buttons", a.requireSession(http.HandlerFunc(a.handleButtonsList)))
	a.mux.Handle("PUT /api/v1/buttons", a.requireSession(http.HandlerFunc(a.handleButtonsReplace)))
	a.mux.Handle("GET /api/v1/custom-services", a.requireSession(http.HandlerFunc(a.handleCustomServicesList)))
	a.mux.Handle("POST /api/v1/custom-services", a.requireSession(http.HandlerFunc(a.handleCustomServiceCreate)))
	a.mux.Handle("PUT /api/v1/custom-services/{id}", a.requireSession(http.HandlerFunc(a.handleCustomServiceUpdate)))
	a.mux.Handle("DELETE /api/v1/custom-services/{id}", a.requireSession(http.HandlerFunc(a.handleCustomServiceDelete)))
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
