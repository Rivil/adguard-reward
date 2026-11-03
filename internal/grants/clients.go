package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

// ErrAdoptFailed names the client whose baseline could not be written.
// Clients before it in the call may already carry their baseline — a
// superset of what they blocked, so a retry is safe.
type ErrAdoptFailed struct {
	Client string
	Err    error
}

func (e *ErrAdoptFailed) Error() string {
	return fmt.Sprintf("grants: could not apply %q's blocked services: %v", e.Client, e.Err)
}

func (e *ErrAdoptFailed) Unwrap() error { return e.Err }

// CreateClient creates an AdGuard persistent client and, when childID is
// non-nil, maps it to that child in the same call (locked:
// new_client_blocked_set). An assigned client gets its own list — the
// child's baseline — with use_global_blocked_services=false, so a grant
// running for the child never leaves the new device open after expiry; an
// unassigned one keeps AdGuard's defaults. It runs under the engine mutex
// with one ApplyTimeout-bounded context, so no grant applies or reverts
// between the baseline read and the add.
//
// An unknown child is store.ErrChildNotFound before AdGuard is touched. If
// the store mapping fails after the add, the client stays in AdGuard with
// its baseline and the error is returned.
func (e *Engine) CreateClient(ctx context.Context, name string, ids []string, childID *int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	nc := adguard.NewClient{Name: name, IDs: ids}
	var child store.Child
	if childID != nil {
		var err error
		if child, err = e.store.GetChild(ctx, *childID); err != nil {
			return err
		}
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if childID != nil {
		base, err := e.baseline(ctx, actx, child.ID, child.Clients)
		if err != nil {
			return err
		}
		nc.Blocked = &base
	}
	if err := e.ag.AddClient(actx, nc); err != nil {
		return err
	}
	if childID == nil {
		return nil
	}
	if err := e.store.AddChildClient(context.WithoutCancel(ctx), child.ID, name); err != nil {
		return fmt.Errorf("client %q created in AdGuard but not assigned to %q: %w", name, child.Name, err)
	}
	// The new device is a child client now: block the custom services on it
	// before returning. A failure leaves it to the reconciler.
	e.resyncRules(context.WithoutCancel(ctx), actx, "client create", name)
	return nil
}

// AdoptClients gives each client being added to a child the child's
// baseline, computed from siblings — the clients that stay with the child —
// as an addition to what the client already blocks: nothing is removed and
// use_global_blocked_services is cleared. A service another active grant
// still covers on that client (of any owner, on the grant's stored clients)
// is left out of that client's addition, so sharing a device never re-blocks
// what another owner's grant unblocked there (locked: shared_device_rule).
// A client whose list already holds everything is not written. A client
// AdGuard does not know is skipped. No added clients means no AdGuard
// request at all. The first failure is *ErrAdoptFailed naming the client.
func (e *Engine) AdoptClients(ctx context.Context, childID int64, siblings, added []string) error {
	if len(added) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	actx, cancel := e.applyContext(ctx)
	defer cancel()
	res, err := e.ag.Clients(actx)
	if err != nil {
		return err
	}
	base, active, err := e.baselineFrom(ctx, res, childID, siblings)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, pc := range res.Persistent {
		known[pc.Name] = true
	}
	for _, name := range added {
		if !known[name] {
			continue
		}
		err := e.ag.AdoptBlockedServices(actx, name, without(base, heldOpen(active, 0, name)))
		switch {
		case err == nil, errors.Is(err, adguard.ErrClientNotFound):
		default:
			return &ErrAdoptFailed{Client: name, Err: err}
		}
	}
	return nil
}

// without returns the members of base not in held (sorted), never nil.
func without(base, held []string) []string {
	out := []string{}
	for _, s := range base {
		if _, ok := slices.BinarySearch(held, s); !ok {
			out = append(out, s)
		}
	}
	return out
}

// baseline reads AdGuard through actx and computes childID's baseline.
// Caller holds mu.
func (e *Engine) baseline(ctx, actx context.Context, childID int64, siblings []string) ([]string, error) {
	res, err := e.ag.Clients(actx)
	if err != nil {
		return nil, err
	}
	base, _, err := e.baselineFrom(ctx, res, childID, siblings)
	return base, err
}

// baselineFrom is what a device joining childID must block: the union of
// the effective blocked sets of the siblings AdGuard knows (the global list
// for one that inherits it), or AdGuard's global list when there are none,
// plus every service under the child's active grants and under any active
// grant (of any child) stored on a known sibling — those grants unblocked
// the services on the siblings, and their revert will never touch the new
// device. Custom service ids are never part of it. It also returns the
// active grants it read. Sorted, never nil.
// Caller holds mu.
func (e *Engine) baselineFrom(ctx context.Context, res adguard.ClientsResult, childID int64, siblings []string) ([]string, []store.Grant, error) {
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}
	out := []string{}
	var known []string
	for _, s := range siblings {
		pc, ok := byName[s]
		if !ok {
			continue
		}
		known = append(known, s)
		if pc.UseGlobalBlockedServices {
			out = append(out, res.GlobalBlockedServices...)
		} else {
			out = append(out, pc.BlockedServices...)
		}
	}
	if len(known) == 0 {
		out = append(out, res.GlobalBlockedServices...)
	}
	grants, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, g := range grants {
		if g.ChildID == childID || slices.ContainsFunc(known, func(s string) bool { return slices.Contains(g.Clients, s) }) {
			// Only built-in ids belong in blocked_services; custom ones are
			// enforced by the managed region.
			out = append(out, builtin(g.Services)...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), grants, nil
}

// resyncRules converges the managed region after a committed client write,
// logging rather than returning a failure: the store already holds the
// intent and the reconciler converges it. Caller holds mu.
func (e *Engine) resyncRules(ctx, actx context.Context, op, client string) {
	if _, err := e.syncRules(ctx, actx, 0); err != nil {
		e.log.Warn(op+": managed rules not updated; the reconciler will retry", "client", client, "err", err)
	}
}
