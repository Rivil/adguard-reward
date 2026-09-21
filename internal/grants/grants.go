// Package grants owns every AdGuard write made on behalf of a grant and is
// the only thing that moves a grant out of active.
//
// A grant is created active and its services are removed from each stored
// client's blocked_services; an in-process timer re-adds them at ends_at.
// Every mutating operation runs under one mutex across its whole
// read-decide-write, and every AdGuard call it makes shares one
// ApplyTimeout-bounded context, so a hung AdGuard can hold the mutex for at
// most ApplyTimeout regardless of client count. The store row is the intent:
// a failed write never rolls it back, the reconciler converges it later.
package grants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

// Duration bounds for a grant and an extension (locked duration_bounds).
// The engine does not enforce them — the API layer does.
const (
	MinDuration = time.Minute
	MaxDuration = 24 * time.Hour
)

// DefaultApplyTimeout bounds the AdGuard writes of one operation.
const DefaultApplyTimeout = 4 * time.Second

// ErrGrantNotFound is returned for an unknown or non-active grant.
var ErrGrantNotFound = store.ErrGrantNotFound

// ErrRevertFailed says which clients End could not re-block; the grant
// stays active with its timer so the reconciler retries.
type ErrRevertFailed struct {
	Clients []string
}

func (e *ErrRevertFailed) Error() string {
	return "grants: revert failed for " + strings.Join(e.Clients, ", ")
}

// Store is the persistence the engine needs; *store.Store satisfies it.
type Store interface {
	CreateGrant(ctx context.Context, childID int64, services, clients []string, startedAt, endsAt time.Time) (store.Grant, error)
	ListActiveGrants(ctx context.Context) ([]store.Grant, error)
	GetGrant(ctx context.Context, id int64) (store.Grant, error)
	ExtendGrant(ctx context.Context, id int64, newEndsAt time.Time) (store.Grant, error)
	SetGrantStatus(ctx context.Context, id int64, from, to string, at time.Time) (bool, error)
}

// AdGuard is the client subset the engine writes through; *adguard.Client
// satisfies it.
type AdGuard interface {
	Clients(ctx context.Context) (adguard.ClientsResult, error)
	RemoveBlockedServices(ctx context.Context, clientName string, ids []string) error
	AddBlockedServices(ctx context.Context, clientName string, ids []string) error
}

// Timer is what AfterFunc returns; *time.Timer satisfies it.
type Timer interface {
	Stop() bool
}

// Options configures an Engine. Zero values: Now time.Now, AfterFunc
// time.AfterFunc, ApplyTimeout DefaultApplyTimeout, Log discarded.
type Options struct {
	Now          func() time.Time
	AfterFunc    func(d time.Duration, f func()) Timer
	ApplyTimeout time.Duration
	Log          *slog.Logger
}

// Result is what Create returns: the stored grant plus whether every stored
// client was unblocked. Failed names the clients that were not (never nil).
type Result struct {
	Grant   store.Grant
	Applied bool
	Failed  []string
}

// Engine schedules and applies grants.
type Engine struct {
	store        Store
	ag           AdGuard
	now          func() time.Time
	afterFunc    func(time.Duration, func()) Timer
	applyTimeout time.Duration
	log          *slog.Logger

	mu     sync.Mutex
	timers map[int64]Timer
	closed bool
}

// New returns an Engine over st and ag. Call Close to stop its timers.
func New(st Store, ag AdGuard, o Options) *Engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.AfterFunc == nil {
		o.AfterFunc = func(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
	}
	if o.ApplyTimeout == 0 {
		o.ApplyTimeout = DefaultApplyTimeout
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		store:        st,
		ag:           ag,
		now:          o.Now,
		afterFunc:    o.AfterFunc,
		applyTimeout: o.ApplyTimeout,
		log:          o.Log,
		timers:       map[int64]Timer{},
	}
}

// List returns every active grant. It does not take the engine mutex — the
// store's single connection already serialises it — so it answers while a
// write to AdGuard is in flight.
func (e *Engine) List(ctx context.Context) ([]store.Grant, error) {
	return e.store.ListActiveGrants(ctx)
}

// Create stores an active grant for d from now and removes services from
// each client's blocked_services. An overlapping active grant is
// *store.ErrGrantOverlap from the store, untouched. A client that cannot be
// written — unknown, inheriting the global list, or an AdGuard error — goes
// into Result.Failed; the row stays active for the reconciler to converge
// (locked partial_apply). The expiry timer is armed either way.
func (e *Engine) Create(ctx context.Context, childID int64, services, clients []string, d time.Duration) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	g, err := e.store.CreateGrant(ctx, childID, services, clients, now, now.Add(d))
	if err != nil {
		return Result{}, err
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	failed := e.unblock(actx, g)
	e.arm(g)
	return Result{Grant: g, Applied: len(failed) == 0, Failed: failed}, nil
}

// unblock removes g's services from each stored client, reading AdGuard once
// for the global flag. A failed read fails every client without a write.
func (e *Engine) unblock(ctx context.Context, g store.Grant) []string {
	res, err := e.ag.Clients(ctx)
	if err != nil {
		e.log.Warn("grant apply: read clients failed", "grant", g.ID, "err", err)
		return slices.Clone(g.Clients)
	}
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}
	failed := []string{}
	for _, name := range g.Clients {
		pc, ok := byName[name]
		switch {
		case !ok:
			e.log.Warn("grant apply: client not found", "grant", g.ID, "client", name)
			failed = append(failed, name)
		case pc.UseGlobalBlockedServices:
			e.log.Warn("grant apply: client uses the global blocked-services list; migrate it first",
				"grant", g.ID, "client", name)
			failed = append(failed, name)
		default:
			if err := e.ag.RemoveBlockedServices(ctx, name, g.Services); err != nil {
				e.log.Warn("grant apply: unblock failed", "grant", g.ID, "client", name, "err", err)
				failed = append(failed, name)
			}
		}
	}
	return failed
}

// revert re-adds g's services to every stored client (set-union, so a second
// revert changes nothing). A client AdGuard no longer has counts as
// reverted; any other failure is returned by name.
func (e *Engine) revert(ctx context.Context, g store.Grant) []string {
	failed := []string{}
	for _, name := range g.Clients {
		err := e.ag.AddBlockedServices(ctx, name, g.Services)
		switch {
		case err == nil:
		case errors.Is(err, adguard.ErrClientNotFound):
			e.log.Debug("grant revert: client gone, nothing to re-block", "grant", g.ID, "client", name)
		default:
			e.log.Warn("grant revert: re-block failed", "grant", g.ID, "client", name, "err", err)
			failed = append(failed, name)
		}
	}
	return failed
}

// expire is the timer callback. It re-checks the row under the mutex: a
// grant extended meanwhile is re-armed, one no longer active is dropped.
// A failed revert leaves the grant active with no timer so the reconciler
// retries it.
func (e *Engine) expire(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.timers, id)

	ctx := context.Background()
	g, err := e.store.GetGrant(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrGrantNotFound) {
			e.log.Error("grant expiry: read failed", "grant", id, "err", err)
		}
		return
	}
	if g.Status != store.StatusActive {
		return
	}
	if e.now().Before(g.EndsAt) {
		e.arm(g)
		return
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	e.expireLocked(ctx, actx, g)
}

// expireLocked reverts an overdue active grant through actx — the
// operation's shared AdGuard context — and marks it expired through ctx.
// It reports whether the status changed. Caller holds mu.
func (e *Engine) expireLocked(ctx, actx context.Context, g store.Grant) bool {
	if failed := e.revert(actx, g); len(failed) > 0 {
		e.log.Warn("grant expiry: revert incomplete, grant stays active", "grant", g.ID, "clients", failed)
		return false
	}
	ok, err := e.store.SetGrantStatus(ctx, g.ID, store.StatusActive, store.StatusExpired, e.now())
	if err != nil {
		e.log.Error("grant expiry: status update failed", "grant", g.ID, "err", err)
		return false
	}
	if ok {
		e.log.Info("grant expired", "grant", g.ID, "child", g.ChildID, "services", g.Services)
		e.stopTimer(g.ID)
	}
	return ok
}

// Extend pushes an active grant's ends_at by d and reschedules its timer.
// An unknown or non-active grant is ErrGrantNotFound.
func (e *Engine) Extend(ctx context.Context, id int64, d time.Duration) (store.Grant, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	g, err := e.store.GetGrant(ctx, id)
	if err != nil {
		return store.Grant{}, err
	}
	if g.Status != store.StatusActive {
		return store.Grant{}, ErrGrantNotFound
	}
	g, err = e.store.ExtendGrant(ctx, id, g.EndsAt.Add(d))
	if err != nil {
		return store.Grant{}, err
	}
	e.arm(g)
	return g, nil
}

// End reverts an active grant now and marks it ended. An unknown or
// non-active grant is ErrGrantNotFound; a client that could not be re-blocked
// is *ErrRevertFailed and the grant stays active with its timer.
func (e *Engine) End(ctx context.Context, id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	g, err := e.store.GetGrant(ctx, id)
	if err != nil {
		return err
	}
	if g.Status != store.StatusActive {
		return ErrGrantNotFound
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if failed := e.revert(actx, g); len(failed) > 0 {
		return &ErrRevertFailed{Clients: failed}
	}
	// The clients are re-blocked; record it even if the request has gone.
	ok, err := e.store.SetGrantStatus(context.WithoutCancel(ctx), id, store.StatusActive, store.StatusEnded, e.now())
	if err != nil {
		return fmt.Errorf("end grant: %w", err)
	}
	if !ok {
		return ErrGrantNotFound
	}
	e.log.Info("grant ended", "grant", g.ID, "child", g.ChildID, "services", g.Services)
	e.stopTimer(id)
	return nil
}

// Close stops every timer. Operations after Close still write the store and
// AdGuard but arm nothing.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for id := range e.timers {
		e.stopTimer(id)
	}
}

// arm (re)schedules g's expiry timer. Caller holds mu.
func (e *Engine) arm(g store.Grant) {
	e.stopTimer(g.ID)
	if e.closed {
		return
	}
	d := g.EndsAt.Sub(e.now())
	if d < 0 {
		d = 0
	}
	e.timers[g.ID] = e.afterFunc(d, func() { e.expire(g.ID) })
}

// stopTimer drops g's timer if one is armed. Caller holds mu.
func (e *Engine) stopTimer(id int64) {
	if t, ok := e.timers[id]; ok {
		t.Stop()
		delete(e.timers, id)
	}
}

// applyContext derives the one ApplyTimeout-bounded context every AdGuard
// call of an operation shares. Cancellation of the caller's ctx is dropped:
// once the row is committed the writes should finish even if the request
// that asked for them has gone.
func (e *Engine) applyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), e.applyTimeout)
}
