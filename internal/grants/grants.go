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
	"maps"
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
// stays active with its timer so the reconciler retries. Err, when set, is
// the cause shared by every client (a failed managed-region write).
type ErrRevertFailed struct {
	Clients []string
	Err     error
}

func (e *ErrRevertFailed) Error() string {
	msg := "grants: revert failed for " + strings.Join(e.Clients, ", ")
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ErrRevertFailed) Unwrap() error { return e.Err }

// ErrUnknownService refuses a grant naming a custom service id that has no
// stored service; nothing is stored.
type ErrUnknownService struct {
	ID string
}

func (e *ErrUnknownService) Error() string {
	return fmt.Sprintf("grants: unknown custom service %s", e.ID)
}

// customPrefix is the namespace of custom service ids. Anything in it —
// canonical or not — is never an AdGuard built-in.
const customPrefix = "custom:"

// builtin returns ids without anything in the custom namespace: only
// AdGuard's own service ids may reach a client's blocked_services. Custom
// services are enforced through the managed user_rules region instead.
func builtin(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !strings.HasPrefix(id, customPrefix) {
			out = append(out, id)
		}
	}
	return out
}

// hasCustom reports whether ids names any custom service.
func hasCustom(ids []string) bool {
	return slices.ContainsFunc(ids, store.IsCustomServiceID)
}

// Store is the persistence the engine needs; *store.Store satisfies it.
type Store interface {
	CreateGrantNamed(ctx context.Context, childID int64, services, clients []string, startedAt, endsAt time.Time, names map[string]string) (store.Grant, error)
	ListActiveGrants(ctx context.Context) ([]store.Grant, error)
	GetGrant(ctx context.Context, id int64) (store.Grant, error)
	ExtendGrant(ctx context.Context, id int64, newEndsAt, at time.Time) (store.Grant, error)
	SetGrantStatus(ctx context.Context, id int64, from, to string, at time.Time) (bool, error)
	GetChild(ctx context.Context, id int64) (store.Child, error)
	ListChildren(ctx context.Context) ([]store.Child, error)
	AddChildClient(ctx context.Context, childID int64, name string) error
	RenameClient(ctx context.Context, oldName, newName string) error
	ListCustomServices(ctx context.Context) ([]store.CustomService, error)
	GetCustomService(ctx context.Context, id int64) (store.CustomService, error)
	CreateCustomService(ctx context.Context, name string, domains []string) (store.CustomService, error)
	UpdateCustomService(ctx context.Context, id int64, name string, domains []string) (store.CustomService, error)
	DeleteCustomService(ctx context.Context, id int64) error
	SetServiceToggle(ctx context.Context, childID int64, serviceID, state string, at time.Time) (store.ToggleChange, error)
	ListServiceToggles(ctx context.Context) ([]store.ServiceToggle, error)
}

// toToggles maps stored toggle rows onto the planners' toggle type.
func toToggles(rows []store.ServiceToggle) []toggle {
	out := make([]toggle, 0, len(rows))
	for _, r := range rows {
		out = append(out, toggle{childID: r.ChildID, serviceID: r.ServiceID, state: r.State})
	}
	return out
}

// readToggles reads the children and every stored toggle — what
// permanentOpen and togglePlan decide from. Callers fail closed on an
// error: a decision made without the toggles could re-block a permanently
// unblocked service or drop its exception from the managed region.
func (e *Engine) readToggles(ctx context.Context) ([]store.Child, []toggle, error) {
	children, err := e.store.ListChildren(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read children: %w", err)
	}
	rows, err := e.store.ListServiceToggles(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read service toggles: %w", err)
	}
	return children, toToggles(rows), nil
}

// AdGuard is the client subset the engine writes through; *adguard.Client
// satisfies it.
type AdGuard interface {
	Clients(ctx context.Context) (adguard.ClientsResult, error)
	RemoveBlockedServices(ctx context.Context, clientName string, ids []string) error
	AddBlockedServices(ctx context.Context, clientName string, ids []string) error
	AddClient(ctx context.Context, nc adguard.NewClient) error
	AdoptBlockedServices(ctx context.Context, clientName string, ids []string) error
	RenameClient(ctx context.Context, oldName, newName string) error
	EditUserRules(ctx context.Context, fn func(current []string) ([]string, error)) (bool, error)
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

// Create stores an active grant for d from now and unblocks its services:
// built-in ids are removed from each client's blocked_services, custom ids
// get @@ exceptions through the managed region. A custom id with no stored
// service is *ErrUnknownService and nothing is stored; the check runs under
// the mutex so a concurrent DeleteService cannot slip between it and the
// insert. An overlapping active grant is *store.ErrGrantOverlap from the
// store, untouched. A client that cannot be written — unknown, inheriting the
// global list, or an AdGuard error; every client when the region write fails
// — goes into Result.Failed; the row stays active for the reconciler to
// converge (locked partial_apply). The expiry timer is armed either way.
func (e *Engine) Create(ctx context.Context, childID int64, services, clients []string, d time.Duration) (Result, error) {
	return e.CreateNamed(ctx, childID, services, clients, d, nil)
}

// CreateNamed is Create that also snapshots names — service id to the
// display name the parent saw — with the grant, in the same store write
// (locked service_names). A nil names snapshots nothing.
func (e *Engine) CreateNamed(ctx context.Context, childID int64, services, clients []string, d time.Duration, names map[string]string) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, id := range services {
		if !strings.HasPrefix(id, customPrefix) {
			continue
		}
		n, ok := store.ParseCustomID(id)
		if !ok {
			return Result{}, &ErrUnknownService{ID: id}
		}
		if _, err := e.store.GetCustomService(ctx, n); err != nil {
			if errors.Is(err, store.ErrCustomServiceNotFound) {
				return Result{}, &ErrUnknownService{ID: id}
			}
			return Result{}, err
		}
	}

	now := e.now()
	g, err := e.store.CreateGrantNamed(ctx, childID, services, clients, now, now.Add(d), names)
	if err != nil {
		return Result{}, err
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	failed := e.unblock(actx, g)
	if hasCustom(g.Services) {
		if _, err := e.syncRules(ctx, actx, 0); err != nil {
			e.log.Warn("grant apply: managed rules not updated", "grant", g.ID, "err", err)
			failed = slices.Clone(g.Clients)
		}
	}
	e.arm(g)
	return Result{Grant: g, Applied: len(failed) == 0, Failed: failed}, nil
}

// unblock removes g's built-in services from each stored client, reading
// AdGuard once for the global flag. A failed read fails every client without
// a write. A grant with only custom services touches no client.
func (e *Engine) unblock(ctx context.Context, g store.Grant) []string {
	services := builtin(g.Services)
	if len(services) == 0 {
		return []string{}
	}
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
			if err := e.ag.RemoveBlockedServices(ctx, name, services); err != nil {
				e.log.Warn("grant apply: unblock failed", "grant", g.ID, "client", name, "err", err)
				failed = append(failed, name)
			}
		}
	}
	return failed
}

// revert re-adds g's services to its stored clients (set-union, so a second
// revert changes nothing), ref-counted (locked: shared_device_rule): it
// re-reads the active grants through ctx on every call and re-blocks on each
// client only what no other active grant still covers there and no current
// owner of the client has permanently unblocked (locked revert_baseline_v2),
// issuing no write for a client that is fully held open. Re-reading rather
// than taking a caller's snapshot means consecutive reverts under mu each
// see the statuses the previous one committed. A failed read of the active
// grants, children or toggles fails every client without a write — never
// re-block blind. AdGuard calls go through actx. A client AdGuard no longer
// has counts as reverted; one inheriting the global list gets the services
// on its own list (the adguard client warns), as phase 04 did; any other
// failure is returned by name. writes counts the Add calls issued. Only
// built-in ids are re-blocked here; a grant with none issues nothing. Unlike
// a toggle, revert does not go through applyPlan, whose global-list rule
// would wedge such a grant active. Caller holds mu.
func (e *Engine) revert(ctx, actx context.Context, g store.Grant) (failed []string, writes int) {
	g.Services = builtin(g.Services)
	if len(g.Services) == 0 {
		return []string{}, 0
	}
	active, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		e.log.Warn("grant revert: read active grants failed", "grant", g.ID, "err", err)
		return slices.Clone(g.Clients), 0
	}
	children, toggles, err := e.readToggles(ctx)
	if err != nil {
		e.log.Warn("grant revert: read permanent toggles failed", "grant", g.ID, "err", err)
		return slices.Clone(g.Clients), 0
	}
	plan := dropOpen(revertPlan(g, active), permanentOpen(children, toggles))
	failed = []string{}
	for _, name := range g.Clients {
		svcs, ok := plan[name]
		if !ok {
			e.log.Debug("grant revert: every service still held open by another grant or a permanent unblock",
				"grant", g.ID, "client", name)
			continue
		}
		writes++
		err := e.ag.AddBlockedServices(actx, name, svcs)
		switch {
		case err == nil:
		case errors.Is(err, adguard.ErrClientNotFound):
			e.log.Debug("grant revert: client gone, nothing to re-block", "grant", g.ID, "client", name)
		default:
			e.log.Warn("grant revert: re-block failed", "grant", g.ID, "client", name, "err", err)
			failed = append(failed, name)
		}
	}
	return failed, writes
}

// applyPlan converges each client in plan to its {remove, add} through
// actx, diffing both against one read of AdGuard's clients so a client
// already in line costs no write. A client AdGuard no longer has is skipped
// (there is nothing left to converge); one inheriting the global
// blocked-services list is failed without a write, since the global list is
// never written and its own list would be ignored; any other write error
// fails the client. A failed read fails every client without a write.
// failed is sorted and never nil; writes counts the Remove and Add calls
// issued. Caller holds mu.
func (e *Engine) applyPlan(actx context.Context, plan map[string]clientPlan) (failed []string, writes int) {
	names := slices.Sorted(maps.Keys(plan))
	failed = []string{}
	if len(names) == 0 {
		return failed, 0
	}
	res, err := e.ag.Clients(actx)
	if err != nil {
		e.log.Warn("toggle apply: read clients failed", "err", err)
		return names, 0
	}
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}
	for _, name := range names {
		pc, ok := byName[name]
		switch {
		case !ok:
			e.log.Debug("toggle apply: client not in AdGuard, skipped", "client", name)
			continue
		case pc.UseGlobalBlockedServices:
			e.log.Warn("toggle apply: client uses the global blocked-services list; migrate it first", "client", name)
			failed = append(failed, name)
			continue
		}
		p := plan[name]
		remove := intersect(p.remove, pc.BlockedServices)
		add := slices.DeleteFunc(slices.Clone(p.add), func(s string) bool { return slices.Contains(pc.BlockedServices, s) })
		var err error
		if len(remove) > 0 {
			writes++
			err = e.ag.RemoveBlockedServices(actx, name, remove)
		}
		if err == nil && len(add) > 0 {
			writes++
			err = e.ag.AddBlockedServices(actx, name, add)
		}
		switch {
		case err == nil:
		case errors.Is(err, adguard.ErrClientNotFound):
			e.log.Debug("toggle apply: client gone, skipped", "client", name)
		default:
			e.log.Warn("toggle apply: write failed", "client", name, "err", err)
			failed = append(failed, name)
		}
	}
	return failed, writes
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
// operation's shared AdGuard context — and marks it expired through ctx, in
// End's order: region without the grant, built-in revert, status CAS. Any
// failure leaves it active for the reconciler. It reports whether the status
// changed and how many blocked_services writes the revert issued. Caller
// holds mu.
func (e *Engine) expireLocked(ctx, actx context.Context, g store.Grant) (bool, int) {
	if hasCustom(g.Services) {
		if _, err := e.syncRules(ctx, actx, g.ID); err != nil {
			e.log.Warn("grant expiry: managed rules not updated, grant stays active", "grant", g.ID, "err", err)
			return false, 0
		}
	}
	failed, writes := e.revert(ctx, actx, g)
	if len(failed) > 0 {
		e.log.Warn("grant expiry: revert incomplete, grant stays active", "grant", g.ID, "clients", failed)
		return false, writes
	}
	ok, err := e.store.SetGrantStatus(ctx, g.ID, store.StatusActive, store.StatusExpired, e.now())
	if err != nil {
		e.log.Error("grant expiry: status update failed", "grant", g.ID, "err", err)
		return false, writes
	}
	if ok {
		e.log.Info("grant expired", "grant", g.ID, "child", g.ChildID, "services", g.Services)
		e.stopTimer(g.ID)
	}
	return ok, writes
}

// Extend pushes an active grant's ends_at by d and reschedules its timer;
// the store records the extension stamped with the engine clock. An unknown
// or non-active grant is ErrGrantNotFound.
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
	g, err = e.store.ExtendGrant(ctx, id, g.EndsAt.Add(d), e.now())
	if err != nil {
		return store.Grant{}, err
	}
	e.arm(g)
	return g, nil
}

// End reverts an active grant now and marks it ended. Like expiry it
// re-blocks a service on a client only when no other active grant still
// covers it there and no current owner of the client has it permanently
// unblocked. The order is: managed region rewritten without the grant's
// exceptions (a permanent unblock's exception stays), then the built-in
// revert, then the status CAS. An unknown or non-active grant is
// ErrGrantNotFound; a client that could not be re-blocked — every client,
// when the active grants, children or toggles could not be read or the
// region write failed (Err carries why, e.g. a malformed region) — is
// *ErrRevertFailed and the grant stays active with its timer; the reconciler
// re-converges both halves from intent.
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
	if hasCustom(g.Services) {
		if _, err := e.syncRules(ctx, actx, g.ID); err != nil {
			return &ErrRevertFailed{Clients: slices.Clone(g.Clients), Err: err}
		}
	}
	if failed, _ := e.revert(ctx, actx, g); len(failed) > 0 {
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
